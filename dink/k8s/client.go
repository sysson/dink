package k8s

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/metrics/pkg/client/clientset/versioned"
)

const (
	defaultNamespace = "dink"
)

type Options struct {
	Namespace      string
	KubeConfigPath string
}

type KubeClient struct {
	client             *kubernetes.Clientset
	metricsAvailable   bool
	metricsClient      *versioned.Clientset
	namespace          string
	serviceAccountName string
	restConfig         *rest.Config
}

func New(ctx context.Context, opts *Options) (*KubeClient, error) {
	if opts == nil {
		opts = &Options{}
	}
	if opts.Namespace == "" {
		opts.Namespace = defaultNamespace
	}

	// An empty path makes BuildConfigFromFlags prefer the in-cluster config and
	// fall back to KUBECONFIG/~/.kube/config, so it must not be defaulted here.
	restConfig, err := clientcmd.BuildConfigFromFlags("", opts.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to build Kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	if _, err := client.Discovery().ServerVersion(); err != nil {
		return nil, fmt.Errorf("unable to reach Kubernetes API server: %w", err)
	}

	metricsClient, err := versioned.NewForConfig(restConfig)
	if err != nil {
		metricsClient = nil
	}

	k := &KubeClient{
		client:             client,
		restConfig:         restConfig,
		namespace:          opts.Namespace,
		serviceAccountName: defaultControlServiceAccount,
		metricsAvailable:   metricsClient != nil,
		metricsClient:      metricsClient,
	}

	if err := k.ensureControlPlane(ctx); err != nil {
		return nil, fmt.Errorf("unable to ensure control plane: %w", err)
	}

	return k, nil
}

func (kc *KubeClient) Client() *kubernetes.Clientset {
	return kc.client
}

func (kc *KubeClient) MetricsClient() *versioned.Clientset {
	return kc.metricsClient
}

func (kc *KubeClient) Namespace() string {
	return kc.namespace
}

func (kc *KubeClient) RestConfig() *rest.Config {
	return kc.restConfig
}

func (kc *KubeClient) MetricsAvailable() bool {
	return kc.metricsAvailable
}

func (kc *KubeClient) SystemNamespace() string {
	return kc.namespace + "-" + defaultControlNamespace
}

func (kc *KubeClient) SystemServiceAccount() string {
	return kc.serviceAccountName
}
