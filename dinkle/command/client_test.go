package command

import (
	"bytes"
	"context"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/syskit/pki"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSyncClientRestoresClusterLeaf(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	ca, err := pki.NewAuthority()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}
	leaf, err := ca.Issue(
		pki.WithCommonName("default"),
		pki.WithOrganization("dev"),
		pki.WithExtKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}),
	)
	if err != nil {
		t.Fatalf("issuing client certificate: %v", err)
	}
	if err := namespaces.NewSecretStore(client, "dev", "").SaveLeaf(ctx, clientSecretName("default"), ca.KeyPair(), leaf); err != nil {
		t.Fatalf("saving cluster leaf: %v", err)
	}

	certsDir := t.TempDir()
	s := newService(&Options{
		certsDir: certsDir,
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) {
			return client, nil
		},
	}, &caOptions{})

	if err := s.SyncClient(ctx, "dev", "default", &bytes.Buffer{}); err != nil {
		t.Fatalf("syncing client: %v", err)
	}

	restoredCA, restoredLeaf, err := clientStore(certsDir, "dev").LoadLeaf(ctx, "default")
	if err != nil {
		t.Fatalf("loading restored client certificate: %v", err)
	}
	if !bytes.Equal(restoredLeaf.Cert, leaf.Cert) || !bytes.Equal(restoredLeaf.Key, leaf.Key) {
		t.Fatal("expected restored leaf to match the cluster leaf")
	}
	if !bytes.Equal(restoredCA.Cert, ca.KeyPair().Cert) {
		t.Fatal("expected restored CA certificate to match the issuing CA")
	}
}

func TestSyncClientWithoutClusterLeafFails(t *testing.T) {
	ctx := context.Background()
	s := newService(&Options{
		certsDir: t.TempDir(),
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) {
			return fake.NewSimpleClientset(), nil
		},
	}, &caOptions{})

	err := s.SyncClient(ctx, "dev", "default", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "dinkle client create dev default") {
		t.Fatalf("expected a helpful error, got %v", err)
	}
}
