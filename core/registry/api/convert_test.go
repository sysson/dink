package api

import (
	"reflect"
	"testing"

	imagetypes "github.com/moby/moby/api/types/image"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
)

func TestManifestSummaryWireRoundTrip(t *testing.T) {
	manifest := imagetypes.ManifestSummary{
		ID: "sha256:platform", Kind: imagetypes.ManifestKindImage, Available: true,
		Descriptor: ocispec.Descriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    "sha256:platform", Size: 100,
			Annotations: map[string]string{"example": "value"},
		},
		ImageData: &imagetypes.ImageProperties{
			Platform:   ocispec.Platform{OS: "linux", Architecture: "arm", Variant: "v7"},
			Containers: []string{},
		},
	}
	manifest.Size.Content, manifest.Size.Total = 120, 120
	want := imagetypes.Summary{ID: "sha256:index", Manifests: []imagetypes.ManifestSummary{manifest}}
	wire, err := SummaryToProto(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SummaryFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Manifests, want.Manifests) {
		t.Fatalf("manifests = %+v, want %+v", got.Manifests, want.Manifests)
	}
	if _, err := SummaryFromProto(&registryv1.ImageSummary{Manifests: []byte("invalid")}); err == nil {
		t.Fatal("malformed manifest response was accepted")
	}
}
