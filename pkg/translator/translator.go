package translator

import "github.com/sysson/dink/pkg/k8s"

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

func New(k *k8s.KubeClient) *Translator {
	return &Translator{
		k8s: k,
		docker: Docker{
			k8s: k,
		},
		swarm: Swarm{
			k8s: k,
		},
		builder: Builder{
			k8s: k,
		},
	}
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
