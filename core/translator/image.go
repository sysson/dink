package translator

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (r *Registry) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	image, err := r.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{})
	if err != nil {
		return nil, err
	}
	images, err := r.Images(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	if count := imageContainerCount(images, image.ID); count > 0 {
		return nil, httpx.Conflict(fmt.Errorf("unable to remove %s: image is used by %d container(s)", name, count))
	}
	return r.registry.ImageDelete(ctx, name, options)
}

func (r *Registry) ImageHistory(ctx context.Context, name string, platform *ocispec.Platform) ([]imagetypes.HistoryResponseItem, error) {
	return r.registry.ImageHistory(ctx, name, platform)
}

func (r *Registry) ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	return r.registry.ImageInspect(ctx, name, options)
}

func (r *Registry) ImageAttestations(ctx context.Context, name string, options imagebackend.AttestationOpts) ([]imagetypes.AttestationStatement, error) {
	return r.registry.ImageAttestations(ctx, name, options)
}

func (r *Registry) GetImage(ctx context.Context, name string, options imagebackend.GetImageOpts) (*imagebackend.InspectData, error) {
	return r.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{Platform: options.Platform})
}

func (r *Registry) TagImage(context.Context, string, ociref.Reference) error {
	return ErrNotImplemented
}

func (r *Registry) LoadImage(context.Context, io.ReadCloser, []ocispec.Platform, io.Writer, bool) error {
	return ErrNotImplemented
}

func (r *Registry) ImportImage(context.Context, ociref.Reference, *ocispec.Platform, string, io.Reader, []string) (string, error) {
	return "", ErrNotImplemented
}

func (r *Registry) ExportImage(context.Context, []string, []ocispec.Platform, io.Writer) error {
	return ErrNotImplemented
}

func (r *Registry) PullImage(ctx context.Context, ref ociref.Reference, options imagebackend.PullOptions) error {
	return r.registry.PullImage(ctx, ref, options)
}

func (r *Registry) PushImage(context.Context, ociref.Reference, imagebackend.PushOptions) error {
	return ErrNotImplemented
}

func (r *Registry) Search(context.Context, filters.Args, string, int, *registry.AuthConfig, map[string][]string) ([]registry.SearchResult, error) {
	return nil, ErrNotImplemented
}

func imageContainerCount(images []imagetypes.Summary, imageID string) int64 {
	var count int64
	for _, image := range images {
		if image.ID == imageID && image.Containers > count {
			count = image.Containers
		}
	}
	return count
}
