package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/registry/api"
	apiserver "github.com/sysson/dink/core/registry/api/server"
	"github.com/sysson/dink/core/registry/pullauth"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/ocistore"
	"github.com/sysson/ocistore/blobstore/memblob"
	"github.com/sysson/ocistore/kv/memkv"
	"github.com/sysson/ocistore/kvmeta"
	"github.com/sysson/ocistore/query"
	"github.com/sysson/syskit/httpx"
)

const (
	manifestType = "application/vnd.oci.image.manifest.v1+json"
	configType   = "application/vnd.oci.image.config.v1+json"
	layerType    = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type upstream struct {
	t        *testing.T
	registry *ocimem.Registry
	host     string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	mem := ocimem.New()
	handler, err := ociserver.New(mem, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &upstream{t: t, registry: mem, host: u.Host}
}

func (u *upstream) blob(repo, mediaType string, data []byte) oci.Descriptor {
	u.t.Helper()
	desc := oci.Descriptor{MediaType: mediaType, Digest: ocidigest.FromBytes(data), Size: int64(len(data))}
	if _, err := u.registry.PushBlob(context.Background(), repo, desc, bytes.NewReader(data)); err != nil {
		u.t.Fatal(err)
	}
	return desc
}

func (u *upstream) image(repo, tag string, layers ...oci.Descriptor) oci.Descriptor {
	u.t.Helper()
	config := u.blob(repo, configType, []byte(`{"architecture":"amd64","os":"linux","created":"2024-01-02T03:04:05Z","rootfs":{"type":"layers","diff_ids":[]},"config":{},"history":[{"created_by":"`+tag+`"}]}`))
	data, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     manifestType,
		"config":        config,
		"layers":        layers,
	})
	if err != nil {
		u.t.Fatal(err)
	}
	desc, err := u.registry.PushManifest(context.Background(), repo, data, manifestType, &oci.PushManifestParameters{Tags: []string{tag}})
	if err != nil {
		u.t.Fatal(err)
	}
	return desc
}

func newAPI(t *testing.T) *api.Client {
	client, _ := newAPIWithCredentials(t)
	return client
}

func newAPIWithCredentials(t *testing.T) (*api.Client, *pullauth.Store) {
	t.Helper()
	ctx := context.Background()
	content, err := (memblob.Config{}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = content.Close() })
	store := memkv.New()
	metadata, err := kvmeta.New(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	local, err := ocistore.New(content, metadata, ocistore.WithAllowMissingManifestChildren())
	if err != nil {
		t.Fatal(err)
	}
	queries, err := query.New(local.Index())
	if err != nil {
		t.Fatal(err)
	}
	credentials := pullauth.New(store)
	server, err := apiserver.New(registry.New(local, local.Index()), queries, credentials)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := server.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return api.NewClient(httpServer.Client(), httpServer.URL), credentials
}

