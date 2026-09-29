package image

import (
	"net/http"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (ir *imageRouter) getImagesJSON(w http.ResponseWriter, r *http.Request) error {
	imageFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	version := version.VersionFromRequest(r)
	var sharedSize bool
	if versions.GreaterThanOrEqualTo(version, "1.42") {
		sharedSize = types.ParseBool(r.URL.Query().Get("shared-size"), false)
	}

	var manifests bool
	if versions.GreaterThanOrEqualTo(version, "1.47") {
		manifests = types.ParseBool(r.URL.Query().Get("manifests"), false)
	}

	var idenity bool
	if versions.GreaterThanOrEqualTo(version, "1.54") {
		idenity = types.ParseBool(r.URL.Query().Get("idenity"), false)
	}

	images, err := ir.translator.Images(r.Context(), types.ImageListOptions{
		Filters:    imageFilters,
		SharedSize: sharedSize,
		Manifests:  manifests,
		Identity:   idenity,
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, 200, images)
}
