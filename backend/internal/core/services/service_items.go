package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sysadminsmedia/homebox/backend/internal/core/services/reporting"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"

	"github.com/google/generative-ai-go/genai"
	"gocloud.dev/blob"
	"google.golang.org/api/option"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrFileNotFound = errors.New("file not found")
)

type ItemService struct {
	repo *repo.AllRepos

	filepath string

	autoIncrementAssetID bool
	aiConf               config.AIConf
}

func (svc *ItemService) Create(ctx Context, item repo.ItemCreate) (repo.ItemOut, error) {
	if svc.autoIncrementAssetID {
		highest, err := svc.repo.Items.GetHighestAssetID(ctx, ctx.GID)
		if err != nil {
			return repo.ItemOut{}, err
		}

		item.AssetID = highest + 1
	}

	return svc.repo.Items.Create(ctx, ctx.GID, item)
}

func (svc *ItemService) Duplicate(ctx Context, gid, id uuid.UUID, options repo.DuplicateOptions) (repo.ItemOut, error) {
	return svc.repo.Items.Duplicate(ctx, gid, id, options)
}

func (svc *ItemService) EnsureAssetID(ctx context.Context, gid uuid.UUID) (int, error) {
	items, err := svc.repo.Items.GetAllZeroAssetID(ctx, gid)
	if err != nil {
		return 0, err
	}

	highest, err := svc.repo.Items.GetHighestAssetID(ctx, gid)
	if err != nil {
		return 0, err
	}

	finished := 0
	for _, item := range items {
		highest++

		err = svc.repo.Items.SetAssetID(ctx, gid, item.ID, highest)
		if err != nil {
			return 0, err
		}

		finished++
	}

	return finished, nil
}

func (svc *ItemService) EnsureImportRef(ctx context.Context, gid uuid.UUID) (int, error) {
	ids, err := svc.repo.Items.GetAllZeroImportRef(ctx, gid)
	if err != nil {
		return 0, err
	}

	finished := 0
	for _, itemID := range ids {
		err = svc.repo.Items.Patch(ctx, gid, itemID, repo.ItemPatch{ImportRef: new(uuid.New().String()[0:8])})
		if err != nil {
			return 0, err
		}

		finished++
	}

	return finished, nil
}

func serializeLocation[T ~[]string](location T) string {
	return strings.Join(location, "/")
}

