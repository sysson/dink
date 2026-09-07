package system

import "github.com/moby/moby/api/types/system"

type Translator interface {
	SystemInfo()
	SystemVersion() (system.VersionResponse, error)
	SystemDiskUsage()
	SubscribeToEvents()
	UnsubscribeFromEvents()
	AuthenticateToRegistry()
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
