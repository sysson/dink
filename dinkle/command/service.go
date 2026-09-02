package command

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/dink/dinkle/store"
	"github.com/sysson/dink/pkg/certs"
)

// Service contains the application logic for dinkle operations.
// The CLI layer is intentionally thin and delegates to this service for the
// actual certificate and namespace orchestration.
type Service struct {
	options *Options
}

func newService(o *Options) *Service {
	return &Service{options: o}
}

func (s *Service) Bootstrap(ctx context.Context, b *bootstrapOptions, out io.Writer) error {
	opts, err := b.certOptions()
	if err != nil {
		return err
	}

	ca, err := b.authority(opts, out)
	if err != nil {
		return err
	}
	if err := certs.SaveCA(s.options.certsDir, ca.KeyPair()); err != nil {
		return err
	}

	server, err := ca.IssueServer()
	if err != nil {
		return fmt.Errorf("issuing server certificate: %w", err)
	}
	if err := certs.SaveServer(s.options.certsDir, server); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote certificates to %s\n", s.options.certsDir)

	if !b.apply {
		return nil
	}

	client, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(client)
	if err := nsManager.Create(ctx, b.systemNamespace, false); err != nil {
		if !b.force {
			return fmt.Errorf("creating system namespace %q: %w", b.systemNamespace, err)
		}
	}
	if err := nsManager.Create(ctx, b.defaultNamespace, false); err != nil {
		if !b.force {
			return fmt.Errorf("creating default namespace %q: %w", b.defaultNamespace, err)
		}
	}

	if err := nsManager.StoreServerSecret(ctx, b.systemNamespace, b.secretName, map[string][]byte{
		"tls.crt": server.Cert,
		"tls.key": server.Key,
		"ca.crt":  ca.KeyPair().Cert,
	}); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "done\n")
	return nil
}

func (s *Service) CreateTenant(ctx context.Context, name string, out io.Writer) error {
	ca, err := certs.LoadAuthorityDir(s.options.certsDir, certs.Options{
		Namespace: name,
	})
	if err != nil {
		return err
	}

	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(kc)
	if err := nsManager.Create(ctx, name, true); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "created tenant namespace %s\n", name)

	return issueClientCert(ctx, nsManager, ca, name, defaultTenantClient, s.options.certsDir, out)
}

func (s *Service) CreateClient(ctx context.Context, namespace, clientName string, out io.Writer) error {
	ca, err := certs.LoadAuthorityDir(s.options.certsDir, certs.Options{
		Namespace: namespace,
		ClientCN:  clientName,
	})
	if err != nil {
		return err
	}

	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(kc)
	if _, err := nsManager.Get(ctx, namespace); err != nil {
		return fmt.Errorf("tenant namespace %q does not exist; create it with `dinkle tenant create %s` first", namespace, namespace)
	}

	return issueClientCert(ctx, nsManager, ca, namespace, clientName, s.options.certsDir, out)
}

func (s *Service) ListTenants(ctx context.Context, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	names, err := namespaces.New(kc).ListTenants(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		_, _ = fmt.Fprintln(out, name)
	}
	return nil
}

func (s *Service) DeleteTenant(ctx context.Context, name string, force bool, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(kc)
	if !force {
		has, err := nsManager.HasDeployments(ctx, name)
		if err != nil {
			return err
		}
		if has {
			return fmt.Errorf("tenant %q has running deployments; use --force to delete anyway", name)
		}
	}

	if err := nsManager.Delete(ctx, name); err != nil {
		return err
	}
	if err := os.RemoveAll(store.TenantDir(s.options.certsDir, name)); err != nil {
		return fmt.Errorf("removing cached certificates for %q: %w", name, err)
	}
	_, _ = fmt.Fprintf(out, "deleted tenant %s\n", name)
	return nil
}

func (s *Service) DeleteClient(ctx context.Context, namespace, clientName string, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	nsManager := namespaces.New(kc)
	secretName := clientSecretName(clientName)
	if err := nsManager.DeleteSecret(ctx, namespace, secretName); err != nil {
		return err
	}
	if err := os.RemoveAll(store.ClientDir(s.options.certsDir, namespace, clientName)); err != nil {
		return fmt.Errorf("removing cached certificate for %s/%s: %w", namespace, clientName, err)
	}
	_, _ = fmt.Fprintf(out, "deleted client %s/%s\n", namespace, clientName)
	return nil
}
