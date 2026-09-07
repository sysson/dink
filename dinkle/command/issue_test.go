package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sysson/syskit/pki"
	"k8s.io/client-go/kubernetes/fake"
)

func TestIssueClientCertWritesDockerBundle(t *testing.T) {
	ctx := context.Background()
	certDir := t.TempDir()
	ca, err := pki.NewAuthority()
	if err != nil {
		t.Fatalf("creating CA: %v", err)
	}

	if err := issueClientCert(ctx, fake.NewSimpleClientset(), ca, "tenant", "client", certDir, &bytes.Buffer{}); err != nil {
		t.Fatalf("issuing client certificate: %v", err)
	}
	for _, name := range []string{"ca.pem", "cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(clientDir(certDir, "tenant", "client"), name)); err != nil {
			t.Errorf("Docker bundle file %q: %v", name, err)
		}
	}
}
