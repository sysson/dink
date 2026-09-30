package build

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

type pruneTranslatorStub struct {
	Translator
}

func (pruneTranslatorStub) PruneCache(context.Context, buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	return &buildtypes.CachePruneReport{CachesDeleted: []string{}}, nil
}

func TestPostPruneWritesReport(t *testing.T) {
	api := &buildRouter{translator: pruneTranslatorStub{}}
	response := httptest.NewRecorder()
	if err := api.postPrune(response, httptest.NewRequest(http.MethodPost, "/build/prune", nil)); err != nil {
		t.Fatal(err)
	}
	var report buildtypes.CachePruneReport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.CachesDeleted == nil {
		t.Fatalf("response = %d %q, want an empty prune report", response.Code, response.Body.String())
	}
}
