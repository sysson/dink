package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/dink/identity"
	"github.com/sysson/dink/pkg/types"
)

const layerMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

// TestMain isolates the tests from the developer's docker config, whose
// credential helper would otherwise be shelled out to for every registry
// client. DOCKER_AUTH_CONFIG takes precedence over every config file
// location.
func TestMain(m *testing.M) {
	if err := os.Setenv("DOCKER_AUTH_CONFIG", "{}"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestPullImageCopiesIntoNamespacedRepository(t *testing.T) {
	source := newTestRegistry(t)
	internal := newTestRegistry(t)
	manifest := seedManifest(t, source.store, testImage{
		repo:     "library/nginx",
		configID: "config-a",
		layerID:  "layer-a",
		tags:     []string{"latest"},
	})

	out := &bytes.Buffer{}
	is := newImageService(t, internal)
	ref := mustRef(t, source.url+"/library/nginx:latest")
	if err := is.PullImage(pullContext("dev"), ref, types.PullOptions{OutStream: out}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	dstRepo := internalRepo("dev", source, "library/nginx")
	got := resolveTag(t, internal.store, dstRepo, "latest")
	if got.Digest != manifest.Digest {
		t.Fatalf("tagged digest = %s, want %s", got.Digest, manifest.Digest)
	}
	assertBlob(t, internal.store, dstRepo, "config-a")
	assertBlob(t, internal.store, dstRepo, "layer-a")

	if _, err := source.store.ResolveTag(t.Context(), dstRepo, "latest"); err == nil {
		t.Fatal("destination repository must not be written to the source registry")
	}
	if body := out.String(); !strings.Contains(body, "Downloaded newer image") {
		t.Fatalf("progress output missing completion message: %s", body)
	}
}

func TestPullImageRewritesTagFromRequestedRef(t *testing.T) {
	source := newTestRegistry(t)
	internal := newTestRegistry(t)
	manifest := seedManifest(t, source.store, testImage{
		repo:     "acme/api",
		configID: "config-b",
		layerID:  "layer-b",
		tags:     []string{"v1.2.3"},
	})

	out := &bytes.Buffer{}
	is := newImageService(t, internal)
	ref := mustRef(t, source.url+"/acme/api:v1.2.3")
	if err := is.PullImage(pullContext("tenant-a"), ref, types.PullOptions{OutStream: out}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	dstRepo := internalRepo("tenant-a", source, "acme/api")
	got := resolveTag(t, internal.store, dstRepo, "v1.2.3")
	if got.Digest != manifest.Digest {
		t.Fatalf("tagged digest = %s, want %s", got.Digest, manifest.Digest)
	}
	if _, err := internal.store.ResolveTag(t.Context(), dstRepo, "latest"); err == nil {
		t.Fatal("unrequested tag \"latest\" was created")
	}
}

func TestPullImageSelectsRequestedPlatformFromIndex(t *testing.T) {
	source := newTestRegistry(t)
	internal := newTestRegistry(t)

	const srcRepo = "library/multi"
	amd64 := seedManifest(t, source.store, testImage{repo: srcRepo, configID: "config-amd64", layerID: "layer-amd64"})
	arm64 := seedManifest(t, source.store, testImage{repo: srcRepo, configID: "config-arm64", layerID: "layer-arm64"})
	seedIndex(t, source.store, srcRepo, "latest", []oci.Descriptor{
		withPlatform(amd64, "linux", "amd64"),
		withPlatform(arm64, "linux", "arm64"),
	})

	out := &bytes.Buffer{}
	is := newImageService(t, internal)
	err := is.PullImage(pullContext("dev"), mustRef(t, source.url+"/"+srcRepo+":latest"), types.PullOptions{
		OutStream: out,
		Platforms: []ocispec.Platform{{OS: "linux", Architecture: "arm64"}},
	})
	if err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	dstRepo := internalRepo("dev", source, srcRepo)
	got := resolveTag(t, internal.store, dstRepo, "latest")
	if got.Digest != arm64.Digest {
		t.Fatalf("tagged digest = %s, want the arm64 manifest %s", got.Digest, arm64.Digest)
	}
	assertBlob(t, internal.store, dstRepo, "layer-arm64")
	if _, err := internal.store.ResolveBlob(t.Context(), dstRepo, blobDigest("layer-amd64")); err == nil {
		t.Fatal("amd64 layer was copied for an arm64 pull")
	}
}

func TestPullImageSkipsCopyWhenTagAlreadyPresent(t *testing.T) {
	var requests atomic.Int64
	source := newTestRegistry(t, requestCounter(&requests))
	internal := newTestRegistry(t)
	seedManifest(t, source.store, testImage{
		repo:     "library/nginx",
		configID: "config-c",
		layerID:  "layer-c",
		tags:     []string{"latest"},
	})

	is := newImageService(t, internal)
	ctx := pullContext("dev")
	ref := mustRef(t, source.url+"/library/nginx:latest")
	if err := is.PullImage(ctx, ref, types.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	afterFirst := requests.Load()
	if afterFirst == 0 {
		t.Fatal("first pull never reached the source registry")
	}

	if err := is.PullImage(ctx, ref, types.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if extra := requests.Load() - afterFirst; extra != 0 {
		t.Fatalf("cached pull made %d requests to the source registry, want 0", extra)
	}
}

func TestPullImageByDigest(t *testing.T) {
	source := newTestRegistry(t)
	internal := newTestRegistry(t)
	manifest := seedManifest(t, source.store, testImage{
		repo:     "library/nginx",
		configID: "config-d",
		layerID:  "layer-d",
		tags:     []string{"latest"},
	})

	out := &bytes.Buffer{}
	is := newImageService(t, internal)
	ref := mustRef(t, source.url+"/library/nginx@"+manifest.Digest.String())
	if err := is.PullImage(pullContext("dev"), ref, types.PullOptions{OutStream: out}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	dstRepo := internalRepo("dev", source, "library/nginx")
	if _, err := internal.store.ResolveManifest(t.Context(), dstRepo, manifest.Digest); err != nil {
		t.Fatalf("pulled digest does not resolve in %s: %v", dstRepo, err)
	}
	assertBlob(t, internal.store, dstRepo, "layer-d")
	if _, err := internal.store.ResolveTag(t.Context(), dstRepo, "latest"); err == nil {
		t.Fatal("a digest pull must not create a tag")
	}
}

// A digest pull of an index must reproduce the index, otherwise the digest
// the caller asked for would not resolve in the internal registry.
func TestPullImageByDigestPreservesIndex(t *testing.T) {
	source := newTestRegistry(t)
	internal := newTestRegistry(t)

	const srcRepo = "library/multi"
	amd64 := seedManifest(t, source.store, testImage{repo: srcRepo, configID: "config-amd64", layerID: "layer-amd64"})
	arm64 := seedManifest(t, source.store, testImage{repo: srcRepo, configID: "config-arm64", layerID: "layer-arm64"})
	index := seedIndex(t, source.store, srcRepo, "latest", []oci.Descriptor{
		withPlatform(amd64, "linux", "amd64"),
		withPlatform(arm64, "linux", "arm64"),
	})

	out := &bytes.Buffer{}
	is := newImageService(t, internal)
	ref := mustRef(t, source.url+"/"+srcRepo+"@"+index.Digest.String())
	if err := is.PullImage(pullContext("dev"), ref, types.PullOptions{OutStream: out}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}

	dstRepo := internalRepo("dev", source, srcRepo)
	if _, err := internal.store.ResolveManifest(t.Context(), dstRepo, index.Digest); err != nil {
		t.Fatalf("index digest does not resolve in %s: %v", dstRepo, err)
	}
	assertBlob(t, internal.store, dstRepo, "layer-amd64")
	assertBlob(t, internal.store, dstRepo, "layer-arm64")
}

func TestPullImageSkipsCopyWhenDigestAlreadyPresent(t *testing.T) {
	var requests atomic.Int64
	source := newTestRegistry(t, requestCounter(&requests))
	internal := newTestRegistry(t)
	manifest := seedManifest(t, source.store, testImage{
		repo:     "library/nginx",
		configID: "config-e",
		layerID:  "layer-e",
		tags:     []string{"latest"},
	})

	is := newImageService(t, internal)
	ctx := pullContext("dev")
	ref := mustRef(t, source.url+"/library/nginx@"+manifest.Digest.String())
	if err := is.PullImage(ctx, ref, types.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	afterFirst := requests.Load()
	if afterFirst == 0 {
		t.Fatal("first pull never reached the source registry")
	}

	if err := is.PullImage(ctx, ref, types.PullOptions{OutStream: &bytes.Buffer{}}); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if extra := requests.Load() - afterFirst; extra != 0 {
		t.Fatalf("cached pull made %d requests to the source registry, want 0", extra)
	}
}

func TestPullImageRequiresIdentity(t *testing.T) {
	source := newTestRegistry(t)
	is := newImageService(t, newTestRegistry(t))
	ref := mustRef(t, source.url+"/library/nginx:latest")
	err := is.PullImage(context.Background(), ref, types.PullOptions{OutStream: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected an error when the context carries no identity")
	}
}

// New must not contact the registry, so a registry that is not up yet cannot
// stop dink starting.
func TestNewDoesNotContactRegistry(t *testing.T) {
	if _, err := NewClient("127.0.0.1:1", ClientOptions{}); err != nil {
		t.Fatalf("client: %v", err)
	}
}

// Endpoints that never touch the internal registry must keep working without
// one, so a missing URL is only reported to the endpoints that need it.
func TestMissingRegistryURLIsReportedOnUse(t *testing.T) {
	is := Unavailable(errors.New("registry URL is not set"))
	if _, err := is.client(); err == nil {
		t.Fatal("expected an error for an unset registry URL")
	}

	ref := mustRef(t, newTestRegistry(t).url+"/library/nginx:latest")
	err := is.PullImage(pullContext("dev"), ref, types.PullOptions{OutStream: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected a pull without an internal registry to fail")
	}
}

// A registry address may include a port, but ":" is not valid in an OCI
// repository name.
func TestRepositoryForEscapesRegistryPort(t *testing.T) {
	got := repositoryFor(identity.Identity{Namespace: "dev"}, sourceRef{
		Host:       "registry.local:5000",
		Repository: "team/api",
		Tag:        "latest",
	})
	const want = "dev/registry.local-5000/team/api"
	if got != want {
		t.Fatalf("repositoryFor = %q, want %q", got, want)
	}
	if !ociref.IsValidRepository(got) {
		t.Fatalf("%q is not a valid repository name", got)
	}
}

// testRegistry is an in-memory registry served over HTTP at a real address,
// so pulls exercise the registry client end to end. Seeding and assertions
// use store directly rather than going over the wire.
type testRegistry struct {
	store oci.Interface
	url   string
}

func newTestRegistry(t *testing.T, mw ...func(http.Handler) http.Handler) *testRegistry {
	t.Helper()
	store := ocimem.New()
	handler, err := ociserver.New(store, &ociserver.ServerConfig{Middlewares: mw})
	if err != nil {
		t.Fatalf("new registry server: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return &testRegistry{store: store, url: strings.TrimPrefix(srv.URL, "http://")}
}

func newImageService(t *testing.T, internal *testRegistry) *ImageService {
	t.Helper()
	client, err := NewClient(internal.url, ClientOptions{})
	if err != nil {
		t.Fatalf("new internal registry client: %v", err)
	}
	return New(client)
}

// requestCounter counts the requests that reach a registry.
func requestCounter(n *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// internalRepo is the repository in the internal registry that a pull of repo
// from source lands in.
func internalRepo(namespace string, source *testRegistry, repo string) string {
	return namespace + "/" + strings.ReplaceAll(source.url, ":", "-") + "/" + repo
}

func pullContext(namespace string) context.Context {
	return identity.NewContext(context.Background(), identity.Identity{Namespace: namespace})
}

func mustRef(t *testing.T, s string) ociref.Reference {
	t.Helper()
	ref, err := ociref.ParseRelative(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ref
}

// blobDigest returns the digest of the blob content identified by id, so that
// tests can refer to blobs without threading descriptors around.
func blobDigest(id string) oci.Digest {
	return ocidigest.FromBytes([]byte(id))
}

func pushBlob(t *testing.T, r oci.Interface, repo, mediaType, id string) oci.Descriptor {
	t.Helper()
	data := []byte(id)
	desc := oci.Descriptor{MediaType: mediaType, Digest: blobDigest(id), Size: int64(len(data))}
	if _, err := r.PushBlob(context.Background(), repo, desc, bytes.NewReader(data)); err != nil {
		t.Fatalf("push blob %s: %v", id, err)
	}
	return desc
}

// testImage describes an image to seed into a registry. Blob IDs double as
// the blob contents, so a given ID always has the same digest.
type testImage struct {
	repo     string
	configID string
	layerID  string
	tags     []string
}

// seedManifest pushes an image manifest, with its config and single layer,
// into img.repo.
func seedManifest(t *testing.T, r oci.Interface, img testImage) oci.Descriptor {
	t.Helper()
	body := mustJSON(t, struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType"`
		Config        oci.Descriptor   `json:"config"`
		Layers        []oci.Descriptor `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageManifest,
		Config:        pushBlob(t, r, img.repo, oci.MediaTypeImageConfig, img.configID),
		Layers:        []oci.Descriptor{pushBlob(t, r, img.repo, layerMediaType, img.layerID)},
	})

	desc, err := r.PushManifest(t.Context(), img.repo, body, oci.MediaTypeImageManifest, &oci.PushManifestParameters{Tags: img.tags})
	if err != nil {
		t.Fatalf("push manifest in %s: %v", img.repo, err)
	}
	return desc
}

func seedIndex(t *testing.T, r oci.Interface, repo, tag string, manifests []oci.Descriptor) oci.Descriptor {
	t.Helper()
	body := mustJSON(t, struct {
		SchemaVersion int              `json:"schemaVersion"`
		MediaType     string           `json:"mediaType"`
		Manifests     []oci.Descriptor `json:"manifests"`
	}{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageIndex,
		Manifests:     manifests,
	})

	desc, err := r.PushManifest(t.Context(), repo, body, oci.MediaTypeImageIndex, &oci.PushManifestParameters{Tags: []string{tag}})
	if err != nil {
		t.Fatalf("push index in %s: %v", repo, err)
	}
	return desc
}

func withPlatform(desc oci.Descriptor, os, arch string) oci.Descriptor {
	desc.Platform = &oci.Platform{OS: os, Architecture: arch}
	return desc
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func resolveTag(t *testing.T, r oci.Interface, repo, tag string) oci.Descriptor {
	t.Helper()
	reader, err := r.GetTag(context.Background(), repo, tag)
	if err != nil {
		t.Fatalf("get tag %s:%s: %v", repo, tag, err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close manifest reader: %v", closeErr)
		}
	}()
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read manifest %s:%s: %v", repo, tag, err)
	}
	return reader.Descriptor()
}

func assertBlob(t *testing.T, r oci.Interface, repo, id string) {
	t.Helper()
	if _, err := r.ResolveBlob(context.Background(), repo, blobDigest(id)); err != nil {
		t.Fatalf("blob %s missing from %s: %v", id, repo, err)
	}
}
