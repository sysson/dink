package network

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/pkg/filters"
)

type networkStub struct{}

func (networkStub) GetNetworkSummaries(_ context.Context, _ filters.Args) ([]networktypes.Summary, error) {
	return []networktypes.Summary{}, nil
}
func (networkStub) GetNetwork(_ context.Context, _ string) (networktypes.Inspect, error) {
	return networktypes.Inspect{Name: "sample", ID: "abc", Containers: map[string]networktypes.EndpointResource{}}, nil
}
func (networkStub) CreateNetwork(_ context.Context, _ networktypes.CreateRequest) (networktypes.CreateResponse, error) {
	return networktypes.CreateResponse{ID: "abc"}, nil
}
func (networkStub) ConnectContainerToNetwork(context.Context, string, string, *networktypes.EndpointSettings) error {
	return nil
}
func (networkStub) DisconnectContainerFromNetwork(context.Context, string, string, bool) error {
	return nil
}
func (networkStub) DeleteNetwork(_ context.Context, _ string) error { return nil }
func (networkStub) NetworkPrune(_ context.Context, _ filters.Args) (networktypes.PruneReport, error) {
	return networktypes.PruneReport{NetworksDeleted: []string{"sample"}}, nil
}

func TestNetworkResponses(t *testing.T) {
	api := &networkRouter{translator: networkStub{}}
	tests := []struct {
		name      string
		handler   func(http.ResponseWriter, *http.Request) error
		method    string
		path      string
		body      string
		status    int
		jsonField string
	}{
		{"create", api.postNetworkCreate, http.MethodPost, "/networks/create", `{"Name":"sample"}`, http.StatusCreated, "Id"},
		{"list", api.getNetworksList, http.MethodGet, "/networks", "", http.StatusOK, ""},
		{"inspect", api.getNetwork, http.MethodGet, "/networks/abc", "", http.StatusOK, "Containers"},
		{"prune", api.postNetworkPrune, http.MethodPost, "/networks/prune", "", http.StatusOK, "NetworksDeleted"},
		{"delete", api.deleteNetwork, http.MethodDelete, "/networks/abc", "", http.StatusNoContent, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.SetPathValue("id", "abc")
			response := httptest.NewRecorder()
			if err := test.handler(response, request); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status {
				t.Fatalf("status %d, want %d", response.Code, test.status)
			}
			if test.status == http.StatusNoContent {
				if response.Body.Len() != 0 {
					t.Fatalf("delete returned a body: %s", response.Body.String())
				}
				return
			}
			var body any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if test.name == "list" {
				if _, ok := body.([]any); !ok {
					t.Fatalf("list is not a JSON array: %s", response.Body.String())
				}
			} else if _, ok := body.(map[string]any)[test.jsonField]; !ok {
				t.Fatalf("missing %s: %s", test.jsonField, response.Body.String())
			}
		})
	}
}
