package command

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/dink/dinkle/store"
	"github.com/sysson/syskit/pki"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCAOptionsValidateSetsCAIdentity(t *testing.T) {
	c := &caOptions{keyType: string(pki.DefaultKeyType)}
	if err := c.validate(); err != nil {
		t.Fatalf("validating CA options: %v", err)
	}
	if c.opts.CommonName != defaultCACommonName {
		t.Errorf("CA common name = %q, want %q", c.opts.CommonName, defaultCACommonName)
	}
	if c.opts.Organization != defaultOrganization {
		t.Errorf("organization = %q, want %q", c.opts.Organization, defaultOrganization)
	}
}

func TestLoadOrCreateCAReusesSystemNamespaceCA(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	ca, err := pki.NewAuthority()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}

	const systemNamespace = "system"
	const caSecretName = "ca"
	if err := namespaces.NewSecretStore(client, systemNamespace, caSecretName).SaveCA(ctx, ca.KeyPair()); err != nil {
		t.Fatalf("saving cluster CA: %v", err)
	}

	s := newService(&Options{
		certsDir: t.TempDir(),
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) {
			return client, nil
		},
	}, &caOptions{
		systemNamespace: systemNamespace,
		caSecretName:    caSecretName,
	})

	loaded, err := s.loadOrCreateCA(ctx, store.NewFileStore(s.options.certsDir), false, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("loading CA: %v", err)
	}
	if loaded.Certificate().SerialNumber.Cmp(ca.Certificate().SerialNumber) != 0 {
		t.Fatal("expected CA stored in the system namespace to be reused")
	}
}

func TestLoadOrCreateCAReturnsClusterReadError(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	clusterErr := errors.New("boom")
	client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, clusterErr
	})

	s := newService(&Options{
		certsDir: t.TempDir(),
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) {
			return client, nil
		},
	}, &caOptions{systemNamespace: "system", caSecretName: "ca"})

	if _, err := s.loadOrCreateCA(ctx, store.NewFileStore(s.options.certsDir), false, &bytes.Buffer{}); err == nil || !errors.Is(err, clusterErr) {
		t.Fatalf("expected cluster read error, got %v", err)
	}
}

func TestGenerateCAWritesServerCertificateAtCertsDir(t *testing.T) {
	ctx := context.Background()
	certsDir := t.TempDir()
	s := newService(&Options{
		certsDir: certsDir,
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) {
			return fake.NewSimpleClientset(), nil
		},
	}, &caOptions{
		opts:             pki.Options{RSABits: pki.DefaultRSABits, Duration: pki.DefaultDuration},
		keyType:          string(pki.DefaultKeyType),
		systemNamespace:  defaultSystemNamespace,
		defaultNamespace: defaultNamespace,
		caSecretName:     defaultCASecretName,
		serverSecretName: defaultServerSecretName,
		serviceName:      defaultServiceName,
		clusterDomain:    "cluster.local",
	})

	if err := s.GenerateCA(ctx, &bytes.Buffer{}); err != nil {
		t.Fatalf("generating CA: %v", err)
	}
	if _, _, err := store.NewFileStore(certsDir).LoadLeaf(ctx, ""); err != nil {
		t.Fatalf("loading server certificate from certs directory: %v", err)
	}
}
