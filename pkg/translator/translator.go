package translator

import "github.com/sysson/dink/pkg/k8s"

type Translator struct {
	k8s *k8s.KubeClient
}

func New(k *k8s.KubeClient) *Translator {
	return &Translator{
		k8s: k,
	}
}
