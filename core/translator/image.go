package translator

import (
	"context"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/types"
)

func (r *Registry) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	return r.registry.ImageDelete(ctx, name, options)
}
func (r *Registry) ImageHistory() {}
func (r *Registry) Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	return r.registry.Images(ctx, options)
}
func (r *Registry) GetImage()          {}
func (r *Registry) ImageInspect()      {}
func (r *Registry) ImageAttestations() {}
func (r *Registry) TagImage()          {}
func (r *Registry) ImagePrune()        {}
func (r *Registry) LoadImage()         {}
func (r *Registry) ImportImage()       {}
func (r *Registry) ExportImage()       {}
func (r *Registry) PushImage()         {}
func (r *Registry) Search()            {}

func (r *Registry) PullImage(ctx context.Context, ref ociref.Reference, options types.ImagePullOptions) error {
	return r.registry.PullImage(ctx, ref, options)
}
