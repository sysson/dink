package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/sysson/syskit/stream"
)

// maxConcurrentLayers bounds how many layer blobs of a single image are
// transferred at the same time.
const maxConcurrentLayers = 3

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

// blobCopy is a single blob to copy.
type blobCopy struct {
	desc oci.Descriptor
	// completeStatus is the status reported once the blob has transferred.
	completeStatus string
	// report enables progress events; config and attestation blobs are
	// copied silently because docker clients don't display them as layers.
	report bool
}

// CopyResult reports the outcome of an image copy.
type CopyResult struct {
	// Digest is the digest the source reference resolved to, which for a
	// multi-platform image is the digest of the index rather than of the
	// platform manifest the copy was flattened onto.
	Digest oci.Digest
	// Cached is true when the destination already resolved the reference and
	// nothing was transferred.
	Cached bool
}

// CopyImage copies the image named by req from the source registry to the
// destination registry.
//
// A copy by tag is flattened onto a single platform, since a tag can only
// name one image. A copy by digest is copied verbatim, so that the requested
// digest resolves in the destination.
func (c *Copier) CopyImage(ctx context.Context, req CopyRequest) (CopyResult, error) {
	// The source reference is resolved even when the destination may already
	// hold the image, since a tag can be moved to a different image at any
	// time and the digest it currently names is part of the result.
	root, tags, err := c.resolveManifest(ctx, req)
	if err != nil {
		return CopyResult{}, err
	}

	cached, err := c.alreadyCopied(ctx, req.repoPair, root, tags)
	if err != nil {
		return CopyResult{}, err
	}
	if !cached {
		if err := c.copyManifest(ctx, req.repoPair, root, tags); err != nil {
			return CopyResult{}, err
		}
	}
	return CopyResult{Digest: root.desc.Digest, Cached: cached}, nil
}

// alreadyCopied reports whether the destination already resolves the copy.
// Because a tagged index is flattened, what the destination tags must name is
// the manifest selected for the platform, not the index itself.
func (c *Copier) alreadyCopied(ctx context.Context, repos repoPair, root fetchedManifest, tags []string) (bool, error) {
	want := root.desc.Digest
	if root.isIndex() && len(tags) > 0 {
		selected, err := selectManifest(c.Platform, root.manifests)
		if err != nil {
			return false, err
		}
		want = selected.Digest
	}

	// An untagged copy is addressed by digest alone.
	if len(tags) == 0 {
		return contentExists(c.Dst.GetManifest(ctx, repos.DstRepo, want))
	}
	for _, tag := range tags {
		manifest, err := c.Dst.GetTag(ctx, repos.DstRepo, tag)
		if isNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		digest := manifest.Descriptor().Digest
		if err := manifest.Close(); err != nil {
			return false, err
		}
		if digest != want {
			return false, nil
		}
	}
	return true, nil
}

// copyLayers copies the layer blobs of a manifest, transferring up to
// maxConcurrentLayers of them at a time. The first failure cancels the
// transfers still in flight and is returned once they have stopped.
func (c *Copier) copyLayers(ctx context.Context, repos repoPair, layers []oci.Descriptor) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, maxConcurrentLayers)
		errs  = make(chan error, len(layers))
	)

	stopAndReturn := func() error {
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return err
		}
		return ctx.Err()
	}

	for _, layer := range layers {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			// stop scheduling new work
			return stopAndReturn()
		}

		wg.Go(func() {
			defer func() { <-slots }()

			b := blobCopy{desc: layer, completeStatus: c.completeStatus(), report: true}
			if err := c.copyBlob(ctx, repos, b); err != nil {
				errs <- err
				cancel()
			}
		})
	}
	return stopAndReturn()
}

// copyBlob copies a single blob from source to destination, reporting
// progress for the transfer. Blob data is streamed directly from the source
// to the destination; it is never buffered in memory.
func (c *Copier) copyBlob(ctx context.Context, repos repoPair, b blobCopy) (err error) {
	id := shortDigest(b.desc.Digest)
	progress := c.progress()

	if cached, err := contentExists(c.Dst.GetBlob(ctx, repos.DstRepo, b.desc.Digest)); err != nil {
		return err
	} else if cached {
		if b.report {
			stream.Update(progress, id, "Already exists")
		}
		return nil
	}

	// Embedded data: push it directly without a network round trip.
	if len(b.desc.Data) > 0 {
		_, err := c.Dst.PushBlob(ctx, repos.DstRepo, b.desc, bytes.NewReader(b.desc.Data))
		if err == nil && b.report {
			stream.Update(progress, id, b.completeStatus)
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
		pr := stream.NewProgressReader(blob, progress, b.desc.Size, id, "Downloading")
		defer func() { _ = pr.Close() }()
		reader = pr
	}
	if _, err := c.Dst.PushBlob(ctx, repos.DstRepo, b.desc, reader); err != nil {
		return fmt.Errorf("push blob %s: %w", b.desc.Digest, err)
	}
	if b.report {
		stream.Update(progress, id, b.completeStatus)
	}
	return nil
}

// contentExists reports whether a fetch found the content, treating a
// registry "not found" as a negative answer rather than an error. It takes
// the results of a get so that it can be called on one directly.
func contentExists(content oci.BlobReader, err error) (bool, error) {
	if err == nil {
		return true, content.Close()
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// isNotFound reports whether err says the registry does not hold the
// requested repository, manifest or blob.
func isNotFound(err error) bool {
	return errors.Is(err, oci.ErrNameUnknown) ||
		errors.Is(err, oci.ErrManifestUnknown) ||
		errors.Is(err, oci.ErrBlobUnknown)
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
