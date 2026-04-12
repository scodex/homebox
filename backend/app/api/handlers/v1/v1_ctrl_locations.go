package v1

import (
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/hay-kot/httpkit/errchain"
	"github.com/hay-kot/httpkit/server"
	"github.com/rs/zerolog/log"
	"github.com/sysadminsmedia/homebox/backend/internal/core/services"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"
	"github.com/sysadminsmedia/homebox/backend/internal/web/adapters"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/fileblob"
)

// HandleLocationTreeQuery godoc
//
//	@Summary	Get Locations Tree
//	@Tags		Locations
//	@Produce	json
//	@Param		withItems	query		bool	false	"include items in response tree"
//	@Success	200			{object}	[]repo.TreeItem
//	@Router		/v1/locations/tree [GET]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationTreeQuery() errchain.HandlerFunc {
	fn := func(r *http.Request, query repo.TreeQuery) ([]repo.TreeItem, error) {
		auth := services.NewContext(r.Context())
		return ctrl.repo.Locations.Tree(auth, auth.GID, query)
	}

	return adapters.Query(fn, http.StatusOK)
}

// HandleLocationGetAll godoc
//
//	@Summary	Get All Locations
//	@Tags		Locations
//	@Produce	json
//	@Param		filterChildren	query		bool	false	"Filter locations with parents"
//	@Success	200				{object}	[]repo.LocationOutCount
//	@Router		/v1/locations [GET]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationGetAll() errchain.HandlerFunc {
	fn := func(r *http.Request, q repo.LocationQuery) ([]repo.LocationOutCount, error) {
		auth := services.NewContext(r.Context())
		return ctrl.repo.Locations.GetAll(auth, auth.GID, q)
	}

	return adapters.Query(fn, http.StatusOK)
}

// HandleLocationCreate godoc
//
//	@Summary	Create Location
//	@Tags		Locations
//	@Produce	json
//	@Param		payload	body		repo.LocationCreate	true	"Location Data"
//	@Success	200		{object}	repo.LocationSummary
//	@Router		/v1/locations [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationCreate() errchain.HandlerFunc {
	fn := func(r *http.Request, createData repo.LocationCreate) (repo.LocationOut, error) {
		auth := services.NewContext(r.Context())
		return ctrl.repo.Locations.Create(auth, auth.GID, createData)
	}

	return adapters.Action(fn, http.StatusCreated)
}

// HandleLocationDelete godoc
//
//	@Summary	Delete Location
//	@Tags		Locations
//	@Produce	json
//	@Param		id	path	string	true	"Location ID"
//	@Success	204
//	@Router		/v1/locations/{id} [DELETE]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationDelete() errchain.HandlerFunc {
	fn := func(r *http.Request, ID uuid.UUID) (any, error) {
		auth := services.NewContext(r.Context())
		err := ctrl.repo.Locations.DeleteByGroup(auth, auth.GID, ID)
		return nil, err
	}

	return adapters.CommandID("id", fn, http.StatusNoContent)
}

func (ctrl *V1Controller) GetLocationWithPrice(auth context.Context, gid uuid.UUID, id uuid.UUID) (repo.LocationOut, error) {
	var location, err = ctrl.repo.Locations.GetOneByGroup(auth, gid, id)
	if err != nil {
		return repo.LocationOut{}, err
	}

	// Add direct child items price
	totalPrice := new(big.Int)
	items, err := ctrl.repo.Items.QueryByGroup(auth, gid, repo.ItemQuery{LocationIDs: []uuid.UUID{id}})
	if err != nil {
		return repo.LocationOut{}, err
	}

	for _, item := range items.Items {
		// Skip items with a non-zero SoldTime
		if !item.SoldTime.IsZero() {
			continue
		}

		itemTotal := big.NewInt(int64(item.PurchasePrice * item.Quantity * 100))
		totalPrice.Add(totalPrice, itemTotal)
	}

	totalPriceFloat := new(big.Float).SetInt(totalPrice)
	totalPriceFloat.Quo(totalPriceFloat, big.NewFloat(100))
	location.TotalPrice, _ = totalPriceFloat.Float64()

	// Add price from child locations
	for _, childLocation := range location.Children {
		var childLocationWithPrice repo.LocationOut
		childLocationWithPrice, err = ctrl.GetLocationWithPrice(auth, gid, childLocation.ID)
		if err != nil {
			return repo.LocationOut{}, err
		}
		location.TotalPrice += childLocationWithPrice.TotalPrice
	}

	return location, nil
}

// HandleLocationGet godoc
//
//	@Summary	Get Location
//	@Tags		Locations
//	@Produce	json
//	@Param		id	path		string	true	"Location ID"
//	@Success	200	{object}	repo.LocationOut
//	@Router		/v1/locations/{id} [GET]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationGet() errchain.HandlerFunc {
	fn := func(r *http.Request, ID uuid.UUID) (repo.LocationOut, error) {
		auth := services.NewContext(r.Context())
		var location, err = ctrl.GetLocationWithPrice(auth, auth.GID, ID)

		return location, err
	}

	return adapters.CommandID("id", fn, http.StatusOK)
}

// HandleLocationUpdate godoc
//
//	@Summary	Update Location
//	@Tags		Locations
//	@Produce	json
//	@Param		id		path		string				true	"Location ID"
//	@Param		payload	body		repo.LocationUpdate	true	"Location Data"
//	@Success	200		{object}	repo.LocationOut
//	@Router		/v1/locations/{id} [PUT]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationUpdate() errchain.HandlerFunc {
	fn := func(r *http.Request, ID uuid.UUID, body repo.LocationUpdate) (repo.LocationOut, error) {
		auth := services.NewContext(r.Context())
		body.ID = ID
		return ctrl.repo.Locations.UpdateByGroup(auth, auth.GID, ID, body)
	}

	return adapters.ActionID("id", fn, http.StatusOK)
}

