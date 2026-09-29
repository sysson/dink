package container

import (
	"net/http"

	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (cr *containerRouter) postContainersPrune(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	pruneFilters, err := filters.FromJSON(r.Form.Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	report, err := cr.translator.ContainerPrune(r.Context(), pruneFilters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}
