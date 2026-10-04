package translator

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
)

func (d *Docker) ContainerPrune(ctx context.Context, pruneFilters filters.Args) (*container.PruneReport, error) {
	if err := pruneFilters.Validate(map[string]bool{"label": true, "label!": true, "until": true}); err != nil {
		return nil, InvalidArgument(err)
	}
	before, err := pruneBefore(pruneFilters.Get("until"))
	if err != nil {
		return nil, InvalidArgument(err)
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	workloads, err := listWorkloads(ctx, d.k8s, id.Namespace)
	if err != nil {
		return nil, err
	}
	report := &container.PruneReport{ContainersDeleted: []string{}}
	for _, deployment := range workloads {
		if deployment.Annotations[containerConfigAnnotation] == "" || deployment.Started && !deployment.Finished ||
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
		active, err := d.hasActivePod(ctx, deployment)
		if err != nil {
			return report, err
		}
		if active {
			continue
		}
		if err := d.ContainerRm(ctx, identity.DockerIDFromUID(deployment.UID), &backend.ContainerRmConfig{}); err != nil {
			return report, err
		}
		report.ContainersDeleted = append(report.ContainersDeleted, identity.DockerIDFromUID(deployment.UID))
	}
	return report, nil
}
