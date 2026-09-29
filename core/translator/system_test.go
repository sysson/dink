package translator

import (
	"context"
	"testing"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestSystemInfoIsNamespaceScoped(t *testing.T) {
	replicas := int32(1)
	stopped := int32(0)
	client := kubernetesfake.NewClientset(
		&appsv1.Deployment{
			Name: "running", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Image: "registry.example/app:v1"}},
				}},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.Deployment{
			Name: "stopped", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{
				Replicas: &stopped,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Image: "registry.example/app:v1"}},
					InitContainers: []corev1.Container{{Image: "registry.example/init:v2"}},
				}},
			},
		},
		&appsv1.Deployment{
			Name: "pending", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		},
		&appsv1.Deployment{
			Name: "other-tenant", Namespace: "other",
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	translator := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})

	info, err := translator.SystemInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Containers != 3 || info.ContainersRunning != 1 || info.ContainersStopped != 1 || info.ContainersPaused != 0 {
		t.Fatalf("container counts = total:%d running:%d stopped:%d paused:%d", info.Containers, info.ContainersRunning, info.ContainersStopped, info.ContainersPaused)
	}
	if info.Images != 2 {
		t.Fatalf("image count = %d, want 2 distinct workload references", info.Images)
	}
	if info.Name != "dink" || info.ServerVersion == "" || len(info.Warnings) == 0 {
		t.Fatalf("system identity or scope warnings missing: %+v", info)
	}
}
