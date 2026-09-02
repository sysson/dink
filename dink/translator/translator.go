package translator

import (
	"context"
	"errors"
	"fmt"

	"github.com/sysson/dink/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/metrics/pkg/client/clientset/versioned"
)

type KubeClient struct {
	client           *kubernetes.Clientset
	metricsAvailable bool
	metricsClient    *versioned.Clientset
	namespace        string
	defaultEnabled   bool
	restConfig       *rest.Config
}

type Translator struct {
	k8s     *KubeClient
	docker  Docker
	swarm   Swarm
	builder Builder
}

type Docker struct {
	k8s *KubeClient
}

type Swarm struct {
	k8s *KubeClient
}

type Builder struct {
	k8s *KubeClient
}

func New(ctx context.Context, systemNamespace string) (*Translator, error) {
	if systemNamespace == "" {
		systemNamespace = types.DefaultSystemNamespace
	}

	var err error
	var restConfig *rest.Config

	restConfig, err = rest.InClusterConfig()

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
		client:           client,
		metricsClient:    metricsClient,
		namespace:        systemNamespace,
		restConfig:       restConfig,
		metricsAvailable: metricsClient != nil,
		defaultEnabled:   true,
	}

	t := &Translator{
		k8s:     k,
		docker:  Docker{k8s: k},
		swarm:   Swarm{k8s: k},
		builder: Builder{k8s: k},
	}

	err = t.EnsureNamespace(ctx, systemNamespace)
	if err != nil {
		return nil, fmt.Errorf("unable to get dink system namespace %s: %w", systemNamespace, err)
	}

	err = t.EnsureNamespace(ctx, types.DefaultNamespace)
	if err != nil {
		k.defaultEnabled = false
	}

	return &Translator{
		k8s:     k,
		docker:  Docker{k8s: k},
		swarm:   Swarm{k8s: k},
		builder: Builder{k8s: k},
	}, nil
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

var ErrNotImplemented = errors.New("not implemented")

func (t *Translator) EnsureNamespace(ctx context.Context, namespace string) error {
	_, err := t.k8s.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return nil
}
