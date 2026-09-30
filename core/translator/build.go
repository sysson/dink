package translator

import (
	"context"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

func (b *Builder) Build(context.Context, buildbackend.BuildConfig) (string, error) {
	return "", ErrNotImplemented
}

// PruneCache reports nothing pruned because Dink does not build images, so has no build cache.
func (b *Builder) PruneCache(context.Context, buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	return &buildtypes.CachePruneReport{CachesDeleted: []string{}}, nil
}

func (b *Builder) Cancel(context.Context, string) error {
	return ErrNotImplemented
}
