package registry

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
)

type childManifest struct {
	descriptor oci.Descriptor
	tags       []string
}

type childManifests []childManifest

// childManifestsForPlatform returns the manifests to copy for an image index:
// the selected platform manifest (carrying any tags) plus the attestation
// manifests that reference it.
func childManifestsForPlatform(manifests []oci.Descriptor, selected oci.Descriptor, tags []string) childManifests {
	children := childManifests{{descriptor: selected, tags: tags}}
	for _, m := range manifests {
		if m.Annotations[types.AnnotationReferenceType] != types.AnnotationReferenceTypeAttestation {
			continue
		}
		if m.Annotations[types.AnnotationReferenceDigest] != selected.Digest.String() {
			continue
		}
		children = append(children, childManifest{descriptor: m})
	}
	return children
}

// selectManifest picks the child manifest matching the configured platform
// from an image index, defaulting to the current runtime platform.
func selectManifest(p platforms.MatchComparer, manifests []oci.Descriptor) (oci.Descriptor, error) {
	matcher := p
	if matcher == nil {
		matcher = platforms.Default()
	}
	for _, m := range manifests {
		if m.Platform == nil {
			continue
		}
		if platform := (types.Platform{Platform: m.Platform}).ToOCI(); platform != nil && matcher.Match(*platform) {
			return m, nil
		}
	}
	return oci.Descriptor{}, fmt.Errorf("no manifest found for platform %s", matcherString(matcher))
}

// platformMatcher returns a matcher for the first requested platform, or nil
// to use the runtime default.
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

// resolveManifest resolves c.SrcRef to a concrete manifest in the source
// registry, and returns it along with the tags to apply to the copy in the
// destination repository.
func (c *Copier) resolveManifest(ctx context.Context) (types.Manifest, []string, error) {
	var tags []string
	if c.DstRef.Tag != "" {
		tags = []string{c.DstRef.Tag}
	}

	var (
		reader oci.BlobReader
		err    error
	)
	if c.SrcRef.Digest != "" {
		reader, err = c.Src.GetManifest(ctx, c.SrcRef.Repository, c.SrcRef.Digest)
	} else {
		srcTag := c.SrcRef.Tag
		if srcTag == "" {
			srcTag = "latest"
		}
		if len(tags) == 0 {
			tags = []string{srcTag}
		}
		reader, err = c.Src.GetTag(ctx, c.SrcRef.Repository, srcTag)
	}
	if err != nil {
		return types.Manifest{}, nil, err
	}
	m, err := readManifest(reader)
	if err != nil {
		return types.Manifest{}, nil, err
	}
	return m, tags, nil
}

// fetchManifest reads the manifest with the given digest from c.SrcRef's
// repository in the source registry.
func (c *Copier) fetchManifest(ctx context.Context, digest oci.Digest) (types.Manifest, error) {
	reader, err := c.Src.GetManifest(ctx, c.SrcRef.Repository, digest)
	if err != nil {
		return types.Manifest{}, fmt.Errorf("get manifest %s: %w", digest, err)
	}
	return readManifest(reader)
}

// readManifest reads a manifest and decodes the descriptors a copy needs from
// it: the config and layer blobs of an image manifest, or the child manifests
// and their platforms for an index.
func readManifest(reader oci.BlobReader) (m types.Manifest, err error) {
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return types.DecodeManifest(reader.Descriptor(), reader)
}

// copyManifest copies m and everything it references: its child manifests,
// then its config blob, then its layer blobs, and finally the manifest itself,
// so that the manifest only becomes resolvable once its content is present.
func (c *Copier) copyManifest(ctx context.Context, m types.Manifest, tags []string) error {
	children := childManifests{{descriptor: m.Descriptor, tags: tags}}
	if m.IsIndex() {
		// Only the requested platform is transferred, even for a digest copy
		// that keeps the index: the other platforms are content the caller
		// will never run.
		selected, err := selectManifest(c.Platform, m.Manifests)
		if err != nil {
			return err
		}
		children = childManifestsForPlatform(m.Manifests, selected, tags)
	}

	for _, child := range children {
		if child.descriptor.Digest == m.Digest {
			continue
		}
		childManifest, err := c.fetchManifest(ctx, child.descriptor.Digest)
		if err != nil {
			return err
		}
		if err := c.copyManifest(ctx, childManifest, child.tags); err != nil {
			return err
		}
	}
	// A tagged index is flattened: the selected child carries the tag, so the
	// index itself is dropped. An untagged index is pushed verbatim so that
	// the digest the caller asked for resolves in the destination, even
	// though only the selected platform's content was copied.
	if m.IsIndex() && len(tags) > 0 {
		return nil
	}

	if m.Config != nil {
		config := blobCopy{desc: *m.Config, completeStatus: "Download complete", report: false}
		if err := c.copyBlob(ctx, config); err != nil {
			return err
		}
	}
	if err := c.copyLayers(ctx, m.Layers); err != nil {
		return err
	}

	if _, err := c.Dst.PushManifest(ctx, c.DstRef.Repository, m.Contents, m.MediaType, &oci.PushManifestParameters{
		Digest: m.Digest,
		Tags:   tags,
	}); err != nil {
		return fmt.Errorf("push manifest %s: %w", m.Digest, err)
	}
	return nil
}
