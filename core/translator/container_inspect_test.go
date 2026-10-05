package translator

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

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
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
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
		Name: "web-pod", Namespace: "tenant", Labels: deployment.Spec.Template.Labels,
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.1.2.3",
			StartTime: &created,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "injected-sidecar", ImageID: "containerd://sha256:sidecar", RestartCount: 99,
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}}},
				{
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
	if inspect.Image != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || inspect.RestartCount != 0 {
		t.Fatalf("inspect selected sidecar image or restart count: %+v", inspect)
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

	if err := client.CoreV1().Pods("tenant").Delete(ctx, "web-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
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
