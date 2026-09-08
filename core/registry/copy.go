package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/sysson/syskit/stream"
)

// Copier copies images (manifests, blobs and tags) from a source registry
// to a destination registry. The source and destination may be any
// registries implementing [oci.Interface], so the same Copier can be used
// for pulls, pushes and mirroring between arbitrary registries.
type Copier struct {
	// Src is the registry content is read from.
	Src oci.Interface
	// Dst is the registry content is written to.
	Dst oci.Interface

	// Progress receives transfer progress events. If nil, progress is
	// discarded.
	Progress stream.ProgressWriter

	// Platform selects the manifest to copy from a manifest list. If nil,
	// the platform of the current runtime is used.
	Platform platforms.MatchComparer

	// CompleteStatus is the status reported when a layer finishes
	// transferring (e.g. "Pull complete" for pulls, "Pushed" for pushes).
	// If empty, "Copy complete" is used.
	CompleteStatus string
}

// CopyRequest describes a single image copy between two repositories.
type CopyRequest struct {
	repoPair
	// SrcTag is the tag to copy. Ignored when SrcDigest is set; if both are
	// empty, "latest" is used.
	SrcTag string
	// SrcDigest pins the exact manifest to copy.
	SrcDigest oci.Digest
	// DstTags are the tags applied to the copied manifest. If empty, a copy
	// by tag uses SrcTag and a copy by digest is left untagged.
	DstTags []string
}

// repos is the source/destination repository pair threaded through a copy.
type repoPair struct {
	SrcRepo string
	DstRepo string
}

// manifestCopy is a single manifest to copy, along with the tags to apply to
// it in the destination repository.
type manifestCopy struct {
	desc     oci.Descriptor
	contents []byte
	tags     []string
}

// blobCopy is a single blob to copy.
type blobCopy struct {
	desc oci.Descriptor
	// completeStatus is the status reported once the blob has transferred.
	completeStatus string
	// report enables progress events; config and attestation blobs are
	// copied silently because docker clients don't display them as layers.
	report bool
}

// CopyImage copies the image named by req from the source registry to the
// destination registry, and returns the digest of the copied root manifest.
//
// A copy by tag is flattened onto a single platform, since a tag can only
// name one image. A copy by digest is copied verbatim, so that the requested
// digest resolves in the destination.
func (c *Copier) CopyImage(ctx context.Context, req CopyRequest) (digest oci.Digest, err error) {
	var (
		manifest oci.BlobReader
		tags     = req.DstTags
	)
	if req.SrcDigest != "" {
		manifest, err = c.Src.GetManifest(ctx, req.SrcRepo, req.SrcDigest)
	} else {
		srcTag := req.SrcTag
		if srcTag == "" {
			srcTag = "latest"
		}
		if len(tags) == 0 {
			tags = []string{srcTag}
		}
		manifest, err = c.Src.GetTag(ctx, req.SrcRepo, srcTag)
	}
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := manifest.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	desc := manifest.Descriptor()
	contents, err := io.ReadAll(manifest)
	if err != nil {
		return "", err
	}

	return desc.Digest, c.copyManifest(ctx, req.repoPair, manifestCopy{desc: desc, contents: contents, tags: tags})
}

