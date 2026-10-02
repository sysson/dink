package translator

import (
	"context"
	"errors"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

func (b *Builder) Build(context.Context, buildbackend.BuildConfig) (string, error) {
	return "", ErrNotImplemented
}

// PruneCache is empty when builds are disabled. Shared backend cache cannot
// safely be pruned on behalf of a single tenant.
func (b *Builder) PruneCache(context.Context, buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	if b.gateway != nil && b.gateway.Enabled() {
		return nil, Unsupported(errors.New("pruning a shared BuildKit cache is not supported"))
	}
	return &buildtypes.CachePruneReport{CachesDeleted: []string{}}, nil
}

func (b *Builder) Cancel(context.Context, string) error {
	return ErrNotImplemented
}
