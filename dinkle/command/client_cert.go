package command

import (
	"context"
	"fmt"
	"io"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/dink/dinkle/store"
	"github.com/sysson/dink/pkg/certs"
)

// issueClientCert issues a client certificate scoped to namespace, caches it
// under g.certsDir/namespace/clientName laid out for DOCKER_CERT_PATH, and
// applies it to the cluster as a Secret so workloads in namespace can use it.
func issueClientCert(ctx context.Context, ns *namespaces.Manager, ca *certs.Authority, namespace, clientName, certDir string, out io.Writer) error {
	leaf, err := ca.IssueTenantClient(namespace, clientName)
	if err != nil {
		return fmt.Errorf("issuing client certificate for %s/%s: %w", namespace, clientName, err)
	}

	dir := store.ClientDir(certDir, namespace, clientName)
	if err := certs.SaveClientDir(dir, ca.KeyPair(), leaf); err != nil {
		return err
	}

	err = ns.StoreClientSecret(ctx, namespace, clientName, map[string][]byte{
		"ca.crt":  ca.KeyPair().Cert,
		"tls.crt": leaf.Cert,
		"tls.key": leaf.Key,
	})
	if err != nil {
		return fmt.Errorf("storing client secret for %s/%s: %w", namespace, clientName, err)
	}
	_, _ = fmt.Fprintf(out, "issued client certificate for %s/%s\nAt %s\n", namespace, clientName, dir)
	return nil
}
