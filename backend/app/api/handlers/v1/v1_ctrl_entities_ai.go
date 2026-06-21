package v1

import (
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/sysadminsmedia/homebox/backend/internal/core/services"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"

	"github.com/google/uuid"
	"github.com/hay-kot/httpkit/errchain"
	"github.com/hay-kot/httpkit/server"
)

// HandleEntityGenerateDescription godoc
//
//	@Summary	Generate Entity Description using AI
//	@Tags		Entities
//	@Produce	json
//	@Param		id		path	string	true	"Entity ID"
//	@Param		overwrite	query	bool	false	"Overwrite existing description"
//	@Success	200		{object}	repo.EntityOut
//	@Router		/v1/entities/{id}/generate-description [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleEntityGenerateDescription() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		overwrite := queryBool(r.URL.Query().Get("overwrite"))
		ctx := services.NewContext(r.Context())

		aiInfo, err := ctrl.svc.Entities.GenerateDescription(r.Context(), ctrl.config.AI, ctx.GID, id)
		if err != nil {
			log.Err(err).Msg("failed to generate description")
			return validate.NewRequestError(err, http.StatusInternalServerError)
		}

		entity, err := ctrl.repo.Entities.GetOneByGroup(ctx, ctx.GID, id)
		if err != nil {
			return err
		}

		patch := repo.EntityPatch{
			ID: id,
		}

		// Description & Barcode
		newDescription := aiInfo.Description
		if aiInfo.Barcode != "" {
			newDescription += "\n\nBarcode: " + aiInfo.Barcode
		}

		if !overwrite && entity.Description != "" {
			newDescription = entity.Description + "\n\n" + newDescription
		}
		patch.Description = &newDescription

		// Name: only set if AI found one and (overwrite or current is empty)
		if aiInfo.Name != "" && (overwrite || entity.Name == "") {
			patch.Name = &aiInfo.Name
		}

		// Quantity
		if aiInfo.Quantity > 0 {
			patch.Quantity = &aiInfo.Quantity
		}

		// Serial Number
		if aiInfo.SerialNumber != "" && (overwrite || entity.SerialNumber == "") {
			patch.SerialNumber = &aiInfo.SerialNumber
		}

		// Model Number
		if aiInfo.ModelNumber != "" && (overwrite || entity.ModelNumber == "") {
			patch.ModelNumber = &aiInfo.ModelNumber
		}

		// Manufacturer
		if aiInfo.Manufacturer != "" && (overwrite || entity.Manufacturer == "") {
			patch.Manufacturer = &aiInfo.Manufacturer
		}

		// Tags: resolve or create AI-suggested tags, merge with existing
		if len(aiInfo.Tags) > 0 {
			// Get existing tags for this group
			existingTags, err := ctrl.repo.Tags.GetAll(ctx, ctx.GID)
			if err != nil {
				log.Err(err).Msg("failed to get existing tags")
				return validate.NewRequestError(err, http.StatusInternalServerError)
			}

			// Build name->ID map (case-insensitive)
			tagMap := make(map[string]uuid.UUID)
			for _, t := range existingTags {
				tagMap[strings.ToLower(t.Name)] = t.ID
			}

			// Collect current entity tag IDs
			currentTagIDs := make([]uuid.UUID, len(entity.Tags))
			for i, t := range entity.Tags {
				currentTagIDs[i] = t.ID
			}
			tagSet := make(map[uuid.UUID]bool)
			for _, id := range currentTagIDs {
				tagSet[id] = true
			}

			// Resolve AI tags (find existing or create new)
			for _, tagName := range aiInfo.Tags {
				tagName = strings.TrimSpace(tagName)
				if tagName == "" {
					continue
				}

				tagID, exists := tagMap[strings.ToLower(tagName)]
				if !exists {
					// Create new tag
					newTag, err := ctrl.repo.Tags.Create(ctx, ctx.GID, repo.TagCreate{Name: tagName})
					if err != nil {
						log.Err(err).Str("tag", tagName).Msg("failed to create AI tag")
						continue
					}
					tagID = newTag.ID
				}

				if !tagSet[tagID] {
					currentTagIDs = append(currentTagIDs, tagID)
					tagSet[tagID] = true
				}
			}

			patch.TagIDs = currentTagIDs
		}

		// Apply patch
		err = ctrl.repo.Entities.Patch(ctx, ctx.GID, id, patch)
		if err != nil {
			log.Err(err).Msg("failed to patch entity with AI data")
			return validate.NewRequestError(err, http.StatusInternalServerError)
		}

		// Return updated entity
		updated, err := ctrl.repo.Entities.GetOneByGroup(ctx, ctx.GID, id)
		if err != nil {
			return err
		}

		return server.JSON(w, http.StatusOK, updated)
	}
}
