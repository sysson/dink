package build

import (
	"context"

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

type Translator interface {
	Build(context.Context, buildbackend.BuildConfig) (string, error)
	PruneCache(context.Context, buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error)
	Cancel(context.Context, string) error
}
