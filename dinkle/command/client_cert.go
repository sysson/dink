package command

import (
	"context"
	"fmt"
	"io"

	dinklecerts "github.com/sysson/dink/dinkle/certs"
	"github.com/sysson/dink/dinkle/namespaces"
	pkgcerts "github.com/sysson/dink/pkg/certs"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// issueClientCert issues a client certificate scoped to namespace, caches it
// under g.certsDir/namespace/clientName laid out for DOCKER_CERT_PATH, and
// applies it to the cluster as a Secret so workloads in namespace can use it.
func issueClientCert(ctx context.Context, g *globalOptions, kc kubernetes.Interface, ca *pkgcerts.Authority, namespace, clientName string, out io.Writer) error {
	leaf, err := ca.IssueTenantClient(namespace, clientName)
	if err != nil {
		return fmt.Errorf("issuing client certificate for %s/%s: %w", namespace, clientName, err)
	}

	dir := dinklecerts.ClientDir(g.certsDir, namespace, clientName)
	if err := pkgcerts.SaveClientDir(dir, ca.KeyPair(), leaf); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote client certificate to %s\n", dir)

	secretName := clientSecretName(clientName)
	secret := &corev1.Secret{
			Name:      secretName,
			Namespace: namespace,
			Labels: map[string]string{
				namespaces.LabelManagedBy: namespaces.ManagedByValue,
				"dink.io/client":          clientName,
			},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ca.crt":  ca.KeyPair().Cert,
			"tls.crt": leaf.Cert,
			"tls.key": leaf.Key,
		},
	}

	secrets := kc.CoreV1().Secrets(namespace)
	_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{FieldManager: certFieldManager})
	if apierrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, secret, metav1.CreateOptions{FieldManager: certFieldManager})
	}
	if err != nil {
		return fmt.Errorf("applying secret %s/%s: %w", namespace, secretName, err)
	}
	_, _ = fmt.Fprintf(out, "applied secret %s/%s\n", namespace, secretName)
	return nil
}
