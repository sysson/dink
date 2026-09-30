package translator

import (
	"context"
	"fmt"
	"strings"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
)

func (r *Registry) Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	images, err := r.registry.Images(ctx, options)
	if err != nil {
		return nil, err
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	workloads, err := listWorkloads(ctx, r.k8s, id.Namespace)
	if err != nil {
		return nil, err
	}
	for index := range images {
		for _, workload := range workloads {
			if workloadUsesImage(workload, id.Namespace, images[index].RepoDigests) {
				images[index].Containers++
			}
		}
	}
	return images, nil
}

func workloadUsesImage(workload *containerWorkload, namespace string, digests []string) bool {
	containers := append(workload.Template.Spec.Containers, workload.Template.Spec.InitContainers...)
	for _, container := range containers {
		for _, digest := range digests {
			if strings.HasSuffix(container.Image, "/"+namespace+"/"+digest) {
				return true
			}
		}
	}
	return false
}
