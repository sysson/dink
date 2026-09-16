package registry

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/logx"
	"github.com/sysson/syskit/stream"
)

func (r *RegistryService) ImageDelete()  {}
func (r *RegistryService) ImageHistory() {}

func (r *RegistryService) GetImage()          {}
func (r *RegistryService) ImageInspect()      {}
func (r *RegistryService) ImageAttestations() {}
func (r *RegistryService) TagImage()          {}
func (r *RegistryService) ImagePrune()        {}
func (r *RegistryService) LoadImage()         {}
func (r *RegistryService) ImportImage()       {}
func (r *RegistryService) ExportImage()       {}
func (r *RegistryService) PushImage()         {}
func (r *RegistryService) Search()            {}

// PullImage pulls the image identified by ref from its source registry into
// the internal registry, namespaced by the identity present in ctx, and
// streams progress to options.OutStream in the JSON stream format understood
// by docker clients.
func (r *RegistryService) PullImage(ctx context.Context, ref ociref.Reference, options types.ImagePullOptions) error {

	if len(options.Platforms) > 1 {
		return fmt.Errorf("pulling multiple platforms is not supported")
	}

	internal, err := r.client()
	if err != nil {
		return err
	}

	client, err := NewClient(ref.Host, ClientOptions{
		Auth:      options.Auth,
		Transport: RegistryTransport(nil, options.MetaHeaders),
	})
	if err != nil {
		return err
	}

	progressChan := make(chan stream.Progress, 100)
	out := stream.ChanOutput(progressChan)
	writesDone := make(chan struct{})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		writeDistributionProgress(ctx, cancel, options.OutStream, progressChan)
		close(writesDone)
	}()

	pm := platformMatcher(options.Platforms)

	p := &puller{
		client:      client,
		destination: internal,
		platform:    pm,
		progress:    out,
	}

	err = p.pullRepo(ctx, ref)

	_ = out.Close()
	close(progressChan)
	<-writesDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		if isNotFound(err) {
			return httpx.NotFound(err)
		}
		logx.G(ctx).WithError(err).Error("Pulling Image", "Ref", ref.String())
		return err
	}
	return nil
}

type puller struct {
	client      oci.Interface
	destination oci.Interface
	progress    stream.ProgressWriter
	platform    platforms.MatchComparer
}

func (p *puller) pullRepo(ctx context.Context, ref ociref.Reference) error {

	var tags []string
	if ref.Tag == "" && ref.Digest == "" {
		all, err := oci.All(p.client.Tags(ctx, ref.Repository, &oci.TagsParameters{
			Limit: -1,
		}))
		if err != nil {
			return err
		}
		tags = all
	} else {
		tags = []string{ref.Tag}
	}

	for _, tag := range tags {
		r := ref
		r.Tag = tag

		ok, err := p.pullTag(ctx, r)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		p.writeStatus(dockerFriendlyName(r), ok)
	}

	return nil
}

func (p *puller) pullTag(ctx context.Context, ref ociref.Reference) (copied bool, err error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false, fmt.Errorf("missing identity in context")
	}

	if ref.Digest == "" {
		desc, err := getTagDigest(ctx, p.client, ref)
		if err != nil {
			return false, err
		}
		ref.Digest = desc.Digest
	}

	dstRef := repositoryFor(id, ref)
	stream.Messagef(p.progress, tagOrDigest(ref), "Pulling from %s", ref.Repository)

	defer func() {
		if err == nil && ctx.Err() == nil {
			stream.Messagef(p.progress, "", "Digest: %s", ref.Digest)
		}
	}()

	existing, err := contentExists(
		p.destination.ResolveManifest(ctx, dstRef.Repository, ref.Digest),
	)
	if err != nil {
		return false, nil
	}

	if existing != nil {
		return false, p.ensureTagged(ctx, ref, dstRef)
	}

	return p.pull(ctx, ref, dstRef)
}

func (p *puller) ensureTagged(ctx context.Context, ref ociref.Reference, dstRef ociref.Reference) error {
	tagged, err := isTagged(ctx, p.destination, dstRef)
	if err != nil {
		return err
	}
	if tagged {
		return nil
	}

	manifest, err := getManifest(ctx, p.client, ref)
	if err != nil {
		return err
	}

	return pushManifest(ctx, p.destination, dstRef, manifest)
}

func (p *puller) pull(ctx context.Context, ref ociref.Reference, dstRef ociref.Reference) (bool, error) {
	descriptors, err := allDescriptors(ctx, p.client, ref, p.platform)
	if err != nil {
		return false, err
	}

	c := &copier{
		client:      p.client,
		destination: p.destination,
		srcRef:      ref,
		dstRef:      dstRef,
		out:         p.progress,
	}

	ok, err := c.copyBlobs(ctx, blobsFromDescriptors(descriptors))
	if err != nil {
		return false, err
	}

	return ok, pushManifests(ctx, p.destination, dstRef, descriptors)
}

