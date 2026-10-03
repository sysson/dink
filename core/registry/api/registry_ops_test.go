package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
)

func TestPushTagAndAllTags(t *testing.T) {
	source, destination := newUpstream(t), newUpstream(t)
	layer := source.blob("app", layerType, []byte("test layer"))
	original := source.image("app", "latest", layer)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	ref := ociref.Reference{Host: source.host, Repository: "app", Tag: "latest"}
	if err := client.PullImage(ctx, ref, imagebackend.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	target := ociref.Reference{Host: destination.host, Repository: "published", Tag: "v1"}
	if err := client.TagImage(ctx, ref.String(), target); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{OutStream: &progress}); err != nil {
		t.Fatal(err)
	}
	got, err := destination.registry.ResolveTag(ctx, "published", "v1")
	if err != nil || got.Digest != original.Digest {
		t.Fatalf("pushed tag = %+v, %v", got, err)
	}
	if _, err := destination.registry.ResolveBlob(ctx, "published", layer.Digest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "Pushed") || !strings.Contains(progress.String(), original.Digest.String()) ||
		strings.Contains(progress.String(), "Pull Complete") {
		t.Fatalf("push progress = %s", progress.String())
	}
	progress.Reset()
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{OutStream: &progress}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "Already exists") {
		t.Fatalf("repeat progress = %s", progress.String())
	}
	target.Tag = "v2"
	if err := client.TagImage(ctx, ref.String(), target); err != nil {
		t.Fatal(err)
	}
	target.Tag = ""
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.registry.ResolveTag(ctx, "published", "v2"); err != nil {
		t.Fatal(err)
	}
	other := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if err := client.PushImage(other, target, imagebackend.PushOptions{}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("other tenant push = %v", err)
	}
	target.Tag = "missing"
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("missing tag push = %v", err)
	}
}

