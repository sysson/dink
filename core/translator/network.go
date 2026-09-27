package translator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
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

func (d *Docker) GetNetworkSummaries(ctx context.Context, filters types.Args) ([]networktypes.Summary, error) {
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

func (d *Docker) CreateNetwork(ctx context.Context, request networktypes.CreateRequest) (networktypes.CreateResponse, error) {
	if strings.TrimSpace(request.Name) == "" {
		return networktypes.CreateResponse{}, httpx.BadRequest(fmt.Errorf("network name is required"))
	}
	if (request.Driver != "" && request.Driver != "bridge") || (request.Scope != "" && request.Scope != "local") ||
		(request.EnableIPv4 != nil && !*request.EnableIPv4) || (request.EnableIPv6 != nil && *request.EnableIPv6) ||
		request.Internal || request.Attachable || request.Ingress || request.ConfigOnly || request.ConfigFrom != nil || len(request.Options) > 0 ||
		(request.IPAM != nil && (request.IPAM.Driver != "" && request.IPAM.Driver != "default" || len(request.IPAM.Options) > 0 || len(request.IPAM.Config) > 0)) {
		return networktypes.CreateResponse{}, httpx.BadRequest(fmt.Errorf("network configuration is not supported by the Kubernetes backend"))
	}
	namespace, err := networkNamespace(ctx)
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	list, err := d.networkList(ctx)
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	for index := range list.Items {
		if networkFromObject(&list.Items[index]).Name == request.Name {
			return networktypes.CreateResponse{}, httpx.Conflict(fmt.Errorf("network with name %s already exists", request.Name))
		}
	}
	labels := make(map[string]any, len(request.Labels))
	for key, value := range request.Labels {
		labels[key] = value
	}
	obj, err := d.k8s.Dynamic.Resource(networkResource).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "dink.io/v1alpha1",
		"kind":       "DockerNetwork",
		"metadata":   map[string]any{"name": networkObjectName(request.Name)},
		"spec":       map[string]any{"name": request.Name, "labels": labels},
	}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return networktypes.CreateResponse{}, httpx.Conflict(fmt.Errorf("network with name %s already exists", request.Name))
	}
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	return networktypes.CreateResponse{ID: identity.DockerIDFromUID(obj.GetUID()), Warning: ""}, nil
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

func (d *Docker) DeleteNetwork(ctx context.Context, nameOrID string) error {
	obj, err := d.findNetwork(ctx, nameOrID)
	if err != nil {
		return err
	}
	if _, builtin := builtinNetworkDrivers[networkFromObject(obj).Name]; builtin {
		return httpx.Forbidden(fmt.Errorf("network %s is a predefined network and cannot be removed", nameOrID))
	}
	inUse, err := d.networkInUse(ctx, obj)
	if err != nil {
		return err
	}
	if inUse {
		return httpx.Conflict(fmt.Errorf("network %s has active endpoints", nameOrID))
	}
	err = d.k8s.Dynamic.Resource(networkResource).Namespace(obj.GetNamespace()).Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: new(obj.GetUID())}})
	if apierrors.IsNotFound(err) {
		return httpx.NotFound(fmt.Errorf("network %s not found", nameOrID))
	}
	return err
}

func (d *Docker) NetworkPrune(ctx context.Context, filters types.Args) (networktypes.PruneReport, error) {
	for _, key := range filters.Keys() {
		if key != "label" && key != "label!" && key != "until" {
			return networktypes.PruneReport{}, httpx.BadRequest(fmt.Errorf("unsupported network prune filter %q", key))
		}
	}
	before, err := pruneBefore(filters.Get("until"))
	if err != nil {
		return networktypes.PruneReport{}, httpx.BadRequest(err)
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

func pruneBefore(values []string) (time.Time, error) {
	if len(values) == 0 {
		return time.Time{}, nil
	}
	if len(values) != 1 {
		return time.Time{}, fmt.Errorf("until filter requires one value")
	}
	value := values[0]
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp, nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return time.Unix(0, int64(seconds*float64(time.Second))), nil
	}
	if duration, err := time.ParseDuration(value); err == nil && duration >= 0 {
		return time.Now().Add(-duration), nil
	}
	return time.Time{}, fmt.Errorf("invalid until filter %q", value)
}

func matchExcludedLabels(excluded []string, labels map[string]string) bool {
	for _, entry := range excluded {
		key, value, hasValue := strings.Cut(entry, "=")
		if actual, ok := labels[key]; ok && (!hasValue || actual == value) {
			return false
		}
	}
	return true
}

func (d *Docker) ConnectContainerToNetwork()      {}
func (d *Docker) DisconnectContainerFromNetwork() {}

func (s *Swarm) GetNetworks()         {}
func (s *Swarm) GetNetworkSummaries() {}
func (s *Swarm) GetNetwork()          {}
func (s *Swarm) GetNetworksByName()   {}
func (s *Swarm) CreateNetwork()       {}
func (s *Swarm) RemoveNetwork()       {}
