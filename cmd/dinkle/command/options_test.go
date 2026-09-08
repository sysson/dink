package command

import (
	"context"
	"errors"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestOptionsKubeClientCachesInitError(t *testing.T) {
	calls := 0
	o := &Options{
		kubeConfig: "test",
		newKubeClient: func(ctx context.Context, kubeConfig string) (kubernetes.Interface, error) {
			calls++
			return nil, errors.New("boom")
		},
	}

	if _, err := o.kubeClient(context.Background()); err == nil || err.Error() != "boom" {
		t.Fatalf("expected boom error, got %v", err)
	}
	if _, err := o.kubeClient(context.Background()); err == nil || err.Error() != "boom" {
		t.Fatalf("expected cached boom error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one kube client initialization call, got %d", calls)
	}
}

func TestOptionsKubeClientCachesSuccess(t *testing.T) {
	calls := 0
	client := fake.NewSimpleClientset()
	o := &Options{
		kubeConfig: "test",
		newKubeClient: func(ctx context.Context, kubeConfig string) (kubernetes.Interface, error) {
			calls++
			return client, nil
		},
	}

	first, err := o.kubeClient(context.Background())
	if err != nil {
		t.Fatalf("unexpected error creating kube client: %v", err)
	}
	if first != client {
		t.Fatal("expected cached client instance to be returned")
	}

	second, err := o.kubeClient(context.Background())
	if err != nil {
		t.Fatalf("unexpected error creating kube client: %v", err)
	}
	if second != client {
		t.Fatal("expected same cached client instance to be returned")
	}
	if calls != 1 {
		t.Fatalf("expected one kube client initialization call, got %d", calls)
	}
}
