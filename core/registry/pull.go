package registry

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/containerd/platforms"
	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
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

	id, ok := identity.FromContext(ctx)
	if !ok {
		return fmt.Errorf("missing identity in context")
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
	writesDone := make(chan struct{})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		writeDistributionProgress(ctx, cancel, options.OutStream, progressChan)
		close(writesDone)
	}()

	out := stream.ChanOutput(progressChan)
	defer func() {
		_ = out.Close()
	}()

	pm := platformMatcher(options.Platforms)
	var cached bool
	if ref.Tag == "" && ref.Digest == "" {
		stream.Messagef(out, ref.Tag, "Pulling repository %s", ref.Repository)
		tags, err := oci.All(client.Tags(ctx, ref.Repository, &oci.TagsParameters{}))
		if err != nil {
			return err
		}

		for _, tag := range tags {
			srcRef := ref
			srcRef.Tag = tag
			dstRef := repositoryFor(id, srcRef)
			p := &puller{
				client:      client,
				destination: internal,
				srcRef:      srcRef,
				dstRef:      dstRef,
			}
			ok, err := p.pullExists(ctx)
			if err != nil {
				return err
			}
			if ok {
				stream.Messagef(out, idForRef(p.digest), "Already Pulled")
				continue
			}

			copier := &copier{
				client:      client,
				destination: internal,
				dstRef:      dstRef,
				srcRef:      srcRef,
				digest:      p.digest,
				platform:    pm,
				Aggregate:   true,
			}
			err = copier.copy(ctx, out)
			if err != nil {
				return err
			}
		}
	} else {
		msg := ref.Tag
		if ref.Tag == "" {
			msg = ref.String()
		}
		stream.Messagef(out, "", msg+": Pulling from %s", ref.Repository)
		dstRef := repositoryFor(id, ref)
		p := &puller{
			client:      client,
			destination: internal,
			srcRef:      ref,
			dstRef:      dstRef,
		}
		ok, err := p.pullExists(ctx)
		if err != nil {
			return err
		}
		if ok {
			cached = true
		} else {
			copier := &copier{
				client:      client,
				destination: internal,
				dstRef:      dstRef,
				srcRef:      ref,
				digest:      p.digest,
				platform:    pm,
			}
			err = copier.copy(ctx, out)
			if err != nil {
				return err
			}
		}
		stream.Messagef(out, "", "Digest: %s", p.digest)
	}
	status := "Downloaded newer image"
	if cached {
		status = "Image is up to date"
	}
	stream.Messagef(out, "", "Status: %s for %s", status, strings.TrimPrefix(ref.String(), "docker.io/library/"))
	stream.Messagef(out, "", "%s", ref.String())
	// Close the writer first so no in-flight update is sent on a closed channel.
	_ = out.Close()
	close(progressChan)
	<-writesDone
	return err
}

func tagImage(ctx context.Context, client oci.Interface, ref ociref.Reference, blob oci.BlobReader) error {
	contents, err := io.ReadAll(blob)
	defer func() {
		_ = blob.Close()
	}()
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", blob.Descriptor().Digest, err)
	}
	desc := blob.Descriptor()
	_, err = client.PushManifest(ctx, ref.Repository, contents, desc.MediaType, &oci.PushManifestParameters{
		Digest: desc.Digest,
		Tags:   []string{ref.Tag},
	})
	if err != nil {
		return fmt.Errorf("tag manifest %s: %w", desc.Digest, err)
	}
	return nil
}

type puller struct {
	client      oci.Interface
	destination oci.Interface
	srcRef      ociref.Reference
	dstRef      ociref.Reference
	digest      oci.Digest
}

