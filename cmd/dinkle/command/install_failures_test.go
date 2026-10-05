package command

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestInstallationReadFailureDoesNotCreateCA(t *testing.T) {
	client := fake.NewSimpleClientset()
	expected := errors.New("API denied")
	client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, expected
	})
	err := prepareInstallation(context.Background(), installationTestOptions(t, client), false, nil, io.Discard)
	if !errors.Is(err, expected) {
		t.Fatalf("expected API failure, got %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("mutated cluster after read failure: %v", action)
		}
	}
}

func TestInstallationDoesNotReplaceMissingCAOnExistingDeployment(t *testing.T) {
	client := fake.NewSimpleClientset(&appsv1.Deployment{
		Name: "dink", Namespace: defaultSystemNamespace,
	})
	err := prepareInstallation(context.Background(), installationTestOptions(t, client), false, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "restore the CA") {
		t.Fatalf("expected CA restore requirement, got %v", err)
	}
}

func TestUpgradeRejectsInvalidTLSSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	o := installationTestOptions(t, client)
	if err := prepareInstallation(ctx, o, false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets(defaultSystemNamespace).Get(ctx, "dinki-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data[corev1.TLSPrivateKeyKey] = []byte("invalid")
	if _, err := client.CoreV1().Secrets(defaultSystemNamespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	if err := prepareInstallation(ctx, o, true, nil, io.Discard); err == nil {
		t.Fatal("upgrade accepted invalid TLS key")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("upgrade repaired certificate implicitly: %v", action)
		}
	}
}
