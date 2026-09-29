package container

import (
	"net/http"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/logx"
)

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