func (p *puller) pullExists(ctx context.Context) (bool, error) {
	if p.srcRef.Digest != "" {
		desc, err := p.client.ResolveManifest(ctx, p.srcRef.Repository, oci.Digest(p.srcRef.Digest))
		if err != nil {
			return false, err
		}
		p.digest = desc.Digest
	}
	if p.srcRef.Tag != "" {
		tagDesc, err := p.client.ResolveTag(ctx, p.srcRef.Repository, p.srcRef.Tag)
		if err != nil {
			return false, err
		}
		if p.srcRef.Digest != "" && p.srcRef.Digest != tagDesc.Digest {
			return false, fmt.Errorf("digest %s does not match tag %s", p.srcRef.Digest, tagDesc.Digest)
		}
		if p.srcRef.Digest == "" {
			p.digest = tagDesc.Digest
		}
	}
	_, err := p.destination.ResolveManifest(ctx, p.dstRef.Repository, p.digest)
	if err != nil {
		if !isNotFound(err) {
			return false, err
		}
		return false, nil
	}
	if p.dstRef.Tag != "" {
		_, err = p.destination.ResolveTag(ctx, p.dstRef.Repository, p.dstRef.Tag)
		if err != nil {
			if !isNotFound(err) {
				return false, err
			} else {
				blob, err := p.client.GetManifest(ctx, p.srcRef.Repository, p.digest)
				if err != nil {
					return false, err
				}
				err = tagImage(ctx, p.destination, p.dstRef, blob)
				if err != nil {
					return false, err
				}
			}
		}
	}
	return true, nil
}

type copier struct {
	client      oci.Interface
	destination oci.Interface
	digest      oci.Digest
	platform    platforms.MatchComparer
	dstRef      ociref.Reference
	srcRef      ociref.Reference
	Aggregate   bool
	TotalSize   atomic.Int64
	blobs       chan oci.Descriptor
	manifests   []manifestCopy
}

type manifestCopy struct {
	raw        []byte
	descriptor oci.Descriptor
}

func (c *copier) copy(ctx context.Context, progress stream.ProgressWriter) error {
	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errChan := make(chan error, 1)
	c.blobs = make(chan oci.Descriptor, 10)
	progressChan := make(chan stream.Progress, 100)
	stopChan := make(chan struct{})
	out := progress

	if c.Aggregate {

		streamOut := stream.ChanOutput(progressChan)
		go func() {
			var currentSize int64
			for p := range progressChan {
				currentSize += p.Current
				if currentSize >= c.TotalSize.Load() {
					stream.Update(progress, idForRef(c.digest), "Download Complete")
					break
				}
				_ = progress.WriteProgress(stream.Progress{
					ID:      idForRef(c.digest),
					Action:  "Downloading",
					Current: currentSize,
					Total:   c.TotalSize.Load(),
				})
			}
			_ = streamOut.Close()
			close(stopChan)
		}()
		out = streamOut
	}

	var wg sync.WaitGroup

	wg.Go(func() {
		if err := c.copyBlobs(copyCtx, cancel, out); err != nil {
			select {
			case errChan <- err:
			default:
			}
			cancel()
		}
	})
	wg.Go(func() {
		defer close(c.blobs)

		if err := c.walkManifest(ctx, c.digest); err != nil {
			select {
			case errChan <- err:
			default:
			}
			cancel()
			return
		}

		referrers, err := oci.All(c.client.Referrers(ctx, c.srcRef.Repository, c.digest, nil))
		if err != nil {
			select {
			case errChan <- err:
			default:
			}
			cancel()
			return
		}

		for _, ref := range referrers {
			if ok, err := contentExists(c.destination.ResolveManifest(ctx, c.dstRef.Repository, ref.Digest)); ok && err == nil {
				continue
			}
			if err := c.walkManifest(ctx, ref.Digest); err != nil {
				select {
				case errChan <- err:
				default:
				}
				cancel()
				return
			}
		}
	})

	wg.Wait()
	if c.Aggregate {
		close(progressChan)
		<-stopChan
	}
	select {
	case err := <-errChan:
		return err
	default:
	}

	return c.copyManifests(ctx)

}

