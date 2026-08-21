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
	GetClusterNetworks()
	GetClusterNetworkSummaries()
	GetClusterNetwork()
	GetClusterNetworksByName()
	CreateClusterNetwork()
	RemoveClusterNetwork()
}
