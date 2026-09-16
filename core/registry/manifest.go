package registry

import (
	"bytes"
	"context"
	"errors"
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

type manifestOptions struct {
	client     oci.Interface
	ref        ociref.Reference
	descriptor oci.Descriptor
	platform   platforms.MatchComparer
}

func allDescriptors(ctx context.Context, client oci.Interface, ref ociref.Reference, platform platforms.MatchComparer) ([]oci.Descriptor, error) {
	descriptor, err := getManifest(ctx, client, ref)
	if err != nil {
		return nil, err
	}

	var descriptors []oci.Descriptor
	opts := manifestOptions{
		client:     client,
		ref:        ref,
		platform:   platform,
		descriptor: descriptor,
	}

	switch {
	case isIndex(descriptor.MediaType):
		index, err := walkIndexManifest(ctx, opts)
		if err != nil {
			return nil, err
		}

		opts.descriptor = index.platform

		descriptors = append(descriptors, index.descriptors...)
		descriptors = append(descriptors, index.platform)

	case isManifest(descriptor.MediaType):
		manifestDescriptors, err := walkManifest(ctx, opts)
		if err != nil {
			return nil, err
		}

		descriptors = append(descriptors, manifestDescriptors...)

	default:
		return nil, fmt.Errorf("unsupported media type: %s", descriptor.MediaType)
	}

	refs, err := referrers(ctx, opts)
	if err != nil {
		return nil, err
	}
	descriptors = append(descriptors, refs...)

	descriptors = append(descriptors, descriptor)
	return descriptors, nil
}

func walkIndexManifest(ctx context.Context, opts manifestOptions) (*indexManifest, error) {
	if !isIndex(opts.descriptor.MediaType) {
		return nil, fmt.Errorf("expected index media type, got: %s", opts.descriptor.MediaType)
	}

	index, err := readBlob[oci.IndexOrManifest](bytes.NewReader(opts.descriptor.Data))
	if err != nil {
		return nil, err
	}

	indexManifest, err := parseIndexManifest(opts.platform, &index)
	if err != nil {
		return nil, err
	}

	if indexManifest.platform.Digest == "" {
		return nil, fmt.Errorf("no manifests found in index for platform %v", opts.platform)
	}

	var descriptors []oci.Descriptor
	var errs []error
	for _, desc := range append([]oci.Descriptor{indexManifest.platform}, indexManifest.descriptors...) {
		opt := manifestOptions{
			client:     opts.client,
			ref:        opts.ref,
			platform:   opts.platform,
			descriptor: desc,
		}

		manifestDescriptors, err := walkManifest(ctx, opt)
		if err != nil {
			errs = append(errs, err)
		}

		descriptors = append(descriptors, manifestDescriptors...)
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("errors occurred while walking manifests: %v", errors.Join(errs...))
	}

	descriptors = append(descriptors, indexManifest.descriptors...)
	indexManifest.descriptors = descriptors
	return indexManifest, nil
}

func referrers(ctx context.Context, opts manifestOptions) ([]oci.Descriptor, error) {
	referrers, err := oci.All(opts.client.Referrers(ctx, opts.ref.Repository, opts.ref.Digest, nil))
	if err != nil {
		return nil, err
	}
	var allreferrers []oci.Descriptor
	for _, r := range referrers {
		referrer, err := getManifest(ctx, opts.client, ociref.Reference{
			Repository: opts.ref.Repository,
			Digest:     r.Digest,
		})
		if err != nil {
			return nil, err
		}
		descs, err := walkManifest(ctx, manifestOptions{
			client:     opts.client,
			ref:        opts.ref,
			platform:   opts.platform,
			descriptor: referrer,
		})
		if err != nil {
			return nil, err
		}
		allreferrers = append(allreferrers, descs...)
		allreferrers = append(allreferrers, referrer)
	}
	return allreferrers, nil
}

func walkManifest(ctx context.Context, opts manifestOptions) ([]oci.Descriptor, error) {
	if !isManifest(opts.descriptor.MediaType) {
		return nil, fmt.Errorf("unsupported media type: %s", opts.descriptor.MediaType)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	descriptor, err := getManifest(ctx, opts.client, opts.ref)
	if err != nil {
		return nil, err
	}

	opts.descriptor = descriptor
	descriptors, err := manifestDescriptors(opts)
	if err != nil {
		return nil, err
	}
	descriptors = append(descriptors, descriptor)
	return descriptors, nil
}

func manifestDescriptors(opts manifestOptions) ([]oci.Descriptor, error) {
	var descriptors []oci.Descriptor

	manifest, err := readBlob[oci.IndexOrManifest](bytes.NewReader(opts.descriptor.Data))
	if err != nil {
		return nil, err
	}
	descriptors = append(descriptors, manifest.Layers...)

	if manifest.Config != nil {
		descriptors = append(descriptors, *manifest.Config)
	}

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

type indexManifest struct {
	platform    oci.Descriptor
	descriptors []oci.Descriptor
}

func parseIndexManifest(p platforms.MatchComparer, index *oci.IndexOrManifest) (*indexManifest, error) {
	matcher := p
	if matcher == nil {
		matcher = platforms.Default()
	}
	if ok := isIndex(index.MediaType); !ok {
		return nil, fmt.Errorf("not an index")
	}
	matches := &indexManifest{}
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
			matches.platform = m
			break
		}
	}
	if matches.platform.Digest == "" {
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
		if m.Annotations[AnnotationReferenceDigest] != matches.platform.Digest.String() {
			continue
		}
		matches.descriptors = append(matches.descriptors, m)
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
