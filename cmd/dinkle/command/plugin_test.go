package command

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"strings"
	"testing"

	"github.com/sysson/syskit/pki"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/dink/cmd/dinkle/store"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/core/secrets"
)

func tenantNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{Name: name, Labels: map[string]string{
		namespaces.LabelManagedBy: namespaces.ManagedByValue,
		namespaces.LabelTenant:    name,
	}}
}

func newTestService(t *testing.T, objects ...runtime.Object) (*Service, *fake.Clientset, dynamic.Interface) {
	t.Helper()
	kc := fake.NewClientset(objects...)
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{plugins.Resource: "PluginList"})
	return newService(&Options{
		certsDir:         t.TempDir(),
		newKubeClient:    func(context.Context, string) (kubernetes.Interface, error) { return kc, nil },
		newDynamicClient: func(context.Context, string) (dynamic.Interface, error) { return dc, nil },
	}, &caOptions{}), kc, dc
}

func TestSecretStoreLifecycle(t *testing.T) {
	ctx := t.Context()
	s, kc, _ := newTestService(t, tenantNamespace("dev"))

	if err := s.SetSecret(ctx, "dev", "db-password", []byte("hunter2"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret(ctx, "dev", "api-key", []byte("k"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stored, err := kc.CoreV1().Secrets("dev").Get(ctx, secrets.StoreName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Labels[secrets.StoreLabel] != "true" || string(stored.Data["db-password"]) != "hunter2" {
		t.Fatalf("unexpected store: %+v", stored)
	}

	var out bytes.Buffer
	if err := s.ListSecrets(ctx, "dev", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "se://k8s/api-key\nse://k8s/db-password\n" || strings.Contains(out.String(), "hunter2") {
		t.Fatalf("list output = %q", out.String())
	}

	if err := s.RemoveSecret(ctx, "dev", "db-password", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSecret(ctx, "dev", "db-password", &bytes.Buffer{}); err == nil {
		t.Fatal("removing a missing key should fail")
	}
}

func TestSecretStoreRefusesForeignSecret(t *testing.T) {
	ctx := t.Context()
	s, _, _ := newTestService(t, tenantNamespace("dev"), &corev1.Secret{
		Name: secrets.StoreName, Namespace: "dev",
	})
	if err := s.SetSecret(ctx, "dev", "k", []byte("v"), &bytes.Buffer{}); err == nil {
		t.Fatal("an unlabelled secret with the store's name must not be written to")
	}
}

func TestSecretStoreRequiresTenant(t *testing.T) {
	s, _, _ := newTestService(t)
	if err := s.SetSecret(t.Context(), "dev", "k", []byte("v"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error for a missing tenant")
	}
}

func TestReadSecretValue(t *testing.T) {
	v, err := readSecretValue(strings.NewReader("s3cret\n"), "")
	if err != nil || string(v) != "s3cret" {
		t.Fatalf("got %q, %v", v, err)
	}
	if _, err := readSecretValue(strings.NewReader(strings.Repeat("x", maxSecretSize+1)), ""); err == nil {
		t.Fatal("expected oversized value to be rejected")
	}
}

func TestPluginLifecycle(t *testing.T) {
	ctx := t.Context()
	s, _, dc := newTestService(t)

	p := &pluginOptions{namespace: "dev", types: []string{"secrets"}, port: 8443, timeout: "2s"}
	if err := s.AddPlugin(ctx, "vault", p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	obj, err := dc.Resource(plugins.Resource).Namespace("dev").Get(ctx, "vault", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := plugins.SpecFromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Service.Name != "vault" || spec.Insecure || spec.Endpoint("dev") != "https://vault.dev.svc:8443" {
		t.Fatalf("unexpected spec: %+v", spec)
	}

	p.types = []string{"secrets", "auth"}
	if err := s.AddPlugin(ctx, "vault", p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	obj, _ = dc.Resource(plugins.Resource).Namespace("dev").Get(ctx, "vault", metav1.GetOptions{})
	if spec, _ := plugins.SpecFromObject(obj); len(spec.Types) != 2 {
		t.Fatalf("expected the registration to be updated, got %+v", spec)
	}

	var out bytes.Buffer
	if err := s.ListPlugins(ctx, "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "vault") || !strings.Contains(out.String(), "secrets,auth") {
		t.Fatalf("list output = %q", out.String())
	}

	if err := s.RemovePlugin(ctx, "dev", "vault", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPlugin(ctx, "vault", &pluginOptions{namespace: "dev", types: []string{"auth"}, timeout: "soon"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected an invalid timeout to be rejected")
	}
}

func TestIssuePluginCert(t *testing.T) {
	ctx := t.Context()
	s, kc, _ := newTestService(t, tenantNamespace("dev"))
	ca, err := pki.NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewFileStore(s.options.certsDir).SaveCA(ctx, ca.KeyPair()); err != nil {
		t.Fatal(err)
	}

	p := &pluginOptions{namespace: "dev", service: "vault", clusterDomain: defaultClusterDomain}
	if err := s.IssuePluginCert(ctx, p, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	secret, err := kc.CoreV1().Secrets("dev").Get(ctx, "vault-tls", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(secret.Data["tls.crt"])
	if block == nil {
		t.Fatal("no certificate in the secret")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cert.DNSNames, "vault.dev.svc") || len(secret.Data["ca.crt"]) == 0 {
		t.Fatalf("unexpected certificate: %v", cert.DNSNames)
	}
	// A plugin certificate must never be usable to authenticate to dink.
	if slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Fatal("plugin certificate must not allow client authentication")
	}

	if err := s.IssuePluginCert(ctx, &pluginOptions{namespace: "missing", service: "vault"}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error for a missing namespace")
	}
}
