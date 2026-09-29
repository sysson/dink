package volume

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

type volumeStub struct {
	Translator
	created volumetypes.CreateRequest
	removed string
	force   bool
}

func (s *volumeStub) ListVolumes(_ context.Context, _ filters.Args) ([]volumetypes.Volume, []string, error) {
	return []volumetypes.Volume{{Name: "data", Driver: "local"}}, []string{}, nil
}

func (s *volumeStub) GetVolume(_ context.Context, name string) (*volumetypes.Volume, error) {
	return &volumetypes.Volume{Name: name, Driver: "local"}, nil
}

func (s *volumeStub) CreateVolume(_ context.Context, request volumetypes.CreateRequest) (*volumetypes.Volume, error) {
	s.created = request
	return &volumetypes.Volume{Name: request.Name, Driver: "local"}, nil
}

func (s *volumeStub) RemoveVolume(_ context.Context, name string, force bool) error {
	s.removed, s.force = name, force
	return nil
}

func (s *volumeStub) PruneVolumes(context.Context, filters.Args) (*volumetypes.PruneReport, error) {
	return &volumetypes.PruneReport{VolumesDeleted: []string{"data"}}, nil
}

func TestVolumeRoutes(t *testing.T) {
	stub := &volumeStub{}
	api := &volumeRouter{backend: stub}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request) error
		method  string
		path    string
		body    string
		status  int
	}{
		{name: "list", handler: api.getVolumesList, method: http.MethodGet, path: "/volumes", status: http.StatusOK},
		{name: "create", handler: api.postVolumesCreate, method: http.MethodPost, path: "/volumes/create", body: `{"Name":"data"}`, status: http.StatusCreated},
		{name: "inspect", handler: api.getVolumeByName, method: http.MethodGet, path: "/volumes/data", status: http.StatusOK},
		{name: "prune", handler: api.postVolumesPrune, method: http.MethodPost, path: "/volumes/prune", status: http.StatusOK},
		{name: "remove", handler: api.deleteVolumes, method: http.MethodDelete, path: "/volumes/data?force=true", status: http.StatusNoContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.SetPathValue("name", "data")
			response := httptest.NewRecorder()
			if err := test.handler(response, request); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusNoContent {
				if response.Body.Len() != 0 {
					t.Fatalf("remove response body = %q", response.Body.String())
				}
				return
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			switch test.name {
			case "list":
				if _, ok := body["Volumes"]; !ok {
					t.Fatalf("list response = %s, missing Volumes", response.Body.String())
				}
			case "prune":
				if _, ok := body["VolumesDeleted"]; !ok {
					t.Fatalf("prune response = %s, missing VolumesDeleted", response.Body.String())
				}
			}
		})
	}
	if stub.created.Name != "data" {
		t.Fatalf("CreateVolume request = %+v", stub.created)
	}
	if stub.removed != "data" || !stub.force {
		t.Fatalf("RemoveVolume = %q, force %t", stub.removed, stub.force)
	}
	err := api.putVolumesUpdate(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/volumes/data", nil))
	if httpErr, ok := errors.AsType[*httpx.HTTPError](err); !ok || httpErr.StatusCode != http.StatusNotImplemented {
		t.Fatalf("volume update error = %v, want 501", err)
	}
}
