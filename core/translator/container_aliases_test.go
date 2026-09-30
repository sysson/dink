package translator

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func createAliasedContainer(t *testing.T, ctx context.Context, docker *Docker, name string, aliases ...string) {
	t.Helper()
	config := &backend.ContainerCreateConfig{
		Name:   name,
		Config: &container.Config{Image: "nginx"},
		NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
			"bridge": {Aliases: aliases},
		}},
	}
	if _, err := docker.ContainerCreate(ctx, *config); err != nil {
		t.Fatalf("ContainerCreate(%s): %v", name, err)
	}
}

func TestNetworkAliasesResolveToTheContainer(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	createAliasedContainer(t, ctx, docker, "myproj-postgres-1", "postgres", "db")

	for _, alias := range []string{"postgres", "db"} {
		service, err := client.CoreV1().Services("tenant").Get(ctx, alias, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("alias Service %s: %v", alias, err)
		}
		if service.Spec.Selector["app"] != "myproj-postgres-1" {
			t.Fatalf("alias %s selector = %v, want the container's Pods", alias, service.Spec.Selector)
		}
		if service.Labels[aliasOfLabel] != "myproj-postgres-1" {
			t.Fatalf("alias %s labels = %v, want the owning container", alias, service.Labels)
		}
		if len(service.OwnerReferences) != 1 {
			t.Fatalf("alias %s owner = %+v, want the workload so it is garbage-collected", alias, service.OwnerReferences)
		}
	}

	inspect, _, err := docker.ContainerInspect(ctx, "myproj-postgres-1", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := inspect.NetworkSettings.Networks["bridge"]
	if endpoint == nil {
		t.Fatalf("networks = %+v, want a bridge endpoint", inspect.NetworkSettings.Networks)
	}
	if len(endpoint.Aliases) != 2 || endpoint.Aliases[0] != "db" || endpoint.Aliases[1] != "postgres" {
		t.Fatalf("aliases = %v, want them reported back on inspect", endpoint.Aliases)
	}
}

func TestNetworkAliasConflictsAreRejected(t *testing.T) {
	ctx, docker, _ := newPolicyFixture(t)

	createAliasedContainer(t, ctx, docker, "proj-a-db-1", "postgres")

	// Kubernetes Service names are namespace-unique, so a second claim must fail.
	err := func() error {
		_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
			Name:   "proj-b-db-1",
			Config: &container.Config{Image: "nginx"},
			NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
				"bridge": {Aliases: []string{"postgres"}},
			}},
		})
		return err
	}()
	if !IsKind(err, KindConflict) {
		t.Fatalf("duplicate alias error = %v, want conflict", err)
	}

	// A container name must not collide with an existing alias either.
	_, err = docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "postgres", Config: &container.Config{Image: "nginx"}})
	if !IsKind(err, KindConflict) {
		t.Fatalf("name/alias collision error = %v, want conflict", err)
	}

	_, err = docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:   "bad",
		Config: &container.Config{Image: "nginx"},
		NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
			"bridge": {Aliases: []string{"Not_A_Label"}},
		}},
	})
	if !IsKind(err, KindInvalidArgument) {
		t.Fatalf("invalid alias error = %v, want invalid argument", err)
	}
}

func TestNetworkConnectManagesAliases(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)

	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "backend"}); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "api", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}

	if err := docker.ConnectContainerToNetwork(ctx, "backend", "api", &networktypes.EndpointSettings{Aliases: []string{"gateway"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "gateway", metav1.GetOptions{}); err != nil {
		t.Fatalf("alias Service after connect: %v", err)
	}

	if err := docker.DisconnectContainerFromNetwork(ctx, "backend", "api", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "gateway", metav1.GetOptions{}); err == nil {
		t.Fatal("alias Service outlived the network disconnect")
	}

	// Settings other than aliases still have no Kubernetes equivalent.
	err := docker.ConnectContainerToNetwork(ctx, "backend", "api", &networktypes.EndpointSettings{Links: []string{"other"}})
	if !IsKind(err, KindInvalidArgument) {
		t.Fatalf("unsupported endpoint settings error = %v, want invalid argument", err)
	}
}

func TestSwarmServiceAliases(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)

	spec := swarmtypes.ServiceSpec{
		Name: "myproj-web-1",
		TaskTemplate: swarmtypes.TaskSpec{
			ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"},
			Networks:      []swarmtypes.NetworkAttachmentConfig{{Target: "bridge", Aliases: []string{"web"}}},
		},
		EndpointSpec: &swarmtypes.EndpointSpec{Ports: []swarmtypes.PortConfig{{TargetPort: 80}}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}
	alias, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("alias Service: %v", err)
	}
	if alias.Spec.Selector[swarmKindLabel] != swarmServiceKind || alias.Spec.Selector["app"] != "myproj-web-1" {
		t.Fatalf("alias selector = %v, want the service's tasks", alias.Spec.Selector)
	}
	if len(alias.Spec.Ports) != 1 || alias.Spec.Ports[0].Port != 80 {
		t.Fatalf("alias ports = %+v, want the task port", alias.Spec.Ports)
	}
	if alias.Spec.ClusterIP == corev1.ClusterIPNone {
		t.Fatal("alias Service is headless despite the service exposing a port")
	}

	// Dropping the alias must remove its Service.
	spec.TaskTemplate.Networks[0].Aliases = nil
	if _, err := swarm.UpdateService(ctx, "myproj-web-1", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("alias Service outlived the spec that asked for it")
	}
}
