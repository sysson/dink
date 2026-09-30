package volume

import (
	"fmt"
	"net/http"
	"strconv"

	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (v *volumeRouter) getVolumesList(w http.ResponseWriter, r *http.Request) error {
	volumeFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	volumes, warnings, err := v.backend.ListVolumes(r.Context(), volumeFilters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, volumetypes.ListResponse{Volumes: volumes, Warnings: warnings})
}

func (v *volumeRouter) getVolumeByName(w http.ResponseWriter, r *http.Request) error {
	volume, err := v.backend.GetVolume(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, volume)
}

func (v *volumeRouter) postVolumesCreate(w http.ResponseWriter, r *http.Request) error {
	var request volumetypes.CreateRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	volume, err := v.backend.CreateVolume(r.Context(), request)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, volume)
}

func (v *volumeRouter) postVolumesPrune(w http.ResponseWriter, r *http.Request) error {
	volumeFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	// Before API 1.42 prune removed all unused volumes, not just anonymous ones.
	if versions.LessThan(version.VersionFromRequest(r), "1.42") {
		volumeFilters.Add("all", "true")
	}
	report, err := v.backend.PruneVolumes(r.Context(), volumeFilters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}

func (v *volumeRouter) putVolumesUpdate(w http.ResponseWriter, r *http.Request) error {
	return httpx.NewHTTPError(http.StatusNotImplemented, fmt.Errorf("volume update is not supported"))
}

func (v *volumeRouter) deleteVolumes(w http.ResponseWriter, r *http.Request) error {
	force := false
	if value := r.URL.Query().Get("force"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return httpx.BadRequest(fmt.Errorf("invalid force value %q", value))
		}
		force = parsed
	}
	if err := v.backend.RemoveVolume(r.Context(), r.PathValue("name"), force); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
