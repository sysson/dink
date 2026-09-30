package translator

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/identity"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Swarm) GetNodes(ctx context.Context, options swarmbackend.NodeListOptions) ([]swarmtypes.Node, error) {
	if err := validateNodeFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	result := make([]swarmtypes.Node, 0, len(nodes.Items))
	for index := range nodes.Items {
		node := swarmNode(&nodes.Items[index])
		if !matchesNodeFilters(options.Filters, node) {
			continue
		}
		result = append(result, node)
	}
	return result, nil
}

func (s *Swarm) GetNode(ctx context.Context, idOrName string) (swarmtypes.Node, error) {
	if idOrName == "" {
		return swarmtypes.Node{}, InvalidArgument(fmt.Errorf("node ID or name is required"))
	}
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return swarmtypes.Node{}, kubeError(err)
	}
	var match *swarmtypes.Node
	for index := range nodes.Items {
		node := swarmNode(&nodes.Items[index])
		if node.Description.Hostname != idOrName && !strings.HasPrefix(node.ID, idOrName) {
			continue
		}
		if match != nil {
			return swarmtypes.Node{}, Conflict(fmt.Errorf("node %s is ambiguous", idOrName))
		}
		match = &node
	}
	if match == nil {
		return swarmtypes.Node{}, NotFound(fmt.Errorf("node %s not found", idOrName))
	}
	return *match, nil
}

func (s *Swarm) UpdateNode(context.Context, string, uint64, swarmtypes.NodeSpec) error {
	return Unsupported(fmt.Errorf("updating a node is not available: node role, availability, and labels belong to Kubernetes; use kubectl label, kubectl cordon, or kubectl drain instead"))
}

func (s *Swarm) RemoveNode(context.Context, string, bool) error {
	return Unsupported(fmt.Errorf("removing a node is not available: cluster membership belongs to Kubernetes; use kubectl delete node and your provider's node lifecycle tooling instead"))
}

func swarmNode(node *corev1.Node) swarmtypes.Node {
	role := swarmtypes.NodeRoleWorker
	if isControlPlaneNode(node) {
		role = swarmtypes.NodeRoleManager
	}
	availability := swarmtypes.NodeAvailabilityActive
	if node.Spec.Unschedulable {
		availability = swarmtypes.NodeAvailabilityDrain
	}
	labels := node.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	cpu := node.Status.Allocatable[corev1.ResourceCPU]
	memory := node.Status.Allocatable[corev1.ResourceMemory]
	architecture := node.Status.NodeInfo.Architecture
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	operatingSystem := node.Status.NodeInfo.OperatingSystem
	if operatingSystem == "" {
		operatingSystem = runtime.GOOS
	}
	result := swarmtypes.Node{
		ID:   identity.DockerIDFromUID(node.UID),
		Meta: swarmMeta(node),
		Spec: swarmtypes.NodeSpec{
			Annotations:  swarmtypes.Annotations{Labels: labels},
			Role:         role,
			Availability: availability,
		},
		Description: swarmtypes.NodeDescription{
			Hostname:  node.Name,
			Platform:  swarmtypes.Platform{Architecture: architecture, OS: operatingSystem},
			Resources: swarmtypes.Resources{NanoCPUs: cpu.MilliValue() * 1_000_000, MemoryBytes: memory.Value()},
			Engine:    swarmtypes.EngineDescription{EngineVersion: node.Status.NodeInfo.KubeletVersion, Labels: labels},
		},
		Status: swarmtypes.NodeStatus{State: nodeState(node), Addr: nodeAddress(node)},
	}
	if role == swarmtypes.NodeRoleManager {
		result.ManagerStatus = &swarmtypes.ManagerStatus{
			Reachability: swarmtypes.ReachabilityReachable,
			Addr:         result.Status.Addr,
		}
	}
	return result
}

func nodeState(node *corev1.Node) swarmtypes.NodeState {
	for _, condition := range node.Status.Conditions {
		if condition.Type != corev1.NodeReady {
			continue
		}
		switch condition.Status {
		case corev1.ConditionTrue:
			return swarmtypes.NodeStateReady
		case corev1.ConditionFalse:
			return swarmtypes.NodeStateDown
		default:
			return swarmtypes.NodeStateUnknown
		}
	}
	return swarmtypes.NodeStateUnknown
}

func validateNodeFilters(filters swarmFilters) error {
	return filters.Validate(map[string]bool{"id": true, "label": true, "membership": true, "name": true, "node.label": true, "role": true})
}

func matchesNodeFilters(filters swarmFilters, node swarmtypes.Node) bool {
	if filters.Len() == 0 {
		return true
	}
	if !filters.FuzzyMatch("id", node.ID) {
		return false
	}
	if !filters.Match("name", node.Description.Hostname) {
		return false
	}
	if !filters.ExactMatch("role", string(node.Spec.Role)) {
		return false
	}
	// Every Kubernetes node is already part of the cluster.
	if !filters.ExactMatch("membership", "accepted") {
		return false
	}
	if !filters.MatchKVList("label", node.Spec.Labels) {
		return false
	}
	return filters.MatchKVList("node.label", node.Spec.Labels)
}
