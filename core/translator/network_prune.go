package translator

import (
	"context"
	"fmt"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/pkg/filters"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) NetworkPrune(ctx context.Context, filters filters.Args) (networktypes.PruneReport, error) {
	for _, key := range filters.Keys() {
		if key != "label" && key != "label!" && key != "until" {
			return networktypes.PruneReport{}, InvalidArgument(fmt.Errorf("unsupported network prune filter %q", key))
		}
	}
	before, err := pruneBefore(filters.Get("until"))
	if err != nil {
		return networktypes.PruneReport{}, InvalidArgument(err)
	}
	list, err := d.networkList(ctx)
	if err != nil {
		return networktypes.PruneReport{}, err
	}
	result := networktypes.PruneReport{NetworksDeleted: []string{}}
	for index := range list.Items {
		obj := &list.Items[index]
		network := networkFromObject(obj)
		if _, builtin := builtinNetworkDrivers[network.Name]; builtin {
			continue
		}
		if !filters.MatchKVList("label", network.Labels) || !matchExcludedLabels(filters.Get("label!"), network.Labels) ||
			(!before.IsZero() && obj.GetCreationTimestamp().After(before)) {
			continue
		}
		inUse, err := d.networkInUse(ctx, obj)
		if err != nil {
			return result, err
		}
		if inUse {
			continue
		}
		if err := d.k8s.Dynamic.Resource(networkResource).Namespace(obj.GetNamespace()).Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: new(obj.GetUID())}}); err != nil {
			return result, err
		}
		result.NetworksDeleted = append(result.NetworksDeleted, network.Name)
	}
	return result, nil
}