// copyManifest recursively copies the given manifest, its child manifests,
// config and blobs.
func (c *Copier) copyManifest(ctx context.Context, repos repoPair, m manifestCopy) error {
	var manifest oci.IndexOrManifest
	if err := json.Unmarshal(m.contents, &manifest); err != nil {
		return fmt.Errorf("decode manifest %s: %w", m.desc.Digest, err)
	}

	isIndex := len(manifest.Manifests) > 0
	children := childManifests{{descriptor: m.desc, tags: m.tags}}
	switch {
	case isIndex && len(m.tags) > 0:
		selected, err := selectManifest(c.Platform, manifest.Manifests)
		if err != nil {
			return err
		}
		children = childManifestsForPlatform(manifest.Manifests, selected, m.tags)
	case isIndex:
		children = allChildManifests(manifest.Manifests)
	}

	for _, child := range children {
		if child.descriptor.Digest == m.desc.Digest {
			continue
		}
		childReader, err := c.Src.GetManifest(ctx, repos.SrcRepo, child.descriptor.Digest)
		if err != nil {
			return fmt.Errorf("get child manifest %s: %w", child.descriptor.Digest, err)
		}
		childDesc := childReader.Descriptor()
		childContents, err := io.ReadAll(childReader)
		closeErr := childReader.Close()
		if err != nil {
			return fmt.Errorf("read child manifest %s: %w", child.descriptor.Digest, err)
		}
		if closeErr != nil {
			return fmt.Errorf("close child manifest %s: %w", child.descriptor.Digest, closeErr)
		}
		childCopy := manifestCopy{desc: childDesc, contents: childContents, tags: child.tags}
		if err := c.copyManifest(ctx, repos, childCopy); err != nil {
			return err
		}
	}
	// A tagged index is flattened: the selected child carries the tag, so the
	// index itself is dropped. An untagged index is preserved in full, since
	// dropping platforms would change its bytes and so its digest.
	if isIndex && len(m.tags) > 0 {
		return nil
	}

	if manifest.Config != nil {
		config := blobCopy{desc: *manifest.Config, completeStatus: "Download complete", report: false}
		if err := c.copyBlob(ctx, repos, config); err != nil {
			return err
		}
	}
	for _, layer := range manifest.Layers {
		if err := c.copyBlob(ctx, repos, blobCopy{desc: layer, completeStatus: c.completeStatus(), report: true}); err != nil {
			return err
		}
	}

	if _, err := c.Dst.PushManifest(ctx, repos.DstRepo, m.contents, m.desc.MediaType, &oci.PushManifestParameters{
		Digest: m.desc.Digest,
		Tags:   m.tags,
	}); err != nil {
		return fmt.Errorf("push manifest %s: %w", m.desc.Digest, err)
	}
	return nil
}

// copyBlob copies a single blob from source to destination, reporting
// progress for the transfer. Blob data is streamed directly from the source
// to the destination; it is never buffered in memory.
func (c *Copier) copyBlob(ctx context.Context, repos repoPair, b blobCopy) (err error) {
	id := ShortDigest(b.desc.Digest)

	if cached, err := c.blobExists(ctx, repos.DstRepo, b.desc.Digest); err != nil {
		return err
	} else if cached {
		if b.report {
			stream.Update(c.progress(), id, "Already exists")
		}
		return nil
	}

	// Embedded data: push it directly without a network round trip.
	if len(b.desc.Data) > 0 {
		_, err := c.Dst.PushBlob(ctx, repos.DstRepo, b.desc, bytes.NewReader(b.desc.Data))
		if err == nil && b.report {
			stream.Update(c.progress(), id, b.completeStatus)
		}
		return err
	}

	blob, err := c.Src.GetBlob(ctx, repos.SrcRepo, b.desc.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", b.desc.Digest, err)
	}
	defer func() {
		if closeErr := blob.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	var reader io.Reader = blob
	if b.report {
		pr := stream.NewProgressReader(blob, c.progress(), b.desc.Size, id, "Downloading")
		defer func() { _ = pr.Close() }()
		reader = pr
	}
	if _, err := c.Dst.PushBlob(ctx, repos.DstRepo, b.desc, reader); err != nil {
		return fmt.Errorf("push blob %s: %w", b.desc.Digest, err)
	}
	if b.report {
		stream.Update(c.progress(), id, b.completeStatus)
	}
	return nil
}

// blobExists reports whether a blob with the given digest is already present
// in the destination repository.
func (c *Copier) blobExists(ctx context.Context, repo string, digest oci.Digest) (bool, error) {
	blob, err := c.Dst.GetBlob(ctx, repo, digest)
	if err == nil {
		return true, blob.Close()
	}
	if errors.Is(err, oci.ErrNameUnknown) || errors.Is(err, oci.ErrBlobUnknown) {
		return false, nil
	}
	return false, err
}

func (c *Copier) progress() stream.ProgressWriter {
	if c.Progress == nil {
		return stream.DiscardOutput()
	}
	return c.Progress
}

func (c *Copier) completeStatus() string {
	if c.CompleteStatus == "" {
		return "Copy complete"
	}
	return c.CompleteStatus
}
