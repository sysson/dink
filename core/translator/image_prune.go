package translator

import (
	"context"
	"time"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
)

func (r *Registry) ImagePrune(ctx context.Context, pruneFilters filters.Args) (*imagetypes.PruneReport, error) {
	if err := pruneFilters.Validate(map[string]bool{"dangling": true, "label": true, "label!": true, "until": true}); err != nil {
		return nil, InvalidArgument(err)
	}
	danglingOnly, err := pruneFilters.GetBoolOrDefault("dangling", true)
	if err != nil {
		return nil, InvalidArgument(err)
	}
	before, err := pruneBefore(pruneFilters.Get("until"))
	if err != nil {
		return nil, InvalidArgument(err)
	}
	report := &imagetypes.PruneReport{ImagesDeleted: []imagetypes.DeleteResponse{}}
	if danglingOnly {
		return report, nil
	}
	images, err := r.Images(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	for _, image := range images {
		if imageContainerCount(images, image.ID) > 0 || len(image.RepoTags) == 0 ||
			(!before.IsZero() && (image.Created == 0 || !time.Unix(image.Created, 0).Before(before))) {
			continue
		}
		if len(pruneFilters.Get("label")) > 0 || len(pruneFilters.Get("label!")) > 0 {
			inspect, err := r.registry.ImageInspect(ctx, image.RepoTags[0], imagebackend.ImageInspectOpts{})
			if err != nil {
				return report, err
			}
			var labels map[string]string
			if inspect.Config != nil {
				labels = inspect.Config.Labels
			}
			if !pruneFilters.MatchKVList("label", labels) ||
				(len(pruneFilters.Get("label!")) > 0 && pruneFilters.MatchKVList("label!", labels)) {
				continue
			}
		}
		for _, tag := range image.RepoTags {
			deleted, err := r.ImageDelete(ctx, tag, imagebackend.RemoveOptions{})
			if err != nil {
				return report, err
			}
			report.ImagesDeleted = append(report.ImagesDeleted, deleted...)
		}
	}
	return report, nil
}
