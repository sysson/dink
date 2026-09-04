package command

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sysson/dink/dinkle/namespaces"
	"github.com/sysson/syskit/pki"
	"github.com/sysson/syskit/pki/file"
)

// Service contains the application logic for dinkle operations.
// The CLI layer is intentionally thin and delegates to this service for the
// actual certificate and namespace orchestration.
type Service struct {
	options   *Options
	caOptions *caOptions
}

func newService(o *Options, ca *caOptions) *Service {
	return &Service{options: o, caOptions: ca}
}

// GenerateCA creates the CA and server certificate if they don't already
// exist, reusing the local cache or the cluster's CA Secret before minting a
// new CA.
func (s *Service) GenerateCA(ctx context.Context, out io.Writer) error {
	return s.applyCA(ctx, false, out)
}

// RotateCA always mints a new CA and server certificate, invalidating every
// certificate issued by the previous CA.
func (s *Service) RotateCA(ctx context.Context, out io.Writer) error {
	return s.applyCA(ctx, true, out)
}

func (s *Service) applyCA(ctx context.Context, force bool, out io.Writer) error {
	err := s.caOptions.validate()
	if err != nil {
		return err
	}

	localStore := file.New(s.options.certsDir)

	ca, err := s.loadOrCreateCA(ctx, localStore, force, out)
	if err != nil {
		return err
	}
	if err := localStore.SaveCA(ctx, ca.KeyPair()); err != nil {
		return err
	}

	dnsNames := []string{
		s.caOptions.serviceName,
		fmt.Sprintf("%s.%s", s.caOptions.serviceName, s.caOptions.systemNamespace),
		fmt.Sprintf("%s.%s.svc", s.caOptions.serviceName, s.caOptions.systemNamespace),
		fmt.Sprintf("%s.%s.svc.%s", s.caOptions.serviceName, s.caOptions.systemNamespace, s.caOptions.clusterDomain),
		"localhost",
	}
	dnsNames = append(dnsNames, s.caOptions.extraDNSNames...)

	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ips = append(ips, s.caOptions.validExtraIPs...)

	server, err := ca.Issue(pki.LeafRequest{
		CommonName:  fmt.Sprintf("%s.%s.svc", s.caOptions.serviceName, s.caOptions.systemNamespace),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    dnsNames,
		IPAddresses: ips,
	})
	if err != nil {
		return fmt.Errorf("issuing server certificate: %w", err)
	}
	if err := localStore.SaveLeaf(ctx, "", ca.KeyPair(), server); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "wrote certificates to %s\n", s.options.certsDir)

	if !s.caOptions.apply {
		return nil
	}

	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	nsManager := namespaces.New(kc)
	if err := nsManager.Ensure(ctx, s.caOptions.systemNamespace); err != nil {
		return fmt.Errorf("creating system namespace %q: %w", s.caOptions.systemNamespace, err)
	}
	if err := nsManager.Ensure(ctx, s.caOptions.defaultNamespace); err != nil {
		return fmt.Errorf("creating default namespace %q: %w", s.caOptions.defaultNamespace, err)
	}

	if err := namespaces.NewSecretStore(kc, s.caOptions.systemNamespace, s.caOptions.caSecretName).SaveCA(ctx, ca.KeyPair()); err != nil {
		return err
	}
	if err := namespaces.NewSecretStore(kc, s.caOptions.systemNamespace, "").SaveLeaf(ctx, s.caOptions.serverSecretName, ca.KeyPair(), server); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "done\n")
	return nil
}

