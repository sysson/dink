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
	"sync"
	"time"

	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/k8s"
	registryapi "github.com/sysson/dink/core/registry/api"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/syskit/logx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dockerRegistry is what Docker needs from dinki.
type dockerRegistry interface {
	Authenticate(ctx context.Context, auth types.RegistryAuth) (string, error)
	ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error)
	IssuePullCredential(ctx context.Context) (string, string, error)
}

type Translator struct {
	k8s      *k8s.KubeClient
	docker   Docker
	swarm    Swarm
	builder  Builder
	registry Registry
}

type Docker struct {
	k8s      *k8s.KubeClient
	registry dockerRegistry
	// pullHost is where nodes pull tenant images from.
	pullHost     string
	pullSecretMu sync.Mutex
}

type Swarm struct {
	k8s *k8s.KubeClient
}

type Builder struct {
	k8s *k8s.KubeClient
}

type Registry struct {
	registry *registryapi.Client
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
		docker:   Docker{k8s: k, registry: r, pullHost: cfg.Registry.PullHost},
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

func newRegistryService(cfg *config.Config) *registryapi.Client {
	httpClient, err := newRegistryHTTPClient(cfg)
	if err != nil {
		return registryapi.Unavailable(fmt.Errorf("registry API client for %q: %w", cfg.Registry.URL, err))
	}
	return registryapi.NewClient(httpClient, strings.TrimSuffix(cfg.Registry.URL, "/"))
}

// newRegistryHTTPClient returns the HTTP client for dinki's internal API. Over
// https it verifies dinki against Registry.CAFile (default TLS.ClientCAFile)
// and presents Registry.CertFile/KeyFile (default dink's own TLS key pair) as
// its client certificate.
func newRegistryHTTPClient(cfg *config.Config) (*http.Client, error) {
	if strings.HasPrefix(cfg.Registry.URL, "http://") {
		return &http.Client{Transport: &http.Transport{
			Proxy:     http.ProxyFromEnvironment,
			Protocols: plaintextHTTP2(),
		}}, nil
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	caFile := cfg.Registry.CAFile
	if caFile == "" {
		caFile = cfg.TLS.ClientCAFile
	}
	if caFile != "" {
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
		tlsConfig.RootCAs = roots
	}
	certFile, keyFile := cfg.Registry.CertFile, cfg.Registry.KeyFile
	if certFile == "" && keyFile == "" {
		certFile, keyFile = cfg.TLS.CertFile, cfg.TLS.KeyFile
	}
	if certFile != "" || keyFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading registry API client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
	}}, nil
}

func plaintextHTTP2() *http.Protocols {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return protocols
}
