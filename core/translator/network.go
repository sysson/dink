package translator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var networkResource = schema.GroupVersionResource{Group: "dink.io", Version: "v1alpha1", Resource: "dockernetworks"}

const networkLabelPrefix = "dink.io/network-"

var builtinNetworkDrivers = map[string]string{
	networktypes.NetworkBridge: "bridge",
	networktypes.NetworkHost:   "host",
	networktypes.NetworkNone:   "null",
}

func networkObjectName(name string) string {
	return fmt.Sprintf("network-%x", sha256.Sum256([]byte(name)))[:40]
}

func (d *Docker) ensureBuiltinNetworks(ctx context.Context, namespace string, list *unstructured.UnstructuredList) (bool, error) {
	existing := make(map[string]bool, len(list.Items))
	for index := range list.Items {
		existing[list.Items[index].GetName()] = true
	}
	created := false
	for name, driver := range builtinNetworkDrivers {
		if existing[networkObjectName(name)] {
			continue
		}
		_, err := d.k8s.Dynamic.Resource(networkResource).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "dink.io/v1alpha1",
			"kind":       "DockerNetwork",
			"metadata":   map[string]any{"name": networkObjectName(name)},
			"spec":       map[string]any{"name": name, "driver": driver},
		}}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return false, err
		}
		created = true
	}
	return created, nil
}

func networkNamespace(ctx context.Context) (string, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return "", httpx.Unauthorized(fmt.Errorf("missing identity in context"))
	}
	return id.Namespace, nil
}

func networkFromObject(obj *unstructured.Unstructured) networktypes.Network {
	spec := obj.Object["spec"].(map[string]any)
	name := spec["name"].(string)
	driver := "bridge"
	if builtinDriver, ok := builtinNetworkDrivers[name]; ok {
		driver = builtinDriver
	}
	labels := make(map[string]string)
	if values, ok := spec["labels"].(map[string]any); ok {
		for key, value := range values {
			labels[key] = value.(string)
		}
	}
	return networktypes.Network{
		Name:       name,
		ID:         identity.DockerIDFromUID(obj.GetUID()),
		Created:    obj.GetCreationTimestamp().Time,
		Scope:      "local",
		Driver:     driver,
		EnableIPv4: driver == "bridge",
		IPAM:       networktypes.IPAM{Driver: "default", Config: []networktypes.IPAMConfig{}},
		Options:    map[string]string{},
		Labels:     labels,
	}
}

func (d *Docker) networkList(ctx context.Context) (*unstructured.UnstructuredList, error) {
	namespace, err := networkNamespace(ctx)
	if err != nil {
		return nil, err
	}
	list, err := d.k8s.Dynamic.Resource(networkResource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	created, err := d.ensureBuiltinNetworks(ctx, namespace, list)
	if err != nil {
		return nil, err
	}
	if created {
		return d.k8s.Dynamic.Resource(networkResource).Namespace(namespace).List(ctx, metav1.ListOptions{})
	}
	return list, nil
}

func (d *Docker) findNetwork(ctx context.Context, nameOrID string) (*unstructured.Unstructured, error) {
	list, err := d.networkList(ctx)
	if err != nil {
		return nil, err
	}
	var match *unstructured.Unstructured
	for index := range list.Items {
		obj := &list.Items[index]
		network := networkFromObject(obj)
		if network.Name != nameOrID && !strings.HasPrefix(network.ID, nameOrID) {
			continue
		}
		if match != nil {
			return nil, httpx.Conflict(fmt.Errorf("network %s is ambiguous", nameOrID))
		}
		match = obj
	}
	if match == nil || nameOrID == "" {
		return nil, httpx.NotFound(fmt.Errorf("network %s not found", nameOrID))
	}
	return match, nil
}

func (s *Swarm) GetNetworks(context.Context, filters.Args, bool) ([]networktypes.Inspect, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) GetNetworksByName(context.Context, string) ([]networktypes.Network, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) RemoveNetwork(context.Context, string) error {
	return ErrNotImplemented
}

func (d *Docker) networkInUse(ctx context.Context, obj *unstructured.Unstructured) (bool, error) {
	namespace := obj.GetNamespace()
	selector := networkLabelPrefix + obj.GetName()
	deployments, err := d.k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	for _, deployment := range deployments.Items {
		if _, attached := deployment.Spec.Template.Labels[selector]; attached {
			return true, nil
		}
	}
	pods, err := d.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	return err == nil && len(pods.Items) > 0, err
}
