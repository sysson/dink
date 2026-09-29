package image

import (
	"net/http"
	"strings"

	"github.com/containerd/platforms"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (ir *imageRouter) deleteImages(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}

	force := types.ParseBool(r.URL.Query().Get("force"), false)
	prune := types.ParseBool(r.URL.Query().Get("prune"), false)

	var p []ocispec.Platform

	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.50") {
		for k, v := range r.URL.Query() {
			if strings.HasPrefix(k, "platform") {
				for _, pp := range v {
					sp, err := platforms.Parse(pp)
					if err != nil {
						return httpx.BadRequest(err)
					}
					p = append(p, sp)
				}
			}
		}
	}

	list, err := ir.translator.ImageDelete(r.Context(), name, imagebackend.RemoveOptions{
		Force:         force,
		PruneChildren: prune,
		Platforms:     p,
	})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, list)
}
