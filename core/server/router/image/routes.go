package image

import (
	"errors"
	"net/http"
	"strings"

	"github.com/containerd/platforms"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client/pkg/versions"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
	"github.com/sysson/syskit/stream"
)

func (ir *imageRouter) getImagesJSON(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesSearch(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesGet(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesHistory(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImagesByName(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) getImageAttestations(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesLoad(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (ir *imageRouter) postImagesCreate(w http.ResponseWriter, r *http.Request) error {
	var (
		img         = r.URL.Query().Get("fromImage")
		tag         = r.URL.Query().Get("tag")
		repo        = r.URL.Query().Get("repo")
		_           = r.URL.Query().Get("message")
		progressErr error
		output      = iox.NewWriteFlusher(w)
		platform    *ocispec.Platform
	)
	defer func() {
		_ = output.Close()
	}()

	w.Header().Set("Content-Type", "application/json")

	v := version.FromContext(r.Context())
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
		authConfig, _ := types.DecodeRegistryAuthHeader(r.Header.Get(registry.AuthHeader))
		pullOptions := types.PullOptions{
			Auth:        authConfig,
			MetaHeaders: metaHeaders,
			OutStream:   output,
		}
		if platform != nil {
			pullOptions.Platforms = append(pullOptions.Platforms, *platform)
		}
		progressErr = ir.translator.PullImage(r.Context(), ref, pullOptions)
	} else {
		return httpx.BadRequest(errors.New("fromImage parameter is required"))
	}
	if progressErr != nil {
		if output.HasWritten() {
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
	return nil
}
