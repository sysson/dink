package translator

import (
	"context"
	"testing"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

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
			Name: item.name, Namespace: "tenant", Annotations: annotations,
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
