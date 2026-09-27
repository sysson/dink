package network

import (
	"context"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/core/types"
)

type Translator interface {
	GetNetworkSummaries(context.Context, types.Args) ([]networktypes.Summary, error)
	GetNetwork(context.Context, string) (networktypes.Inspect, error)
	CreateNetwork(context.Context, networktypes.CreateRequest) (networktypes.CreateResponse, error)
	ConnectContainerToNetwork()
	DisconnectContainerFromNetwork()
	DeleteNetwork(context.Context, string) error
	NetworkPrune(context.Context, types.Args) (networktypes.PruneReport, error)
}

type ClusterTranslator interface {
	GetNetworks()
	GetNetworkSummaries()
	GetNetwork()
	GetNetworksByName()
	CreateNetwork()
	RemoveNetwork()
}
