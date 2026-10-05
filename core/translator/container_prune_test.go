package translator

import (
	"context"
	"testing"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestContainerPrunePreservesReusedName(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "old", Namespace: "tenant", UID: "original",
		Annotations: map[string]string{containerConfigAnnotation: `{}`},
		Spec:        appsv1.DeploymentSpec{Replicas: new(int32(0))},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	renamed := false
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		if !renamed {
			renamed = true
			resource := appsv1.SchemeGroupVersion.WithResource("deployments")
			object, err := client.Tracker().Get(resource, "tenant", "old")
			if err != nil {
				t.Fatal(err)
			}
			current := object.(*appsv1.Deployment).DeepCopy()
			current.Annotations[containerNameAnnotation] = "Renamed"
			if err := client.Tracker().Update(resource, current, "tenant"); err != nil {
				t.Fatal(err)
			}
			if err := client.Tracker().Create(resource, &appsv1.Deployment{
				Name: "reused", Namespace: "tenant", UID: "replacement",
				Annotations: map[string]string{containerConfigAnnotation: `{}`, containerNameAnnotation: "old"},
				Spec:        appsv1.DeploymentSpec{Replicas: new(int32(0))},
			}, "tenant"); err != nil {
				t.Fatal(err)
			}
		}
		return false, nil, nil
	})
	report, err := docker.ContainerPrune(ctx, filters.NewArgs())
	if err != nil || len(report.ContainersDeleted) != 1 || report.ContainersDeleted[0] != identity.DockerIDFromUID("original") {
		t.Fatalf("prune report = %+v, err = %v", report, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "old", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("original workload remains: %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "reused", metav1.GetOptions{}); err != nil {
		t.Fatalf("prune removed the reused Docker name: %v", err)
	}
}

func TestContainerPrune(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	for _, item := range []struct {
		name     string
		replicas int32
		label    string
	}{
		{"old", 0, "web"}, {"running", 1, "web"}, {"other", 0, "db"},
	} {
		annotations := map[string]string{containerConfigAnnotation: `{"Labels":{"app":"` + item.label + `"}}`}
		if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
			Name: item.name, Namespace: "tenant", UID: types.UID(item.name), Annotations: annotations,
			Spec: appsv1.DeploymentSpec{Replicas: &item.replicas},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "unmanaged", Namespace: "tenant", Spec: appsv1.DeploymentSpec{Replicas: &zero},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, err := docker.ContainerPrune(ctx, filters.NewArgs(filters.Arg("label", "app=web")))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ContainersDeleted) != 1 {
		t.Fatalf("deleted = %v, want one container", report.ContainersDeleted)
	}
	for _, name := range []string{"running", "other", "unmanaged"} {
		if _, err := client.AppsV1().Deployments("tenant").Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatalf("%s removed: %v", name, err)
		}
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "old", metav1.GetOptions{}); err == nil {
		t.Fatal("pruned deployment still exists")
	}
	if _, err := docker.ContainerPrune(ctx, filters.NewArgs(filters.Arg("unknown", "value"))); err == nil {
		t.Fatal("unsupported filter accepted")
	}
}
