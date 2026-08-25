package translator

import (
	"runtime"

	"github.com/moby/moby/api/types/system"
	"github.com/sysson/dink/cmd/version"
)

func (d *Docker) SystemInfo() {
}

func (d *Docker) SystemVersion() (system.VersionResponse, error) {
	v := system.VersionResponse{
		Platform: system.PlatformInfo{
			Name: "Dink Translation Layer",
		},
		Version:       version.GetVersion(),
		APIVersion:    "1.55",
		MinAPIVersion: "1.40",
		Os:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Components: []system.ComponentVersion{
			{
				Name:    "Dink Translation Layer",
				Version: version.GetVersion(),
				Details: map[string]string{
					"GitCommit":     version.GetCommit(),
					"ApiVersion":    "1.55",
					"MinAPIVersion": "1.40",
					"GoVersion":     runtime.Version(),
					"Os":            runtime.GOOS,
					"Arch":          runtime.GOARCH,
					"BuildTime":     version.GetDate(),
					"KernelVersion": "unknown",
					"Module":        "github.com/sysson/dink",
					"ModuleVersion": "moduleVersion()",
					"Experimental":  "false",
				},
			},
		},
	}
	return v, nil
}

func (d *Docker) SystemDiskUsage() {
}

func (d *Docker) SubscribeToEvents() {
}

func (d *Docker) UnsubscribeFromEvents() {
}

func (d *Docker) AuthenticateToRegistry() {
}

func (s *Swarm) Info() {
}

func (b *Builder) DiskUsage() {
}

func (d *Docker) Status() {
}