func (c *copier) walkManifest(ctx context.Context, digest oci.Digest) error {
	blob, err := c.client.GetManifest(ctx, c.srcRef.Repository, digest)
	if err != nil {
		return err
	}
	defer func() {
		_ = blob.Close()
	}()

	raw, err := io.ReadAll(blob)
	if err != nil {
		return err
	}

	manifest, err := readBlob[oci.IndexOrManifest](bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c.manifests = append(c.manifests, manifestCopy{
		raw:        raw,
		descriptor: blob.Descriptor(),
	})

	switch {
	case isIndex(blob.Descriptor().MediaType):
		mfstDescriptors, err := selectManifests(c.platform, &manifest)
		if err != nil {
			return err
		}
		if len(mfstDescriptors) == 0 {
			return fmt.Errorf("no manifests found in index for platform %v", c.platform)
		}
		var errs []error
		for _, desc := range mfstDescriptors {
			if ok, err := contentExists(c.destination.ResolveManifest(ctx, c.dstRef.Repository, desc.Digest)); ok && err == nil {
				continue
			}
			err := c.walkManifest(ctx, oci.Digest(desc.Digest))
			if err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("errors occurred while walking manifests: %v", errs)
		}
	case isManifest(blob.Descriptor().MediaType):
		if manifest.Config != nil {
			c.blobs <- *manifest.Config
		}

		for _, layer := range manifest.Layers {
			c.blobs <- layer
			c.TotalSize.Add(int64(layer.Size))
		}
	default:
		return fmt.Errorf("unsupported media type: %s", blob.Descriptor().MediaType)
	}

	return nil
}

func (c *copier) copyBlobs(ctx context.Context, cancel context.CancelFunc, out stream.ProgressWriter) error {

	const maxConcurrentLayers = 3

	var (
		wg    sync.WaitGroup
		slots = make(chan struct{}, maxConcurrentLayers)
		errs  = make(chan error)
	)

	stopAndReturn := func() error {
		wg.Wait()
		close(errs)
		if err := <-errs; err != nil {
			return err
		}
		return ctx.Err()
	}

	for layer := range c.blobs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			// stop scheduling new work
			return stopAndReturn()
		}

		wg.Go(func() {
			defer func() { <-slots }()
			if err := c.copyBlob(ctx, layer, out); err != nil {
				errs <- err
				cancel()
			}
		})
	}
	return stopAndReturn()
}

func (c *copier) copyBlob(ctx context.Context, desc oci.Descriptor, out stream.ProgressWriter) error {
	id := idForRef(desc.Digest)
	report := func(msg string) {
		if !c.Aggregate {
			stream.Update(out, id, msg)
		}
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
		return err
	} else if cached {
		report("Already exists")
		return nil
	}
	if len(desc.Data) > 0 {
		_, err := c.destination.PushBlob(ctx, c.dstRef.Repository, desc, bytes.NewReader(desc.Data))
		if err == nil {
			reportType()
		}
		return err
	}
	blob, err := c.client.GetBlob(ctx, c.srcRef.Repository, desc.Digest)
	if err != nil {
		return fmt.Errorf("get blob %s: %w", desc.Digest, err)
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
		pr := stream.NewProgressReader(blob, out, desc.Size, id, msg)
		defer func() { _ = pr.Close() }()
		reader = pr
	}
	if _, err := c.destination.PushBlob(ctx, c.dstRef.Repository, desc, reader); err != nil {
		return fmt.Errorf("push blob %s: %w", desc.Digest, err)
	}
	reportType()
	return nil
}

func (c *copier) copyManifests(ctx context.Context) error {
	for _, manifest := range slices.Backward(c.manifests) {
		var tags []string
		if manifest.descriptor.Digest == c.digest {
			tags = append(tags, c.srcRef.Tag)
		}
		if len(manifest.descriptor.Data) == 0 {
			manifest.descriptor.Data = manifest.raw
		}
		_, err := c.destination.PushManifest(ctx, c.dstRef.Repository, manifest.descriptor.Data, manifest.descriptor.MediaType, &oci.PushManifestParameters{
			Digest: manifest.descriptor.Digest,
			Tags:   tags,
		})
		if err != nil {
			return fmt.Errorf("\npush manifest %s: %w", manifest.descriptor.Digest, err)
		}
	}
	return nil
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

func readBlob[T any](reader io.Reader) (T, error) {
	var result T
	err := json.UnmarshalDecode(jsontext.NewDecoder(reader), &result)
	return result, err
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

func contentExists(_ oci.Descriptor, err error) (bool, error) {
	if err == nil {
		return true, err
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

func isNotFound(err error) bool {
	return errors.Is(err, oci.ErrNameUnknown) ||
		errors.Is(err, oci.ErrManifestUnknown) ||
		errors.Is(err, oci.ErrBlobUnknown)
}

func idForRef(ref oci.Digest) string {
	const maxLen = 12
	s := ref.Encoded()
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
