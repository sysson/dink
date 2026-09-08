package translator

import (
	"context"
	"errors"
	"fmt"

	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/syskit/logx"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Translator struct {
	k8s     *k8s.KubeClient
	docker  Docker
	swarm   Swarm
	builder Builder
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

func New(ctx context.Context, systemNamespace string) (*Translator, error) {
	if systemNamespace == "" {
		systemNamespace = types.DefaultSystemNamespace
	}
	k, err := k8s.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("unable to create Kubernetes client: %w", err)
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
		logx.G(ctx).WithError(err).Warn("default namespace not available", "namespace", types.DefaultNamespace)
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

var ErrNotImplemented = errors.New("not implemented")

func (t *Translator) EnsureNamespace(ctx context.Context, namespace string) error {
	_, err := t.k8s.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return nil
}
