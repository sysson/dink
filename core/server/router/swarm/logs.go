package swarm

import (
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/moby/moby/api/types"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

// swarmLogs streams the Pod logs behind a service or task. Kubernetes cannot
// separate stdout from stderr, so it mirrors the container log endpoint.
func (sr *swarmRouter) swarmLogs(w http.ResponseWriter, r *http.Request, selector *backend.LogSelector) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	stdout, err := queryBool(r, "stdout")
	if err != nil {
		return err
	}
	stderr, err := queryBool(r, "stderr")
	if err != nil {
		return err
	}
	if !stdout && !stderr {
		return httpx.BadRequest(fmt.Errorf("must specify at least one of 'stdout' or 'stderr'"))
	}
	if !stdout {
		return httpx.NewHTTPError(http.StatusNotImplemented, fmt.Errorf("kubernetes pod logs cannot select stderr independently"))
	}
	details, err := queryBool(r, "details")
	if err != nil {
		return err
	}
	if details {
		return httpx.NewHTTPError(http.StatusNotImplemented, fmt.Errorf("kubernetes pod logs do not provide Docker log attributes"))
	}
	since, err := parseLogTime(r.Form.Get("since"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	follow, err := queryBool(r, "follow")
	if err != nil {
		return err
	}
	timestamps, err := queryBool(r, "timestamps")
	if err != nil {
		return err
	}
	options := &backend.ContainerLogsOptions{
		ShowStdout: stdout,
		ShowStderr: stderr,
		Since:      since,
		Follow:     follow,
		Timestamps: timestamps,
		Tail:       r.Form.Get("tail"),
	}
	messages, err := sr.translator.ServiceLogs(r.Context(), selector, options)
	if err != nil {
		return err
	}
	contentType := types.MediaTypeRawStream
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.42") {
		contentType = types.MediaTypeMultiplexedStream
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return nil
		case message, ok := <-messages:
			if !ok {
				return nil
			}
			if message.Err != nil {
				return message.Err
			}
			line := message.Line
			if options.Timestamps {
				line = append([]byte(message.Timestamp.Format(time.RFC3339Nano)+" "), line...)
			}
			var header [8]byte
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
			if _, err := w.Write(header[:]); err != nil {
				return err
			}
			if _, err := w.Write(line); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func parseLogTime(raw string) (time.Time, error) {
	if raw == "" || raw == "0" {
		return time.Time{}, nil
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return time.Time{}, fmt.Errorf("invalid log timestamp %q", raw)
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second))), nil
}
