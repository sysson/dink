package k8s

import (
	"context"
	"fmt"

	"github.com/sysson/dink/internal/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/metrics/pkg/client/clientset/versioned"
)

type KubeClient struct {
	client        kubernetes.Interface
	metricsClient *versioned.Clientset
	namespace     string
	apiHost       string
	restConfig    *rest.Config
}

func New(ctx context.Context, cfg *config.Config) (*KubeClient, error) {
	if cfg == nil {
		cfg = config.NewConfig()
	}
	
	restConfig, err := clientcmd.BuildConfigFromFlags("", cfg.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to build Kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	if _, err := client.CoreV1().Namespaces().Get(ctx, cfg.DefaultNamespace, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("unable to reach namespace %q in cluster: %w", cfg.DefaultNamespace, err)
	}

	metricsClient, err := versioned.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes metrics client: %w", err)
	}

	return &KubeClient{
		client:        client,
		metricsClient: metricsClient,
		namespace:     cfg.DefaultNamespace,
		apiHost:       restConfig.Host,
		restConfig:    restConfig,
	}, nil
}

func (kc *KubeClient) Client() kubernetes.Interface {
	return kc.client
}

func (kc *KubeClient) MetricsClient() *versioned.Clientset {
	return kc.metricsClient
}

func (kc *KubeClient) Namespace() string {
	return kc.namespace
}

func (kc *KubeClient) APIHost() string {
	return kc.apiHost
}

func (kc *KubeClient) RestConfig() *rest.Config {
	return kc.restConfig
}
