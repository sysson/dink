package container

import (
	"io"
	"net/http"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) getContainersStats(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	stream := true
	if r.Form.Has("stream") {
		var err error
		stream, err = queryBool(r, "stream")
		if err != nil {
			return err
		}
	}
	var oneShot bool
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.41") {
		var err error
		oneShot, err = queryBool(r, "one-shot")
		if err != nil {
			return err
		}
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
	}
	return cr.translator.ContainerStats(r.Context(), r.PathValue("name"), &backend.ContainerStatsConfig{
		Stream: stream, OneShot: oneShot,
		OutStream: func() io.Writer {
			w.WriteHeader(http.StatusOK)
			if stream {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			return w
		},
	})
}
