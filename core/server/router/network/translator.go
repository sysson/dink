package network

import (
	"context"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/pkg/filters"
)

type Translator interface {
	GetNetworkSummaries(context.Context, filters.Args) ([]networktypes.Summary, error)
	GetNetwork(context.Context, string) (networktypes.Inspect, error)
	CreateNetwork(context.Context, networktypes.CreateRequest) (networktypes.CreateResponse, error)
	ConnectContainerToNetwork(context.Context, string, string, *networktypes.EndpointSettings) error
	DisconnectContainerFromNetwork(context.Context, string, string, bool) error
	DeleteNetwork(context.Context, string) error
	NetworkPrune(context.Context, filters.Args) (networktypes.PruneReport, error)
}

type ClusterTranslator interface {
	GetNetworks(context.Context, filters.Args, bool) ([]networktypes.Inspect, error)
	GetNetworkSummaries(context.Context, filters.Args) ([]networktypes.Summary, error)
	GetNetwork(context.Context, string, bool) (networktypes.Inspect, error)
	GetNetworksByName(context.Context, string) ([]networktypes.Network, error)
	CreateNetwork(context.Context, networktypes.CreateRequest) (string, error)
	RemoveNetwork(context.Context, string) error
}
