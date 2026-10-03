package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/moby/moby/api/types/auxprogress"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/stream"
)

func (r *RegistryService) PushImage(ctx context.Context, ref ociref.Reference, options imagebackend.PushOptions) error {
	if options.OutStream == nil {
		options.OutStream = io.Discard
	}
	if len(options.Platforms) > 1 {
		return httpx.BadRequest(errors.New("pushing multiple platforms is not supported"))
	}
	if len(options.Platforms) == 1 && (options.Platforms[0].OS == "" || options.Platforms[0].Architecture == "") {
		return httpx.BadRequest(errors.New("both OS and Architecture must be provided"))
	}
	if ref.Digest != "" {
		return httpx.BadRequest(errors.New("pushing by digest is not supported; use a tag"))
	}
	normalized, err := ociref.ParseRelative(ref.String())
	if err != nil {
		return httpx.BadRequest(err)
	}
	ref = normalized
	id, ok := identity.FromContext(ctx)
	if !ok {
		return httpx.Unauthorized(errors.New("missing identity in context"))
	}
	internal, err := r.client()
	if err != nil {
		return err
	}
	source := repositoryFor(id, ref)
	var tags []string
	if ref.Tag != "" {
		tags = []string{ref.Tag}
	} else {
		after := ""
		for {
			records, err := r.index.TagRecords(ctx, source.Repository, after, indexPage)
			if isNotFound(err) {
				break
			}
			if err != nil {
				return err
			}
			for _, record := range records {
				if _, byDigest := digestForTag(record.Tag); !byDigest {
					tags = append(tags, record.Tag)
				}
			}
			if len(records) < indexPage {
				break
			}
			after = records[len(records)-1].Tag
		}
	}
	if len(tags) == 0 {
		return httpx.NotFound(fmt.Errorf("no tagged image found for %s", ref.String()))
	}
	destination, err := NewClient(ref.Host, ClientOptions{
		Auth: options.AuthConfig, Transport: RegistryTransport(nil, options.MetaHeaders),
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	progress := make(chan stream.Progress, 100)
	done := make(chan struct{})
	out := stream.ChanOutputContext(ctx, progress)
	go func() {
		writeDistributionProgress(ctx, cancel, options.OutStream, progress)
		close(done)
	}()
	stream.Messagef(out, "", "The push refers to repository [%s/%s]", ref.Host, ref.Repository)
	for _, tag := range tags {
		target := ref
		target.Tag = tag
		if err = r.pushTag(ctx, internal, destination, target, options.Platforms, out); err != nil {
			break
		}
	}
	_ = out.Close()
	close(progress)
	<-done
	if err != nil {
		if errors.Is(err, oci.ErrUnauthorized) {
			return httpx.Unauthorized(err)
		}
		if errors.Is(err, oci.ErrDenied) {
			return httpx.Forbidden(err)
		}
		if isNotFound(err) {
			return httpx.NotFound(err)
		}
		return err
	}
	return ctx.Err()
}

func (r *RegistryService) pushTag(ctx context.Context, internal, destination oci.Interface, ref ociref.Reference, requested []ocispec.Platform, out stream.ProgressWriter) error {
	source, tree, err := r.inspectTree(ctx, ref.String())
	if err != nil {
		return err
	}
	srcRef := ociref.Reference{Repository: source.repository, Digest: source.digest}
	original, err := getManifest(ctx, internal, srcRef)
	if err != nil {
		return err
	}
	target := original
	var descriptors []oci.Descriptor
	if len(requested) == 0 {
		descriptors, err = pushDescriptors(ctx, internal, srcRef)
	}
	if len(requested) > 0 || (isNotFound(err) && isIndex(tree.MediaType)) {
		if target, err = r.pushPlatform(ctx, source.repository, tree, requested); err != nil {
			return err
		}
		srcRef.Digest = target.Digest
		descriptors, err = pushDescriptors(ctx, internal, srcRef)
	}
	if err != nil {
		return err
	}
	// A platform-specific push deliberately excludes indexes and attestations.
	// A normal push preserves separately stored referrers as well as index children.
	if len(requested) == 0 && target.Digest == original.Digest {
		attached, err := (&manifest{client: internal, ref: srcRef}).referrers(ctx, target.Digest)
		if err != nil {
			return err
		}
		descriptors = append(descriptors, attached...)
	}
	dstRef := ref
	dstRef.Digest = target.Digest
	copier := &copier{client: internal, destination: destination, srcRef: srcRef, dstRef: dstRef, out: out, pushing: true}
	if _, err := copier.copyBlobs(ctx, blobsFromDescriptors(descriptors)); err != nil {
		return err
	}
	if err := pushManifests(ctx, destination, dstRef, descriptors); err != nil {
		return err
	}
	if len(requested) == 0 && original.Digest != target.Digest {
		stream.Aux(out, auxprogress.ManifestPushedInsteadOfIndex{
			ManifestPushedInsteadOfIndex: true,
			OriginalIndex:                pushOCIDescriptor(original),
			SelectedManifest:             pushOCIDescriptor(target),
		})
	}
	stream.Messagef(out, "", "%s: digest: %s size: %d", tagOrDigest(ref), target.Digest, target.Size)
	return nil
}

func (r *RegistryService) pushPlatform(ctx context.Context, repository string, tree *storedImageTree, requested []ocispec.Platform) (oci.Descriptor, error) {
	summaries, _, err := r.listManifestSummaries(ctx, repository, tree)
	if err != nil {
		return oci.Descriptor{}, err
	}
	var candidates []ocispec.Descriptor
	for _, summary := range summaries {
		if !summary.Available || summary.ImageData == nil {
			continue
		}
		if len(requested) > 0 && !platforms.OnlyStrict(requested[0]).Match(summary.ImageData.Platform) {
			continue
		}
		candidates = append(candidates, summary.Descriptor)
	}
	if len(candidates) == 0 {
		return oci.Descriptor{}, httpx.NotFound(errors.New("no complete image manifest is stored for the requested platform"))
	}
	selected := candidates[0]
	if len(candidates) > 1 {
		if len(requested) > 0 {
			return oci.Descriptor{}, httpx.Conflict(errors.New("multiple manifests match the requested platform"))
		}
		found := false
		for _, candidate := range candidates {
			if candidate.Platform != nil && platforms.Default().Match(*candidate.Platform) {
				selected, found = candidate, true
				break
			}
		}
		if !found {
			return oci.Descriptor{}, httpx.Conflict(errors.New("incomplete multi-platform image; specify a platform to push"))
		}
	}
	return oci.Descriptor{MediaType: selected.MediaType, Digest: oci.Digest(selected.Digest), Size: selected.Size}, nil
}

// pushDescriptors retains the original manifest bytes and visits children before
// parents, validating content before publishing a tag to a remote registry.
func pushDescriptors(ctx context.Context, client oci.Interface, ref ociref.Reference) ([]oci.Descriptor, error) {
	return imageDescriptors(ctx, client, ref, false)
}

// Local retagging can copy a partial graph without altering its index. Remote
// pushes require a complete graph before publishing the tag.
func imageDescriptors(ctx context.Context, client oci.Interface, ref ociref.Reference, allowMissing bool) ([]oci.Descriptor, error) {
	var descriptors []oci.Descriptor
	seen := map[oci.Digest]bool{}
	var walk func(ociref.Reference, int) error
	walk = func(ref ociref.Reference, depth int) error {
		if depth > 32 {
			return errors.New("image manifest nesting exceeds 32 levels")
		}
		if seen[ref.Digest] {
			return nil
		}
		seen[ref.Digest] = true
		descriptor, err := getManifest(ctx, client, ref)
		if allowMissing && depth > 0 && isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if isIndex(descriptor.MediaType) {
			index, err := readBlob[oci.IndexOrManifest](bytes.NewReader(descriptor.Data))
			if err != nil {
				return err
			}
			for _, child := range index.Manifests {
				childRef := ref
				childRef.Digest = child.Digest
				if err := walk(childRef, depth+1); err != nil {
					return err
				}
			}
		} else {
			blobs, err := manifestDescriptors(descriptor)
			if err != nil {
				return err
			}
			for _, blob := range blobs {
				if seen[blob.Digest] {
					continue
				}
				if _, err := client.ResolveBlob(ctx, ref.Repository, blob.Digest); err != nil {
					if allowMissing && isNotFound(err) {
						continue
					}
					return err
				}
				seen[blob.Digest] = true
				descriptors = append(descriptors, blob)
			}
		}
		descriptors = append(descriptors, descriptor)
		return nil
	}
	if err := walk(ref, 0); err != nil {
		return nil, err
	}
	return descriptors, nil
}

func pushOCIDescriptor(descriptor oci.Descriptor) ocispec.Descriptor {
	return ocispec.Descriptor{MediaType: descriptor.MediaType, Digest: digest.Digest(descriptor.Digest), Size: descriptor.Size}
}
