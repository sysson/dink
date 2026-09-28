package translator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/syskit/httpx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

type fakeRegistry struct {
	digests map[string]string
	issued  int
}

func (f *fakeRegistry) Authenticate(context.Context, *registry.AuthConfig) (string, error) {
	return "", nil
}

func (f *fakeRegistry) ImageInspect(_ context.Context, name string, _ imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	digest, ok := f.digests[name]
	if !ok {
		return nil, errors.New("no such image: " + name)
	}
	return &imagebackend.InspectData{ID: digest, RepoDigests: []string{name + "@" + digest}}, nil
}

func (f *fakeRegistry) IssuePullCredential(ctx context.Context) (string, string, error) {
	f.issued++
	id, _ := identity.FromContext(ctx)
	return id.Namespace, "secret", nil
}

func TestContainerCreatePullsFromDinki(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	registry := &fakeRegistry{digests: map[string]string{"nginx": digest}}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry, pullHost: "localhost:5000"}

	for _, name := range []string{"web", "api"} {
		if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: name, Config: &container.Config{Image: "nginx"}}); err != nil {
			t.Fatalf("ContainerCreate(%s): %v", name, err)
		}
	}
	if registry.issued != 1 {
		t.Fatalf("credential issued %d times, want once per namespace", registry.issued)
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "web", Config: &container.Config{Image: "nginx"}})
	if httpErr, ok := errors.AsType[*httpx.HTTPError](err); !ok || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate ContainerCreate error = %v, want 409", err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := deployment.Spec.Template.Spec
	if got := spec.Containers[0].Image; got != "localhost:5000/tenant/nginx@"+digest {
		t.Fatalf("pod image = %q", got)
	}
	if spec.Containers[0].ImagePullPolicy != corev1.PullAlways || len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != pullSecretName {
		t.Fatalf("pod pull settings = %+v, %+v", spec.Containers[0].ImagePullPolicy, spec.ImagePullSecrets)
	}
	if got := deployment.Spec.Template.Labels[networkLabelPrefix+networkObjectName("bridge")]; got != "true" {
		t.Fatalf("default network membership label = %q, want true", got)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("DNS service = %+v, err = %v; want headless service", dnsService, err)
	}

	secret, err := client.CoreV1().Secrets("tenant").Get(ctx, pullSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config) != nil {
		t.Fatalf("pull secret = %+v", secret)
	}
	entry, ok := config.Auths["localhost:5000/tenant"]
	if !ok || len(config.Auths) != 1 || entry.Username != "tenant" || entry.Password != "secret" || entry.Auth != "dGVuYW50OnNlY3JldA==" {
		t.Fatalf("pull secret auths = %+v", config.Auths)
	}

	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "missing", Config: &container.Config{Image: "redis"}}); err == nil {
		t.Fatal("ContainerCreate with a missing image succeeded")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "missing", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment created for a missing image")
	}
}

func TestContainerCreateUsesRequestedNetworkMembership(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	const networkName = "isolated"
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "dink.io/v1alpha1",
		"kind":       "DockerNetwork",
		"metadata": map[string]any{
			"name":      networkObjectName(networkName),
			"namespace": "tenant",
			"uid":       "12345678-1234-1234-1234-123456789abc",
		},
		"spec": map[string]any{"name": networkName, "driver": "bridge"},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		networkResource: "DockerNetworkList",
	}, obj)
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client, Dynamic: dynamicClient},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:   "web",
		Config: &container.Config{Image: "nginx"},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	labels := deployment.Spec.Template.Labels
	if labels[networkLabelPrefix+networkObjectName(networkName)] != "true" {
		t.Fatalf("requested network label missing: %v", labels)
	}
	if _, hasBridge := labels[networkLabelPrefix+networkObjectName("bridge")]; hasBridge {
		t.Fatalf("default bridge label also present for explicit network: %v", labels)
	}
}

