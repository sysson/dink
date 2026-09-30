package translator

import (
	"context"
	"fmt"
	"strconv"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/identity"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Dink labels the Kubernetes objects that back Swarm resources so they are not
// confused with Docker containers, volumes, or unrelated cluster objects.
const (
	swarmKindLabel   = "dink.io/swarm"
	swarmServiceKind = "service"
	swarmSecretKind  = "secret"
	swarmConfigKind  = "config"

	// swarmSpecAnnotation stores the Docker spec verbatim so inspect can return
	// the fields Kubernetes does not model.
	swarmSpecAnnotation = "dink.io/swarm-spec"

	// swarmPayloadKey is the Secret/ConfigMap key holding a Swarm secret or config value.
	swarmPayloadKey = "payload"
)

const (
	swarmServiceSelector = swarmKindLabel + "=" + swarmServiceKind
	swarmSecretSelector  = swarmKindLabel + "=" + swarmSecretKind
	swarmConfigSelector  = swarmKindLabel + "=" + swarmConfigKind
)

// swarmFilters is the subset of Docker's filter arguments the Swarm endpoints
// use. The concrete type lives in an internal Moby package.
type swarmFilters interface {
	Len() int
	Get(string) []string
	Validate(map[string]bool) error
	Match(string, string) bool
	ExactMatch(string, string) bool
	FuzzyMatch(string, string) bool
	MatchKVList(string, map[string]string) bool
}

func (s *Swarm) namespace(ctx context.Context) (string, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return "", Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	return id.Namespace, nil
}

// swarmVersion maps a Kubernetes resourceVersion onto Docker's object version,
// which clients echo back on update for optimistic concurrency.
func swarmVersion(resourceVersion string) swarmtypes.Version {
	index, err := strconv.ParseUint(resourceVersion, 10, 64)
	if err != nil {
		return swarmtypes.Version{}
	}
	return swarmtypes.Version{Index: index}
}

func swarmMeta(object metav1.Object) swarmtypes.Meta {
	created := object.GetCreationTimestamp().Time
	return swarmtypes.Meta{
		Version:   swarmVersion(object.GetResourceVersion()),
		CreatedAt: created,
		UpdatedAt: created,
	}
}

// resourceVersionFor turns a client-supplied Docker object version back into
// the Kubernetes resourceVersion that guards the update.
func resourceVersionFor(version uint64) string {
	if version == 0 {
		return ""
	}
	return strconv.FormatUint(version, 10)
}

// validateSwarmName rejects Docker names Kubernetes cannot use as an object name.
func validateSwarmName(kind, name string) error {
	if name == "" {
		return InvalidArgument(fmt.Errorf("%s name is required", kind))
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return InvalidArgument(fmt.Errorf("%s name %q is not usable as a Kubernetes object name: %s", kind, name, errs[0]))
	}
	return nil
}

// notSwarm explains that cluster membership belongs to Kubernetes, not Docker.
func notSwarm(operation string) error {
	return Unsupported(fmt.Errorf("%s is not available: Dink presents an existing Kubernetes cluster as a Swarm, so cluster membership is managed with kubectl and your Kubernetes provider, not through the Docker API", operation))
}

func (s *Swarm) Init(context.Context, swarmtypes.InitRequest) (string, error) {
	return "", notSwarm("swarm init")
}

func (s *Swarm) Join(context.Context, swarmtypes.JoinRequest) error {
	return notSwarm("swarm join")
}

func (s *Swarm) Leave(context.Context, bool) error {
	return notSwarm("swarm leave")
}

func (s *Swarm) Update(context.Context, uint64, swarmtypes.Spec, swarmbackend.UpdateFlags) error {
	return notSwarm("swarm update")
}

func (s *Swarm) GetUnlockKey(context.Context) (string, error) {
	return "", Unsupported(fmt.Errorf("swarm unlock keys are not available: Dink has no Raft store to autolock, so protect the Kubernetes cluster instead"))
}

func (s *Swarm) UnlockSwarm(context.Context, swarmtypes.UnlockRequest) error {
	return Unsupported(fmt.Errorf("swarm unlock is not available: Dink has no Raft store to autolock, so protect the Kubernetes cluster instead"))
}

func (s *Swarm) Inspect(ctx context.Context) (swarmtypes.Swarm, error) {
	cluster, err := s.clusterInfo(ctx)
	if err != nil {
		return swarmtypes.Swarm{}, err
	}
	// Join tokens stay empty: there is nothing to join.
	return swarmtypes.Swarm{ClusterInfo: cluster}, nil
}

// clusterInfo identifies the Kubernetes cluster by its kube-system namespace,
// the closest stable cluster-wide identifier Kubernetes offers.
func (s *Swarm) clusterInfo(ctx context.Context) (swarmtypes.ClusterInfo, error) {
	namespace, err := s.k8s.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return swarmtypes.ClusterInfo{}, kubeError(err)
	}
	return swarmtypes.ClusterInfo{
		ID:   identity.DockerIDFromUID(namespace.UID),
		Meta: swarmMeta(namespace),
		Spec: swarmtypes.Spec{
			Annotations: swarmtypes.Annotations{Name: "default", Labels: map[string]string{}},
		},
	}, nil
}

// Info reports cluster-wide Swarm state. It degrades to an inactive node rather
// than failing, because clients call it as part of GET /info.
func (s *Swarm) Info(ctx context.Context) (swarmtypes.Info, error) {
	info := swarmtypes.Info{LocalNodeState: swarmtypes.LocalNodeStateInactive}
	cluster, err := s.clusterInfo(ctx)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		info.Error = kubeError(err).Error()
		return info, nil
	}
	info.Cluster = &cluster
	info.LocalNodeState = swarmtypes.LocalNodeStateActive
	info.ControlAvailable = true
	info.Nodes = len(nodes.Items)
	for index := range nodes.Items {
		node := &nodes.Items[index]
		if !isControlPlaneNode(node) {
			continue
		}
		info.Managers++
		peer := swarmtypes.Peer{NodeID: identity.DockerIDFromUID(node.UID), Addr: nodeAddress(node)}
		info.RemoteManagers = append(info.RemoteManagers, peer)
		if info.NodeID == "" {
			info.NodeID, info.NodeAddr = peer.NodeID, peer.Addr
		}
	}
	if info.NodeID == "" && len(nodes.Items) > 0 {
		info.NodeID = identity.DockerIDFromUID(nodes.Items[0].UID)
		info.NodeAddr = nodeAddress(&nodes.Items[0])
	}
	info.Warnings = []string{
		"Swarm is a view of a Kubernetes cluster: services are Deployments, tasks are Pods, secrets are Secrets, and configs are ConfigMaps.",
		"Cluster membership, node availability, and Raft settings are managed with kubectl, not the Docker API.",
		"Services, tasks, secrets, and configs are scoped to the authenticated namespace; nodes and cluster state are cluster-wide.",
	}
	return info, nil
}

func isControlPlaneNode(node *corev1.Node) bool {
	for _, key := range []string{"node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master"} {
		if _, ok := node.Labels[key]; ok {
			return true
		}
	}
	return false
}

func nodeAddress(node *corev1.Node) string {
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			return address.Address
		}
	}
	return ""
}
