package command

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/syskit/pki"
	"k8s.io/client-go/kubernetes"
)

// issueClientCert issues a client certificate scoped to namespace, caches it
// under certDir/namespace/clientName laid out for DOCKER_CERT_PATH, and
// applies it to the cluster as a Secret so workloads in namespace can use it.
func issueClientCert(ctx context.Context, kc kubernetes.Interface, ca *pki.Authority, namespace, clientName, certDir string, out io.Writer) error {
	if namespace == "" {
		return fmt.Errorf("tenant namespace is required")
	}
	if clientName == "" {
		return fmt.Errorf("client name is required")
	}
	leaf, err := ca.Issue(
		pki.WithCommonName(clientName),
		pki.WithOrganization(namespace),
		pki.WithExtKeyUsage([]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}),
	)
	if err != nil {
		return fmt.Errorf("issuing client certificate for %s/%s: %w", namespace, clientName, err)
	}

	localStore := clientStore(certDir, namespace)
	if err := localStore.SaveLeaf(ctx, clientName, ca.KeyPair(), leaf); err != nil {
		return err
	}

	clusterStore := namespaces.NewSecretStore(kc, namespace, "")
	if err := clusterStore.SaveLeaf(ctx, clientSecretName(clientName), ca.KeyPair(), leaf); err != nil {
		return fmt.Errorf("storing client secret for %s/%s: %w", namespace, clientName, err)
	}
	_, _ = fmt.Fprintf(out, "issued client certificate for %s/%s\nAt %s\n", namespace, clientName, tenantDir(certDir, namespace))
	return nil
}
