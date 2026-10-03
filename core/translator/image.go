package translator

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	localregistry "github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
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
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if count := imageRemovalContainerCount(images, image.ID, name, id); count > 0 {
		return nil, Conflict(fmt.Errorf("unable to remove %s: image is used by %d container(s)", name, count))
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

func (r *Registry) TagImage(ctx context.Context, name string, ref ociref.Reference) error {
	return r.registry.TagImage(ctx, name, ref)
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

func (r *Registry) PushImage(ctx context.Context, ref ociref.Reference, options imagebackend.PushOptions) error {
	return r.registry.PushImage(ctx, ref, options)
}

func (r *Registry) Search(ctx context.Context, searchFilters filters.Args, term string, limit int, auth *registry.AuthConfig, headers map[string][]string) ([]registry.SearchResult, error) {
	return r.registry.Search(ctx, searchFilters, term, limit, auth, headers)
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

func imageRemovalContainerCount(images []imagetypes.Summary, imageID, name string, id identity.Identity) int64 {
	name = strings.TrimSpace(name)
	_, encoded, _ := strings.Cut(imageID, ":")
	// IDs remove the image rather than one tag, including truncated IDs.
	if (strings.Contains(name, ":") && strings.HasPrefix(imageID, name)) ||
		(len(name) >= 4 && strings.HasPrefix(encoded, name)) {
		return imageContainerCount(images, imageID)
	}
	ref, err := ociref.ParseRelative(name)
	if err != nil || ref.Digest != "" {
		return imageContainerCount(images, imageID)
	}
	ref = localregistry.BuildReference(id, ref)
	if ref.Tag == "" {
		ref.Tag = "latest"
	}
	tag := strings.TrimPrefix(ref.Repository, id.Namespace+"/") + ":" + ref.Tag
	for _, target := range images {
		if target.ID != imageID || !slices.Contains(target.RepoTags, tag) {
			continue
		}
		var count int64
		for _, image := range images {
			if image.ID != imageID || !slices.ContainsFunc(image.RepoDigests, func(digest string) bool {
				return slices.Contains(target.RepoDigests, digest)
			}) {
				continue
			}
			// Another reference in this repository keeps the pinned digest available.
			if slices.ContainsFunc(image.RepoTags, func(other string) bool { return other != tag }) {
				return 0
			}
			if image.Containers > count {
				count = image.Containers
			}
		}
		return count
	}
	return imageContainerCount(images, imageID)
}
