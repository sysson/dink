package image

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
	"github.com/sysson/syskit/stream"
)

func registryMetaHeaders(r *http.Request) map[string][]string {
	headers := map[string][]string{}
	for key, values := range r.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(key), "X-Meta-") {
			headers[http.CanonicalHeaderKey(key)] = values
		}
	}
	return headers
}

func (ir *imageRouter) getImagesSearch(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	var limit int
	if value := r.Form.Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 0 || limit > 100 {
			return httpx.BadRequest(errors.New("limit must be between 1 and 100, or 0 for the default"))
		}
	}
	searchFilters, err := filters.FromJSON(r.Form.Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	auth, err := authconfig.Decode(r.Header.Get(registry.AuthHeader))
	if err != nil {
		return httpx.BadRequest(err)
	}
	results, err := ir.searcher.Search(r.Context(), searchFilters, r.Form.Get("term"), limit, auth, registryMetaHeaders(r))
	if err != nil {
		return err
	}
	if results == nil {
		results = []registry.SearchResult{}
	}
	return httpx.WriteJSON(w, http.StatusOK, results)
}

func (ir *imageRouter) postImagesPush(w http.ResponseWriter, r *http.Request) error {
	name, err := imageName(r)
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	ref, err := ociref.ParseRelative(name)
	if err != nil {
		return httpx.BadRequest(err)
	}
	if tag := r.Form.Get("tag"); tag != "" {
		if !ociref.IsValidTag(tag) {
			return httpx.BadRequest(fmt.Errorf("invalid tag %q", tag))
		}
		ref.Tag = tag
	}
	if ref.Digest != "" {
		return httpx.BadRequest(errors.New("pushing by digest is not supported; use a tag"))
	}
	auth, err := authconfig.Decode(r.Header.Get(registry.AuthHeader))
	if err != nil {
		return httpx.BadRequest(err)
	}
	rw := iox.NewResponseWrapper(w)
	output := iox.NewWriteFlusher(rw)
	defer func() { _ = output.Close() }()
	options := imagebackend.PushOptions{AuthConfig: auth, MetaHeaders: registryMetaHeaders(r), OutStream: output}
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.46") {
		platform, err := decodePlatform(r.Form.Get("platform"))
		if err != nil {
			return err
		}
		if platform != nil {
			options.Platforms = append(options.Platforms, *platform)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := ir.translator.PushImage(r.Context(), ref, options); err != nil {
		if rw.StatusCode() == 0 {
			return err
		}
		streamErr := err
		if httpErr, ok := errors.AsType[*httpx.HTTPError](err); ok {
			streamErr = &stream.StreamError{Code: httpErr.StatusCode, Message: httpErr.Message.Error()}
		}
		_, writeErr := output.Write(stream.FormatError(streamErr))
		return writeErr
	}
	return nil
}
