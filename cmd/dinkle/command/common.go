package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sysson/dink/cmd/dinkle/store"
	"github.com/sysson/dink/pkg/k8s"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultConfigDirName    = "dink"
	defaultCertsDirName     = ".certs"
	defaultOrganization     = "dink"
	defaultCACommonName     = "dink-ca"
	defaultClientCN         = "dink-client"
	defaultServiceName      = "dink"
	defaultSystemNamespace  = "dink-system"
	defaultNamespace        = "dink"
	defaultCASecretName     = "dink-ca"
	defaultServerSecretName = "dink-tls"
)

var (
	ErrNamespaceRequired  = errors.New("namespace is required")
	ErrClientNameRequired = errors.New("client name is required")
	ErrTenantNameRequired = errors.New("tenant name is required")
)

// Options holds the flags shared by every dinkle subcommand.
type Options struct {
	kubeConfig string
	certsDir   string

	once          sync.Once
	client        kubernetes.Interface
	clientErr     error
	newKubeClient func(context.Context, string) (kubernetes.Interface, error)
}

func (o *Options) kubeClient(ctx context.Context) (kubernetes.Interface, error) {
	if o.newKubeClient == nil {
		o.newKubeClient = func(ctx context.Context, kubeconfig string) (kubernetes.Interface, error) {
			return k8s.New(ctx, kubeconfig)
		}
	}

	o.once.Do(func() {
		o.client, o.clientErr = o.newKubeClient(ctx, o.kubeConfig)
	})
	if o.clientErr != nil {
		return nil, o.clientErr
	}
	if o.client == nil {
		return nil, fmt.Errorf("kube client factory returned nil client without error")
	}
	return o.client, nil
}

func clientSecretName(clientName string) string {
	return "dink-client-" + clientName
}

func requireString(value string, missingErr error) error {
	if value == "" {
		return missingErr
	}
	return nil
}

func defaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", defaultConfigDirName, defaultCertsDirName), nil
}

func tenantDir(certsDir, namespace string) string {
	return filepath.Join(certsDir, namespace)
}

func clientStore(certsDir, namespace string) *store.FileStore {
	store := store.NewFileStore(tenantDir(certsDir, namespace))
	store.LeafCACertFile = "ca.pem"
	store.LeafCertFile = "cert.pem"
	store.LeafKeyFile = "key.pem"
	return store
}

// clientDir returns the directory holding a single client's certificate,
// laid out so it can be used directly as DOCKER_CERT_PATH.
func clientDir(certsDir, namespace, client string) string {
	return filepath.Join(tenantDir(certsDir, namespace), client)
}
