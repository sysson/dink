package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func blobsFromDescriptors(desc []oci.Descriptor) chan oci.Descriptor {
	blobs := make(chan oci.Descriptor, len(desc))
	go func() {
		defer close(blobs)
		for _, m := range desc {
			if isIndex(m.MediaType) || isManifest(m.MediaType) {
				continue
			}
			blobs <- m
		}
	}()
	return blobs
}

func allDescriptors(ctx context.Context, client oci.Interface, ref ociref.Reference, platform platforms.MatchComparer) ([]oci.Descriptor, error) {
	referrers, err := referrers(ctx, client, ref, platform)
	if err != nil {
		return nil, err
	}
	manifests, err := walkManifest(ctx, client, ref, platform)
	if err != nil {
		return nil, err
	}
	return append(manifests, referrers...), nil
}

func referrers(ctx context.Context, client oci.Interface, ref ociref.Reference, platform platforms.MatchComparer) ([]oci.Descriptor, error) {
	referrers, err := oci.All(client.Referrers(ctx, ref.Repository, ref.Digest, nil))
	if err != nil {
		return nil, err
	}
	var allDescs []oci.Descriptor
	for _, r := range referrers {
		descs, err := walkManifest(ctx, client, ociref.Reference{
			Repository: ref.Repository,
			Digest:     r.Digest,
		}, platform)
		if err != nil {
			return nil, err
		}
		allDescs = append(allDescs, descs...)
	}

	return allDescs, nil
}

func walkManifest(ctx context.Context, client oci.Interface, ref ociref.Reference, platform platforms.MatchComparer) ([]oci.Descriptor, error) {
	var descriptors []oci.Descriptor
	descriptor, err := getManifest(ctx, client, ref)
	if err != nil {
		return nil, err
	}

	manifest, err := readBlob[oci.IndexOrManifest](bytes.NewReader(descriptor.Data))
	if err != nil {
		return nil, err
	}

	switch {
	case isIndex(descriptor.MediaType):

		mfstDescriptors, err := selectManifests(platform, &manifest)
		if err != nil {
			return nil, err
		}
		if len(mfstDescriptors) == 0 {
			return nil, fmt.Errorf("no manifests found in index for platform %v", platform)
		}
		var errs []error
		for _, desc := range mfstDescriptors {
			walkedDescriptors, err := walkManifest(ctx, client, ociref.Reference{
				Repository: ref.Repository,
				Digest:     desc.Digest,
			}, platform)
			if err != nil {
				errs = append(errs, err)
			}
			descriptors = append(descriptors, walkedDescriptors...)
		}
		if len(errs) > 0 {
			return nil, fmt.Errorf("errors occurred while walking manifests: %v", errs)
		}

	case isManifest(descriptor.MediaType):
		descriptors = append(descriptors, manifest.Layers...)

		if manifest.Config != nil {
			descriptors = append(descriptors, *manifest.Config)
		}
	default:
		return nil, fmt.Errorf("unsupported media type: %s", descriptor.MediaType)
	}

	descriptors = append(descriptors, descriptor)
	return descriptors, nil
}

func getManifest(ctx context.Context, client oci.Interface, ref ociref.Reference) (oci.Descriptor, error) {
	blob, err := client.GetManifest(ctx, ref.Repository, ref.Digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	defer func() {
		_ = blob.Close()
	}()
	desc := blob.Descriptor()
	if len(desc.Data) == 0 {
		desc.Data, err = io.ReadAll(blob)
		if err != nil {
			return oci.Descriptor{}, err
		}
	}
	return desc, nil
}

func pushManifests(ctx context.Context, client oci.Interface, ref ociref.Reference, descriptors []oci.Descriptor) error {
	for _, desc := range descriptors {
		if isIndex(desc.MediaType) || isManifest(desc.MediaType) {
			refToPush := ref
			if ref.Digest != desc.Digest {
				refToPush.Tag = ""
			}
			if err := pushManifest(ctx, client, refToPush, desc); err != nil {
				return err
			}
		}
	}
	return nil
}

func pushManifest(ctx context.Context, client oci.Interface, ref ociref.Reference, desc oci.Descriptor) error {
	if len(desc.Data) == 0 {
		return fmt.Errorf("manifest data is empty")
	}
	var tags []string
	if ref.Tag != "" {
		tags = append(tags, ref.Tag)
	}
	_, err := client.PushManifest(ctx, ref.Repository, desc.Data, desc.MediaType, &oci.PushManifestParameters{
		Digest: desc.Digest,
		Tags:   tags,
	})
	if err != nil {
		return fmt.Errorf("unable to push manifest %s: %w", desc.Digest, err)
	}
	return nil
}

func selectManifests(p platforms.MatchComparer, index *oci.IndexOrManifest) ([]oci.Descriptor, error) {
	matcher := p
	if matcher == nil {
		matcher = platforms.Default()
	}
	if ok := isIndex(index.MediaType); !ok {
		return nil, fmt.Errorf("not an index")
	}
	var matches []oci.Descriptor
	for _, m := range index.Manifests {
		if m.Platform == nil {
			continue
		}
		plat := ocispec.Platform{
			Architecture: m.Platform.Architecture,
			OS:           m.Platform.OS,
			OSVersion:    m.Platform.OSVersion,
			OSFeatures:   m.Platform.OSFeatures,
			Variant:      m.Platform.Variant,
		}
		if matcher.Match(plat) {
			matches = append(matches, m)
			break
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no manifest found for platform %s", matcherString(matcher))
	}

	const (
		AnnotationReferenceType   = "vnd.docker.reference.type"
		AnnotationReferenceDigest = "vnd.docker.reference.digest"

		AnnotationReferenceTypeAttestation = "attestation-manifest"
	)
	//now match docker annotations
	for _, m := range index.Manifests {
		if m.Annotations[AnnotationReferenceType] != AnnotationReferenceTypeAttestation {
			continue
		}
		if m.Annotations[AnnotationReferenceDigest] != matches[0].Digest.String() {
			continue
		}
		matches = append(matches, m)
	}
	return matches, nil
}

func platformMatcher(pls []ocispec.Platform) platforms.MatchComparer {
	if len(pls) == 0 {
		return nil
	}
	return platforms.Only(pls[0])
}

func matcherString(m platforms.MatchComparer) string {
	if m == nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	return fmt.Sprintf("%v", m)
}

func isIndex(mediaType string) bool {
	return mediaType == oci.MediaTypeImageIndex || mediaType == oci.MediaTypeDockerManifestList
}

func isManifest(mediaType string) bool {
	return mediaType == oci.MediaTypeImageManifest || mediaType == oci.MediaTypeDockerManifest
}

func isImageLayer(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageLayer || mediaType == ocispec.MediaTypeImageLayerGzip || mediaType == ocispec.MediaTypeImageLayerZstd
}

func isConfig(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageConfig
}
