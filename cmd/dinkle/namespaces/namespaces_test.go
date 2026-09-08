package namespaces

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureIgnoresExistingNamespace(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Namespace{})

	if err := New(client).Ensure(ctx, "existing"); err != nil {
		t.Fatalf("ensuring existing namespace: %v", err)
	}
}

func TestGetAndDeleteRejectUnmanagedNamespace(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset(&corev1.Namespace{})
	manager := New(client)

	if _, err := manager.Get(ctx, "existing"); err == nil {
		t.Fatal("expected unmanaged namespace to be rejected")
	}
	if err := manager.Delete(ctx, "existing"); err == nil {
		t.Fatal("expected deleting unmanaged namespace to be rejected")
	}
}
