package translator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// newNetworkDynamicClient backs DockerNetwork objects with stable UIDs so
// network IDs behave like they do against a real API server.
func newNetworkDynamicClient() *fake.FakeDynamicClient {
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		networkResource: "DockerNetworkList",
	})
	dynamicClient.PrependReactor("create", "dockernetworks", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		hash := sha256.Sum256([]byte(action.GetNamespace() + "/" + obj.GetName()))
		obj.SetUID(k8stypes.UID(fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])))
		obj.SetCreationTimestamp(metav1.Now())
		return false, nil, nil
	})
	return dynamicClient
}

func TestDockerNetworkLifecycle(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	dynamicClient := newNetworkDynamicClient()
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client, Dynamic: dynamicClient}}
	builtins, err := docker.GetNetworkSummaries(ctx, filters.NewArgs(filters.Arg("type", "builtin")))
	if err != nil || len(builtins) != 4 {
		t.Fatalf("built-in network list: %+v, %v", builtins, err)
	}
	for _, network := range builtins {
		if network.Driver != builtinNetworkDrivers[network.Name] || len(network.ID) != 64 {
			t.Fatalf("built-in network: %+v", network)
		}
		if network.Name == ingressNetworkName && (network.Scope != "swarm" || !network.Ingress) {
			t.Fatalf("ingress network: %+v, want a swarm-scoped routing mesh", network)
		}
		inspected, err := docker.GetNetwork(ctx, network.ID[:12])
		if err != nil || inspected.Name != network.Name || inspected.Driver != network.Driver {
			t.Fatalf("inspect built-in network: %+v, %v", inspected, err)
		}
		if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: network.Name}); err == nil {
			t.Fatalf("recreated built-in network %s", network.Name)
		}
		for _, reference := range []string{network.Name, network.ID} {
			if err := docker.DeleteNetwork(ctx, reference); !IsKind(err, KindForbidden) {
				t.Fatalf("delete built-in network %s: %v", reference, err)
			}
		}
	}
	createCount := 0
	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "create" {
			createCount++
		}
	}
	if createCount != len(builtinNetworkDrivers) {
		t.Fatalf("created built-ins %d times, want %d", createCount, len(builtinNetworkDrivers))
	}

	created, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "my.network", Labels: map[string]string{"team": "dev"}})
	if err != nil || len(created.ID) != 64 || created.Warning != "" {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "my.network"}); err == nil {
		t.Fatal("duplicate network name was accepted")
	}
	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "unsupported", Internal: true}); err == nil {
		t.Fatal("unsupported internal network was accepted")
	}

	items, err := docker.GetNetworkSummaries(ctx, filters.NewArgs(filters.Arg("label", "team=dev")))
	if err != nil || len(items) != 1 || items[0].Name != "my.network" || items[0].ID != created.ID {
		t.Fatalf("list: %+v, %v", items, err)
	}
	inspect, err := docker.GetNetwork(ctx, created.ID[:12])
	if err != nil || inspect.Name != "my.network" || inspect.Containers == nil || inspect.Created.IsZero() {
		t.Fatalf("inspect: %+v, %v", inspect, err)
	}

	objects, err := dynamicClient.Resource(networkResource).Namespace("tenant").List(ctx, metav1.ListOptions{})
	if err != nil || len(objects.Items) != len(builtinNetworkDrivers)+1 {
		t.Fatalf("CRD list: %+v, %v", objects, err)
	}
	label := networkLabelPrefix + networkObjectName("my.network")
	_, err = client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "attached",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{label: "true"}},
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	used, err := docker.GetNetworkSummaries(ctx, filters.NewArgs(filters.Arg("dangling", "false"), filters.Arg("type", "custom")))
	if err != nil || len(used) != 1 {
		t.Fatalf("list used networks: %+v, %v", used, err)
	}
	unused, err := docker.GetNetworkSummaries(ctx, filters.NewArgs(filters.Arg("dangling", "true"), filters.Arg("type", "custom")))
	if err != nil || len(unused) != 0 {
		t.Fatalf("list unused networks: %+v, %v", unused, err)
	}
	if err := docker.DeleteNetwork(ctx, "my.network"); err == nil {
		t.Fatal("deleted network with active endpoints")
	}
	pruned, err := docker.NetworkPrune(ctx, filters.NewArgs())
	if err != nil || len(pruned.NetworksDeleted) != 0 {
		t.Fatalf("prune active network: %+v, %v", pruned, err)
	}
	if err := client.AppsV1().Deployments("tenant").Delete(ctx, "attached", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	pruned, err = docker.NetworkPrune(ctx, filters.NewArgs(filters.Arg("until", "1h")))
	if err != nil || len(pruned.NetworksDeleted) != 0 {
		t.Fatalf("prune recent network: %+v, %v", pruned, err)
	}
	pruned, err = docker.NetworkPrune(ctx, filters.NewArgs(filters.Arg("label", "team=dev")))
	if err != nil || len(pruned.NetworksDeleted) != 1 || pruned.NetworksDeleted[0] != "my.network" {
		t.Fatalf("prune: %+v, %v", pruned, err)
	}
	if _, err := docker.GetNetwork(ctx, "my.network"); err == nil {
		t.Fatal("pruned network remains visible")
	}
	pruned, err = docker.NetworkPrune(ctx, filters.NewArgs())
	if err != nil || len(pruned.NetworksDeleted) != 0 {
		t.Fatalf("pruned built-in networks: %+v, %v", pruned, err)
	}
	for _, network := range builtins {
		inspected, err := docker.GetNetwork(ctx, network.Name)
		if err != nil || inspected.ID != network.ID {
			t.Fatalf("built-in network changed after prune: %+v, %v", inspected, err)
		}
	}
	createCount = 0
	for _, action := range dynamicClient.Actions() {
		if action.GetVerb() == "create" {
			createCount++
		}
	}
	if createCount != len(builtinNetworkDrivers)+1 {
		t.Fatalf("network CRDs were created %d times, want %d", createCount, len(builtinNetworkDrivers)+1)
	}
	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	otherNetworks, err := docker.GetNetworkSummaries(otherTenant, filters.NewArgs())
	if err != nil || len(otherNetworks) != len(builtinNetworkDrivers) {
		t.Fatalf("other tenant networks: %+v, %v", otherNetworks, err)
	}
	for _, otherNetwork := range otherNetworks {
		for _, network := range builtins {
			if network.Name == otherNetwork.Name && network.ID == otherNetwork.ID {
				t.Fatalf("network %s shares an ID across tenants", network.Name)
			}
		}
	}

	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "my.network"}); err != nil {
		t.Fatal(err)
	}
	if err := docker.DeleteNetwork(ctx, "my.network"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// `docker stack deploy` creates its networks with the overlay driver before it
// creates any service.
func TestOverlayNetworksAreSwarmScoped(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: kubernetesfake.NewClientset(), Dynamic: newNetworkDynamicClient()}}

	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "stack_default", Driver: "overlay", Attachable: true}); err != nil {
		t.Fatalf("create overlay network: %v", err)
	}
	created, err := docker.GetNetwork(ctx, "stack_default")
	if err != nil {
		t.Fatal(err)
	}
	if created.Driver != "overlay" || created.Scope != "swarm" || !created.Attachable {
		t.Fatalf("overlay network = %+v", created)
	}

	ingress, err := docker.GetNetwork(ctx, ingressNetworkName)
	if err != nil {
		t.Fatal(err)
	}
	if !ingress.Ingress || ingress.Driver != "overlay" || ingress.Scope != "swarm" {
		t.Fatalf("ingress network = %+v", ingress)
	}
	if err := docker.DeleteNetwork(ctx, ingressNetworkName); !IsKind(err, KindForbidden) {
		t.Fatalf("delete ingress error = %v, want forbidden", err)
	}
	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "second-ingress", Driver: "overlay", Ingress: true}); !IsKind(err, KindConflict) {
		t.Fatalf("second ingress error = %v, want conflict", err)
	}
	if _, err := docker.CreateNetwork(ctx, networktypes.CreateRequest{Name: "macvlan", Driver: "macvlan"}); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("unsupported driver error = %v, want invalid argument", err)
	}

	swarmScoped, err := docker.GetNetworkSummaries(ctx, filters.NewArgs(filters.Arg("scope", "swarm")))
	if err != nil || len(swarmScoped) != 2 {
		t.Fatalf("swarm-scoped networks = %+v, err = %v", swarmScoped, err)
	}
}