func TestPullCredentials(t *testing.T) {
	client, credentials := newAPIWithCredentials(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	username, password, err := client.IssuePullCredential(ctx)
	if err != nil || username != "tenant" || password == "" {
		t.Fatalf("IssuePullCredential() = %q, %q, %v", username, password, err)
	}
	if ok, err := credentials.Verify(ctx, username, password); err != nil || !ok {
		t.Fatalf("Verify(issued) = %v, %v", ok, err)
	}
	_, rotated, err := client.IssuePullCredential(ctx)
	if err != nil || rotated == password {
		t.Fatalf("reissued password = %q, %v; want a new one", rotated, err)
	}
	if ok, _ := credentials.Verify(ctx, username, password); ok {
		t.Fatal("replaced password still verifies")
	}
	if err := client.RevokePullCredential(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := credentials.Verify(ctx, username, rotated); ok {
		t.Fatal("revoked password still verifies")
	}
	invalid := identity.NewContext(context.Background(), identity.Identity{Namespace: "Not_Valid"})
	if _, _, err := client.IssuePullCredential(invalid); statusCode(err) != http.StatusBadRequest {
		t.Fatalf("IssuePullCredential(invalid namespace) error = %v, want 400", err)
	}
}

func TestPullListQueryRemove(t *testing.T) {
	up := newUpstream(t)
	shared := up.blob("app", layerType, []byte("shared layer"))
	onlyV1 := up.blob("app", layerType, []byte("v1 layer"))
	v1 := up.image("app", "v1", shared, onlyV1)
	v2 := up.image("app", "v2", shared)

	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant", Organization: "tenant", CommonName: "alice"})

	for _, tag := range []string{"v1", "v2"} {
		var progress bytes.Buffer
		ref := ociref.Reference{Host: up.host, Repository: "app", Tag: tag}
		if err := client.PullImage(ctx, ref, imagebackend.PullOptions{OutStream: &progress}); err != nil {
			t.Fatalf("PullImage(%s) error = %v", tag, err)
		}
		if !strings.Contains(progress.String(), "Digest: sha256:") {
			t.Fatalf("pull %s progress = %q, want digest status", tag, progress.String())
		}
	}

	display := strings.ReplaceAll(up.host, ":", "-") + "/app"
	images, err := client.Images(ctx, types.ImageListOptions{})
	if err != nil {
		t.Fatalf("Images() error = %v", err)
	}
	var tags []string
	for _, image := range images {
		tags = append(tags, image.RepoTags...)
	}
	slices.Sort(tags)
	if want := []string{display + ":v1", display + ":v2"}; !slices.Equal(tags, want) {
		t.Fatalf("image tags = %v, want %v", tags, want)
	}
	treeImages, err := client.Images(ctx, types.ImageListOptions{Manifests: true})
	if err != nil || len(treeImages) != 2 {
		t.Fatalf("single-platform tree images = %+v, %v", treeImages, err)
	}
	for _, image := range treeImages {
		if len(image.Manifests) != 1 {
			t.Fatalf("single-platform image manifests = %+v", image.Manifests)
		}
		manifest := image.Manifests[0]
		if manifest.ID != image.ID || !manifest.Available || manifest.ImageData == nil ||
			manifest.ImageData.Platform.Architecture != "amd64" || manifest.Size.Content != image.Size {
			t.Fatalf("single-platform manifest = %+v, parent size = %d", manifest, image.Size)
		}
	}
	other := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if images, err := client.Images(other, types.ImageListOptions{}); err != nil || len(images) != 0 {
		t.Fatalf("other namespace Images() = %v, %v; want none", images, err)
	}

	inspect, err := client.ImageInspect(ctx, up.host+"/app:v1", imagebackend.ImageInspectOpts{})
	if err != nil {
		t.Fatalf("ImageInspect() error = %v", err)
	}
	if inspect.ID != string(v1.Digest) || inspect.Os != "linux" || inspect.Architecture != "amd64" ||
		!slices.Equal(inspect.RepoTags, []string{display + ":v1"}) {
		t.Fatalf("ImageInspect() = %+v", inspect.InspectResponse)
	}
	history, err := client.ImageHistory(ctx, v1.Digest.Encoded()[:12], nil)
	if err != nil || len(history) != 1 || history[0].CreatedBy != "v1" || history[0].ID != string(v1.Digest) {
		t.Fatalf("ImageHistory(by ID) = %+v, %v", history, err)
	}
	if _, err := client.ImageInspect(other, up.host+"/app:v1", imagebackend.ImageInspectOpts{}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("other namespace ImageInspect() error = %v, want 404", err)
	}

	raw, err := client.Query(ctx, `query($repo: String!) { repository(name: $repo) { tags { name } } }`, "", map[string]any{"repo": "tenant/" + display})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if !strings.Contains(string(raw), `"v1"`) || !strings.Contains(string(raw), `"v2"`) {
		t.Fatalf("Query() = %s, want both tags", raw)
	}
	raw, err = client.Query(ctx, `query($repo: String!, $n: Int) { repository(name: $repo) { tags(first: $n) { name } } }`, "", map[string]any{"repo": "tenant/" + display, "n": 1})
	if err != nil || !strings.Contains(string(raw), `"v1"`) || strings.Contains(string(raw), `"v2"`) {
		t.Fatalf("Query(first: 1) = %s, %v; want only v1", raw, err)
	}

	records, err := client.ImageDelete(ctx, up.host+"/app:v1", imagebackend.RemoveOptions{})
	if err != nil {
		t.Fatalf("ImageDelete() error = %v", err)
	}
	var untagged, deleted []string
	for _, record := range records {
		if record.Untagged != "" {
			untagged = append(untagged, record.Untagged)
		}
		if record.Deleted != "" {
			deleted = append(deleted, record.Deleted)
		}
	}
	if want := []string{display + ":v1"}; !slices.Equal(untagged, want) {
		t.Fatalf("untagged = %v, want %v", untagged, want)
	}
	for _, digest := range []oci.Digest{v1.Digest, onlyV1.Digest} {
		if !slices.Contains(deleted, string(digest)) {
			t.Fatalf("deleted = %v, want %s", deleted, digest)
		}
	}
	for _, digest := range []oci.Digest{shared.Digest, v2.Digest} {
		if slices.Contains(deleted, string(digest)) {
			t.Fatalf("deleted = %v, must keep %s", deleted, digest)
		}
	}

	images, err = client.Images(ctx, types.ImageListOptions{})
	if err != nil || len(images) != 1 || images[0].RepoTags[0] != display+":v2" {
		t.Fatalf("Images() after remove = %v, %v; want only v2", images, err)
	}

	_, err = client.ImageDelete(ctx, up.host+"/app:v1", imagebackend.RemoveOptions{})
	if status := statusCode(err); status != http.StatusNotFound {
		t.Fatalf("second ImageDelete() error = %v (status %d), want 404", err, status)
	}
}

func TestTagImageCopiesIntoTargetRepository(t *testing.T) {
	up := newUpstream(t)
	amd64Layer := up.blob("app", layerType, []byte("amd64 layer"))
	s390xLayer := up.blob("app", layerType, []byte("s390x layer"))
	amd64 := up.platformImage("app", "amd64", amd64Layer)
	s390x := up.platformImage("app", "s390x", s390xLayer)
	image := up.index("app", "source", amd64, s390x)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	if err := client.PullImage(ctx, ociref.Reference{Host: up.host, Repository: "app", Tag: "source"}, imagebackend.PullOptions{
		Platforms: []ocispec.Platform{{OS: "linux", Architecture: "amd64"}},
	}); err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}
	target := ociref.Reference{Host: "docker.io", Repository: "alias/app", Tag: "release"}
	if err := client.TagImage(ctx, image.Digest.String(), target); err != nil {
		t.Fatalf("TagImage() error = %v", err)
	}
	aliased, err := client.ImageInspect(ctx, "alias/app:release", imagebackend.ImageInspectOpts{})
	if err != nil || aliased.ID != string(image.Digest) || aliased.Architecture != "amd64" ||
		!slices.Equal(aliased.RepoTags, []string{"alias/app:release"}) {
		t.Fatalf("tagged image inspect = %+v, %v", aliased, err)
	}
	images, err := client.Images(ctx, types.ImageListOptions{Manifests: true})
	if err != nil || len(images) != 2 {
		t.Fatalf("tagged image list = %+v, %v", images, err)
	}
	for _, entry := range images {
		if entry.ID != string(image.Digest) || len(entry.Manifests) != 2 {
			t.Fatalf("tagged image summary = %+v", entry)
		}
		for _, summary := range entry.Manifests {
			if summary.Available != (summary.ID == string(amd64.Digest)) {
				t.Fatalf("tagged platform availability = %+v", summary)
			}
		}
	}
	if _, err := client.ImageInspect(ctx, "alias/app:release", imagebackend.ImageInspectOpts{Platform: &ocispec.Platform{OS: "linux", Architecture: "s390x"}}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("ImageInspect(unpulled platform) error = %v, want 404", err)
	}
	byID, err := client.ImageInspect(ctx, image.Digest.String(), imagebackend.ImageInspectOpts{})
	if err != nil || byID.ID != string(image.Digest) {
		t.Fatalf("ImageInspect(by ID) after tagging = %+v, %v", byID, err)
	}
	other := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if _, err := client.ImageInspect(other, "alias/app:release", imagebackend.ImageInspectOpts{}); statusCode(err) != http.StatusNotFound {
		t.Fatalf("other namespace ImageInspect() error = %v, want 404", err)
	}
}

func TestPullNotFoundKeepsStatus(t *testing.T) {
	up := newUpstream(t)
	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	err := client.PullImage(ctx, ociref.Reference{Host: up.host, Repository: "missing", Tag: "latest"}, imagebackend.PullOptions{})
	if status := statusCode(err); status != http.StatusNotFound {
		t.Fatalf("PullImage() error = %v (status %d), want 404", err, status)
	}
}

func TestMissingIdentityIsRejectedByClient(t *testing.T) {
	client := newAPI(t)
	if _, err := client.Images(context.Background(), types.ImageListOptions{}); err == nil {
		t.Fatal("Images() without identity succeeded")
	}
}

func TestUnavailableClient(t *testing.T) {
	want := errors.New("no dinki")
	client := api.Unavailable(want)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	if _, err := client.Images(ctx, types.ImageListOptions{}); !errors.Is(err, want) {
		t.Fatalf("Images() error = %v, want %v", err, want)
	}
	if _, err := client.Authenticate(ctx, &registrytypes.AuthConfig{}); !errors.Is(err, want) {
		t.Fatalf("Authenticate() error = %v, want %v", err, want)
	}
}

func TestConnectErrorRoundTrip(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict} {
		err := api.FromConnectError(api.ToConnectError(httpx.NewHTTPError(status, errors.New("boom"))))
		if got := statusCode(err); got != status {
			t.Fatalf("round trip of %d = %d (%v)", status, got, err)
		}
		if !strings.Contains(err.Error(), "boom") {
			t.Fatalf("round trip lost message: %v", err)
		}
	}
}

