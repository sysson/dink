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
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/logx"
)

func (cr *containerRouter) headContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersJSON(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	filter, err := filters.FromJSON(r.Form.Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	filterJSON, err := filters.ToJSON(filter)
	if err != nil {
		return httpx.BadRequest(err)
	}
	if filterJSON == "" {
		filterJSON = "{}"
	}
	limit := 0
	if rawLimit := r.Form.Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil {
			return httpx.BadRequest(err)
		}
	}
	all, err := queryBool(r, "all")
	if err != nil {
		return err
	}
	size, err := queryBool(r, "size")
	if err != nil {
		return err
	}
	options := &backend.ContainerListOptions{
		All:   all,
		Size:  size,
		Limit: limit,
	}
	if err := json.Unmarshal([]byte(filterJSON), &options.Filters); err != nil {
		return httpx.BadRequest(err)
	}
	containers, err := cr.translator.Containers(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, containers)
}

func (cr *containerRouter) getContainersExport(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersChanges(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersByName(w http.ResponseWriter, r *http.Request) error {
	size, err := queryBool(r, "size")
	if err != nil {
		return err
	}
	result, _, err := cr.translator.ContainerInspect(r.Context(), r.PathValue("name"), backend.ContainerInspectOptions{Size: size})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (cr *containerRouter) getContainersTop(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersLogs(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersStats(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) wsContainersAttach(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getExecByID(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersCreate(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("name")
	var request container.CreateRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	ccr, err := cr.translator.ContainerCreate(r.Context(), backend.ContainerCreateConfig{
		Name:             name,
		Config:           request.Config,
		HostConfig:       request.HostConfig,
		NetworkingConfig: request.NetworkingConfig,
	})
	if err != nil {
		// The translator reports client errors as httpx errors; anything else becomes a 500.
		return err
	}
	if len(ccr.Warnings) > 0 {
		logx.G(r.Context()).With("warnings", ccr.Warnings).Warn("container creation warnings")
	}

	return httpx.WriteJSON(w, http.StatusCreated, ccr)
}

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
	return nil
}

func (cr *containerRouter) postContainersResize(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersAttach(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainerExecCreate(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainerExecStart(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainerExecResize(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainerRename(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainerUpdate(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersPrune(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postCommit(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) putContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return nil
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

func queryBool(r *http.Request, key string) (bool, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, httpx.BadRequest(fmt.Errorf("invalid %s value %q", key, value))
	}
	return parsed, nil
}
