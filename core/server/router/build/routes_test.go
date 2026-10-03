package build

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

type pruneTranslatorStub struct {
	Translator
	options buildbackend.CachePruneOptions
}

func (s *pruneTranslatorStub) PruneCache(_ context.Context, options buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	s.options = options
	return &buildtypes.CachePruneReport{CachesDeleted: []string{}}, nil
}

func TestPostPruneWritesReport(t *testing.T) {
	translator := &pruneTranslatorStub{}
	api := &buildRouter{translator: translator}
	response := httptest.NewRecorder()
	if err := api.postPrune(response, httptest.NewRequest(http.MethodPost, "/build/prune", nil)); err != nil {
		t.Fatal(err)
	}
	var report buildtypes.CachePruneReport
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &report) != nil || report.CachesDeleted == nil {
		t.Fatalf("response = %d %q, want an empty prune report", response.Code, response.Body.String())
	}
	if translator.options.All || translator.options.Filters.Len() != 0 {
		t.Fatalf("default prune options = %+v", translator.options)
	}
}

func TestPostPruneParsesDockerOptions(t *testing.T) {
	translator := &pruneTranslatorStub{}
	api := &buildRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/build/prune", strings.NewReader(
		"all=1&filters=%7B%22type%22%3A%7B%22regular%22%3Atrue%7D%7D&reserved-space=100&max-used-space=200&min-free-space=300",
	))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("version", "1.55")

	if err := api.postPrune(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	options := translator.options
	if !options.All || options.ReservedSpace != 100 || options.MaxUsedSpace != 200 || options.MinFreeSpace != 300 ||
		!options.Filters.ExactMatch("type", "regular") {
		t.Fatalf("prune options = %+v", options)
	}
}

func TestPostPruneParsesLegacyOptions(t *testing.T) {
	translator := &pruneTranslatorStub{}
	api := &buildRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/build/prune", strings.NewReader(
		"filters=%7B%22type%22%3A%5B%22regular%22%5D%7D&keep-storage=100",
	))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetPathValue("version", "1.47")

	if err := api.postPrune(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	if translator.options.ReservedSpace != 100 || !translator.options.Filters.ExactMatch("type", "regular") {
		t.Fatalf("legacy prune options = %+v", translator.options)
	}
}

func TestPostPruneRejectsInvalidByteOptions(t *testing.T) {
	translator := &pruneTranslatorStub{}
	api := &buildRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/build/prune?reserved-space=not-bytes", nil)
	request.SetPathValue("version", "1.55")
	if err := api.postPrune(httptest.NewRecorder(), request); err == nil {
		t.Fatal("invalid reserved-space accepted")
	}
	if translator.options.Filters.Len() != 0 {
		t.Fatal("translator called after invalid request parameters")
	}
}