// CsvImport imports items from a CSV file. using the standard defined format.
//
// CsvImport applies the following rules/operations
//
//  1. If the item does not exist, it is created.
//  2. If the item has a ImportRef and it exists it is skipped
//  3. Locations and Tags are created if they do not exist.
func (svc *ItemService) CsvImport(ctx context.Context, gid uuid.UUID, data io.Reader) (int, error) {
	sheet := reporting.IOSheet{}

	err := sheet.Read(data)
	if err != nil {
		return 0, err
	}

	// ========================================
	// Tags

	var tagMap map[string]uuid.UUID
	{
		tags, err := svc.repo.Tags.GetAll(ctx, gid)
		if err != nil {
			return 0, err
		}

		tagMap = lo.SliceToMap(tags, func(tag repo.TagSummary) (string, uuid.UUID) {
			return tag.Name, tag.ID
		})
	}

	// ========================================
	// Locations

	locationMap := make(map[string]uuid.UUID)
	{
		locations, err := svc.repo.Locations.Tree(ctx, gid, repo.TreeQuery{WithItems: false})
		if err != nil {
			return 0, err
		}

		// Traverse the tree and build a map of location full paths to IDs
		// where the full path is the location name joined by slashes.
		var traverse func(location *repo.TreeItem, path []string)
		traverse = func(location *repo.TreeItem, path []string) {
			path = append(path, location.Name)

			locationMap[serializeLocation(path)] = location.ID

			for _, child := range location.Children {
				traverse(child, path)
			}
		}

		for _, location := range locations {
			traverse(&location, []string{})
		}
	}

	// ========================================
	// Import items

	// Asset ID Pre-Check
	highestAID := repo.AssetID(-1)
	if svc.autoIncrementAssetID {
		highestAID, err = svc.repo.Items.GetHighestAssetID(ctx, gid)
		if err != nil {
			return 0, err
		}
	}

	finished := 0

	for i := range sheet.Rows {
		row := sheet.Rows[i]

		createRequired := true

		// ========================================
		// Preflight check for existing item
		if row.ImportRef != "" {
			exists, err := svc.repo.Items.CheckRef(ctx, gid, row.ImportRef)
			if err != nil {
				return 0, fmt.Errorf("error checking for existing item with ref %q: %w", row.ImportRef, err)
			}

			if exists {
				createRequired = false
			}
		}

		// ========================================
		// Pre-Create tags as necessary
		tagIds := make([]uuid.UUID, len(row.TagStr))

		for j := range row.TagStr {
			tag := row.TagStr[j]

			id, ok := tagMap[tag]
			if !ok {
				newTag, err := svc.repo.Tags.Create(ctx, gid, repo.TagCreate{Name: tag})
				if err != nil {
					return 0, err
				}
				id = newTag.ID
			}

			tagIds[j] = id
			tagMap[tag] = id
		}

		// ========================================
		// Pre-Create Locations as necessary
		path := serializeLocation(row.Location)

		locationID, ok := locationMap[path]
		if !ok { // Traverse the path of LocationStr and check each path element to see if it exists already, if not create it.
			paths := []string{}
			for i, pathElement := range row.Location {
				paths = append(paths, pathElement)
				path := serializeLocation(paths)

				locationID, ok = locationMap[path]
				if !ok {
					parentID := uuid.Nil

					// Get the parent ID
					if i > 0 {
						parentPath := serializeLocation(row.Location[:i])
						parentID = locationMap[parentPath]
					}

					newLocation, err := svc.repo.Locations.Create(ctx, gid, repo.LocationCreate{
						ParentID: parentID,
						Name:     pathElement,
					})
					if err != nil {
						return 0, err
					}
					locationID = newLocation.ID
				}

				locationMap[path] = locationID
			}

			locationID, ok = locationMap[path]
			if !ok {
				return 0, errors.New("failed to create location")
			}
		}

		var effAID repo.AssetID
		if svc.autoIncrementAssetID && row.AssetID.Nil() {
			effAID = highestAID + 1
			highestAID++
		} else {
			effAID = row.AssetID
		}

		// ========================================
		// Create Item
		var item repo.ItemOut
		switch {
		case createRequired:
			newItem := repo.ItemCreate{
				ImportRef:   row.ImportRef,
				Name:        row.Name,
				Description: row.Description,
				AssetID:     effAID,
				LocationID:  locationID,
				TagIDs:      tagIds,
			}

			item, err = svc.repo.Items.Create(ctx, gid, newItem)
			if err != nil {
				return 0, err
			}
		default:
			item, err = svc.repo.Items.GetByRef(ctx, gid, row.ImportRef)
			if err != nil {
				return 0, err
			}
		}

		if item.ID == uuid.Nil {
			panic("item ID is nil on import - this should never happen")
		}

		fields := lo.Map(row.Fields, func(f reporting.ExportItemFields, _ int) repo.ItemField {
			return repo.ItemField{
				Name:      f.Name,
				Type:      "text",
				TextValue: f.Value,
			}
		})

		updateItem := repo.ItemUpdate{
			ID:         item.ID,
			TagIDs:     tagIds,
			LocationID: locationID,

			Name:        row.Name,
			Description: row.Description,
			AssetID:     effAID,
			Insured:     row.Insured,
			Quantity:    row.Quantity,
			Archived:    row.Archived,

			PurchasePrice: row.PurchasePrice,
			PurchaseFrom:  row.PurchaseFrom,
			PurchaseTime:  row.PurchaseTime,

			Manufacturer: row.Manufacturer,
			ModelNumber:  row.ModelNumber,
			SerialNumber: row.SerialNumber,

			LifetimeWarranty: row.LifetimeWarranty,
			WarrantyExpires:  row.WarrantyExpires,
			WarrantyDetails:  row.WarrantyDetails,

			SoldTo:    row.SoldTo,
			SoldTime:  row.SoldTime,
			SoldPrice: row.SoldPrice,
			SoldNotes: row.SoldNotes,

			Notes:  row.Notes,
			Fields: fields,
		}

		item, err = svc.repo.Items.UpdateByGroup(ctx, gid, updateItem)
		if err != nil {
			return 0, err
		}

		finished++
	}

	return finished, nil
}