// loadOrCreateCA reuses the CA cached in localStore, falling back to the
// cluster's CA Secret if the cache is empty, so that a second host converges
// on the same CA instead of minting a new one. It always mints a new CA when
// force is set.
func (s *Service) loadOrCreateCA(ctx context.Context, localStore *file.FileStore, force bool, out io.Writer) (*pki.Authority, error) {
	if !force {
		pair, err := localStore.LoadCA(ctx)
		switch {
		case err == nil:
			ca, loadErr := pki.LoadAuthority(pair)
			if loadErr != nil {
				return nil, loadErr
			}
			_, _ = fmt.Fprintf(out, "reusing existing CA in %s\n", s.options.certsDir)
			return ca, nil
		case !errors.Is(err, pki.ErrNoAuthority):
			return nil, err
		}

		if kc, kcErr := s.options.kubeClient(ctx); kcErr == nil {
			pair, err := namespaces.NewSecretStore(kc, s.caOptions.systemNamespace, s.caOptions.caSecretName).LoadCA(ctx)
			switch {
			case err == nil:
				ca, loadErr := pki.LoadAuthority(pair)
				if loadErr != nil {
					return nil, fmt.Errorf("loading CA from cluster secret %s/%s: %w", s.caOptions.systemNamespace, s.caOptions.caSecretName, loadErr)
				}
				_, _ = fmt.Fprintf(out, "reusing existing CA from cluster secret %s/%s\n", s.caOptions.systemNamespace, s.caOptions.caSecretName)
				return ca, nil
			case !errors.Is(err, pki.ErrNoAuthority):
				return nil, fmt.Errorf("loading CA from cluster secret %s/%s: %w", s.caOptions.systemNamespace, s.caOptions.caSecretName, err)
			}
		}
	}

	_, _ = fmt.Fprintf(out, "generating a new %s CA\n", s.caOptions.keyType)
	return pki.NewAuthority(
		pki.WithCACommonName(s.caOptions.opts.CACommonName),
		pki.WithKeyType(pki.KeyType(s.caOptions.keyType)),
		pki.WithRSABits(s.caOptions.opts.RSABits),
		pki.WithCADuration(s.caOptions.opts.CADuration),
		pki.WithOrganization(s.caOptions.opts.Organization),
	)
}

// CAInfo prints the CA cached locally and the CA stored in the cluster, and
// whether the two match, so drift between hosts is visible without syncing.
func (s *Service) CAInfo(ctx context.Context, out io.Writer) error {
	localPair, localErr := file.New(s.options.certsDir).LoadCA(ctx)
	localInfo, localOK := printKeyPairInfo(out, "local", s.options.certsDir, localPair, localErr)

	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	location := fmt.Sprintf("%s/%s", s.caOptions.systemNamespace, s.caOptions.caSecretName)
	clusterPair, clusterErr := namespaces.NewSecretStore(kc, s.caOptions.systemNamespace, s.caOptions.caSecretName).LoadCA(ctx)
	clusterInfo, clusterOK := printKeyPairInfo(out, "cluster", location, clusterPair, clusterErr)

	if localOK && clusterOK {
		if localInfo.Fingerprint == clusterInfo.Fingerprint {
			_, _ = fmt.Fprintln(out, "local and cluster CA match")
		} else {
			_, _ = fmt.Fprintln(out, "WARNING: local and cluster CA differ; run `dinkle ca sync` to reconcile")
		}
	}
	return nil
}

// SyncCA reconciles the local CA cache with the cluster's CA Secret. By
// default it pulls the cluster's CA into the local cache; push uploads the
// local CA to the cluster instead.
func (s *Service) SyncCA(ctx context.Context, systemNamespace, caSecretName string, push bool, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	localStore := file.New(s.options.certsDir)
	clusterStore := namespaces.NewSecretStore(kc, systemNamespace, caSecretName)

	if push {
		pair, err := localStore.LoadCA(ctx)
		if err != nil {
			return fmt.Errorf("no local CA found in %s; run `dinkle ca generate` first: %w", s.options.certsDir, err)
		}
		if err := clusterStore.SaveCA(ctx, pair); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "pushed local CA to cluster secret %s/%s\n", systemNamespace, caSecretName)
		return nil
	}

	pair, err := clusterStore.LoadCA(ctx)
	if err != nil {
		return fmt.Errorf("no CA found in cluster secret %s/%s; run `dinkle ca generate` first: %w", systemNamespace, caSecretName, err)
	}
	if err := localStore.SaveCA(ctx, pair); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "pulled CA from cluster secret %s/%s into %s\n", systemNamespace, caSecretName, s.options.certsDir)
	return nil
}

// printKeyPairInfo prints a KeyPair's certificate summary (or why it
// couldn't be loaded/parsed) and reports whether info is usable for a
// fingerprint comparison.
func printKeyPairInfo(out io.Writer, source, location string, pair pki.KeyPair, err error) (pki.Info, bool) {
	switch {
	case errors.Is(err, pki.ErrNoAuthority), errors.Is(err, pki.ErrNoLeaf):
		_, _ = fmt.Fprintf(out, "%s (%s): not found\n", source, location)
		return pki.Info{}, false
	case err != nil:
		_, _ = fmt.Fprintf(out, "%s (%s): error: %v\n", source, location, err)
		return pki.Info{}, false
	}

	info, err := pki.ParseCertInfo(pair.Cert)
	if err != nil {
		_, _ = fmt.Fprintf(out, "%s (%s): error: %v\n", source, location, err)
		return pki.Info{}, false
	}
	_, _ = fmt.Fprintf(out, "%s (%s): CN=%s serial=%s expires=%s fingerprint=%s\n",
		source, location, info.CommonName, info.SerialNumber, info.NotAfter.Format(time.RFC3339), info.Fingerprint)
	return info, true
}

