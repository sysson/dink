package container

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) postContainersAttach(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	stdin, err := queryBool(r, "stdin")
	if err != nil {
		return err
	}
	stdout, err := queryBool(r, "stdout")
	if err != nil {
		return err
	}
	stderr, err := queryBool(r, "stderr")
	if err != nil {
		return err
	}
	logs, err := queryBool(r, "logs")
	if err != nil {
		return err
	}
	stream, err := queryBool(r, "stream")
	if err != nil {
		return err
	}
	if !stdin && !stdout && !stderr {
		return httpx.BadRequest(fmt.Errorf("must specify at least one attach stream"))
	}
	hijacked := false
	err = cr.translator.ContainerAttach(r.Context(), r.PathValue("name"), &backend.ContainerAttachConfig{
		UseStdin: stdin, UseStdout: stdout, UseStderr: stderr, Logs: logs, Stream: stream,
		DetachKeys: r.Form.Get("detachKeys"), MuxStreams: true,
		GetStreams: func(multiplexed bool, cancel func()) (io.ReadCloser, io.Writer, io.Writer, error) {
			conn, input, output, errorsOutput, err := hijackDockerStreams(w, r, !multiplexed)
			if err != nil {
				return nil, nil, nil, err
			}
			hijacked = true
			return &dockerAttachInput{Reader: input, Closer: conn}, output, errorsOutput, nil
		},
	})
	if hijacked {
		return nil
	}
	return err
}

func (cr *containerRouter) postContainersResize(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	height, err := strconv.ParseUint(r.Form.Get("h"), 10, 32)
	if err != nil {
		return httpx.BadRequest(fmt.Errorf("invalid resize height %q", r.Form.Get("h")))
	}
	width, err := strconv.ParseUint(r.Form.Get("w"), 10, 32)
	if err != nil {
		return httpx.BadRequest(fmt.Errorf("invalid resize width %q", r.Form.Get("w")))
	}
	if err := cr.translator.ContainerResize(r.Context(), r.PathValue("name"), uint32(height), uint32(width)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}
