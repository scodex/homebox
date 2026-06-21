package v1

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/sysadminsmedia/homebox/backend/internal/core/services"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"
	"github.com/hay-kot/httpkit/server"
	"github.com/hay-kot/httpkit/errchain"
	"github.com/google/uuid"
	"gocloud.dev/blob"
)

// FloorPlanPositionUpdate represents a single item's coordinate
type FloorPlanPositionUpdate struct {
	ID string  `json:"id" validate:"required"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
}

// FloorPlanPositionsUpdateRequest is the payload for saving positions
type FloorPlanPositionsUpdateRequest struct {
	Locations []FloorPlanPositionUpdate `json:"locations"`
	Items     []FloorPlanPositionUpdate `json:"items"`
}

// ============================================================================
// Floor Plan Handlers for Entities
// ============================================================================

// HandleEntityFloorPlanUpload uploads a floor plan image for an entity.
//
//	@Summary	Upload Floor Plan
//	@Tags		Entities
//	@Accept		multipart/form-data
//	@Produce	json
//	@Param		id		path		string	true	"Entity ID"
//	@Param		file	formData	file	true	"Floor plan image"
//	@Success	200		{object}	repo.EntityOut
//	@Router		/v1/entities/{id}/floor-plan [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleEntityFloorPlanUpload() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		err := r.ParseMultipartForm(ctrl.maxUploadSize << 20)
		if err != nil {
			return validate.NewRequestError(errors.New("failed to parse multipart form"), http.StatusBadRequest)
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			return validate.NewRequestError(errors.New("file is required"), http.StatusBadRequest)
		}
		defer file.Close()

		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())

		// Read file bytes
		fileBytes, err := io.ReadAll(file)
		if err != nil {
			return validate.NewRequestError(err, http.StatusBadRequest)
		}

		// Save floor plan image to blob storage
		path := "floor-plans/" + id + "/" + uuid.NewString()
		
		bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
		if err != nil {
			return err
		}
		defer bucket.Close()
		
		err = bucket.WriteAll(r.Context(), path, fileBytes, &blob.WriterOptions{
			ContentType: header.Header.Get("Content-Type"),
		})
		if err != nil {
			return err
		}

		// Update Entity
		entity, err := ctrl.repo.Entities.GetOneByGroup(r.Context(), auth.GID, id)
		if err != nil {
			return err
		}

		// Update floor plan path
		entity.FloorPlanPath = path
		entity.FloorPlanMimeType = header.Header.Get("Content-Type")

		// Save entity updates
		updatedEntity, err := ctrl.repo.Entities.Update(r.Context(), auth.GID, entity)
		if err != nil {
			return err
		}

		return server.JSON(w, http.StatusOK, updatedEntity)
	}
}

// HandleEntityFloorPlanDelete deletes a floor plan image for an entity.
//
//	@Summary	Delete Floor Plan
//	@Tags		Entities
//	@Produce	json
//	@Param		id	path	string	true	"Entity ID"
//	@Success	204
//	@Router		/v1/entities/{id}/floor-plan [DELETE]
//	@Security	Bearer
func (ctrl *V1Controller) HandleEntityFloorPlanDelete() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())

		entity, err := ctrl.repo.Entities.GetOneByGroup(r.Context(), auth.GID, id)
		if err != nil {
			return err
		}

		if entity.FloorPlanPath != "" {
			bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
			if err == nil {
				_ = bucket.Delete(r.Context(), entity.FloorPlanPath)
				bucket.Close()
			}
			entity.FloorPlanPath = ""
			entity.FloorPlanMimeType = ""
			
			// Save
			_, err = ctrl.repo.Entities.Update(r.Context(), auth.GID, entity)
			if err != nil {
				return err
			}
		}

		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// HandleEntityFloorPlanImage serves the floor plan image.
//
//	@Summary	Get Floor Plan Image
//	@Tags		Entities
//	@Produce	image/*
//	@Param		id	path	string	true	"Entity ID"
//	@Success	200
//	@Router		/v1/entities/{id}/floor-plan/image [GET]
//	@Security	Bearer
func (ctrl *V1Controller) HandleEntityFloorPlanImage() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())

		entity, err := ctrl.repo.Entities.GetOneByGroup(r.Context(), auth.GID, id)
		if err != nil {
			return err
		}

		if entity.FloorPlanPath == "" {
			return validate.NewRequestError(errors.New("no floor plan"), http.StatusNotFound)
		}

		bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
		if err != nil {
			return err
		}
		defer bucket.Close()

		reader, err := bucket.NewReader(r.Context(), entity.FloorPlanPath, nil)
		if err != nil {
			return err
		}
		defer reader.Close()

		w.Header().Set("Content-Type", entity.FloorPlanMimeType)
		w.Header().Set("Cache-Control", "public, max-age=31536000")
		_, err = io.Copy(w, reader)
		return err
	}
}

// HandleEntityFloorPlanPositionsUpdate updates the X/Y coordinates of children on the floor plan
//
//	@Summary	Update Floor Plan Positions
//	@Tags		Entities
//	@Accept		json
//	@Produce	json
//	@Param		id		path	string									true	"Entity ID"
//	@Param		body	body	FloorPlanPositionsUpdateRequest			true	"Positions"
//	@Success	204
//	@Router		/v1/entities/{id}/floor-plan/positions [PUT]
//	@Security	Bearer
func (ctrl *V1Controller) HandleEntityFloorPlanPositionsUpdate() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		_, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())
		var req FloorPlanPositionsUpdateRequest

		if err := validate.BindJSON(r, &req); err != nil {
			return err
		}

		// Update each entity's coordinates
		// In the new architecture, both Locations and Items from the request are just Entities.
		for _, loc := range req.Locations {
			ent, err := ctrl.repo.Entities.GetOneByGroup(r.Context(), auth.GID, loc.ID)
			if err == nil {
				ent.FloorPlanX = loc.X
				ent.FloorPlanY = loc.Y
				_, _ = ctrl.repo.Entities.Update(r.Context(), auth.GID, ent)
			}
		}

		for _, item := range req.Items {
			ent, err := ctrl.repo.Entities.GetOneByGroup(r.Context(), auth.GID, item.ID)
			if err == nil {
				ent.FloorPlanX = item.X
				ent.FloorPlanY = item.Y
				_, _ = ctrl.repo.Entities.Update(r.Context(), auth.GID, ent)
			}
		}

		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}
