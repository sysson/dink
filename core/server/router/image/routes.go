package image

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/containerd/platforms"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/pkg/authconfig"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
	"github.com/sysson/syskit/stream"
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
func (ir *imageRouter) getImagesSearch(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesGet(w http.ResponseWriter, r *http.Request) error {
	return nil
}

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

// imageName is the image a request names, which may be a repository with an
// optional tag, a digest reference or a full or truncated image ID.
func imageName(r *http.Request) (string, error) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		return "", httpx.BadRequest(errors.New("name parameter is required"))
	}
	return name, nil
}

// decodePlatform reads the JSON-encoded OCI platform docker clients send.
func decodePlatform(value string) (*ocispec.Platform, error) {
	if value == "" {
		return nil, nil
	}
	var platform ocispec.Platform
	if err := json.Unmarshal([]byte(value), &platform); err != nil {
		return nil, httpx.BadRequest(fmt.Errorf("failed to parse platform: %w", err))
	}
	if platform.OS == "" || platform.Architecture == "" {
		return nil, httpx.BadRequest(errors.New("both OS and Architecture must be provided"))
	}
	return &platform, nil
}

func (ir *imageRouter) postImagesLoad(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesCreate(w http.ResponseWriter, r *http.Request) error {
	var (
		rw          = iox.NewResponseWrapper(w)
		img         = r.URL.Query().Get("fromImage")
		tag         = r.URL.Query().Get("tag")
		repo        = r.URL.Query().Get("repo")
		_           = r.URL.Query().Get("message")
		progressErr error
		output      = iox.NewWriteFlusher(rw)
		platform    *ocispec.Platform
	)
	defer func() {
		_ = output.Close()
	}()

	w.Header().Set("Content-Type", "application/json")

	v := version.VersionFromRequest(r)
	if versions.GreaterThanOrEqualTo(v, "1.32") {
		if p := r.URL.Query().Get("platform"); p != "" {
			sp, err := platforms.Parse(p)
			if err != nil {
				return httpx.BadRequest(err)
			}
			platform = &sp
		}
	}

	if img != "" {
		metaHeaders := map[string][]string{}
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Meta-") {
				metaHeaders[k] = v
			}
		}
		name := img
		if repo != "" {
			name = repo + "/" + name
		}
		if tag != "" {
			// Docker clients send a digest in the tag parameter when pulling
			// by digest.
			if _, err := ocidigest.Parse(tag); err == nil {
				name = name + "@" + tag
			} else {
				name = name + ":" + tag
			}
		}
		ref, err := ociref.ParseRelative(name)
		if err != nil {
			return httpx.BadRequest(err)
		}
		authconfig, err := authconfig.Decode(r.Header.Get(registry.AuthHeader))
		if err != nil {
			return httpx.BadRequest(err)
		}
		pullOptions := imagebackend.PullOptions{
			AuthConfig:  authconfig,
			MetaHeaders: metaHeaders,
			OutStream:   output,
			Platforms:   []ocispec.Platform{},
		}
		if platform != nil {
			pullOptions.Platforms = append(pullOptions.Platforms, *platform)
		}
		progressErr = ir.translator.PullImage(r.Context(), ref, pullOptions)
	} else {
		return httpx.BadRequest(errors.New("fromImage parameter is required"))
	}

	if progressErr != nil {
		if rw.StatusCode() != 0 {
			_, _ = output.Write(stream.FormatError(progressErr))
		} else {
			return progressErr
		}
	}
	return nil
}

func (ir *imageRouter) postImagesPush(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesTag(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesPrune(w http.ResponseWriter, r *http.Request) error {
	return nil
}

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
