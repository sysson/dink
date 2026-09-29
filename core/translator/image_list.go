package translator

import (
	"context"
	"fmt"
	"strings"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	deployments, err := r.k8s.AppsV1().Deployments(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	for index := range images {
		for deploymentIndex := range deployments.Items {
			if deploymentUsesImage(&deployments.Items[deploymentIndex], id.Namespace, images[index].RepoDigests) {
				images[index].Containers++
			}
		}
	}
	return images, nil
}

func deploymentUsesImage(deployment *appsv1.Deployment, namespace string, digests []string) bool {
	containers := append(deployment.Spec.Template.Spec.Containers, deployment.Spec.Template.Spec.InitContainers...)
	for _, container := range containers {
		for _, digest := range digests {
			if strings.HasSuffix(container.Image, "/"+namespace+"/"+digest) {
				return true
			}
		}
	}
	return false
}
