package translator

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	networktypes "github.com/moby/moby/api/types/network"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
)

func (d *Docker) ConnectContainerToNetwork(ctx context.Context, networkID, containerID string, endpoint *networktypes.EndpointSettings) error {
	var aliases []string
	if endpoint != nil {
		requested := *endpoint
		aliases = slices.Concat(requested.Aliases, requested.DNSNames)
		requested.Aliases, requested.DNSNames = nil, nil
		if !reflect.DeepEqual(requested, networktypes.EndpointSettings{}) {
			return InvalidArgument(fmt.Errorf("network endpoint settings other than aliases are not supported by the Kubernetes backend"))
		}
	}
	workload, err := d.findContainer(ctx, containerID)
	if err != nil {
		return err
	}
	networkObject, err := d.findNetwork(ctx, networkID)
	if err != nil {
		return err
	}
	name := networkFromObject(networkObject).Name
	label := networkLabelPrefix + networkObjectName(name)
	if len(aliases) > 0 {
		validated, err := aliasesFromEndpoints(map[string]*networktypes.EndpointSettings{name: {Aliases: aliases}})
		if err != nil {
			return err
		}
		current, err := decodeContainerAliases(workload.Annotations)
		if err != nil {
			return err
		}
		if err := d.setContainerAliases(ctx, workload, mergeNetworkAliases(current, name, validated[name])); err != nil {
			return err
		}
	}
	if workload.Template.Labels[label] == "true" {
		return nil
	}
	return d.updateNetworkLabels(ctx, workload, func(labels map[string]string) {
		labels[label] = "true"
	})
}

func (d *Docker) DisconnectContainerFromNetwork(ctx context.Context, networkID, containerID string, force bool) error {
	workload, err := d.findContainer(ctx, containerID)
	if err != nil {
		return err
	}
	networkObject, err := d.findNetwork(ctx, networkID)
	if err != nil {
		return err
	}
	name := networkFromObject(networkObject).Name
	label := networkLabelPrefix + networkObjectName(name)
	if workload.Template.Labels[label] != "true" {
		if force {
			return nil
		}
		return NotFound(fmt.Errorf("container %s is not connected to network %s", containerID, networkID))
	}
	current, err := decodeContainerAliases(workload.Annotations)
	if err != nil {
		return err
	}
	if _, ok := current[name]; ok {
		if err := d.setContainerAliases(ctx, workload, mergeNetworkAliases(current, name, nil)); err != nil {
			return err
		}
	}
	return d.updateNetworkLabels(ctx, workload, func(labels map[string]string) {
		delete(labels, label)
	})
}

func (d *Docker) updateNetworkLabels(ctx context.Context, workload *containerWorkload, mutate func(map[string]string)) error {
	if workload.OneShot {
		// Kubernetes only allows Job pod labels to change before the Job first starts.
		if workload.Started {
			return Unsupported(fmt.Errorf("changing networks of a running --rm container is not supported by the Kubernetes backend"))
		}
		return d.updateContainerJob(ctx, workload, func(current *batchv1.Job) error {
			if current.Spec.Template.Labels == nil {
				current.Spec.Template.Labels = map[string]string{}
			}
			mutate(current.Spec.Template.Labels)
			return nil
		})
	}
	_, err := d.updateContainerDeployment(ctx, workload, func(current *appsv1.Deployment) error {
		if current.Spec.Template.Labels == nil {
			current.Spec.Template.Labels = map[string]string{}
		}
		mutate(current.Spec.Template.Labels)
		return nil
	})
	return err
}