func (p *puller) writeStatus(requestedTag string, layersDownloaded bool) {
	if layersDownloaded {
		stream.Message(p.progress, "", "Status: Downloaded newer image for "+requestedTag)
	} else {
		stream.Message(p.progress, "", "Status: Image is up to date for "+requestedTag)
	}
}

type copier struct {
	client      oci.Interface
	destination oci.Interface
	srcRef      ociref.Reference
	dstRef      ociref.Reference
	out         stream.ProgressWriter
}

func (c *copier) copyBlobs(ctx context.Context, blobs <-chan oci.Descriptor) (bool, error) {
	const maxConcurrentLayers = 3

	var (
		wg     sync.WaitGroup
		slots  = make(chan struct{}, maxConcurrentLayers)
		errs   = make(chan error)
		copied atomic.Bool
	)

	stopAndReturn := func() (bool, error) {
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return false, err
		}
		return copied.Load(), ctx.Err()
	}

	for layer := range blobs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			// stop scheduling new work
			return stopAndReturn()
		}

		wg.Go(func() {
			defer func() { <-slots }()
			if ok, err := c.copyBlob(ctx, layer); err != nil {
				errs <- err
			} else if ok {
				copied.Store(true)
			}
		})
	}
	return stopAndReturn()
}

func (c *copier) copyBlob(ctx context.Context, desc oci.Descriptor) (bool, error) {
	id := idForRef(desc.Digest)

	report := func(msg string) {
		stream.Update(c.out, id, msg)
	}

	reportType := func() {
		if isConfig(desc.MediaType) {
			return
		}
		if isImageLayer(desc.MediaType) {
			report("Pull Complete")
			return
		}
		report("Download Complete")
	}

	if cached, err := contentExists(c.destination.ResolveBlob(ctx, c.dstRef.Repository, desc.Digest)); err != nil {
		return false, err
	} else if cached != nil {
		report("Already exists")
		return false, nil
	}

	if len(desc.Data) > 0 {
		_, err := c.destination.PushBlob(ctx, c.dstRef.Repository, desc, bytes.NewReader(desc.Data))
		if err == nil {
			reportType()
		}
		return err == nil, err
	}

	blob, err := c.client.GetBlob(ctx, c.srcRef.Repository, desc.Digest)
	if err != nil {
		return false, fmt.Errorf("get blob %s: %w", desc.Digest, err)
	}

	defer func() {
		if closeErr := blob.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	var reader io.Reader = blob

	if !isConfig(desc.MediaType) {
		msg := "Pulling"
		if !isImageLayer(desc.MediaType) {
			msg = "Downloading"
		}
		pr := stream.NewProgressReader(blob, c.out, desc.Size, id, msg)
		defer func() { _ = pr.Close() }()
		reader = pr
	}

	if _, err := c.destination.PushBlob(ctx, c.dstRef.Repository, desc, reader); err != nil {
		return false, fmt.Errorf("push blob %s: %w", desc.Digest, err)
	}

	reportType()
	return true, nil
}

func getTagDigest(ctx context.Context, client oci.Interface, ref ociref.Reference) (oci.Descriptor, error) {
	if ref.Tag == "" {
		return oci.Descriptor{}, fmt.Errorf("tag must be specified")
	}
	desc, err := client.ResolveTag(ctx, ref.Repository, ref.Tag)
	if err != nil {
		return oci.Descriptor{}, err
	}
	if ref.Digest != "" && ref.Digest != desc.Digest {
		return oci.Descriptor{}, fmt.Errorf("digest %s does not match tag %s", ref.Digest, desc.Digest)
	}
	return desc, nil
}

func isTagged(ctx context.Context, client oci.Interface, ref ociref.Reference) (bool, error) {
	tag, err := contentExists(getTagDigest(ctx, client, ref))
	if err != nil {
		return false, err
	}
	return tag != nil, nil
}

func readBlob[T any](reader io.Reader) (T, error) {
	var result T
	err := json.UnmarshalDecode(jsontext.NewDecoder(reader), &result)
	return result, err
}

func contentExists(desc oci.Descriptor, err error) (*oci.Descriptor, error) {
	if err == nil {
		return &desc, nil
	}
	if isNotFound(err) {
		return nil, nil
	}
	return nil, err
}

func isNotFound(err error) bool {
	return errors.Is(err, oci.ErrNameUnknown) ||
		errors.Is(err, oci.ErrManifestUnknown) ||
		errors.Is(err, oci.ErrBlobUnknown)
}
