package container

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) getExecByID(w http.ResponseWriter, r *http.Request) error {
	result, err := cr.translator.ContainerExecInspect(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (cr *containerRouter) postContainerExecCreate(w http.ResponseWriter, r *http.Request) error {
	var config container.ExecCreateRequest
	if err := httpx.ParseJSON(r, &config); err != nil {
		return httpx.BadRequest(err)
	}
	if len(config.Cmd) == 0 {
		return httpx.BadRequest(fmt.Errorf("no exec command specified"))
	}
	id, err := cr.translator.ContainerExecCreate(r.Context(), r.PathValue("name"), &config)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, container.ExecCreateResponse{ID: id})
}

func (cr *containerRouter) postContainerExecStart(w http.ResponseWriter, r *http.Request) error {
	var options container.ExecStartRequest
	if err := httpx.ParseJSON(r, &options); err != nil {
		return httpx.BadRequest(err)
	}
	if options.Detach {
		return httpx.NewHTTPError(http.StatusNotImplemented, fmt.Errorf("detached exec is not supported"))
	}
	execID := r.PathValue("name")
	inspect, err := cr.translator.ContainerExecInspect(r.Context(), execID)
	if err != nil {
		return err
	}
	if inspect.ProcessConfig == nil || inspect.ProcessConfig.Tty != options.Tty {
		return httpx.BadRequest(fmt.Errorf("exec TTY must match the create request"))
	}
	if inspect.Running || inspect.ExitCode != nil {
		return httpx.Conflict(fmt.Errorf("exec %s has already started", execID))
	}
	conn, stdin, stdout, stderr, err := hijackDockerStreams(w, r, options.Tty)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	streams := backend.ExecStartConfig{Stdout: stdout, Stderr: stderr, ConsoleSize: options.ConsoleSize}
	if inspect.OpenStdin {
		streams.Stdin = stdin
	}
	if err := cr.translator.ContainerExecStart(r.Context(), execID, streams); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
	}
	return nil
}

func (cr *containerRouter) postContainerExecResize(w http.ResponseWriter, r *http.Request) error {
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
	if err := cr.translator.ContainerExecResize(r.Context(), r.PathValue("name"), uint32(height), uint32(width)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}
