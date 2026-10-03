package translator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

func TestBuildKitPruneRequest(t *testing.T) {
	options := buildbackend.CachePruneOptions{All: true, ReservedSpace: 100, MaxUsedSpace: 200, MinFreeSpace: 300}
	if err := json.Unmarshal([]byte(`{"id":{"abcd":true},"type":{"regular":true},"until":{"1h":true}}`), &options.Filters); err != nil {
		t.Fatal(err)
	}
	request, err := buildKitPruneRequest(options)
	if err != nil {
		t.Fatal(err)
	}
	if !request.All || request.ReservedSpace != 100 || request.MaxUsedSpace != 200 || request.MinFreeSpace != 300 {
		t.Fatalf("prune request = %+v", request)
	}
	if len(request.Filter) != 1 || request.Filter[0] != "id~=abcd,type==regular" {
		t.Fatalf("filters = %v", request.Filter)
	}
	if time.Duration(request.KeepDuration) < 59*time.Minute || time.Duration(request.KeepDuration) > time.Hour+time.Second {
		t.Fatalf("keep duration = %s, want about 1h", time.Duration(request.KeepDuration))
	}
}

func TestBuildKitPruneRequestRejectsUnsupportedOrAmbiguousFilters(t *testing.T) {
	for _, filter := range []string{
		`{"unknown":{"value":true}}`,
		`{"type":{"regular":true,"source.local":true}}`,
		`{"until":{"1h":true},"unused-for":{"2h":true}}`,
		`{"until":{"not-a-time":true}}`,
	} {
		options := buildbackend.CachePruneOptions{}
		if err := json.Unmarshal([]byte(filter), &options.Filters); err != nil {
			t.Fatal(err)
		}
		if _, err := buildKitPruneRequest(options); err == nil {
			t.Fatalf("invalid filter accepted: %+v", filter)
		}
	}
}
