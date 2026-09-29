package image

import (
	"errors"
	"net/http"
	"strings"

	"github.com/containerd/platforms"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
	"github.com/sysson/syskit/stream"
)

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
