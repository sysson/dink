package translator

import (
	"context"
	"fmt"
	"reflect"

	networktypes "github.com/moby/moby/api/types/network"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) ConnectContainerToNetwork(ctx context.Context, networkID, containerID string, endpoint *networktypes.EndpointSettings) error {
	if endpoint != nil && !reflect.DeepEqual(*endpoint, networktypes.EndpointSettings{}) {
		return InvalidArgument(fmt.Errorf("network endpoint settings are not supported by the Kubernetes backend"))
	}
	deployment, err := d.findDeployment(ctx, containerID)
	if err != nil {
		return err
	}
	networkObject, err := d.findNetwork(ctx, networkID)
	if err != nil {
		return err
	}
	if deployment.Spec.Template.Labels == nil {
		deployment.Spec.Template.Labels = map[string]string{}
	}
	label := networkLabelPrefix + networkObjectName(networkFromObject(networkObject).Name)
	if deployment.Spec.Template.Labels[label] == "true" {
		return nil
	}
	deployment.Spec.Template.Labels[label] = "true"
	if _, err := d.k8s.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	return nil
}

func (d *Docker) DisconnectContainerFromNetwork(ctx context.Context, networkID, containerID string, force bool) error {
	deployment, err := d.findDeployment(ctx, containerID)
	if err != nil {
		return err
	}
	networkObject, err := d.findNetwork(ctx, networkID)
	if err != nil {
		return err
	}
	label := networkLabelPrefix + networkObjectName(networkFromObject(networkObject).Name)
	if deployment.Spec.Template.Labels == nil || deployment.Spec.Template.Labels[label] != "true" {
		if force {
			return nil
		}
		return NotFound(fmt.Errorf("container %s is not connected to network %s", containerID, networkID))
	}
	delete(deployment.Spec.Template.Labels, label)
	if _, err := d.k8s.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	return nil
}
