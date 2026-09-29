package translator

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestSubscribeToEventsIsScopedToIdentityNamespace(t *testing.T) {
	client := kubernetesfake.NewClientset()
	other := &appsv1.Deployment{Name: "outside", Namespace: "tenant-b"}
	if _, err := client.AppsV1().Deployments("tenant-b").Create(context.Background(), other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	translator := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant-a"})
	_, stream, err := translator.SubscribeToEvents(ctx, time.Time{}, time.Time{}, filters.NewArgs())
	if err != nil {
		t.Fatal(err)
	}

	outside := other.DeepCopy()
	outside.Name = "outside-after-subscribe"
	if _, err := client.AppsV1().Deployments("tenant-b").Create(context.Background(), outside, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-stream:
		t.Fatalf("received another namespace event: %#v", value)
	case <-time.After(25 * time.Millisecond):
	}

	inside := &appsv1.Deployment{
		Name: "inside", Namespace: "tenant-a",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inside", Image: "example/image:v1"}},
		}}},
	}
	if _, err := client.AppsV1().Deployments("tenant-a").Create(context.Background(), inside, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case value, open := <-stream:
		if !open {
			t.Fatal("event stream closed before tenant event")
		}
		message, ok := value.(events.Message)
		if !ok || message.Type != events.ContainerEventType || message.Actor.Attributes["name"] != "inside" {
			t.Fatalf("event = %#v, want tenant-a container event", value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tenant namespace event")
	}

	if err := translator.UnsubscribeFromEvents(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-stream:
		if open {
			t.Fatal("event stream remained open after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("event stream did not close after unsubscribe")
	}
}

func TestSubscribeToEventsRequiresIdentityNamespace(t *testing.T) {
	translator := &Docker{}
	for _, ctx := range []context.Context{
		context.Background(),
		identity.NewContext(context.Background(), identity.Identity{}),
	} {
		if _, _, err := translator.SubscribeToEvents(ctx, time.Time{}, time.Time{}, filters.NewArgs()); !IsKind(err, KindUnauthenticated) {
			t.Fatalf("SubscribeToEvents without namespace = %v, want unauthenticated", err)
		}
	}
}
