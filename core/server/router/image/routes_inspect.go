package image

import (
	"errors"
	"net/http"
	"strings"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (ir *imageRouter) getImagesHistory(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}
	var platform *ocispec.Platform
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.48") {
		if platform, err = decodePlatform(r.URL.Query().Get("platform")); err != nil {
			return err
		}
	}
	history, err := ir.translator.ImageHistory(r.Context(), name, platform)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, history)
}

func (ir *imageRouter) getImagesByName(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}
	v := version.VersionFromRequest(r)

	var manifests bool
	if versions.GreaterThanOrEqualTo(v, "1.48") {
		manifests = types.ParseBool(r.URL.Query().Get("manifests"), false)
	}

	var platform *ocispec.Platform
	if versions.GreaterThanOrEqualTo(v, "1.49") {
		if platform, err = decodePlatform(r.URL.Query().Get("platform")); err != nil {
			return err
		}
	}
	if manifests && platform != nil {
		return httpx.BadRequest(errors.New("conflicting options: manifests and platform options cannot both be set"))
	}

	data, err := ir.translator.ImageInspect(r.Context(), name, imagebackend.ImageInspectOpts{
		Manifests: manifests,
		Identity:  versions.GreaterThanOrEqualTo(v, "1.53"),
		Platform:  platform,
	})
	if err != nil {
		return err
	}
	// Docker clients distinguish an empty list from a missing one.
	inspect := data.InspectResponse
	if inspect.RepoTags == nil {
		inspect.RepoTags = []string{}
	}
	if inspect.RepoDigests == nil {
		inspect.RepoDigests = []string{}
	}
	return httpx.WriteJSON(w, http.StatusOK, inspect)
}

func (ir *imageRouter) getImageAttestations(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}
	query := r.URL.Query()

	// The platform parameter is a multi-value array in the API so the wire
	// shape stays forward-compatible, but only one value is accepted.
	var platform *ocispec.Platform
	if values := query["platform"]; len(values) > 0 {
		if len(values) > 1 {
			return httpx.BadRequest(errors.New("only one platform value is currently supported"))
		}
		if platform, err = decodePlatform(values[0]); err != nil {
			return err
		}
	}

	var predicateTypes []string
	for _, predicate := range query["type"] {
		if predicate = strings.TrimSpace(predicate); predicate != "" {
			predicateTypes = append(predicateTypes, predicate)
		}
	}

	statements, err := ir.translator.ImageAttestations(r.Context(), name, imagebackend.AttestationOpts{
		Platform:         platform,
		PredicateTypes:   predicateTypes,
		IncludeStatement: types.ParseBool(query.Get("statement"), false),
	})
	if err != nil {
		return err
	}
	if statements == nil {
		statements = []imagetypes.AttestationStatement{}
	}
	return httpx.WriteJSON(w, http.StatusOK, statements)
}
