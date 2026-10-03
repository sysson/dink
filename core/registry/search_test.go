package registry

import (
	"context"
	"testing"

	"github.com/sysson/dink/pkg/filters"
)

func TestSearchEndpoint(t *testing.T) {
	for _, tc := range []struct{ term, host, query, scheme string }{
		{"alpine", "index.docker.io", "alpine", "https"},
		{"library/alpine", "index.docker.io", "alpine", "https"},
		{"docker.io/library/alpine", "index.docker.io", "alpine", "https"},
		{"myuser/app", "index.docker.io", "myuser/app", "https"},
		{"example.com/myuser/app", "example.com", "myuser/app", "https"},
		{"127.0.0.1:5000/app", "127.0.0.1:5000", "app", "http"},
	} {
		t.Run(tc.term, func(t *testing.T) {
			endpoint, err := searchEndpoint(tc.term)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint.Host != tc.host || endpoint.Query().Get("q") != tc.query || endpoint.Scheme != tc.scheme || endpoint.Path != "/v1/search" {
				t.Fatalf("endpoint = %s", endpoint)
			}
		})
	}
	for _, term := range []string{"", "https://example.com/app", "example.com/", "user:password@example.com/app"} {
		if _, err := searchEndpoint(term); err == nil {
			t.Fatalf("invalid term %q accepted", term)
		}
	}
}

func TestSearchValidation(t *testing.T) {
	service := Unavailable(nil)
	for _, f := range []filters.Args{
		filters.NewArgs(filters.Arg("unknown", "true")),
		filters.NewArgs(filters.Arg("stars", "bad")),
		filters.NewArgs(filters.Arg("is-official", "bad")),
		filters.NewArgs(filters.Arg("is-automated", "bad")),
	} {
		if _, err := service.Search(context.Background(), f, "alpine", 0, nil, nil); err == nil {
			t.Fatalf("invalid filter %+v accepted", f)
		}
	}
	for _, limit := range []int{-1, 101} {
		if _, err := service.Search(context.Background(), filters.NewArgs(), "alpine", limit, nil, nil); err == nil {
			t.Fatalf("invalid limit %d accepted", limit)
		}
	}
	results, err := service.Search(context.Background(), filters.NewArgs(filters.Arg("is-automated", "true")), "alpine", 0, nil, nil)
	if err != nil || results == nil || len(results) != 0 {
		t.Fatalf("deprecated automated search = %v, %v", results, err)
	}
}