func statusCode(err error) int {
	if httpErr, ok := errors.AsType[*httpx.HTTPError](err); ok {
		return httpErr.StatusCode
	}
	return 0
}

func (u *upstream) platformImage(repo, arch string, layers ...oci.Descriptor) oci.Descriptor {
	u.t.Helper()
	config := u.blob(repo, configType, []byte(`{"architecture":"`+arch+`","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"config":{}}`))
	data, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": manifestType, "config": config, "layers": layers})
	if err != nil {
		u.t.Fatal(err)
	}
	desc, err := u.registry.PushManifest(context.Background(), repo, data, manifestType, nil)
	if err != nil {
		u.t.Fatal(err)
	}
	desc.Platform = &oci.Platform{OS: "linux", Architecture: arch}
	desc.Data = nil
	return desc
}

func (u *upstream) index(repo, tag string, manifests ...oci.Descriptor) oci.Descriptor {
	u.t.Helper()
	data, err := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": oci.MediaTypeImageIndex, "manifests": manifests})
	if err != nil {
		u.t.Fatal(err)
	}
	desc, err := u.registry.PushManifest(context.Background(), repo, data, oci.MediaTypeImageIndex, &oci.PushManifestParameters{Tags: []string{tag}})
	if err != nil {
		u.t.Fatal(err)
	}
	return desc
}

