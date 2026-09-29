package command

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	dinkiconfig "github.com/sysson/dink/cmd/dinki/config"
	"github.com/sysson/dink/core/registry/server"
	"github.com/sysson/dink/pkg/ocistore/blobstore"
	"github.com/sysson/dink/pkg/ocistore/kv/boltkv"
	"github.com/sysson/dink/pkg/ocistore/kv/drivers"
	"github.com/sysson/dink/pkg/ocistore/kv/memkv"
	"github.com/sysson/dink/sdk/registry/v1/registryconnect"
)

func TestNewBackendOpensPersistentConfiguredStores(t *testing.T) {
	dir := t.TempDir()
	cfg := dinkiconfig.Default()
	cfg.Storage = blobstore.Config{File: &blobstore.FileConfig{Path: filepath.Join(dir, "blobs")}}
	cfg.Metadata = drivers.Config{BBolt: &boltkv.Config{Path: filepath.Join(dir, "metadata.db")}}

	backend, err := newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("persisted by dinki backend factory")
	digest := ocidigest.FromBytes(content)
	if _, err := backend.registry.PushBlob(context.Background(), "test/app", oci.Descriptor{
		Digest: digest,
		Size:   int64(len(content)),
	}, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}

	backend, err = newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Errorf("closing reopened backend: %v", err)
		}
	}()
	reader, err := backend.registry.GetBlob(context.Background(), "test/app", digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	var got bytes.Buffer
	if _, err := got.ReadFrom(reader); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Fatalf("persisted blob = %q, want %q", got.Bytes(), content)
	}
}

func TestNewBackendRejectsInvalidDrivers(t *testing.T) {
	file := blobstore.Config{File: &blobstore.FileConfig{Path: filepath.Join(t.TempDir(), "blobs")}}
	mem := drivers.Config{Mem: &memkv.Config{}}
	for name, cfg := range map[string]dinkiconfig.Config{
		"no storage":        {Metadata: mem},
		"no metadata":       {Storage: file},
		"two metadata":      {Storage: file, Metadata: drivers.Config{Mem: &memkv.Config{}, BBolt: &boltkv.Config{Path: "/tmp/db"}}},
		"relative bbolt":    {Storage: file, Metadata: drivers.Config{BBolt: &boltkv.Config{Path: "relative.db"}}},
		"invalid s3 bucket": {Storage: blobstore.Config{S3: &blobstore.S3Config{Bucket: "_"}}, Metadata: mem},
	} {
		if backend, err := newBackend(context.Background(), cfg); err == nil {
			_ = backend.Close()
			t.Errorf("%s: newBackend succeeded, want error", name)
		}
	}
}

func TestNewBackendSupportsMemory(t *testing.T) {
	backend, err := newBackend(context.Background(), dinkiconfig.Config{Storage: blobstore.Config{Mem: &blobstore.MemConfig{}}, Metadata: drivers.Config{Mem: &memkv.Config{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func verified(organization, commonName string) *tls.ConnectionState {
	return &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{
		Subject: pkix.Name{Organization: []string{organization}, CommonName: commonName},
	}}}}
}

func TestVerifyAPIClientRequiresDinkSubject(t *testing.T) {
	verify := func(state *tls.ConnectionState) error {
		return verifyAPIClient(state, "dink-system", "dink.dink-system.svc")
	}
	if err := verify(verified("dink-system", "dink.dink-system.svc")); err != nil {
		t.Fatalf("dink certificate rejected: %v", err)
	}
	unverified := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
		Subject: pkix.Name{Organization: []string{"dink-system"}, CommonName: "dink.dink-system.svc"},
	}}}
	for name, peer := range map[string]*tls.ConnectionState{
		"tenant":        verified("team-a", "alice"),
		"spoofed CN":    verified("team-a", "dink.dink-system.svc"),
		"spoofed org":   verified("dink-system", "alice"),
		"unverified":    unverified,
		"no peer certs": {},
		"plaintext":     nil,
	} {
		if err := verify(peer); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestHealthHandlerServesProbeRoutesWithoutTLS(t *testing.T) {
	handler := healthHandler()
	for _, path := range []string{"/livez", "/readyz"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("GET %s = %d %q, want 200 with an empty body", path, response.Code, response.Body.String())
		}
	}
}