func (svc *ItemService) ExportCSV(ctx context.Context, gid uuid.UUID, hbURL string) ([][]string, error) {
	items, err := svc.repo.Items.GetAll(ctx, gid)
	if err != nil {
		return nil, err
	}

	sheet := reporting.IOSheet{}

	err = sheet.ReadItems(ctx, items, gid, svc.repo, hbURL)
	if err != nil {
		return nil, err
	}

	return sheet.CSV()
}

func (svc *ItemService) ExportBillOfMaterialsCSV(ctx context.Context, gid uuid.UUID) ([]byte, error) {
	items, err := svc.repo.Items.GetAll(ctx, gid)
	if err != nil {
		return nil, err
	}

	return reporting.BillOfMaterialsCSV(items)
}

// AIItemInfo contains structured item information extracted by the AI.
type AIItemInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Quantity     float64  `json:"quantity"`
	SerialNumber string   `json:"serial_number"`
	ModelNumber  string   `json:"model_number"`
	Manufacturer string   `json:"manufacturer"`
	Tags         []string `json:"tags"`
	Barcode      string   `json:"barcode"`
}

// itemAnalyzer is the interface that AI providers must implement.
type itemAnalyzer interface {
	AnalyzeImage(ctx context.Context, mimeType string, data []byte) (*AIItemInfo, error)
}

// aiPromptText is the shared prompt used by all AI providers.
const aiPromptText = `Analysiere diesen Gegenstand auf dem Bild und extrahiere folgende Informationen.
Antworte ausschließlich als JSON-Objekt mit diesen Feldern:
- "name": Produktname oder Bezeichnung des Gegenstands (kurz und prägnant)
- "description": Detaillierte Beschreibung (Merkmale, Zustand, Nutzen). Auf Deutsch.
- "quantity": Anzahl der sichtbaren Gegenstände (als Zahl, Standard: 1)
- "serial_number": Seriennummer, falls auf dem Bild sichtbar (sonst leerer String)
- "model_number": Modellnummer, falls erkennbar (sonst leerer String)
- "manufacturer": Hersteller/Marke, falls erkennbar (sonst leerer String)
- "tags": Genau 2 passende Kategorie-Tags als Array von Strings. Die Tags sollen den Gegenstand kategorisieren (z.B. Produktkategorie, Verwendungszweck). Kurz, auf Deutsch, ein Wort pro Tag.
- "barcode": Inhalt eines sichtbaren Barcodes oder QR-Codes (falls vorhanden, sonst leerer String)

Beispiel:
{"name": "Bosch Akkuschrauber", "description": "Blauer Akkuschrauber...", "quantity": 1, "serial_number": "", "model_number": "GSR 18V-60", "manufacturer": "Bosch", "tags": ["Werkzeug", "Elektro"], "barcode": "4059952513959"}`

// parseAIResponse parses a JSON string from any AI provider into AIItemInfo.
func parseAIResponse(raw string) (*AIItemInfo, error) {
	raw = strings.TrimSpace(raw)
	var info AIItemInfo

	if len(raw) > 0 && raw[0] == '[' {
		var items []AIItemInfo
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, fmt.Errorf("failed to parse AI JSON array response: %w", err)
		}
		if len(items) == 0 {
			return nil, errors.New("AI returned empty array")
		}
		info = items[0]
	} else {
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			return nil, fmt.Errorf("failed to parse AI JSON response: %w", err)
		}
	}

	if info.Quantity == 0 {
		info.Quantity = 1
	}

	return &info, nil
}

// ============================================================================
// Gemini Provider
// ============================================================================

type geminiAnalyzer struct {
	apiKey string
}

