package translator

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) ContainerPrune(ctx context.Context, pruneFilters filters.Args) (*container.PruneReport, error) {
	if err := pruneFilters.Validate(map[string]bool{"label": true, "label!": true, "until": true}); err != nil {
		return nil, httpx.BadRequest(err)
	}
	before, err := pruneBefore(pruneFilters.Get("until"))
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, httpx.Unauthorized(fmt.Errorf("missing identity in context"))
	}
	deployments, err := d.k8s.AppsV1().Deployments(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	report := &container.PruneReport{ContainersDeleted: []string{}}
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		if deployment.Annotations[containerConfigAnnotation] == "" || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 ||
			(!before.IsZero() && !deployment.CreationTimestamp.Time.Before(before)) {
			continue
		}
		config, _, err := containerMetadata(deployment)
		if err != nil {
			return report, err
		}
		if !pruneFilters.MatchKVList("label", config.Labels) || !matchExcludedLabels(pruneFilters.Get("label!"), config.Labels) {
			continue
		}
		pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
		if err != nil {
			return report, kubeError(err)
		}
		active := false
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
				active = true
				break
			}
		}
		if active {
			continue
		}
		if err := d.ContainerRm(ctx, deployment.Name, &backend.ContainerRmConfig{}); err != nil {
			return report, err
		}
		report.ContainersDeleted = append(report.ContainersDeleted, identity.DockerIDFromUID(deployment.UID))
	}
	return report, nil
}
