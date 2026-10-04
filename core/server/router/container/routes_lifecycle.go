package container

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) postContainersKill(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	if err := cr.translator.ContainerKill(r.Context(), r.PathValue("name"), r.Form.Get("signal")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersPause(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	if err := cr.translator.ContainerPause(r.Context(), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersUnpause(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	if err := cr.translator.ContainerUnpause(r.Context(), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersRestart(w http.ResponseWriter, r *http.Request) error {
	options, err := parseContainerStopOptions(r)
	if err != nil {
		return err
	}
	if err := cr.translator.ContainerRestart(r.Context(), r.PathValue("name"), options); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersStart(w http.ResponseWriter, r *http.Request) error {
	if r.ContentLength > 7 || r.ContentLength == -1 {
		return httpx.BadRequest(fmt.Errorf("starting a container with a request body is not supported"))
	}
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	if err := cr.translator.ContainerStart(r.Context(), r.PathValue("name"), r.Form.Get("checkpoint"), r.Form.Get("checkpoint-dir")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersStop(w http.ResponseWriter, r *http.Request) error {
	options, err := parseContainerStopOptions(r)
	if err != nil {
		return err
	}
	if err := cr.translator.ContainerStop(r.Context(), r.PathValue("name"), options); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainersWait(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	condition := container.WaitConditionNotRunning
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.30") && r.Form.Get("condition") != "" {
		condition = container.WaitCondition(r.Form.Get("condition"))
	}
	switch condition {
	case container.WaitConditionNotRunning, container.WaitConditionNextExit, container.WaitConditionRemoved:
	default:
		return httpx.BadRequest(fmt.Errorf("invalid wait condition %q", condition))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	response, err := cr.translator.ContainerWait(r.Context(), r.PathValue("name"), condition)
	if err != nil {
		if r.Context().Err() != nil {
			return nil
		}
		response = container.WaitResponse{Error: &container.WaitExitError{Message: err.Error()}}
	}
	return json.NewEncoder(w).Encode(response)
}

func (cr *containerRouter) postContainerRename(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	name := r.Form.Get("name")
	if name == "" {
		return httpx.BadRequest(fmt.Errorf("new name is required"))
	}
	if err := cr.translator.ContainerRename(r.Context(), r.PathValue("name"), name); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (cr *containerRouter) postContainerUpdate(w http.ResponseWriter, r *http.Request) error {
	var config container.UpdateConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		return httpx.BadRequest(err)
	}
	response, err := cr.translator.ContainerUpdate(r.Context(), r.PathValue("name"), &config)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, response)
}

func (cr *containerRouter) deleteContainers(w http.ResponseWriter, r *http.Request) error {
	force, err := queryBool(r, "force")
	if err != nil {
		return err
	}
	removeVolume, err := queryBool(r, "v")
	if err != nil {
		return err
	}
	removeLink, err := queryBool(r, "link")
	if err != nil {
		return err
	}
	if err := cr.translator.ContainerRm(r.Context(), r.PathValue("name"), &backend.ContainerRmConfig{
		ForceRemove:  force,
		RemoveVolume: removeVolume,
		RemoveLink:   removeLink,
	}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func parseContainerStopOptions(r *http.Request) (backend.ContainerStopOptions, error) {
	if err := r.ParseForm(); err != nil {
		return backend.ContainerStopOptions{}, httpx.BadRequest(err)
	}
	var options backend.ContainerStopOptions
	if versions.GreaterThanOrEqualTo(version.VersionFromRequest(r), "1.42") {
		options.Signal = r.Form.Get("signal")
	}
	if timeout := r.Form.Get("t"); timeout != "" {
		seconds, err := strconv.Atoi(timeout)
		if err != nil {
			return backend.ContainerStopOptions{}, httpx.BadRequest(err)
		}
		options.Timeout = &seconds
	}
	return options, nil
}
