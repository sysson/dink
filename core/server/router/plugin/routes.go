package plugin

import (
	"net/http"

	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (pr *pluginRouter) listPlugins(w http.ResponseWriter, r *http.Request) error {
	args, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	result, err := pr.translator.ListPlugins(r.Context(), args)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (pr *pluginRouter) inspectPlugin(w http.ResponseWriter, r *http.Request) error {
	result, err := pr.translator.InspectPlugin(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (pr *pluginRouter) getPrivileges(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) removePlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) enablePlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) disablePlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) pullPlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) pushPlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) upgradePlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) setPlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (pr *pluginRouter) createPlugin(w http.ResponseWriter, r *http.Request) error {
	return nil
}
