package distribution

import (
	"net/http"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/sysson/syskit/httpx"
)

func (dr *distributionRouter) getDistributionInfo(w http.ResponseWriter, r *http.Request) error {
	auth, err := authconfig.Decode(r.Header.Get(registry.AuthHeader))
	if err != nil {
		return httpx.BadRequest(err)
	}
	result, err := dr.translator.GetDistributionInfo(r.Context(), r.PathValue("name"), auth)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}
