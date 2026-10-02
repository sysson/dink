package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/docker/oci"
	imagetypes "github.com/moby/moby/api/types/image"
)

func TestListManifestSummariesSinglePlatform(t *testing.T) {
	tree := &storedImageTree{
		Digest: "sha256:manifest", MediaType: oci.MediaTypeImageManifest, Size: 100,
		Config:      &storedSized{Digest: "sha256:config", Size: 20},
		Layers:      []storedDescriptor{{Digest: "sha256:layer", Size: 50}},
		ImageConfig: &storedImageConfig{OS: "linux", Architecture: "arm", Variant: "v7"}}
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "missing layer"}[missing], func(t *testing.T) {
			service := New(&oci.Funcs{ResolveBlob_: func(_ context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
				if repo != "tenant/app" {
					t.Fatalf("repository = %q", repo)
				}
				if digest == "sha256:config" {
					return oci.Descriptor{Digest: digest, Size: 20}, nil
				}
				if missing {
					return oci.Descriptor{}, oci.ErrBlobUnknown
				}
				return oci.Descriptor{Digest: digest, Size: 50}, nil
			}}, nil)
			manifests, total, err := service.listManifestSummaries(context.Background(), "tenant/app", tree)
			if err != nil {
				t.Fatal(err)
			}
			if len(manifests) != 1 {
				t.Fatalf("manifests = %+v", manifests)
			}
			size := int64(170)
			if missing {
				size = 120
			}
			manifest := manifests[0]
			if manifest.Available == missing || manifest.ID != tree.Digest || manifest.Kind != imagetypes.ManifestKindImage ||
				manifest.Size.Content != size || manifest.Size.Total != size || total != size {
				t.Fatalf("manifest/total = %+v/%d, want size %d", manifest, total, size)
			}
			if manifest.ImageData == nil || manifest.ImageData.Platform.Variant != "v7" ||
				manifest.ImageData.Platform.Architecture != "arm" || manifest.ImageData.Size.Unpacked != 0 {
				t.Fatalf("platform data = %+v", manifest.ImageData)
			}
		})
	}
}

func TestListManifestSummariesPreservesUnavailableEntries(t *testing.T) {
	tree := &storedImageTree{
		Digest: "sha256:index", MediaType: oci.MediaTypeImageIndex, Size: 100,
		Manifests: []storedIndexChild{{
			Digest: "sha256:arm64", MediaType: oci.MediaTypeImageManifest, Size: 500,
			Platform: &storedPlatform{OS: "linux", Architecture: "arm64"},
		}, {
			Digest: "sha256:attestation", MediaType: oci.MediaTypeImageManifest, Size: 300,
			Annotations: []storedAnnotation{
				{Key: AnnotationReferenceType, Value: AnnotationReferenceTypeAttestation},
				{Key: AnnotationReferenceDigest, Value: "sha256:arm64"},
			},
		}},
	}
	service := New(&oci.Funcs{}, nil)
	manifests, size, err := service.listManifestSummaries(context.Background(), "tenant/app", tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifests) != 2 || size != 100 {
		t.Fatalf("manifests/size = %+v/%d", manifests, size)
	}
	for _, manifest := range manifests {
		if manifest.Available || manifest.Size.Content != 0 || manifest.Size.Total != 0 {
			t.Fatalf("unavailable manifest = %+v", manifest)
		}
	}
	if manifests[1].Kind != imagetypes.ManifestKindAttestation || manifests[1].AttestationData == nil ||
		manifests[1].AttestationData.For != "sha256:arm64" {
		t.Fatalf("attestation = %+v", manifests[1])
	}
}

func TestListManifestSummariesReportsStorageErrors(t *testing.T) {
	want := errors.New("storage unavailable")
	service := New(&oci.Funcs{ResolveBlob_: func(context.Context, string, oci.Digest) (oci.Descriptor, error) {
		return oci.Descriptor{}, want
	}}, nil)
	tree := &storedImageTree{
		Digest: "sha256:manifest", MediaType: oci.MediaTypeImageManifest, Size: 100,
		Config: &storedSized{Digest: "sha256:config", Size: 20}}
	if _, _, err := service.listManifestSummaries(context.Background(), "tenant/app", tree); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
