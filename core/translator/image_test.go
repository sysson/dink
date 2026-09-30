package translator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/core/registry/api"
	registryv1 "github.com/sysson/dink/core/registry/api/v1"
	"github.com/sysson/dink/core/registry/api/v1/registryconnect"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

type imageUsageRegistry struct {
	registryconnect.UnimplementedRegistryServiceHandler
	images  []*registryv1.ImageSummary
	removed []string
}

func (r *imageUsageRegistry) ListImages(context.Context, *registryv1.ListImagesRequest) (*registryv1.ListImagesResponse, error) {
	if r.images != nil {
		return &registryv1.ListImagesResponse{Images: r.images}, nil
	}
	return &registryv1.ListImagesResponse{Images: []*registryv1.ImageSummary{
		{Id: "sha256:used", RepoTags: []string{"web:latest"}, RepoDigests: []string{"web@sha256:used"}},
		{Id: "sha256:unused", RepoTags: []string{"other:latest"}, RepoDigests: []string{"other@sha256:unused"}},
	}}, nil
}

func (r *imageUsageRegistry) InspectImage(_ context.Context, request *registryv1.InspectImageRequest) (*registryv1.InspectImageResponse, error) {
	for _, image := range r.images {
		if slices.Contains(image.RepoTags, request.GetName()) {
			return &registryv1.InspectImageResponse{Image: []byte(`{"Id":"` + image.Id + `","Config":{"Labels":{"keep":"yes"}}}`)}, nil
		}
	}
	if request.GetName() == "other" {
		return &registryv1.InspectImageResponse{Image: []byte(`{"Id":"sha256:unused"}`)}, nil
	}
	return &registryv1.InspectImageResponse{Image: []byte(`{"Id":"sha256:used"}`)}, nil
}

func (r *imageUsageRegistry) RemoveImage(_ context.Context, request *registryv1.RemoveImageRequest) (*registryv1.RemoveImageResponse, error) {
	r.removed = append(r.removed, request.GetName())
	return &registryv1.RemoveImageResponse{Records: []*registryv1.RemoveImageRecord{{Untagged: request.GetName()}}}, nil
}

func TestImageUsageInNamespace(t *testing.T) {
	const pinned = "registry.local/tenant/web@sha256:used"
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(
		&appsv1.Deployment{Name: "stopped", Namespace: "tenant", Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(0)), Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: pinned}}}},
		}},
		&appsv1.Deployment{Name: "init", Namespace: "tenant", Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Image: pinned}}}},
		}},
		&appsv1.Deployment{Name: "elsewhere", Namespace: "other", Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: pinned}}}},
		}},
	)
	service := &imageUsageRegistry{}
	path, handler := registryconnect.NewRegistryServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	translator := &Registry{k8s: &k8s.KubeClient{Interface: client}, registry: api.NewClient(server.Client(), server.URL)}

	images, err := translator.Images(ctx, types.ImageListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Containers != 2 || images[1].Containers != 0 {
		t.Fatalf("image usage = %+v", images)
	}
	for _, name := range []string{"web", "sha256:used"} {
		_, err := translator.ImageDelete(ctx, name, imagebackend.RemoveOptions{Force: true})
		if !IsKind(err, KindConflict) {
			t.Fatalf("ImageDelete(%q) = %v, want conflict", name, err)
		}
	}
	if len(service.removed) != 0 {
		t.Fatal("in-use image was removed")
	}
	if _, err := translator.ImageDelete(ctx, "other", imagebackend.RemoveOptions{}); err != nil {
		t.Fatalf("ImageDelete(unused) = %v", err)
	}
	if !slices.Equal(service.removed, []string{"other"}) {
		t.Fatal("unused image was not removed")
	}
}

func TestImagePrune(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "stopped", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Replicas: new(int32(0)), Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "registry.local/tenant/web@sha256:used"}}},
		}},
	})
	service := &imageUsageRegistry{images: []*registryv1.ImageSummary{
		{Id: "sha256:used", RepoTags: []string{"alias:latest"}, RepoDigests: []string{"alias@sha256:used"}, Created: 100},
		{Id: "sha256:used", RepoTags: []string{"web:latest"}, RepoDigests: []string{"web@sha256:used"}, Created: 100},
		{Id: "sha256:unused", RepoTags: []string{"unused:latest"}, RepoDigests: []string{"unused@sha256:unused"}, Created: 100},
	}}
	path, handler := registryconnect.NewRegistryServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	translator := &Registry{k8s: &k8s.KubeClient{Interface: client}, registry: api.NewClient(server.Client(), server.URL)}

	report, err := translator.ImagePrune(ctx, filters.NewArgs())
	if err != nil || report == nil || len(report.ImagesDeleted) != 0 || len(service.removed) != 0 {
		t.Fatalf("default prune = %+v, %v; removed %v", report, err, service.removed)
	}
	pruneFilters := filters.NewArgs(filters.Arg("dangling", "false"))
	report, err = translator.ImagePrune(ctx, pruneFilters)
	if err != nil || report == nil || len(report.ImagesDeleted) != 1 || report.ImagesDeleted[0].Untagged != "unused:latest" ||
		report.SpaceReclaimed != 0 || !slices.Equal(service.removed, []string{"unused:latest"}) {
		t.Fatalf("all prune = %+v, %v; removed %v", report, err, service.removed)
	}
	for _, test := range []struct {
		name    string
		filters []filters.KeyValuePair
		remove  bool
	}{
		{name: "newer than cutoff", filters: []filters.KeyValuePair{filters.Arg("until", "1970-01-01T00:00:50Z")}},
		{name: "equal to cutoff", filters: []filters.KeyValuePair{filters.Arg("until", "1970-01-01T00:01:40Z")}},
		{name: "missing label", filters: []filters.KeyValuePair{filters.Arg("label", "missing")}},
		{name: "excluded label", filters: []filters.KeyValuePair{filters.Arg("label!", "keep=yes")}},
		{name: "partial exclusion", filters: []filters.KeyValuePair{filters.Arg("label!", "keep=yes"), filters.Arg("label!", "missing")}, remove: true},
		{name: "matching label", filters: []filters.KeyValuePair{filters.Arg("label", "keep=yes")}, remove: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.removed = nil
			pruneFilters := filters.NewArgs(filters.Arg("dangling", "false"))
			for _, filter := range test.filters {
				pruneFilters.Add(filter.Key, filter.Value)
			}
			report, err := translator.ImagePrune(ctx, pruneFilters)
			if err != nil || report == nil || (len(service.removed) == 1) != test.remove || len(report.ImagesDeleted) != len(service.removed) {
				t.Fatalf("prune = %+v, %v; removed %v", report, err, service.removed)
			}
		})
	}
	for _, filter := range []filters.KeyValuePair{filters.Arg("dangling", "invalid"), filters.Arg("unsupported", "true")} {
		_, err := translator.ImagePrune(ctx, filters.NewArgs(filter))
		if !IsKind(err, KindInvalidArgument) {
			t.Fatalf("prune with %q = %v, want invalid-argument", filter.Key, err)
		}
	}
}
