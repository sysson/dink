package k8s

import (
	"context"
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/metrics/pkg/client/clientset/versioned"
	metricsv1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1"
)

type KubeClient struct {
	kubernetes.Interface
	metricsAvailable bool
	metricsv1.MetricsV1Interface
	restConfig *rest.Config
}

func DefaultPath() string {
	if home := os.Getenv("HOME"); home != "" {
		return fmt.Sprintf("%s/.kube/config", home)
	}
	return "/root/.kube/config"
}

func New(ctx context.Context, kubePath string) (*KubeClient, error) {

	var err error
	var restConfig *rest.Config

	if kubePath != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubePath)
	} else {
		restConfig, err = rest.InClusterConfig()
	}

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

	return &KubeClient{
		Interface:          client,
		metricsAvailable:   metricsClient != nil,
		MetricsV1Interface: metricsClient.MetricsV1(),
		restConfig:         restConfig,
	}, nil
}

func (k *KubeClient) MetricsAvailable() bool {
	return k.metricsAvailable
}
