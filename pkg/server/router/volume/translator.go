package volume

type Translator interface {
	ListVolumes()
	GetVolume()
	CreateVolume()
	RemoveVolume()
	PruneVolumes()
}

type ClusterTranslator interface {
	GetVolume()
	GetVolumes()
	CreateVolume()
	RemoveVolume()
	UpdateVolume()
	IsManager()
}
