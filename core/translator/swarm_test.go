package translator

import (
	"context"
	"testing"

	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

const swarmTestDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newSwarmFixture(t *testing.T, objects ...any) (context.Context, *Swarm, *kubernetesfake.Clientset) {
	t.Helper()
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	for _, object := range objects {
		switch typed := object.(type) {
		case *corev1.Node:
			if _, err := client.CoreV1().Nodes().Create(ctx, typed, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		case *corev1.Namespace:
			if _, err := client.CoreV1().Namespaces().Create(ctx, typed, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client, Dynamic: newNetworkDynamicClient()},
		registry: &fakeRegistry{digests: map[string]string{"nginx": swarmTestDigest}},
		pullHost: "localhost:5000",
	}
	return ctx, &Swarm{k8s: docker.k8s, docker: docker, systemNamespace: "dink-system"}, client
}

func testNode(name string, controlPlane bool) *corev1.Node {
	node := &corev1.Node{
		Name:   name,
		UID:    types.UID("11111111-2222-3333-4444-55555555555" + name[len(name)-1:]),
		Labels: map[string]string{"kubernetes.io/hostname": name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewMilliQuantity(2000, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(1<<30, resource.BinarySI),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0." + name[len(name)-1:]}},
			NodeInfo:   corev1.NodeSystemInfo{Architecture: "amd64", OperatingSystem: "linux", KubeletVersion: "v1.31.0"},
		},
	}
	if controlPlane {
		node.Labels["node-role.kubernetes.io/control-plane"] = ""
	}
	return node
}

func TestSwarmInfoReportsCluster(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t,
		&corev1.Namespace{Name: "kube-system", UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		testNode("node1", true),
		testNode("node2", false),
	)

	info, err := swarm.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.LocalNodeState != swarmtypes.LocalNodeStateActive || !info.ControlAvailable {
		t.Fatalf("swarm state = %q, control available = %v", info.LocalNodeState, info.ControlAvailable)
	}
	if info.Nodes != 2 || info.Managers != 1 {
		t.Fatalf("nodes = %d, managers = %d, want 2 and 1", info.Nodes, info.Managers)
	}
	if info.Cluster == nil || info.Cluster.ID != identity.DockerIDFromUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("cluster = %+v, want the kube-system namespace identity", info.Cluster)
	}
	if info.NodeID == "" || info.NodeAddr != "10.0.0.1" {
		t.Fatalf("local node = %q at %q, want the control-plane node", info.NodeID, info.NodeAddr)
	}
}

// Without node access GET /info must still answer, because Docker clients call
// it before anything else.
func TestSwarmInfoDegradesWithoutCluster(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t)

	info, err := swarm.Info(ctx)
	if err != nil {
		t.Fatalf("Info returned an error instead of degrading: %v", err)
	}
	if info.LocalNodeState != swarmtypes.LocalNodeStateInactive || info.Error == "" {
		t.Fatalf("info = %+v, want an inactive node carrying the error", info)
	}
}

func TestSwarmNodesMapKubernetesNodes(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t, testNode("node1", true), testNode("node2", false))

	nodes, err := swarm.GetNodes(ctx, swarmbackend.NodeListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	manager := nodes[0]
	if manager.Spec.Role != swarmtypes.NodeRoleManager || manager.ManagerStatus == nil {
		t.Fatalf("node1 role = %q, manager status = %+v", manager.Spec.Role, manager.ManagerStatus)
	}
	if manager.Status.State != swarmtypes.NodeStateReady || manager.Description.Hostname != "node1" {
		t.Fatalf("node1 status = %+v, hostname = %q", manager.Status, manager.Description.Hostname)
	}
	if manager.Description.Resources.NanoCPUs != 2_000_000_000 || manager.Description.Resources.MemoryBytes != 1<<30 {
		t.Fatalf("node1 resources = %+v", manager.Description.Resources)
	}
	if nodes[1].Spec.Role != swarmtypes.NodeRoleWorker {
		t.Fatalf("node2 role = %q, want worker", nodes[1].Spec.Role)
	}

	node, err := swarm.GetNode(ctx, "node2")
	if err != nil || node.Description.Hostname != "node2" {
		t.Fatalf("GetNode(node2) = %+v, err = %v", node, err)
	}
	if _, err := swarm.GetNode(ctx, "missing"); !IsKind(err, KindNotFound) {
		t.Fatalf("GetNode(missing) error = %v, want not found", err)
	}
}

// Cluster membership belongs to Kubernetes, so these must explain themselves
// rather than return a bare not-implemented error.
func TestSwarmClusterOperationsExplainKubernetes(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t)

	for name, err := range map[string]error{
		"init":        errorFrom(swarm.Init(ctx, swarmtypes.InitRequest{})),
		"join":        swarm.Join(ctx, swarmtypes.JoinRequest{}),
		"leave":       swarm.Leave(ctx, false),
		"update":      swarm.Update(ctx, 1, swarmtypes.Spec{}, swarmbackend.UpdateFlags{}),
		"unlock":      swarm.UnlockSwarm(ctx, swarmtypes.UnlockRequest{}),
		"node update": swarm.UpdateNode(ctx, "node1", 1, swarmtypes.NodeSpec{}),
		"node remove": swarm.RemoveNode(ctx, "node1", false),
	} {
		if !IsKind(err, KindUnsupported) {
			t.Fatalf("%s error = %v, want unsupported", name, err)
		}
		if len(err.Error()) < 40 {
			t.Fatalf("%s error %q does not explain why", name, err)
		}
	}
}

func errorFrom(_ string, err error) error { return err }

func TestSwarmServiceLifecycle(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)

	spec := swarmtypes.ServiceSpec{
		Name: "web", Labels: map[string]string{"tier": "front"},
		Mode: swarmtypes.ServiceMode{Replicated: &swarmtypes.ReplicatedService{Replicas: new(uint64(3))}},
		TaskTemplate: swarmtypes.TaskSpec{
			ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx", Env: []string{"A=b"}},
			Resources: &swarmtypes.ResourceRequirements{
				Limits: &swarmtypes.Limit{NanoCPUs: 500_000_000, MemoryBytes: 256 << 20},
			},
		},
		EndpointSpec: &swarmtypes.EndpointSpec{Ports: []swarmtypes.PortConfig{{TargetPort: 80, PublishedPort: 8080}}},
	}
	created, err := swarm.CreateService(ctx, spec, "", false)
	if err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != identity.DockerIDFromUID(deployment.UID) {
		t.Fatalf("CreateService ID = %q, want the Deployment identity", created.ID)
	}
	if *deployment.Spec.Replicas != 3 {
		t.Fatalf("replicas = %d, want 3", *deployment.Spec.Replicas)
	}
	if deployment.Labels[swarmKindLabel] != swarmServiceKind {
		t.Fatalf("deployment labels = %v, want a swarm service marker", deployment.Labels)
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Image; got != "localhost:5000/tenant/nginx@"+swarmTestDigest {
		t.Fatalf("image = %q, want the pinned tenant reference", got)
	}
	cpu := deployment.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU]
	if cpu.MilliValue() != 500 {
		t.Fatalf("cpu limit = %s, want 500m", cpu.String())
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{}); err != nil {
		t.Fatalf("published service: %v", err)
	}

	// A Swarm service must not show up as a Docker container.
	containers, err := listWorkloads(ctx, client, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 0 {
		t.Fatalf("listWorkloads returned %d container workloads, want none", len(containers))
	}

	service, err := swarm.GetService(ctx, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Labels["tier"] != "front" || service.ID != created.ID {
		t.Fatalf("service = %+v, want the round-tripped spec", service.Spec)
	}
	if len(service.Endpoint.Ports) != 1 || service.Endpoint.Ports[0].PublishedPort != 8080 {
		t.Fatalf("endpoint ports = %+v", service.Endpoint.Ports)
	}

	services, err := swarm.GetServices(ctx, swarmbackend.ServiceListOptions{Status: true})
	if err != nil || len(services) != 1 {
		t.Fatalf("GetServices = %d services, err = %v", len(services), err)
	}
	if services[0].ServiceStatus == nil || services[0].ServiceStatus.DesiredTasks != 3 {
		t.Fatalf("service status = %+v", services[0].ServiceStatus)
	}

	if err := swarm.RemoveService(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := swarm.GetService(ctx, "web", false); !IsKind(err, KindNotFound) {
		t.Fatalf("GetService after removal = %v, want not found", err)
	}
}

// Publishing a port joins the ingress network, as it does in Swarm, and host
// mode binds on the node running the task instead of a Service.
func TestSwarmServicePublishModes(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)

	spec := swarmtypes.ServiceSpec{
		Name:         "web",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}},
		EndpointSpec: &swarmtypes.EndpointSpec{Ports: []swarmtypes.PortConfig{
			{TargetPort: 80, PublishedPort: 8080, PublishMode: swarmtypes.PortConfigPublishModeIngress},
			{TargetPort: 53, PublishedPort: 5353, Protocol: "udp", PublishMode: swarmtypes.PortConfigPublishModeHost},
		}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hostPorts := map[int32]int32{}
	for _, port := range deployment.Spec.Template.Spec.Containers[0].Ports {
		hostPorts[port.ContainerPort] = port.HostPort
	}
	if hostPorts[53] != 5353 {
		t.Fatalf("host-mode port = %d, want a hostPort of 5353", hostPorts[53])
	}
	if hostPorts[80] != 0 {
		t.Fatalf("ingress port bound hostPort %d; it must go through a Service", hostPorts[80])
	}

	published, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(published.Spec.Ports) != 1 || published.Spec.Ports[0].Port != 8080 {
		t.Fatalf("published Service ports = %+v, want only the ingress port", published.Spec.Ports)
	}

	ingress, err := swarm.docker.findNetwork(ctx, ingressNetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if _, attached := deployment.Spec.Template.Labels[networkLabelPrefix+ingress.GetName()]; !attached {
		t.Fatalf("pod labels = %v, want ingress network membership", deployment.Spec.Template.Labels)
	}

	// Kubernetes allocates the real published port, so inspect must read it back.
	published.Spec.Ports[0].NodePort = 31234
	if _, err := client.CoreV1().Services("tenant").Update(ctx, published, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	service, err := swarm.GetService(ctx, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	byTarget := map[uint32]swarmtypes.PortConfig{}
	for _, port := range service.Endpoint.Ports {
		byTarget[port.TargetPort] = port
	}
	if got := byTarget[80]; got.PublishedPort != 31234 || got.PublishMode != swarmtypes.PortConfigPublishModeIngress {
		t.Fatalf("ingress endpoint = %+v, want the allocated node port", got)
	}
	if got := byTarget[53]; got.PublishedPort != 5353 || got.PublishMode != swarmtypes.PortConfigPublishModeHost {
		t.Fatalf("host endpoint = %+v", got)
	}
	// Updating the service must not move the allocated port.
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	published, err = client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if published.Spec.Ports[0].NodePort != 31234 {
		t.Fatalf("node port after update = %d, want it retained", published.Spec.Ports[0].NodePort)
	}
}

func TestSwarmServiceJoinsRequestedNetworks(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)

	if _, err := swarm.docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "backend", Driver: "overlay", Attachable: true}); err != nil {
		t.Fatal(err)
	}
	spec := swarmtypes.ServiceSpec{
		Name: "api",
		TaskTemplate: swarmtypes.TaskSpec{
			ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"},
			Networks:      []swarmtypes.NetworkAttachmentConfig{{Target: "backend"}},
		},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := swarm.docker.findNetwork(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	if _, attached := deployment.Spec.Template.Labels[networkLabelPrefix+backend.GetName()]; !attached {
		t.Fatalf("pod labels = %v, want backend network membership", deployment.Spec.Template.Labels)
	}
	// An unpublished service must not be dragged onto the ingress network.
	ingress, err := swarm.docker.findNetwork(ctx, ingressNetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if _, attached := deployment.Spec.Template.Labels[networkLabelPrefix+ingress.GetName()]; attached {
		t.Fatalf("pod labels = %v, want no ingress membership without published ports", deployment.Spec.Template.Labels)
	}

	inUse, err := swarm.docker.networkInUse(ctx, backend)
	if err != nil || !inUse {
		t.Fatalf("networkInUse = %v, err = %v; a service must count as an endpoint", inUse, err)
	}
	if _, err := swarm.CreateService(ctx, swarmtypes.ServiceSpec{
		Name:         "ghost",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}, Networks: []swarmtypes.NetworkAttachmentConfig{{Target: "missing"}}},
	}, "", false); !IsKind(err, KindNotFound) {
		t.Fatalf("unknown network error = %v, want not found", err)
	}
}

func TestSwarmServiceRejectsUnsupportedModes(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t)

	spec := swarmtypes.ServiceSpec{
		Name:         "web",
		Mode:         swarmtypes.ServiceMode{Global: &swarmtypes.GlobalService{}},
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); !IsKind(err, KindUnsupported) {
		t.Fatalf("global service error = %v, want unsupported", err)
	}

	spec.Mode = swarmtypes.ServiceMode{}
	spec.Name = "Not_A_Valid_Name"
	if _, err := swarm.CreateService(ctx, spec, "", false); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("invalid name error = %v, want invalid argument", err)
	}
}

func TestSwarmSecretsAndConfigs(t *testing.T) {
	ctx, swarm, _ := newSwarmFixture(t)

	_, err := swarm.CreateSecret(ctx, swarmtypes.SecretSpec{
		Name: "api-key", Labels: map[string]string{"env": "prod"},
		Data: []byte("s3cr3t"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := swarm.GetSecret(ctx, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret.Spec.Data) != 0 {
		t.Fatal("secret inspect returned the payload; Docker never does")
	}
	if secret.Spec.Name != "api-key" || secret.Spec.Labels["env"] != "prod" {
		t.Fatalf("secret spec = %+v", secret.Spec)
	}
	if err := swarm.UpdateSecret(ctx, "api-key", 0, swarmtypes.SecretSpec{Data: []byte("new")}); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("secret data update error = %v, want invalid argument", err)
	}

	if _, err := swarm.CreateConfig(ctx, swarmtypes.ConfigSpec{
		Name: "nginx-conf",
		Data: []byte("worker_processes 1;"),
	}); err != nil {
		t.Fatal(err)
	}
	config, err := swarm.GetConfig(ctx, "nginx-conf")
	if err != nil {
		t.Fatal(err)
	}
	// Configs are not secret, so Docker returns their payload.
	if string(config.Spec.Data) != "worker_processes 1;" {
		t.Fatalf("config data = %q", config.Spec.Data)
	}

	configs, err := swarm.GetConfigs(ctx, swarmbackend.ConfigListOptions{})
	if err != nil || len(configs) != 1 {
		t.Fatalf("GetConfigs = %d configs, err = %v", len(configs), err)
	}
	if err := swarm.RemoveConfig(ctx, "nginx-conf"); err != nil {
		t.Fatal(err)
	}
	if _, err := swarm.GetConfig(ctx, "nginx-conf"); !IsKind(err, KindNotFound) {
		t.Fatalf("GetConfig after removal = %v, want not found", err)
	}
}

func TestSwarmTasksFromPods(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t, testNode("node1", false))

	spec := swarmtypes.ServiceSpec{
		Name:         "web",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		Name:      "web-abc",
		Namespace: "tenant",
		UID:       "99999999-8888-7777-6666-555555555555",
		Labels:    deployment.Spec.Template.Labels,
		Spec:      corev1.PodSpec{NodeName: "node1", Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			PodIP:             "10.244.1.7",
			ContainerStatuses: []corev1.ContainerStatus{{Name: "web", ContainerID: "containerd://abc123"}},
		},
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	tasks, err := swarm.GetTasks(ctx, swarmbackend.TaskListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Status.State != swarmtypes.TaskStateRunning || task.DesiredState != swarmtypes.TaskStateRunning {
		t.Fatalf("task state = %q, desired = %q", task.Status.State, task.DesiredState)
	}
	if task.NodeID != identity.DockerIDFromUID("11111111-2222-3333-4444-555555555551") {
		t.Fatalf("task node = %q, want the Swarm ID of node1", task.NodeID)
	}
	if task.Status.ContainerStatus == nil || task.Status.ContainerStatus.ContainerID != "abc123" {
		t.Fatalf("container status = %+v", task.Status.ContainerStatus)
	}
	if task.Labels["com.docker.swarm.service.name"] != "web" {
		t.Fatalf("task labels = %v", task.Labels)
	}
	if len(task.NetworksAttachments) != 1 || task.NetworksAttachments[0].Network.Spec.Name != "bridge" {
		t.Fatalf("task networks = %+v, want the bridge attachment", task.NetworksAttachments)
	}
	if got := task.NetworksAttachments[0].Addresses; len(got) != 1 || got[0].Addr().String() != "10.244.1.7" {
		t.Fatalf("task addresses = %+v, want the Pod IP", got)
	}

	found, err := swarm.GetTask(ctx, task.ID)
	if err != nil || found.ID != task.ID {
		t.Fatalf("GetTask = %+v, err = %v", found, err)
	}
}

func TestNodePlacementConfinesTenantsToTheirNodes(t *testing.T) {
	placement := config.NodePlacement{Enabled: new(true), LabelKey: "dink.io/tenant", Tolerate: new(true)}
	spec := &corev1.PodSpec{}

	applyNodePlacement(spec, placement, "tenant")

	terms := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 2 {
		t.Fatalf("got %d node selector terms, want unlabelled OR own-tenant", len(terms))
	}
	if terms[0].MatchExpressions[0].Operator != corev1.NodeSelectorOpDoesNotExist {
		t.Fatalf("first term = %+v, want nodes without a tenant label", terms[0])
	}
	if terms[1].MatchExpressions[0].Values[0] != "tenant" {
		t.Fatalf("second term = %+v, want the tenant's own nodes", terms[1])
	}
	if len(spec.Tolerations) != 1 || spec.Tolerations[0].Value != "tenant" {
		t.Fatalf("tolerations = %+v, want one for the tenant taint", spec.Tolerations)
	}

	disabled := &corev1.PodSpec{}
	applyNodePlacement(disabled, config.NodePlacement{LabelKey: "dink.io/tenant"}, "tenant")
	if disabled.Affinity != nil || len(disabled.Tolerations) != 0 {
		t.Fatalf("placement applied while disabled: %+v", disabled)
	}
}

func TestPlacementConstraintsBecomeNodeSelectors(t *testing.T) {
	placement := &swarmtypes.Placement{Constraints: []string{
		"node.labels.disk==ssd",
		"node.hostname!=node2",
		"node.role==manager",
	}}

	requirements, err := placementRequirements(placement)
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) != 3 {
		t.Fatalf("got %d requirements, want 3", len(requirements))
	}
	if requirements[0].Key != "disk" || requirements[0].Operator != corev1.NodeSelectorOpIn {
		t.Fatalf("label constraint = %+v", requirements[0])
	}
	if requirements[1].Key != corev1.LabelHostname || requirements[1].Operator != corev1.NodeSelectorOpNotIn {
		t.Fatalf("hostname constraint = %+v", requirements[1])
	}
	if requirements[2].Key != controlPlaneLabel || requirements[2].Operator != corev1.NodeSelectorOpExists {
		t.Fatalf("role constraint = %+v", requirements[2])
	}

	if _, err := placementRequirements(&swarmtypes.Placement{Constraints: []string{"node.platform.os==linux"}}); !IsKind(err, KindUnsupported) {
		t.Fatalf("unknown constraint error = %v, want unsupported", err)
	}
}
