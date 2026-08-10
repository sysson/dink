package k8s

import (
	"context"
	"fmt"
	"log/slog"

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
	logger        *slog.Logger
}

type Options struct {
	Config *config.Config
	Logger *slog.Logger
}

func New(ctx context.Context, opt *Options) (*KubeClient, error) {
	if opt.Config == nil {
		opt.Config = config.NewConfig()
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	restConfig, err := clientcmd.BuildConfigFromFlags("", opt.Config.KubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("unable to build Kubernetes config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
	}

	if _, err := client.CoreV1().Namespaces().Get(ctx, opt.Config.DefaultNamespace, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("unable to reach namespace %q in cluster: %w", opt.Config.DefaultNamespace, err)
	}

	metricsClient, err := versioned.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes metrics client: %w", err)
	}

	return &KubeClient{
		client:        client,
		metricsClient: metricsClient,
		namespace:     opt.Config.DefaultNamespace,
		apiHost:       restConfig.Host,
		restConfig:    restConfig,
		logger:        opt.Logger,
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

func (kc *KubeClient) Logger() *slog.Logger {
	return kc.logger
}