func (g *geminiAnalyzer) AnalyzeImage(ctx context.Context, mimeType string, data []byte) (*AIItemInfo, error) {
	client, err := genai.NewClient(ctx, option.WithAPIKey(g.apiKey))
	if err != nil {
		return nil, err
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-2.5-flash-lite")
	model.ResponseMIMEType = "application/json"
	prompt := []genai.Part{
		genai.ImageData(mimeType, data),
		genai.Text(aiPromptText),
	}

	resp, err := model.GenerateContent(ctx, prompt...)
	if err != nil {
		return nil, err
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, errors.New("no description generated")
	}

	part, ok := resp.Candidates[0].Content.Parts[0].(genai.Text)
	if !ok {
		return nil, errors.New("failed to parse Gemini response")
	}

	return parseAIResponse(string(part))
}

// ============================================================================
// OpenAI-Compatible Provider (OpenAI, DeepSeek, OpenRouter, Ollama)
// ============================================================================

type openaiAnalyzer struct {
	apiKey  string
	baseURL string
	model   string
}

// openAI request/response types
type openaiRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

type openaiMessage struct {
	Role    string          `json:"role"`
	Content []openaiContent `json:"content"`
}

type openaiContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openaiImageURL `json:"image_url,omitempty"`
}

type openaiImageURL struct {
	URL string `json:"url"`
}

type openaiResponse struct {
	Choices []openaiChoice `json:"choices"`
	Error   *openaiError   `json:"error,omitempty"`
}

type openaiChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type openaiError struct {
	Message string `json:"message"`
}

func (o *openaiAnalyzer) AnalyzeImage(ctx context.Context, mimeType string, data []byte) (*AIItemInfo, error) {
	b64 := base64.StdEncoding.EncodeToString(data)
	dataURI := fmt.Sprintf("data:%s;base64,%s", mimeType, b64)

	reqBody := openaiRequest{
		Model:     o.model,
		MaxTokens: 1024,
		Messages: []openaiMessage{
			{
				Role: "user",
				Content: []openaiContent{
					{
						Type: "image_url",
						ImageURL: &openaiImageURL{
							URL: dataURI,
						},
					},
					{
						Type: "text",
						Text: aiPromptText,
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	url := strings.TrimRight(o.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenAI API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var oResp openaiResponse
	if err := json.Unmarshal(body, &oResp); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	if oResp.Error != nil {
		return nil, fmt.Errorf("OpenAI API error: %s", oResp.Error.Message)
	}

	if len(oResp.Choices) == 0 {
		return nil, errors.New("no response from OpenAI-compatible API")
	}

	raw := oResp.Choices[0].Message.Content

	// Some models wrap JSON in markdown code fences — strip them
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```json") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	} else if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}

	return parseAIResponse(raw)
}

// ============================================================================
// GenerateDescription — entry point selecting the right provider
// ============================================================================

func (svc *ItemService) GenerateDescription(ctx context.Context, gid, id uuid.UUID) (*AIItemInfo, error) {
	// Select AI provider
	var analyzer itemAnalyzer
	switch strings.ToLower(svc.aiConf.Provider) {
	case "openai":
		if svc.aiConf.OpenAIKey == "" {
			return nil, errors.New("OpenAI API key not configured (HBOX_AI_OPENAI_KEY)")
		}
		analyzer = &openaiAnalyzer{
			apiKey:  svc.aiConf.OpenAIKey,
			baseURL: svc.aiConf.OpenAIBaseURL,
			model:   svc.aiConf.OpenAIModel,
		}
	default: // "gemini"
		if svc.aiConf.GeminiAPIKey == "" {
			return nil, errors.New("Gemini API key not configured (HBOX_AI_GEMINI_API_KEY)")
		}
		analyzer = &geminiAnalyzer{apiKey: svc.aiConf.GeminiAPIKey}
	}

	item, err := svc.repo.Items.GetOneByGroup(ctx, gid, id)
	if err != nil {
		return nil, err
	}

	// Find primary photo
	var primaryAtt *repo.ItemAttachment
	for i := range item.Attachments {
		att := &item.Attachments[i]
		if att.Primary && att.Type == "photo" {
			primaryAtt = att
			break
		}
	}

	if primaryAtt == nil {
		return nil, errors.New("no primary photo found for this item")
	}

	// Read photo content
	bucket, err := blob.OpenBucket(ctx, svc.repo.Attachments.GetConnString())
	if err != nil {
		return nil, err
	}
	defer bucket.Close()

	reader, err := bucket.NewReader(ctx, svc.repo.Attachments.GetFullPath(primaryAtt.Path), nil)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return analyzer.AnalyzeImage(ctx, primaryAtt.MimeType, data)
}
