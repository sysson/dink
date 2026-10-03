package image

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/pkg/authconfig"
	registrytypes "github.com/moby/moby/api/types/registry"
	mobyclient "github.com/moby/moby/client"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/stream"
)

type searchStub struct {
	term    string
	limit   int
	filters filters.Args
	auth    *registrytypes.AuthConfig
	headers map[string][]string
	results []registrytypes.SearchResult
	err     error
}

func (s *searchStub) Search(_ context.Context, f filters.Args, term string, limit int, auth *registrytypes.AuthConfig, headers map[string][]string) ([]registrytypes.SearchResult, error) {
	s.term, s.limit, s.filters, s.auth, s.headers = term, limit, f, auth, headers
	return s.results, s.err
}

func TestGetImagesSearch(t *testing.T) {
	auth, err := authconfig.Encode(registrytypes.AuthConfig{Username: "tester", Password: "test-password"})
	if err != nil {
		t.Fatal(err)
	}
	query := url.Values{"term": {"alpine"}, "limit": {"7"}, "filters": {`{"stars":{"10":true}}`}}
	request := httptest.NewRequest(http.MethodGet, "/images/search?"+query.Encode(), nil)
	request.Header.Set(registrytypes.AuthHeader, auth)
	request.Header.Set("X-Meta-Test", "search")
	request.Header.Set("X-Unrelated", "ignored")
	stub := &searchStub{results: []registrytypes.SearchResult{{Name: "alpine", IsOfficial: true, StarCount: 100}}}
	response := httptest.NewRecorder()
	if err := (&imageRouter{searcher: stub}).getImagesSearch(response, request); err != nil {
		t.Fatal(err)
	}
	if stub.term != "alpine" || stub.limit != 7 || !stub.filters.ExactMatch("stars", "10") ||
		stub.auth.Username != "tester" || stub.headers["X-Meta-Test"][0] != "search" || len(stub.headers) != 1 {
		t.Fatalf("search call = %+v", stub)
	}
	var results []registrytypes.SearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || len(results) != 1 || results[0].Name != "alpine" {
		t.Fatalf("search response = %d %s", response.Code, response.Body.String())
	}
	stub.results = nil
	response = httptest.NewRecorder()
	if err := (&imageRouter{searcher: stub}).getImagesSearch(response, httptest.NewRequest(http.MethodGet, "/images/search?term=alpine", nil)); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("empty results = %q", response.Body.String())
	}
	for _, query := range []string{"limit=abc", "limit=-1", "limit=101", "filters=invalid"} {
		if err := (&imageRouter{searcher: stub}).getImagesSearch(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/images/search?"+query, nil)); err == nil {
			t.Fatalf("invalid query %q accepted", query)
		}
	}
	stub.err = errors.New("search failed")
	if err := (&imageRouter{searcher: stub}).getImagesSearch(httptest.NewRecorder(), request); !errors.Is(err, stub.err) {
		t.Fatalf("search error = %v", err)
	}
}

type pushStub struct {
	Translator
	ref      ociref.Reference
	options  imagebackend.PushOptions
	progress bool
	err      error
}

func (s *pushStub) PushImage(_ context.Context, ref ociref.Reference, options imagebackend.PushOptions) error {
	s.ref, s.options = ref, options
	if s.progress {
		if _, err := options.OutStream.Write(stream.FormatStatus("", "Pushing")); err != nil {
			return err
		}
	}
	return s.err
}

