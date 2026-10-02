package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/sysson/syskit/httpx"
)

func TestInspectDistributionFromAuthenticatedRegistry(t *testing.T) {
	config := []byte(`{"architecture":"amd64","os":"linux","variant":"v3","rootfs":{"type":"layers","diff_ids":[]}}`)
	configDigest := ocidigest.FromBytes(config)
	manifest, err := json.Marshal(oci.IndexOrManifest{
		SchemaVersion: 2, MediaType: oci.MediaTypeImageManifest,
		Config: &oci.Descriptor{Digest: configDigest, Size: int64(len(config)), MediaType: oci.MediaTypeImageConfig},
		Layers: []oci.Descriptor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := ocidigest.FromBytes(manifest)
	index, err := json.Marshal(oci.IndexOrManifest{
		SchemaVersion: 2, MediaType: oci.MediaTypeImageIndex,
		Manifests: []oci.Descriptor{
			{Digest: manifestDigest, Size: int64(len(manifest)), MediaType: oci.MediaTypeImageManifest, Platform: &oci.Platform{OS: "linux", Architecture: "amd64"}},
			{Digest: manifestDigest, Size: int64(len(manifest)), MediaType: oci.MediaTypeImageManifest, Platform: &oci.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	indexDigest := ocidigest.FromBytes(index)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="upstream"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`))
			return
		}
		var data []byte
		var mediaType string
		switch r.URL.Path {
		case "/v2/app/manifests/latest", "/v2/app/manifests/" + manifestDigest.String():
			data, mediaType = manifest, oci.MediaTypeImageManifest
		case "/v2/app/manifests/multi", "/v2/app/manifests/" + indexDigest.String():
			data, mediaType = index, oci.MediaTypeImageIndex
		case "/v2/app/blobs/" + configDigest.String():
			data, mediaType = config, oci.MediaTypeImageConfig
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"missing image"}]}`))
			return
		}
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", ocidigest.FromBytes(data).String())
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	auth := &registrytypes.AuthConfig{Username: "user", Password: "password"}
	for _, name := range []string{host + "/app", host + "/app:latest", host + "/app@" + manifestDigest.String()} {
		result, err := InspectDistribution(context.Background(), name, auth)
		if err != nil {
			t.Fatal(err)
		}
		if result.Descriptor.Digest.String() != manifestDigest.String() || result.Descriptor.Size != int64(len(manifest)) || result.Descriptor.MediaType != oci.MediaTypeImageManifest ||
			len(result.Platforms) != 1 || result.Platforms[0].OS != "linux" || result.Platforms[0].Architecture != "amd64" || result.Platforms[0].Variant != "v3" {
			t.Fatalf("incorrect distribution metadata: %v", result)
		}
	}
	result, err := InspectDistribution(context.Background(), host+"/app:multi", auth)
	if err != nil || result.Descriptor.Digest.String() != indexDigest.String() || len(result.Platforms) != 2 || result.Platforms[1].Architecture != "arm64" {
		t.Fatalf("index distribution metadata: %v %v", result, err)
	}
	for _, test := range []struct {
		name string
		auth *registrytypes.AuthConfig
		code int
	}{
		{host + "/app:latest", &registrytypes.AuthConfig{Username: "user", Password: "wrong"}, http.StatusUnauthorized},
		{host + "/missing:tag", auth, http.StatusNotFound},
		{"bad image", auth, http.StatusBadRequest},
	} {
		_, err := InspectDistribution(context.Background(), test.name, test.auth)
		var httpErr *httpx.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != test.code {
			t.Fatalf("inspect %q: %v, want HTTP %d", test.name, err, test.code)
		}
	}
}