func TestPullSinglePlatformOfIndex(t *testing.T) {
	up := newUpstream(t)
	shared := up.blob("multi", layerType, []byte("shared"))
	amd64Layer := up.blob("multi", layerType, []byte("amd64 only"))
	s390xLayer := up.blob("multi", layerType, []byte("s390x only"))
	amd64 := up.platformImage("multi", "amd64", shared, amd64Layer)
	s390x := up.platformImage("multi", "s390x", shared, s390xLayer)
	index := up.index("multi", "latest", amd64, s390x)

	client := newAPI(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	ref := ociref.Reference{Host: up.host, Repository: "multi", Tag: "latest"}
	pull := func(arch string) string {
		t.Helper()
		var progress bytes.Buffer
		options := imagebackend.PullOptions{OutStream: &progress, Platforms: []ocispec.Platform{{OS: "linux", Architecture: arch}}}
		if err := client.PullImage(ctx, ref, options); err != nil {
			t.Fatalf("PullImage(%s) error = %v", arch, err)
		}
		return progress.String()
	}
	repo := "tenant/" + strings.ReplaceAll(up.host, ":", "-") + "/multi"
	children := func() map[string]bool {
		t.Helper()
		var data struct {
			Data struct {
				Image struct {
					Digest    string `json:"digest"`
					Manifests []struct {
						Digest   string          `json:"digest"`
						Manifest json.RawMessage `json:"manifest"`
					} `json:"manifests"`
				} `json:"image"`
			} `json:"data"`
		}
		raw, err := client.Query(ctx, `query($repo: String!) { image(repository: $repo, reference: "latest") { digest manifests { digest manifest { digest } } } }`, "", map[string]any{"repo": repo})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if data.Data.Image.Digest != string(index.Digest) {
			t.Fatalf("stored index digest = %s, want upstream %s", data.Data.Image.Digest, index.Digest)
		}
		present := map[string]bool{}
		for _, child := range data.Data.Image.Manifests {
			present[child.Digest] = string(child.Manifest) != "null"
		}
		return present
	}

	if progress := pull("s390x"); !strings.Contains(progress, "Downloaded newer image") {
		t.Fatalf("first pull progress = %q", progress)
	}
	if present := children(); !present[string(s390x.Digest)] || present[string(amd64.Digest)] {
		t.Fatalf("children after s390x pull = %v, want only s390x", present)
	}
	images, err := client.Images(ctx, types.ImageListOptions{})
	if err != nil || len(images) != 1 || images[0].ID != string(index.Digest) {
		t.Fatalf("Images() = %v, %v; want the index", images, err)
	}
	if platform := images[0].Descriptor.Platform; platform == nil || platform.Architecture != "s390x" {
		t.Fatalf("image platform = %v, want s390x", platform)
	}
	if len(images[0].Manifests) != 0 {
		t.Fatal("ordinary image list unexpectedly includes manifests")
	}
	configSize := func(image oci.Descriptor) int64 {
		t.Helper()
		reader, err := up.registry.GetManifest(ctx, "multi", image.Digest)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = reader.Close() }()
		var manifest struct {
			Config oci.Descriptor `json:"config"`
		}
		if err := json.NewDecoder(reader).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		return manifest.Config.Size
	}
	amd64Size := amd64.Size + configSize(amd64) + shared.Size + amd64Layer.Size
	s390xSize := s390x.Size + configSize(s390x) + shared.Size + s390xLayer.Size
	checkTree := func(amd64Available bool) {
		t.Helper()
		images, err := client.Images(ctx, types.ImageListOptions{Manifests: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(images) != 1 || len(images[0].Manifests) != 2 {
			t.Fatalf("tree images = %+v, want one image with two platforms", images)
		}
		if images[0].Descriptor.Size != index.Size || images[0].Descriptor.Platform != nil {
			t.Fatalf("index descriptor = %+v, want size %d and no platform", images[0].Descriptor, index.Size)
		}
		for _, manifest := range images[0].Manifests {
			if manifest.ImageData == nil || manifest.Kind != "image" || manifest.Descriptor.Platform == nil {
				t.Fatalf("manifest lacks platform image data: %+v", manifest)
			}
			var available bool
			var size int64
			switch manifest.ID {
			case string(amd64.Digest):
				available = amd64Available
				if available {
					size = amd64Size
				}
				if manifest.ImageData.Platform.Architecture != "amd64" {
					t.Fatalf("amd64 platform = %+v", manifest.ImageData.Platform)
				}
			case string(s390x.Digest):
				available, size = true, s390xSize
				if manifest.ImageData.Platform.Architecture != "s390x" {
					t.Fatalf("s390x platform = %+v", manifest.ImageData.Platform)
				}
			default:
				t.Fatalf("unexpected manifest %s", manifest.ID)
			}
			if manifest.Available != available || manifest.Size.Content != size || manifest.Size.Total != size ||
				manifest.ImageData.Size.Unpacked != 0 {
				t.Fatalf("manifest = %+v, want available=%v and content/total=%d", manifest, available, size)
			}
		}
		size := index.Size + s390xSize
		if amd64Available {
			size += amd64Size - shared.Size
		}
		if images[0].Size != size {
			t.Fatalf("parent size = %d, want %d (shared layer counted once)", images[0].Size, size)
		}
	}
	checkTree(false)

	if progress := pull("s390x"); !strings.Contains(progress, "Image is up to date") {
		t.Fatalf("repeated pull progress = %q", progress)
	}
	if progress := pull("amd64"); !strings.Contains(progress, "Downloaded newer image") {
		t.Fatalf("second platform pull progress = %q", progress)
	}
	if present := children(); !present[string(s390x.Digest)] || !present[string(amd64.Digest)] {
		t.Fatalf("children after amd64 pull = %v, want both", present)
	}
	checkTree(true)

	records, err := client.ImageDelete(ctx, up.host+"/multi:latest", imagebackend.RemoveOptions{})
	if err != nil {
		t.Fatalf("ImageDelete() error = %v", err)
	}
	var deleted []string
	for _, record := range records {
		if record.Deleted != "" {
			deleted = append(deleted, record.Deleted)
		}
	}
	for _, digest := range []oci.Digest{index.Digest, amd64.Digest, s390x.Digest, shared.Digest, amd64Layer.Digest, s390xLayer.Digest} {
		if !slices.Contains(deleted, string(digest)) {
			t.Fatalf("deleted = %v, want %s", deleted, digest)
		}
	}
}
