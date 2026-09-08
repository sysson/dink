package system

import (
	"context"

	"github.com/moby/moby/api/types/system"
	"github.com/sysson/dink/pkg/types"
)

type Translator interface {
	SystemInfo()
	SystemVersion() (system.VersionResponse, error)
	SystemDiskUsage()
	SubscribeToEvents()
	UnsubscribeFromEvents()
	AuthenticateToRegistry(ctx context.Context, auth types.RegistryAuth) (string, error)
}

type ClusterTranslator interface {
	Info()
}

type BuildTranslator interface {
	DiskUsage()
}

type StatusTranslator interface {
	Status()
}
