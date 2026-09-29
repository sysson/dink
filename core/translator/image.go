package translator

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/oci/ociref"
	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *Registry) ImageDelete(ctx context.Context, name string, options imagebackend.RemoveOptions) ([]imagetypes.DeleteResponse, error) {
	image, err := r.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{})
	if err != nil {
		return nil, err
	}
	images, err := r.Images(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	if count := imageContainerCount(images, image.ID); count > 0 {
		return nil, httpx.Conflict(fmt.Errorf("unable to remove %s: image is used by %d container(s)", name, count))
	}
	return r.registry.ImageDelete(ctx, name, options)
}

func (r *Registry) ImageHistory(ctx context.Context, name string, platform *ocispec.Platform) ([]imagetypes.HistoryResponseItem, error) {
	return r.registry.ImageHistory(ctx, name, platform)
}

func (r *Registry) Images(ctx context.Context, options types.ImageListOptions) ([]imagetypes.Summary, error) {
	images, err := r.registry.Images(ctx, options)
	if err != nil {
		return nil, err
	}
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, httpx.Unauthorized(fmt.Errorf("missing identity in context"))
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

func imageContainerCount(images []imagetypes.Summary, imageID string) int64 {
	var count int64
	for _, image := range images {
		if image.ID == imageID && image.Containers > count {
			count = image.Containers
		}
	}
	return count
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

func (r *Registry) ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	return r.registry.ImageInspect(ctx, name, options)
}

func (r *Registry) ImageAttestations(ctx context.Context, name string, options imagebackend.AttestationOpts) ([]imagetypes.AttestationStatement, error) {
	return r.registry.ImageAttestations(ctx, name, options)
}

func (r *Registry) GetImage(ctx context.Context, name string, options imagebackend.GetImageOpts) (*imagebackend.InspectData, error) {
	return r.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{Platform: options.Platform})
}

func (r *Registry) TagImage(context.Context, string, ociref.Reference) error {
	return ErrNotImplemented
}

func (r *Registry) ImagePrune(ctx context.Context, pruneFilters filters.Args) (*imagetypes.PruneReport, error) {
	if err := pruneFilters.Validate(map[string]bool{"dangling": true, "label": true, "label!": true, "until": true}); err != nil {
		return nil, httpx.BadRequest(err)
	}
	danglingOnly, err := pruneFilters.GetBoolOrDefault("dangling", true)
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	before, err := pruneBefore(pruneFilters.Get("until"))
	if err != nil {
		return nil, httpx.BadRequest(err)
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

func (r *Registry) LoadImage(context.Context, io.ReadCloser, []ocispec.Platform, io.Writer, bool) error {
	return ErrNotImplemented
}

func (r *Registry) ImportImage(context.Context, ociref.Reference, *ocispec.Platform, string, io.Reader, []string) (string, error) {
	return "", ErrNotImplemented
}

func (r *Registry) ExportImage(context.Context, []string, []ocispec.Platform, io.Writer) error {
	return ErrNotImplemented
}

func (r *Registry) PullImage(ctx context.Context, ref ociref.Reference, options imagebackend.PullOptions) error {
	return r.registry.PullImage(ctx, ref, options)
}

func (r *Registry) PushImage(context.Context, ociref.Reference, imagebackend.PushOptions) error {
	return ErrNotImplemented
}

func (r *Registry) Search(context.Context, filters.Args, string, int, *registry.AuthConfig, map[string][]string) ([]registry.SearchResult, error) {
	return nil, ErrNotImplemented
}