func TestPostImagesPush(t *testing.T) {
	for _, tc := range []struct {
		version, name, query, tag string
		platforms                 int
	}{
		{"1.46", "example.com/app", `?tag=v2&platform=` + url.QueryEscape(`{"os":"linux","architecture":"arm64"}`), "v2", 1},
		{"1.45", "example.com/app", "?tag=v2&platform=invalid", "v2", 0},
		{"1.55", "example.com/app:v1", "", "v1", 0},
		{"1.55", "example.com/app", "", "", 0},
	} {
		t.Run(tc.version+tc.name+tc.query, func(t *testing.T) {
			auth, err := authconfig.Encode(registrytypes.AuthConfig{RegistryToken: "test-token"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/images/"+tc.name+"/push"+tc.query, nil)
			request.SetPathValue("name", tc.name)
			request.SetPathValue("version", tc.version)
			request.Header.Set(registrytypes.AuthHeader, auth)
			request.Header.Set("X-Meta-Test", "push")
			stub := &pushStub{progress: true}
			response := httptest.NewRecorder()
			if err := (&imageRouter{translator: stub}).postImagesPush(response, request); err != nil {
				t.Fatal(err)
			}
			if stub.ref.Host != "example.com" || stub.ref.Repository != "app" || stub.ref.Tag != tc.tag ||
				len(stub.options.Platforms) != tc.platforms || stub.options.AuthConfig.RegistryToken != "test-token" ||
				stub.options.MetaHeaders["X-Meta-Test"][0] != "push" {
				t.Fatalf("push call = %+v, %+v", stub.ref, stub.options)
			}
			if response.Code != 200 || !strings.Contains(response.Body.String(), "Pushing") {
				t.Fatalf("push response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPostImagesPushErrors(t *testing.T) {
	for _, progress := range []bool{false, true} {
		stub := &pushStub{progress: progress, err: httpx.Unauthorized(errors.New("push denied"))}
		request := httptest.NewRequest(http.MethodPost, "/images/example.com/app/push?tag=v1", nil)
		request.SetPathValue("name", "example.com/app")
		response := httptest.NewRecorder()
		err := (&imageRouter{translator: stub}).postImagesPush(response, request)
		if !progress {
			if !errors.Is(err, stub.err) {
				t.Fatalf("error = %v, want %v", err, stub.err)
			}
		} else {
			if err != nil || !strings.Contains(response.Body.String(), `"code":401`) || !strings.Contains(response.Body.String(), "push denied") {
				t.Fatalf("stream error = %s, %v", response.Body.String(), err)
			}
		}
	}
	for _, query := range []string{"tag=bad/tag", "platform=invalid", "platform=%7B%7D"} {
		request := httptest.NewRequest(http.MethodPost, "/images/example.com/app/push?"+query, nil)
		request.SetPathValue("name", "example.com/app")
		if err := (&imageRouter{translator: &pushStub{}}).postImagesPush(httptest.NewRecorder(), request); err == nil {
			t.Fatalf("invalid push query %q accepted", query)
		}
	}
}

func TestDockerClientSearchAndPush(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "stream failure"}[failure], func(t *testing.T) {
			push := &pushStub{progress: true}
			if failure {
				push.err = httpx.Forbidden(errors.New("publishing denied"))
			}
			search := &searchStub{results: []registrytypes.SearchResult{{Name: "alpine", StarCount: 100}}}
			api := &imageRouter{translator: push, searcher: search}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.SetPathValue("version", "1.55")
				var err error
				if strings.HasSuffix(r.URL.Path, "/search") {
					err = api.getImagesSearch(w, r)
				} else {
					name := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/push"), "/v1.55/images/")
					r.SetPathValue("name", name)
					err = api.postImagesPush(w, r)
				}
				if err != nil {
					t.Errorf("handler error: %v", err)
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}))
			defer server.Close()
			client, err := mobyclient.New(mobyclient.WithHost(server.URL), mobyclient.WithAPIVersion("1.55"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = client.Close() }()
			ctx := context.Background()
			results, err := client.ImageSearch(ctx, "alpine", mobyclient.ImageSearchOptions{})
			if err != nil || len(results.Items) != 1 || results.Items[0].Name != "alpine" {
				t.Fatalf("Docker client search = %+v, %v", results, err)
			}
			response, err := client.ImagePush(ctx, "example.com/app:v1", mobyclient.ImagePushOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Close() }()
			err = response.Wait(ctx)
			if failure {
				if err == nil || !strings.Contains(err.Error(), "publishing denied") {
					t.Fatalf("Docker client missed stream failure: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Docker client push = %v", err)
			}
		})
	}
}
