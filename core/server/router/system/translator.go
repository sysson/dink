package system

import (
	"context"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/pkg/filters"
)

type Translator interface {
	SystemInfo(context.Context) (*system.Info, error)
	SystemVersion(context.Context) (system.VersionResponse, error)
	SystemDiskUsage(context.Context, backend.DiskUsageOptions) (*backend.DiskUsage, error)
	SubscribeToEvents(context.Context, time.Time, time.Time, filters.Args) ([]events.Message, chan any, error)
	UnsubscribeFromEvents(context.Context, chan any) error
	AuthenticateToRegistry(ctx context.Context, auth *registry.AuthConfig) (string, error)
}

type ClusterTranslator interface {
	Info(context.Context) (swarm.Info, error)
}

type BuildTranslator interface {
	DiskUsage(context.Context, buildbackend.DiskUsageOptions) (*buildbackend.DiskUsage, error)
}

type StatusTranslator interface {
	Status(context.Context) (string, error)
}
