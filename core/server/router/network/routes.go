package network

import (
	"net/http"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/networkbackend"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (nr *networkRouter) getNetworksList(w http.ResponseWriter, r *http.Request) error {
	filters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	items, err := nr.translator.GetNetworkSummaries(r.Context(), filters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, items)
}

func (nr *networkRouter) getNetwork(w http.ResponseWriter, r *http.Request) error {
	result, err := nr.translator.GetNetwork(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (nr *networkRouter) postNetworkCreate(w http.ResponseWriter, r *http.Request) error {
	var request networktypes.CreateRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	result, err := nr.translator.CreateNetwork(r.Context(), request)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, result)
}

func (nr *networkRouter) postNetworkConnect(w http.ResponseWriter, r *http.Request) error {
	var request networkbackend.ConnectRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	if err := nr.translator.ConnectContainerToNetwork(r.Context(), r.PathValue("id"), request.Container, request.EndpointConfig); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (nr *networkRouter) postNetworkDisconnect(w http.ResponseWriter, r *http.Request) error {
	var request networkbackend.DisconnectRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	if err := nr.translator.DisconnectContainerFromNetwork(r.Context(), r.PathValue("id"), request.Container, request.Force); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (nr *networkRouter) postNetworkPrune(w http.ResponseWriter, r *http.Request) error {
	filters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	result, err := nr.translator.NetworkPrune(r.Context(), filters)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, result)
}

func (nr *networkRouter) deleteNetwork(w http.ResponseWriter, r *http.Request) error {
	if err := nr.translator.DeleteNetwork(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
