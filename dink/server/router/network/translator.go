package network

type Translator interface {
	GetNetworks()
	GetNetworkSummaries()
	CreateNetwork()
	ConnectContainerToNetwork()
	DisconnectContainerFromNetwork()
	DeleteNetwork()
	NetworkPrune()
}

type ClusterTranslator interface {
	GetNetworks()
	GetNetworkSummaries()
	GetNetwork()
	GetNetworksByName()
	CreateNetwork()
	RemoveNetwork()
}