// ============================================================================
// Floor Plan Handlers
// ============================================================================

// HandleLocationFloorPlanUpload uploads a floor plan image for a location.
//
//	@Summary	Upload Floor Plan
//	@Tags		Locations
//	@Accept		multipart/form-data
//	@Produce	json
//	@Param		id		path		string	true	"Location ID"
//	@Param		file	formData	file	true	"Floor plan image"
//	@Success	200		{object}	repo.LocationOut
//	@Router		/v1/locations/{id}/floor-plan [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationFloorPlanUpload() errchain.HandlerFunc {
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

		// Determine MIME type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		mimeType := "image/jpeg"
		switch ext {
		case ".png":
			mimeType = "image/png"
		case ".webp":
			mimeType = "image/webp"
		case ".gif":
			mimeType = "image/gif"
		case ".svg":
			mimeType = "image/svg+xml"
		}

		// Save file to storage
		bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
		if err != nil {
			return err
		}
		defer bucket.Close()

		storagePath := filepath.Join(auth.GID.String(), "floor-plans", id.String()+ext)

		writer, err := bucket.NewWriter(r.Context(), storagePath, nil)
		if err != nil {
			return err
		}

		if _, err := io.Copy(writer, file); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}

		// Update database
		err = ctrl.repo.Locations.UpdateFloorPlan(r.Context(), auth.GID, repo.FloorPlanUpdate{
			LocationID: id,
			Path:       storagePath,
			MimeType:   mimeType,
		})
		if err != nil {
			return err
		}

		location, err := ctrl.GetLocationWithPrice(auth, auth.GID, id)
		if err != nil {
			return err
		}

		return server.JSON(w, http.StatusOK, location)
	}
}

// HandleLocationFloorPlanDelete deletes a floor plan image for a location.
//
//	@Summary	Delete Floor Plan
//	@Tags		Locations
//	@Produce	json
//	@Param		id	path	string	true	"Location ID"
//	@Success	204
//	@Router		/v1/locations/{id}/floor-plan [DELETE]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationFloorPlanDelete() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())

		// Get current floor plan path to delete from storage
		location, err := ctrl.repo.Locations.GetOneByGroup(r.Context(), auth.GID, id)
		if err != nil {
			return err
		}

		if location.FloorPlanPath != "" {
			bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
			if err == nil {
				_ = bucket.Delete(r.Context(), location.FloorPlanPath)
				bucket.Close()
			}
		}

		err = ctrl.repo.Locations.UpdateFloorPlan(r.Context(), auth.GID, repo.FloorPlanUpdate{
			LocationID: id,
			Path:       "",
			MimeType:   "",
		})
		if err != nil {
			return err
		}

		w.WriteHeader(http.StatusNoContent)
		return nil
	}
}

// HandleLocationFloorPlanImage serves the floor plan image.
//
//	@Summary	Get Floor Plan Image
//	@Tags		Locations
//	@Produce	image/*
//	@Param		id	path	string	true	"Location ID"
//	@Success	200
//	@Router		/v1/locations/{id}/floor-plan/image [GET]
//	@Security	Bearer
func (ctrl *V1Controller) HandleLocationFloorPlanImage() errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := ctrl.routeID(r)
		if err != nil {
			return err
		}

		auth := services.NewContext(r.Context())

		location, err := ctrl.repo.Locations.GetOneByGroup(r.Context(), auth.GID, id)
		if err != nil {
			return err
		}

		if location.FloorPlanPath == "" {
			return validate.NewRequestError(errors.New("no floor plan"), http.StatusNotFound)
		}

		bucket, err := blob.OpenBucket(r.Context(), ctrl.repo.Attachments.GetConnString())
		if err != nil {
			return err
		}
		defer bucket.Close()

		reader, err := bucket.NewReader(r.Context(), location.FloorPlanPath, nil)
		if err != nil {
			return err
		}
		defer reader.Close()

		w.Header().Set("Content-Type", location.FloorPlanMimeType)
		w.Header().Set("Cache-Control", "max-age=3600")
		_, err = io.Copy(w, reader)
		return err
	}
}

// HandleFloorPlanPositionsUpdate updates X/Y coordinates for child locations and items.
//
//	@Summary	Update Floor Plan Positions
//	@Tags		Locations
//	@Accept		json
//	@Produce	json
//	@Param		id		path	string							true	"Location ID"
//	@Param		payload	body	FloorPlanPositionsUpdateRequest	true	"Positions"
//	@Success	204
//	@Router		/v1/locations/{id}/floor-plan/positions [PUT]
//	@Security	Bearer
func (ctrl *V1Controller) HandleFloorPlanPositionsUpdate() errchain.HandlerFunc {
	fn := func(r *http.Request, ID uuid.UUID, body FloorPlanPositionsUpdateRequest) (any, error) {
		auth := services.NewContext(r.Context())

		if len(body.Locations) > 0 {
			if err := ctrl.repo.Locations.UpdateFloorPlanPositions(auth, auth.GID, body.Locations); err != nil {
				log.Err(err).Msg("failed to update location positions")
				return nil, err
			}
		}

		if len(body.Items) > 0 {
			if err := ctrl.repo.Locations.UpdateItemFloorPlanPositions(auth, auth.GID, body.Items); err != nil {
				log.Err(err).Msg("failed to update item positions")
				return nil, err
			}
		}

		return nil, nil
	}

	return adapters.ActionID("id", fn, http.StatusNoContent)
}

type FloorPlanPositionsUpdateRequest struct {
	Locations []repo.FloorPlanPositionUpdate `json:"locations"`
	Items     []repo.FloorPlanPositionUpdate `json:"items"`
}