func TestHandlerRoutesAPIToDinkAndRegistryToPullCredentials(t *testing.T) {
	cfg := dinkiconfig.Default()
	cfg.GraphQL.Enabled = true
	cfg.Storage = blobstore.Config{Mem: &blobstore.MemConfig{}}
	cfg.Metadata = drivers.Config{Mem: &memkv.Config{}}
	backend, err := newBackend(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	handler, err := newHandler(cfg, backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	push := func(repo, content string) oci.Digest {
		digest := ocidigest.FromBytes([]byte(content))
		if _, err := backend.registry.PushBlob(ctx, repo, oci.Descriptor{Digest: digest, Size: int64(len(content))}, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	ownBlob := push("team-a/app", "team a layer")
	otherBlob := push("team-b/app", "team b layer")
	password, err := backend.credentials.Issue(ctx, "team-a")
	if err != nil {
		t.Fatal(err)
	}

	dink := verified(cfg.API.ClientOrganization, cfg.API.ClientCommonName)
	tenant := verified("team-a", "alice")
	type credential struct{ username, password string }
	teamA := &credential{"team-a", password}
	serveBody := func(method, target, contentType, body string, state *tls.ConnectionState, auth *credential) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		if contentType != "" {
			request.Header.Set("Content-Type", contentType)
		}
		if auth != nil {
			request.SetBasicAuth(auth.username, auth.password)
		}
		request.TLS = state
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	serve := func(method, target, contentType, body string, state *tls.ConnectionState) int {
		return serveBody(method, target, contentType, body, state, nil).Code
	}

	for name, auth := range map[string]*credential{
		"anonymous":      nil,
		"wrong password": {"team-a", "guess"},
		"other username": {"team-b", password},
	} {
		response := serveBody(http.MethodGet, "/v2/", "", "", dink, auth)
		if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s GET /v2/ = %d %v, want a 401 challenge", name, response.Code, response.Header())
		}
	}
	if code := serveBody(http.MethodGet, "/v2/", "", "", nil, teamA).Code; code != http.StatusOK {
		t.Fatalf("GET /v2/ with credential = %d, want 200", code)
	}
	if code := serveBody(http.MethodGet, "/v2/team-a/app/blobs/"+ownBlob.String(), "", "", nil, teamA).Code; code != http.StatusOK {
		t.Fatalf("own blob = %d, want 200", code)
	}
	if code := serveBody(http.MethodGet, "/v2/team-b/app/blobs/"+otherBlob.String(), "", "", nil, teamA).Code; code != http.StatusNotFound {
		t.Fatalf("other namespace blob = %d, want 404", code)
	}
	if body := serveBody(http.MethodGet, "/v2/_catalog", "", "", nil, teamA).Body.String(); !strings.Contains(body, "team-a/app") || strings.Contains(body, "team-b") {
		t.Fatalf("catalog = %s, want only team-a repositories", body)
	}
	if code := serveBody(http.MethodPost, "/v2/team-a/app/blobs/uploads/", "", "", nil, teamA).Code; code < 400 {
		t.Fatalf("upload = %d, want the registry to be read-only", code)
	}
	if err := backend.credentials.Revoke(ctx, "team-a"); err != nil {
		t.Fatal(err)
	}
	if code := serveBody(http.MethodGet, "/v2/", "", "", nil, teamA).Code; code != http.StatusUnauthorized {
		t.Fatalf("GET /v2/ after revoke = %d, want 401", code)
	}

	const listImages = registryconnect.RegistryServiceListImagesProcedure
	const graphql = `{"query":"{ repositories { name } }"}`
	for name, state := range map[string]*tls.ConnectionState{"anonymous": nil, "tenant": tenant} {
		if code := serve(http.MethodPost, listImages, "application/json", `{}`, state); code != http.StatusForbidden {
			t.Fatalf("%s ListImages = %d, want 403", name, code)
		}
		if code := serve(http.MethodPost, server.GraphQLPath, "application/json", graphql, state); code != http.StatusForbidden {
			t.Fatalf("%s GraphQL = %d, want 403", name, code)
		}
	}
	if code := serve(http.MethodPost, server.GraphQLPath, "application/json", graphql, dink); code != http.StatusOK {
		t.Fatalf("dink GraphQL = %d, want 200", code)
	}
	// Connect rejects the request for its missing identity, so it got past the gate.
	if code := serve(http.MethodPost, listImages, "application/json", `{}`, dink); code == http.StatusForbidden || code == http.StatusNotFound {
		t.Fatalf("dink ListImages = %d, want it to reach the API", code)
	}
}
