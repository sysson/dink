package translator

import (
	"context"
	"runtime"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
)

func (d *Docker) SystemInfo(context.Context) (*system.Info, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) SystemVersion(context.Context) (system.VersionResponse, error) {
	v := version.Get()
	ver := system.VersionResponse{
		Platform: system.PlatformInfo{
			Name: "Dink Translation Layer",
		},
		Version:       v.Version,
		APIVersion:    v.APIVersion,
		MinAPIVersion: v.MinAPIVersion,
		Os:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Components: []system.ComponentVersion{
			{
				Name:    "Dink Translation Layer",
				Version: v.Version,
				Details: map[string]string{
					"GitCommit":     v.Commit,
					"ApiVersion":    v.APIVersion,
					"MinAPIVersion": v.MinAPIVersion,
					"GoVersion":     runtime.Version(),
					"Os":            runtime.GOOS,
					"Arch":          runtime.GOARCH,
					"BuildTime":     v.Date,
					"KernelVersion": "unknown",
					"Module":        "github.com/sysson/dink",
					"ModuleVersion": "moduleVersion()",
					"Experimental":  "false",
				},
			},
		},
	}
	return ver, nil
}

func (d *Docker) SystemDiskUsage(context.Context, backend.DiskUsageOptions) (*backend.DiskUsage, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) SubscribeToEvents(context.Context, time.Time, time.Time, filters.Args) ([]events.Message, <-chan any, error) {
	return nil, nil, ErrNotImplemented
}

func (d *Docker) UnsubscribeFromEvents(context.Context, chan any) error {
	return ErrNotImplemented
}

func (d *Docker) AuthenticateToRegistry(ctx context.Context, auth *registry.AuthConfig) (string, error) {
	return d.registry.Authenticate(ctx, auth)
}

func (s *Swarm) Info(context.Context) (swarm.Info, error) {
	return swarm.Info{}, ErrNotImplemented
}

func (b *Builder) DiskUsage(context.Context, buildbackend.DiskUsageOptions) (*buildbackend.DiskUsage, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) Status(context.Context) (string, error) {
	return "", ErrNotImplemented
}
