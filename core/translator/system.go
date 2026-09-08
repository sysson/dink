package translator

import (
	"context"
	"runtime"

	"github.com/moby/moby/api/types/system"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/types"
)

func (d *Docker) SystemInfo() {
}

func (d *Docker) SystemVersion() (system.VersionResponse, error) {
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

func (d *Docker) SystemDiskUsage() {
}

func (d *Docker) SubscribeToEvents() {
}

func (d *Docker) UnsubscribeFromEvents() {
}

func (d *Docker) AuthenticateToRegistry(ctx context.Context, auth types.RegistryAuth) (string, error) {
	return registry.Authenticate(ctx, auth)
}

func (s *Swarm) Info() {
}

func (b *Builder) DiskUsage() {
}

func (d *Docker) Status() {
}
