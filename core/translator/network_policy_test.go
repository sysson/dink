package translator

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func newPolicyFixture(t *testing.T) (context.Context, *Docker, *kubernetesfake.Clientset) {
	t.Helper()
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client, Dynamic: newNetworkDynamicClient()},
		registry: &fakeRegistry{digests: map[string]string{"nginx": swarmTestDigest}},
		pullHost: "localhost:5000",
	}
	return ctx, docker, client
}

func TestNetworkPolicyIsolatesNamespaceAndNetworks(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "web", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}

	denyAll, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, namespaceIsolationPolicy, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("default-deny policy: %v", err)
	}
	if len(denyAll.Spec.Ingress) != 0 {
		t.Fatalf("default-deny has ingress rules %+v; it must allow nothing", denyAll.Spec.Ingress)
	}
	if denyAll.Spec.PodSelector.MatchLabels[managedByLabel] != managedByDink {
		t.Fatalf("default-deny selector = %+v, want only Dink Pods", denyAll.Spec.PodSelector)
	}
	if len(denyAll.Spec.PolicyTypes) != 1 || denyAll.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Fatalf("default-deny policy types = %v; egress must stay open so DNS works", denyAll.Spec.PolicyTypes)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Labels[managedByLabel] != managedByDink {
		t.Fatalf("pod labels = %v, want the Dink marker the policy selects on", deployment.Spec.Template.Labels)
	}

	bridge, err := docker.findNetwork(ctx, "bridge")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, networkPolicyPrefix+bridge.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("bridge network policy: %v", err)
	}
	label := networkLabelPrefix + bridge.GetName()
	if policy.Spec.PodSelector.MatchLabels[label] != "true" {
		t.Fatalf("policy selector = %+v, want bridge members", policy.Spec.PodSelector)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
		t.Fatalf("policy ingress = %+v", policy.Spec.Ingress)
	}
	from := policy.Spec.Ingress[0].From[0]
	if from.PodSelector == nil || from.PodSelector.MatchLabels[label] != "true" {
		t.Fatalf("policy peer = %+v, want bridge members only", from)
	}
	// A namespace selector would let another tenant reach these Pods.
	if from.NamespaceSelector != nil || from.IPBlock != nil {
		t.Fatalf("policy peer = %+v, want a same-namespace Pod selector", from)
	}
	if len(policy.OwnerReferences) != 1 || policy.OwnerReferences[0].Kind != "DockerNetwork" {
		t.Fatalf("policy owner = %+v, want the DockerNetwork so it is garbage-collected", policy.OwnerReferences)
	}
}

// `none` needs no allow rule and `host` Pods bypass NetworkPolicy entirely.
func TestNetworkPolicySkipsNoneAndHost(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	if _, err := docker.GetNetworkSummaries(ctx, filters.NewArgs()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"none", "host"} {
		network, err := docker.findNetwork(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, networkPolicyPrefix+network.GetName(), metav1.GetOptions{})
		if err == nil {
			t.Fatalf("%s network has an allow policy", name)
		}
	}
	ingress, err := docker.findNetwork(ctx, ingressNetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, networkPolicyPrefix+ingress.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatalf("ingress network policy: %v", err)
	}
}

func TestPublishedPortsPolicyAllowsAnySource(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	config := &container.Config{
		Image:        "nginx",
		ExposedPorts: networktypes.PortSet{networktypes.MustParsePort("80/tcp"): {}},
	}
	hostConfig := &container.HostConfig{PortBindings: networktypes.PortMap{
		networktypes.MustParsePort("80/tcp"): {{HostPort: "8080"}},
	}}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "web", Config: config, HostConfig: hostConfig}); err != nil {
		t.Fatal(err)
	}

	policy, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, publishedPolicyPrefix+"web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("published ports policy: %v", err)
	}
	if len(policy.Spec.Ingress) != 1 {
		t.Fatalf("policy ingress = %+v", policy.Spec.Ingress)
	}
	// NodePort traffic is translated to a node address, so no peer can match it.
	if len(policy.Spec.Ingress[0].From) != 0 {
		t.Fatalf("policy peers = %+v, want any source", policy.Spec.Ingress[0].From)
	}
	ports := policy.Spec.Ingress[0].Ports
	if len(ports) != 1 || ports[0].Port.IntValue() != 80 || *ports[0].Protocol != corev1.ProtocolTCP {
		t.Fatalf("policy ports = %+v, want only the published container port", ports)
	}
	if len(policy.OwnerReferences) != 1 || policy.OwnerReferences[0].Name != "web" {
		t.Fatalf("policy owner = %+v, want the workload", policy.OwnerReferences)
	}

	// A container with no published ports must not open anything.
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "quiet", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, publishedPolicyPrefix+"quiet", metav1.GetOptions{}); err == nil {
		t.Fatal("unpublished container got a published-ports policy")
	}
}

func TestUserNetworkGetsItsOwnPolicy(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "backend"}); err != nil {
		t.Fatal(err)
	}
	network, err := docker.findNetwork(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, networkPolicyPrefix+network.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("user network policy: %v", err)
	}
	label := networkLabelPrefix + network.GetName()
	if policy.Spec.Ingress[0].From[0].PodSelector.MatchLabels[label] != "true" {
		t.Fatalf("policy = %+v, want members of the backend network", policy.Spec)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, namespaceIsolationPolicy, metav1.GetOptions{}); err != nil {
		t.Fatalf("creating a network must also establish the default deny: %v", err)
	}
}

func TestSwarmServiceGetsIsolationPolicies(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)

	spec := swarmtypes.ServiceSpec{
		Name:         "web",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}},
		EndpointSpec: &swarmtypes.EndpointSpec{Ports: []swarmtypes.PortConfig{{TargetPort: 80, PublishedPort: 8080}}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Labels[managedByLabel] != managedByDink {
		t.Fatalf("service pod labels = %v, want the Dink marker", deployment.Spec.Template.Labels)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, namespaceIsolationPolicy, metav1.GetOptions{}); err != nil {
		t.Fatalf("default-deny policy: %v", err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, publishedPolicyPrefix+"web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("published ports policy: %v", err)
	}
	if len(policy.Spec.Ingress[0].Ports) != 1 || policy.Spec.Ingress[0].Ports[0].Port.IntValue() != 80 {
		t.Fatalf("policy ports = %+v, want the task's container port", policy.Spec.Ingress[0].Ports)
	}

	// Removing the published port must close the rule again.
	spec.EndpointSpec = &swarmtypes.EndpointSpec{}
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.NetworkingV1().NetworkPolicies("tenant").Get(ctx, publishedPolicyPrefix+"web", metav1.GetOptions{}); err == nil {
		t.Fatal("published-ports policy outlived the published port")
	}
}
