package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sysson/dink/internal/config"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/metrics/pkg/client/clientset/versioned"
)

type KubeClient struct {
	client             *kubernetes.Clientset
	metricsAvailable   bool
	metricsClient      *versioned.Clientset
	namespace          string
	serviceAccountName string
	restConfig         *rest.Config
}

func New(ctx context.Context, cfg *config.Config) (*KubeClient, error) {
	if cfg == nil {
		cfg = config.NewConfig()
	}

	restConfig, err := loadConfig(cfg.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to build Kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	metricsClient, err := versioned.NewForConfig(restConfig)
	if err != nil {
		metricsClient = nil
	}

	k := &KubeClient{
		client:             client,
		restConfig:         restConfig,
		namespace:          cfg.Namespace,
		serviceAccountName: defaultControlServiceAccount,
		metricsAvailable:   metricsClient != nil,
		metricsClient:      metricsClient,
	}

	if err := k.ensureControlPlane(ctx); err != nil {
		return nil, fmt.Errorf("unable to ensure control plane: %w", err)
	}

	return k, nil
}

func loadConfig(kubeConfigPath string) (*rest.Config, error) {

	if kubeConfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}

	home, exists := os.LookupEnv("HOME")
	if !exists {
		home = "/root"
	}

	configPath := filepath.Join(home, ".kube", "config")
	return clientcmd.BuildConfigFromFlags("", configPath)
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
