package translator

import (
	"context"
	"fmt"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (d *Docker) GetNetworkSummaries(ctx context.Context, filters filters.Args) ([]networktypes.Summary, error) {
	for _, key := range filters.Keys() {
		switch key {
		case "name", "id", "driver", "scope", "label", "label!", "type", "dangling":
		default:
			return nil, httpx.BadRequest(fmt.Errorf("unsupported network filter %q", key))
		}
	}
	list, err := d.networkList(ctx)
	if err != nil {
		return nil, err
	}
	dangling, err := filters.GetBoolOrDefault("dangling", false)
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	result := make([]networktypes.Summary, 0, len(list.Items))
	for index := range list.Items {
		obj := &list.Items[index]
		network := networkFromObject(obj)
		_, builtin := builtinNetworkDrivers[network.Name]
		networkType := "custom"
		if builtin {
			networkType = "builtin"
		}
		if !filters.Match("name", network.Name) || !filters.Match("id", network.ID) ||
			!filters.ExactMatch("driver", network.Driver) || !filters.ExactMatch("scope", network.Scope) ||
			!filters.ExactMatch("type", networkType) ||
			!filters.MatchKVList("label", network.Labels) || !matchExcludedLabels(filters.Get("label!"), network.Labels) {
			continue
		}
		if len(filters.Get("dangling")) > 0 {
			if builtin {
				if dangling {
					continue
				}
				result = append(result, networktypes.Summary{Network: network})
				continue
			}
			inUse, err := d.networkInUse(ctx, obj)
			if err != nil {
				return nil, err
			}
			if dangling == inUse {
				continue
			}
		}
		result = append(result, networktypes.Summary{Network: network})
	}
	return result, nil
}

func (d *Docker) GetNetwork(ctx context.Context, nameOrID string) (networktypes.Inspect, error) {
	obj, err := d.findNetwork(ctx, nameOrID)
	if err != nil {
		return networktypes.Inspect{}, err
	}
	return networktypes.Inspect{Network: networkFromObject(obj), Containers: map[string]networktypes.EndpointResource{}}, nil
}

func (s *Swarm) GetNetworkSummaries(context.Context, filters.Args) ([]networktypes.Summary, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetNetwork(context.Context, string, bool) (networktypes.Inspect, error) {
	return networktypes.Inspect{}, ErrNotImplemented
}
