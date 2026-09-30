package build

import (
	"net/http"

	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/syskit/httpx"
)

func (br *buildRouter) postBuild(w http.ResponseWriter, r *http.Request) error {
	// Implement the postBuild handler
	return nil
}

func (br *buildRouter) postPrune(w http.ResponseWriter, r *http.Request) error {
	report, err := br.translator.PruneCache(r.Context(), buildbackend.CachePruneOptions{})
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}

func (br *buildRouter) postCancel(w http.ResponseWriter, r *http.Request) error {
	// Implement the postCancel handler
	return nil
}
