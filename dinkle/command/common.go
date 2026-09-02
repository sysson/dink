package command

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/sysson/dink/pkg/k8s"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrNamespaceRequired      = errors.New("namespace is required")
	ErrClientNameRequired     = errors.New("client name is required")
	ErrTenantNameRequired     = errors.New("tenant name is required")
	ErrNamespaceClientMissing = errors.New("namespace and client name are required")
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