func (s *Service) CreateTenant(ctx context.Context, name, defaultClient string, out io.Writer) error {
	a, err := file.New(s.options.certsDir).LoadCA(ctx)
	if err != nil {
		return err
	}

	ca, err := pki.LoadAuthority(a)
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

	return issueClientCert(ctx, kc, ca, name, defaultClient, s.options.certsDir, out)
}

func (s *Service) CreateClient(ctx context.Context, namespace, clientName string, out io.Writer) error {
	a, err := file.New(s.options.certsDir).LoadCA(ctx)
	if err != nil {
		return err
	}

	ca, err := pki.LoadAuthority(a)
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

	return issueClientCert(ctx, kc, ca, namespace, clientName, s.options.certsDir, out)
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
	if err := os.RemoveAll(tenantDir(s.options.certsDir, name)); err != nil {
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
	if _, err := namespaces.New(kc).Get(ctx, namespace); err != nil {
		return fmt.Errorf("tenant namespace %q does not exist; create it with `dinkle tenant create %s` first", namespace, namespace)
	}
	if err := namespaces.NewSecretStore(kc, namespace, "").DeleteLeaf(ctx, clientSecretName(clientName)); err != nil {
		return err
	}
	if err := clientStore(s.options.certsDir, namespace).DeleteLeaf(ctx, clientName); err != nil {
		return fmt.Errorf("removing cached certificate for %s/%s: %w", namespace, clientName, err)
	}
	_, _ = fmt.Fprintf(out, "deleted client %s/%s\n", namespace, clientName)
	return nil
}

// TenantInfo prints a tenant namespace's status and its local and cluster
// clients, so drift between hosts is visible without syncing.
func (s *Service) TenantInfo(ctx context.Context, name string, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}
	nsManager := namespaces.New(kc)

	ns, err := nsManager.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("tenant %q not found: %w", name, err)
	}
	_, _ = fmt.Fprintf(out, "tenant: %s\n", name)
	_, _ = fmt.Fprintf(out, "namespace created: %s\n", ns.CreationTimestamp.Format(time.RFC3339))

	hasDeployments, err := nsManager.HasDeployments(ctx, name)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "has deployments: %t\n", hasDeployments)

	clusterClients, err := namespaces.NewSecretStore(kc, name, "").ListLeaves(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "cluster clients (%d): %s\n", len(clusterClients), strings.Join(clusterClients, ", "))

	localClients, err := clientStore(s.options.certsDir, name).ListLeaves(ctx)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "local clients (%d): %s\n", len(localClients), strings.Join(localClients, ", "))
	return nil
}

// ClientInfo prints a client certificate's local and cluster state, and
// whether the two match.
func (s *Service) ClientInfo(ctx context.Context, namespace, clientName string, out io.Writer) error {
	kc, err := s.options.kubeClient(ctx)
	if err != nil {
		return err
	}

	secretName := clientSecretName(clientName)
	location := fmt.Sprintf("%s/%s", namespace, secretName)
	_, clusterLeaf, clusterErr := namespaces.NewSecretStore(kc, namespace, "").LoadLeaf(ctx, secretName)
	clusterInfo, clusterOK := printKeyPairInfo(out, "cluster", location, clusterLeaf, clusterErr)

	clientDir := clientDir(s.options.certsDir, namespace, clientName)
	_, localLeaf, localErr := clientStore(s.options.certsDir, namespace).LoadLeaf(ctx, clientName)
	localInfo, localOK := printKeyPairInfo(out, "local", clientDir, localLeaf, localErr)

	if localOK && clusterOK {
		if localInfo.Fingerprint == clusterInfo.Fingerprint {
			_, _ = fmt.Fprintln(out, "local and cluster certificate match")
		} else {
			_, _ = fmt.Fprintln(out, "WARNING: local and cluster certificate differ")
		}
	}
	return nil
}
