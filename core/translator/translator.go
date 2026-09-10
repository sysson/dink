package translator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/syskit/logx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Translator struct {
	k8s      *k8s.KubeClient
	docker   Docker
	swarm    Swarm
	builder  Builder
	registry Registry
}

type Docker struct {
	k8s *k8s.KubeClient
}

type Swarm struct {
	k8s *k8s.KubeClient
}

type Builder struct {
	k8s *k8s.KubeClient
}

type Registry struct {
	registry *registry.RegistryService
	k8s      *k8s.KubeClient
}

func New(ctx context.Context, cfg *config.Config) (*Translator, error) {

	k, err := k8s.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	r := newRegistryService(cfg)

	t := &Translator{
		k8s:      k,
		docker:   Docker{k8s: k},
		swarm:    Swarm{k8s: k},
		builder:  Builder{k8s: k},
		registry: Registry{registry: r, k8s: k},
	}

	err = t.EnsureNamespace(ctx, cfg.Kubernetes.SystemNamespace)
	if err != nil {
		return nil, fmt.Errorf("unable to get dink system namespace %s: %w", cfg.Kubernetes.SystemNamespace, err)
	}

	err = t.EnsureNamespace(ctx, cfg.Kubernetes.DefaultNamespace)
	if err != nil {
		logx.G(ctx).WithError(err).Warn("default namespace not available", "namespace", cfg.Kubernetes.DefaultNamespace)
	}

	return t, nil
}

func (t *Translator) Docker() *Docker {
	return &t.docker
}

func (t *Translator) Swarm() *Swarm {
	return &t.swarm
}

func (t *Translator) Builder() *Builder {
	return &t.builder
}

func (t *Translator) Registry() *Registry {
	return &t.registry
}

var ErrNotImplemented = errors.New("not implemented")

func (t *Translator) EnsureNamespace(ctx context.Context, namespace string) error {
	_, err := t.k8s.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return nil
}

func newRegistryService(cfg *config.Config) *registry.RegistryService {
	transport, err := newRegistryTransport(cfg)
	if err != nil {
		return registry.Unavailable(fmt.Errorf("registry transport for %q: %w", cfg.Registry.URL, err))
	}
	internal, err := registry.NewClient(cfg.Registry.URL, registry.ClientOptions{Transport: transport})
	if err != nil {
		return registry.Unavailable(fmt.Errorf("registry client for %q: %w", cfg.Registry.URL, err))
	}
	return registry.New(internal)
}

func newRegistryTransport(cfg *config.Config) (http.RoundTripper, error) {
	if strings.HasPrefix(cfg.Registry.URL, "http://") {
		return nil, nil
	}

	caFile := cfg.Registry.CAFile
	if caFile == "" {
		caFile = cfg.TLS.ClientCAFile
	}
	if caFile == "" {
		return nil, nil
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	transport := registry.RegistryTransport(tlsConfig, nil)
	return transport, nil
}
