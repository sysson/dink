package container

import (
	"net/http"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/dink/pkg/httputils"
	"github.com/sysson/dink/dink/pkg/log"
)

func (cr *containerRouter) headContainersArchive(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersJSON(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersExport(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersChanges(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) getContainersByName(w http.ResponseWriter, r *http.Request) error {
	return nil
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
	if err := httputils.ParseJSON(r, &request); err != nil {
		return httputils.BadRequest(err)
	}
	ccr, err := cr.translator.ContainerCreate(r.Context(), backend.ContainerCreateConfig{
		Name:   name,
		Config: request.Config,
	})
	if err != nil {
		return httputils.InternalServerError(err)
	}
	if len(ccr.Warnings) > 0 {
		log.G(r.Context()).With("warnings", ccr.Warnings).Warn("container creation warnings")
	}

	return httputils.WriteJSON(w, http.StatusCreated, ccr)
}

func (cr *containerRouter) postContainersKill(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersPause(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersUnpause(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersRestart(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersStart(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (cr *containerRouter) postContainersStop(w http.ResponseWriter, r *http.Request) error {
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
	return nil
}