func TestPushMultiPlatform(t *testing.T) {
	source, destination := newUpstream(t), newUpstream(t)
	amd64 := source.platformImage("multi", "amd64", source.blob("multi", layerType, []byte("amd64")))
	s390x := source.platformImage("multi", "s390x", source.blob("multi", layerType, []byte("s390x")))
	index := source.index("multi", "latest", amd64, s390x)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	ref := ociref.Reference{Host: source.host, Repository: "multi", Tag: "latest"}
	pull := func(arch string) {
		t.Helper()
		if err := client.PullImage(ctx, ref, imagebackend.PullOptions{OutStream: &bytes.Buffer{},
			Platforms: []ocispec.Platform{{OS: "linux", Architecture: arch}}}); err != nil {
			t.Fatal(err)
		}
	}
	// Push back to the same upstream, after clearing the upstream tag: the
	// tenant content is separate and a missing platform must not be downloaded.
	pull("s390x")
	partialTarget := ociref.Reference{Host: destination.host, Repository: "partial", Tag: "latest"}
	if err := client.TagImage(ctx, ref.String(), partialTarget); err != nil {
		t.Fatal(err)
	}
	var partialProgress bytes.Buffer
	if err := client.PushImage(ctx, partialTarget, imagebackend.PushOptions{OutStream: &partialProgress}); err != nil {
		t.Fatal(err)
	}
	partial, err := destination.registry.ResolveTag(ctx, "partial", "latest")
	if err != nil || partial.Digest != s390x.Digest ||
		!strings.Contains(partialProgress.String(), `"manifestPushedInsteadOfIndex":true`) ||
		!strings.Contains(partialProgress.String(), index.Digest.String()) {
		t.Fatalf("retagged partial push = %+v, %v, %s", partial, err, partialProgress.String())
	}
	local, err := client.ImageInspect(ctx, partialTarget.String(), imagebackend.ImageInspectOpts{})
	if err != nil || local.ID != index.Digest.String() {
		t.Fatalf("push changed local index = %+v, %v", local, err)
	}
	if err := source.registry.DeleteTag(ctx, "multi", "latest"); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := client.PushImage(ctx, ref, imagebackend.PushOptions{OutStream: &progress}); err != nil {
		t.Fatal(err)
	}
	got, err := source.registry.ResolveTag(ctx, "multi", "latest")
	if err != nil || got.Digest != s390x.Digest || !strings.Contains(progress.String(), `"manifestPushedInsteadOfIndex":true`) {
		t.Fatalf("partial push = %+v, %v, %s", got, err, progress.String())
	}
	// Restore the original index upstream, then pull its other platform.
	source.index("multi", "latest", amd64, s390x)
	pull("amd64")
	target := ociref.Reference{Host: destination.host, Repository: "multi", Tag: "latest"}
	if err := client.TagImage(ctx, ref.String(), target); err != nil {
		t.Fatal(err)
	}
	stored, err := client.ImageInspect(ctx, target.String(), imagebackend.ImageInspectOpts{})
	if err != nil || stored.ID != index.Digest.String() {
		t.Fatalf("retagged full index = %+v, %v, want %s", stored, err, index.Digest)
	}
	progress.Reset()
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{OutStream: &progress}); err != nil {
		t.Fatal(err)
	}
	got, err = destination.registry.ResolveTag(ctx, "multi", "latest")
	if err != nil || string(got.Digest) != stored.ID {
		t.Fatalf("full push = %+v, %v, want stored index %s", got, err, stored.ID)
	}
	for _, child := range []oci.Descriptor{amd64, s390x} {
		if _, err := destination.registry.ResolveManifest(ctx, "multi", child.Digest); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{
		Platforms: []ocispec.Platform{{OS: "linux", Architecture: "s390x"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err = destination.registry.ResolveTag(ctx, "multi", "latest")
	if err != nil || got.Digest != s390x.Digest {
		t.Fatalf("platform push = %+v, %v", got, err)
	}
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{
		Platforms: []ocispec.Platform{{OS: "linux", Architecture: "arm64"}},
	}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("missing platform push = %v", err)
	}
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{
		Platforms: []ocispec.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "linux", Architecture: "s390x"}},
	}); statusCode(err) != http.StatusBadRequest {
		t.Fatalf("multiple platform push = %v", err)
	}
}

func TestSearchThroughRegistryAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" || r.URL.Query().Get("q") != "app" ||
			r.URL.Query().Get("n") != "25" || r.Header.Get("X-Meta-Test") != "search" {
			t.Errorf("search request = %s %v", r.URL, r.Header)
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != "tester" || password != "test-password" {
			t.Errorf("search auth not forwarded")
		}
		_ = json.NewEncoder(w).Encode(registrytypes.SearchResults{Results: []registrytypes.SearchResult{
			{Name: "official", IsOfficial: true, StarCount: 100},
			{Name: "unofficial", StarCount: 50},
			{Name: "small", IsOfficial: true, StarCount: 1},
		}})
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	searchFilters := filters.NewArgs(filters.Arg("is-official", "true"), filters.Arg("stars", "5"), filters.Arg("stars", "10"))
	results, err := client.Search(ctx, searchFilters, endpoint.Host+"/app", 0,
		&registrytypes.AuthConfig{Username: "tester", Password: "test-password"},
		map[string][]string{"X-Meta-Test": {"search"}})
	if err != nil || len(results) != 1 || results[0].Name != "official" {
		t.Fatalf("search = %+v, %v", results, err)
	}
	if _, err := client.Search(ctx, filters.NewArgs(), "https://bad/query", 0, nil, nil); statusCode(err) != http.StatusBadRequest {
		t.Fatalf("invalid search = %v", err)
	}
}

