package registry

import (
	"context"
	"fmt"

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
func (r *RegistryService) PullImage(ctx context.Context, ref types.Reference, options types.ImagePullOptions) error {

	progressChan := make(chan stream.Progress, 100)
	writesDone := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		writeDistributionProgress(ctx, cancel, options.OutStream, progressChan)
		close(writesDone)
	}()
	out := stream.ChanOutput(progressChan)
	err := r.pullImage(ctx, ref, options, out)
	// Close the writer first so no in-flight update is sent on a closed channel.
	_ = out.Close()
	close(progressChan)
	<-writesDone
	return err
}

func (r *RegistryService) pullImage(ctx context.Context, src types.Reference, options types.ImagePullOptions, out stream.ProgressWriter) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return fmt.Errorf("missing identity in context")
	}
	internal, err := r.client()
	if err != nil {
		return err
	}

	dstRef := repositoryFor(id, src)

	source, err := NewClient(src.Host, ClientOptions{
		Auth:      options.Auth,
		Transport: RegistryTransport(nil, options.MetaHeaders),
	})
	if err != nil {
		return err
	}

	stream.Messagef(out, src.String(), "Pulling from %s", src.Repository)

	copier := &Copier{
		Src:            source,
		SrcRef:         src,
		Dst:            internal,
		DstRef:         dstRef,
		Progress:       out,
		Platform:       platformMatcher(options.Platforms),
		CompleteStatus: "Pull complete",
	}
	result, err := copier.CopyImage(ctx)
	if err != nil {
		return err
	}

	status := "Downloaded newer image"
	if result.Cached {
		status = "Image is up to date"
	}
	stream.Messagef(out, "", "Digest: %s", result.Digest)
	stream.Messagef(out, "", "Status: %s for %s", status, src.ID())
	return nil
}