func TestContainerInspectAndList(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx:latest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	created := metav1.NewTime(time.Now().Add(-time.Minute))
	response, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image:      "nginx:latest",
			Entrypoint: []string{"/entrypoint"},
			Cmd:        []string{"--serve"},
			Env:        []string{"MODE=production"},
			WorkingDir: "/srv",
			Labels:     map[string]string{"team": "blue"},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{
			network.MustParsePort("8080/tcp"): {{HostPort: "18080"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deployment.CreationTimestamp = created
	if _, err := client.AppsV1().Deployments("tenant").Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-pod", Namespace: "tenant", Labels: deployment.Spec.Template.Labels},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.1.2.3",
			StartTime: &created,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:    "web",
				Image:   "localhost:5000/tenant/nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				ImageID: "containerd://sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}},
			}},
		},
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	inspect, _, err := docker.ContainerInspect(ctx, "web", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.ID != response.ID || inspect.Name != "/web" || inspect.Config.Image != "nginx:latest" || inspect.Path != "/entrypoint" || len(inspect.Args) != 1 || inspect.Args[0] != "--serve" {
		t.Fatalf("inspect response = %+v", inspect)
	}
	if inspect.State.Status != container.StateRunning || inspect.State.StartedAt == "" || inspect.NetworkSettings.Networks["bridge"].IPAddress.String() != "10.1.2.3" {
		t.Fatalf("inspect state/network = %+v, %+v", inspect.State, inspect.NetworkSettings)
	}
	port := network.MustParsePort("8080/tcp")
	if got := inspect.NetworkSettings.Ports[port]; len(got) != 1 || got[0].HostPort != "18080" {
		t.Fatalf("inspect port bindings = %+v", inspect.NetworkSettings.Ports)
	}

	listed, err := docker.Containers(ctx, &backend.ContainerListOptions{})
	if err != nil || len(listed) != 1 || listed[0].ID != response.ID || listed[0].Image != "nginx:latest" || listed[0].State != container.StateRunning || listed[0].Labels["team"] != "blue" {
		t.Fatalf("containers = %+v, err = %v", listed, err)
	}
	if listed[0].Ports == nil || len(listed[0].Ports) != 1 || listed[0].Ports[0].PublicPort != 18080 {
		t.Fatalf("listed ports = %+v", listed[0].Ports)
	}

	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	listed, err = docker.Containers(ctx, &backend.ContainerListOptions{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("default container list after stop = %+v, err = %v; want empty", listed, err)
	}
	listed, err = docker.Containers(ctx, &backend.ContainerListOptions{All: true})
	if err != nil || len(listed) != 1 || listed[0].State != container.StateExited {
		t.Fatalf("all container list after stop = %+v, err = %v", listed, err)
	}
}

func TestContainerNetworkModeMembership(t *testing.T) {
	docker := &Docker{}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	defaultLabels, _, err := docker.containerNetworkLabels(ctx, backend.ContainerCreateConfig{
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			network.NetworkDefault: {},
		}},
	})
	if err != nil || defaultLabels[networkLabelPrefix+networkObjectName(network.NetworkBridge)] != "true" {
		t.Fatalf("default endpoint mapping = %v, err = %v", defaultLabels, err)
	}
	for _, test := range []struct {
		mode        container.NetworkMode
		wantHost    bool
		wantNetwork string
	}{
		{mode: "host", wantHost: true},
		{mode: "none", wantNetwork: "none"},
	} {
		labels, hostNetwork, err := docker.containerNetworkLabels(ctx, backend.ContainerCreateConfig{HostConfig: &container.HostConfig{NetworkMode: test.mode}})
		if err != nil {
			t.Fatalf("network mode %q: %v", test.mode, err)
		}
		if hostNetwork != test.wantHost {
			t.Fatalf("network mode %q hostNetwork = %v, want %v", test.mode, hostNetwork, test.wantHost)
		}
		if test.wantNetwork == "" {
			if len(labels) != 0 {
				t.Fatalf("network mode %q labels = %v, want none", test.mode, labels)
			}
			continue
		}
		if labels[networkLabelPrefix+networkObjectName(test.wantNetwork)] != "true" {
			t.Fatalf("network mode %q labels = %v", test.mode, labels)
		}
	}
}

func TestContainerCreatePublishesExplicitPorts(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	response, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image: "nginx",
			ExposedPorts: network.PortSet{
				network.MustParsePort("80/tcp"): {},
				network.MustParsePort("53/udp"): {},
			},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{
			network.MustParsePort("80/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "8080"}},
			network.MustParsePort("53/udp"): {{HostPort: "5353"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Warnings) != 1 {
		t.Fatalf("warnings = %v, want host IP warning", response.Warnings)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Ports; len(got) != 2 {
		t.Fatalf("pod ports = %+v, want TCP and UDP container ports", got)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.Type != corev1.ServiceTypeClusterIP || len(dnsService.Spec.Ports) != 2 {
		t.Fatalf("DNS service = %+v, err = %v; want ClusterIP with two target ports", dnsService, err)
	}

	service, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer || len(service.Spec.Ports) != 2 {
		t.Fatalf("published service = %+v, want LoadBalancer with two ports", service.Spec)
	}
	portsByProtocol := make(map[corev1.Protocol]corev1.ServicePort, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		portsByProtocol[port.Protocol] = port
	}
	if tcp := portsByProtocol[corev1.ProtocolTCP]; tcp.Port != 8080 || tcp.TargetPort.IntVal != 80 {
		t.Fatalf("published TCP service port = %+v", tcp)
	}
	if udp := portsByProtocol[corev1.ProtocolUDP]; udp.Port != 5353 || udp.TargetPort.IntVal != 53 {
		t.Fatalf("published UDP service port = %+v", udp)
	}
}

func TestContainerCreatePublishesAllPortsWithNodePort(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:       "web",
		Config:     &container.Config{Image: "nginx", ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}}},
		HostConfig: &container.HostConfig{PublishAllPorts: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.Type != corev1.ServiceTypeClusterIP || len(dnsService.Spec.Ports) != 1 {
		t.Fatalf("DNS service = %+v, err = %v; want ClusterIP with one target port", dnsService, err)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Type != corev1.ServiceTypeNodePort || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 80 || service.Spec.Ports[0].TargetPort.IntVal != 80 {
		t.Fatalf("published service = %+v, want NodePort service for 80/tcp", service.Spec)
	}
}

func TestContainerLifecycle(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(1)
	deployment, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dockerID := identity.DockerIDFromUID(deployment.UID)

	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas after stop = %v, err = %v; want 0", deployment.Spec.Replicas, err)
	}

	if err := docker.ContainerStart(ctx, dockerID[:12], "", ""); err != nil {
		t.Fatalf("ContainerStart by ID prefix: %v", err)
	}
	if err := docker.ContainerRestart(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerRestart: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Annotations["dink.io/restarted-at"] == "" {
		t.Fatalf("deployment after restart = %+v, err = %v", deployment.Spec, err)
	}

	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerStop(otherTenant, "web", backend.ContainerStopOptions{}); err == nil {
		t.Fatal("ContainerStop crossed tenant namespace")
	}
	for _, serviceName := range []string{"web", publishedPortsServiceName("web")} {
		if _, err := client.CoreV1().Services("tenant").Create(ctx, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName, Namespace: "tenant"}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{}); err != nil {
		t.Fatalf("ContainerRm by ID: %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("published service remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("DNS service remains after ContainerRm")
	}
}