func TestSearchRegistryFailures(t *testing.T) {
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			_, err := client.Search(ctx, filters.NewArgs(), endpoint.Host+"/app", 10, nil, nil)
			if err == nil {
				t.Fatal("registry failure returned success")
			}
			if status < 500 && statusCode(err) != status {
				t.Fatalf("status error = %v, want %d", err, status)
			}
		})
	}
	for _, body := range []string{"invalid JSON", "{}", "null", `{"results":null}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			if _, err := client.Search(ctx, filters.NewArgs(), endpoint.Host+"/app", 10, nil, nil); err == nil {
				t.Fatal("malformed search response accepted")
			}
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := client.Search(cancelled, filters.NewArgs(), "127.0.0.1:1/app", 10, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search = %v", err)
	}
}

func TestPushRegistryAuthentication(t *testing.T) {
	up := newUpstream(t)
	original := up.image("app", "latest", up.blob("app", layerType, []byte("authenticated layer")))
	remote := ocimem.New()
	handler, err := ociserver.New(remote, nil)
	if err != nil {
		t.Fatal(err)
	}
	var metadata atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "tester" || password != "test-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
			return
		}
		if r.Header.Get("X-Meta-Test") == "push" {
			metadata.Store(true)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	source := ociref.Reference{Host: up.host, Repository: "app", Tag: "latest"}
	if err := client.PullImage(ctx, source, imagebackend.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	target := ociref.Reference{Host: endpoint.Host, Repository: "published", Tag: "latest"}
	if err := client.TagImage(ctx, source.String(), target); err != nil {
		t.Fatal(err)
	}
	var progress bytes.Buffer
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{OutStream: &progress}); statusCode(err) != http.StatusUnauthorized {
		t.Fatalf("unauthenticated push = %v", err)
	}
	auth := &registrytypes.AuthConfig{Username: "tester", Password: "test-password"}
	if err := client.PushImage(ctx, target, imagebackend.PushOptions{
		OutStream: &progress, AuthConfig: auth, MetaHeaders: map[string][]string{"X-Meta-Test": {"push"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := remote.ResolveTag(ctx, "published", "latest")
	if err != nil || got.Digest != original.Digest || !metadata.Load() {
		t.Fatalf("authenticated push = %+v, %v, metadata=%v", got, err, metadata.Load())
	}
}

type failingProgressWriter struct{}

func (failingProgressWriter) Write([]byte) (int, error) {
	return 0, errors.New("client disconnected")
}

func TestPushProgressDisconnect(t *testing.T) {
	up := newUpstream(t)
	up.image("app", "latest", up.blob("app", layerType, []byte("layer")))
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	ref := ociref.Reference{Host: up.host, Repository: "app", Tag: "latest"}
	if err := client.PullImage(ctx, ref, imagebackend.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if err := client.PushImage(ctx, ref, imagebackend.PushOptions{OutStream: failingProgressWriter{}}); err == nil ||
		!strings.Contains(err.Error(), "client disconnected") {
		t.Fatalf("disconnected push = %v", err)
	}
}

func TestSearchTokenAuthentication(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh=%v", refresh), func(t *testing.T) {
			var tokenCalls atomic.Int32
			server := httptest.NewUnstartedServer(nil)
			server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					tokenCalls.Add(1)
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					if r.Form.Get("refresh_token") != "refresh-token" || r.Form.Get("scope") != "registry:catalog:search" {
						t.Errorf("token request = %v", r.Form)
					}
					_, _ = w.Write([]byte(`{"access_token":"search-token","expires_in":3600}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer search-token" {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="registry:catalog:search"`, server.URL))
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				_, _ = w.Write([]byte(`{"results":[{"name":"app","star_count":10}]}`))
			})
			server.Start()
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			auth := &registrytypes.AuthConfig{RegistryToken: "search-token"}
			if refresh {
				auth = &registrytypes.AuthConfig{Username: "tester", IdentityToken: "refresh-token"}
			}
			client := newAPI(t)
			ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
			results, err := client.Search(ctx, filters.NewArgs(), endpoint.Host+"/app", 10, auth, nil)
			if err != nil || len(results) != 1 || results[0].Name != "app" {
				t.Fatalf("token search = %+v, %v", results, err)
			}
			if refresh && tokenCalls.Load() == 0 {
				t.Fatal("identity token was not exchanged")
			}
		})
	}
}
