package translator

import (
	"context"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

func (b *Builder) Build(context.Context, buildbackend.BuildConfig) (string, error) {
	return "", ErrNotImplemented
}

func (b *Builder) PruneCache(context.Context, buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	return nil, ErrNotImplemented
}

func (b *Builder) Cancel(context.Context, string) error {
	return ErrNotImplemented
}
