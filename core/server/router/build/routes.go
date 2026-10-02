package build

import (
	"errors"
	"net/http"

	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/translator"
	"github.com/sysson/syskit/httpx"
)

func (br *buildRouter) postBuild(w http.ResponseWriter, r *http.Request) error {
	return translator.Unsupported(errors.New("POST /build is not supported; use Docker Buildx with the Docker driver"))
}

func (br *buildRouter) postPrune(w http.ResponseWriter, r *http.Request) error {
	report, err := br.translator.PruneCache(r.Context(), buildbackend.CachePruneOptions{})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}

func (br *buildRouter) postCancel(w http.ResponseWriter, r *http.Request) error {
	return translator.Unsupported(errors.New("POST /build/cancel is not supported; cancel the Buildx request"))
}
