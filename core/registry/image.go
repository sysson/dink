package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/types"
	"github.com/sysson/syskit/stream"
)

func (is *ImageService) ImageDelete()       {}
func (is *ImageService) ImageHistory()      {}
func (is *ImageService) Images()            {}
func (is *ImageService) GetImage()          {}
func (is *ImageService) ImageInspect()      {}
func (is *ImageService) ImageAttestations() {}
func (is *ImageService) TagImage()          {}
func (is *ImageService) ImagePrune()        {}
func (is *ImageService) LoadImage()         {}
func (is *ImageService) ImportImage()       {}
func (is *ImageService) ExportImage()       {}
func (is *ImageService) PushImage()         {}
func (is *ImageService) Search()            {}

// PullImage pulls the image identified by ref from its source registry into
// the internal registry, namespaced by the identity present in ctx, and
// streams progress to options.OutStream in the JSON stream format understood
// by docker clients.
func (is *ImageService) PullImage(ctx context.Context, ref ociref.Reference, options types.PullOptions) (err error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return fmt.Errorf("missing identity in context")
	}

	src := newSourceRef(ref)

	out := stream.NewJSONProgressOutput(options.OutStream, false)
	defer func() {
		if closeErr := out.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	internal, err := is.client()
	if err != nil {
		return err
	}

	dstRepo := repositoryFor(id, src)

	// If the image is already present locally, skip the copy entirely.
	if cached, err := imageExists(ctx, internal, dstRepo, src); err != nil {
		return err
	} else if cached {
		stream.Messagef(out, "", "Image is up to date for %s", src)
		return nil
	}

	source, err := NewClient(src.Host, ClientOptions{
		Auth:      options.Auth,
		Transport: RegistryTransport(nil, options.MetaHeaders),
	})
	if err != nil {
		return err
	}

	stream.Messagef(out, src.progressID(), "Pulling from %s/%s", src.Host, src.Repository)

	copier := &Copier{
		Src:            source,
		Dst:            internal,
		Progress:       out,
		Platform:       platformMatcher(options.Platforms),
		CompleteStatus: "Pull complete",
	}
	digest, err := copier.CopyImage(ctx, CopyRequest{
		SrcRepo:   src.Repository,
		SrcTag:    src.Tag,
		SrcDigest: src.Digest,
		DstRepo:   dstRepo,
	})
	if err != nil {
		return err
	}

	stream.Messagef(out, "", "Digest: %s", digest)
	stream.Messagef(out, "", "Downloaded newer image for %s", src)
	return nil
}

// imageExists reports whether the referenced image is already present in a
// repository.
func imageExists(ctx context.Context, reg oci.Interface, repo string, src sourceRef) (bool, error) {
	var (
		manifest oci.BlobReader
		err      error
	)
	if src.Digest != "" {
		manifest, err = reg.GetManifest(ctx, repo, src.Digest)
	} else {
		manifest, err = reg.GetTag(ctx, repo, src.Tag)
	}
	if err == nil {
		return true, manifest.Close()
	}
	if errors.Is(err, oci.ErrNameUnknown) || errors.Is(err, oci.ErrManifestUnknown) {
		return false, nil
	}
	return false, err
}
