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

	"github.com/google/generative-ai-go/genai"
	"github.com/google/uuid"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
	"gocloud.dev/blob"
	"google.golang.org/api/option"
)

// AIItemInfo represents the structured result from AI image analysis.
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

	// Some models (including Gemini occasionally) wrap JSON in markdown code fences — strip them
	if strings.HasPrefix(raw, "```json") {
		raw = strings.TrimPrefix(raw, "```json")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	} else if strings.HasPrefix(raw, "```") {
		raw = strings.TrimPrefix(raw, "```")
		raw = strings.TrimSuffix(raw, "```")
		raw = strings.TrimSpace(raw)
	}

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
	format := strings.TrimPrefix(mimeType, "image/")
	prompt := []genai.Part{
		genai.ImageData(format, data),
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

	return parseAIResponse(raw)
}

// ============================================================================
// GenerateDescription — entry point selecting the right provider
// Adapted for the new Entity-based architecture.
// ============================================================================

func (svc *EntityService) GenerateDescription(ctx context.Context, aiConf config.AIConf, gid, id uuid.UUID) (*AIItemInfo, error) {
	// Select AI provider
	var analyzer itemAnalyzer
	switch strings.ToLower(aiConf.Provider) {
	case "openai":
		if aiConf.OpenAIKey == "" {
			return nil, errors.New("OpenAI API key not configured (HBOX_AI_OPENAI_KEY)")
		}
		analyzer = &openaiAnalyzer{
			apiKey:  aiConf.OpenAIKey,
			baseURL: aiConf.OpenAIBaseURL,
			model:   aiConf.OpenAIModel,
		}
	default: // "gemini"
		if aiConf.GeminiAPIKey == "" {
			return nil, errors.New("Gemini API key not configured (HBOX_AI_GEMINI_API_KEY)")
		}
		analyzer = &geminiAnalyzer{apiKey: aiConf.GeminiAPIKey}
	}

	entity, err := svc.repo.Entities.GetOneByGroup(ctx, gid, id)
	if err != nil {
		return nil, err
	}

	// Find primary photo attachment
	var primaryAtt *repo.ItemAttachment
	for i := range entity.Attachments {
		att := &entity.Attachments[i]
		if att.Primary && att.Type == "photo" {
			primaryAtt = att
			break
		}
	}

	if primaryAtt == nil {
		return nil, errors.New("no primary photo found for this entity")
	}

	// Read photo content from storage
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
