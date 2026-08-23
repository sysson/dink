package translator

import "github.com/sysson/dink/pkg/k8s"

type Translator struct {
	k8s     *k8s.KubeClient
	docker  Docker
	cluster Cluster
	builder Builder
}

type Docker struct {
	k8s *k8s.KubeClient
}

type Cluster struct {
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
		cluster: Cluster{
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

func (t *Translator) Cluster() *Cluster {
	return &t.cluster
}

func (t *Translator) Builder() *Builder {
	return &t.builder
}
