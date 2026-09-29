package container

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

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
