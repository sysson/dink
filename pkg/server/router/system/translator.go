package system

type Translator interface {
	SystemInfo()
	SystemVersion()
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
