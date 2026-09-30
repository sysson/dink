package translator

import (
	"context"
	"testing"

	imagetypes "github.com/moby/moby/api/types/image"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestSystemInfoIsNamespaceScoped(t *testing.T) {
	replicas := int32(1)
	stopped := int32(0)
	client := kubernetesfake.NewClientset(
		&appsv1.Deployment{
			Name: "running", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Image: "registry.example/app:v1"}},
				}},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.Deployment{
			Name: "stopped", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{
				Replicas: &stopped,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers:     []corev1.Container{{Image: "registry.example/app:v1"}},
					InitContainers: []corev1.Container{{Image: "registry.example/init:v2"}},
				}},
			},
		},
		&appsv1.Deployment{
			Name: "pending", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		},
		&appsv1.Deployment{
			Name: "other-tenant", Namespace: "other",
			Spec: appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	translator := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})

	info, err := translator.SystemInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.Containers != 3 || info.ContainersRunning != 1 || info.ContainersStopped != 1 || info.ContainersPaused != 0 {
		t.Fatalf("container counts = total:%d running:%d stopped:%d paused:%d", info.Containers, info.ContainersRunning, info.ContainersStopped, info.ContainersPaused)
	}
	if info.Images != 2 {
		t.Fatalf("image count = %d, want 2 distinct workload references", info.Images)
	}
	if info.Name != "dink" || info.ServerVersion == "" || len(info.Warnings) == 0 {
		t.Fatalf("system identity or scope warnings missing: %+v", info)
	}
}

func TestSystemDiskUsageReportsImagesAndVolumes(t *testing.T) {
	client := kubernetesfake.NewClientset(
		&appsv1.Deployment{
			Name: "web", Namespace: "tenant",
			Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Image: "localhost:5000/tenant/app@sha256:used"}},
				Volumes: []corev1.Volume{{Name: "data",
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}},
			}}},
		},
		&corev1.PersistentVolumeClaim{
			Name: "data", Namespace: "tenant",
			Labels:      map[string]string{volumeManagedLabel: volumeManagedValue},
			Annotations: map[string]string{volumeNameAnnotation: "data"},
			Status:      corev1.PersistentVolumeClaimStatus{Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}},
		},
		&corev1.PersistentVolumeClaim{
			Name: "spare", Namespace: "tenant",
			Labels:      map[string]string{volumeManagedLabel: volumeManagedValue},
			Annotations: map[string]string{volumeNameAnnotation: "spare"},
			Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			}},
		},
	)
	registry := &fakeRegistry{images: []imagetypes.Summary{
		{ID: "sha256:used", RepoDigests: []string{"app@sha256:used"}, RepoTags: []string{"app:v1"}, Size: 100},
		{ID: "sha256:used", RepoDigests: []string{"app@sha256:used"}, RepoTags: []string{"app:latest"}, Size: 100},
		{ID: "sha256:unused", RepoDigests: []string{"old@sha256:unused"}, Size: 40},
	}}
	translator := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})

	usage, err := translator.SystemDiskUsage(ctx, backend.DiskUsageOptions{Images: true, Volumes: true, Verbose: true})
	if err != nil {
		t.Fatal(err)
	}
	images := usage.Images
	if images.TotalCount != 2 || images.ActiveCount != 1 || images.TotalSize != 140 || images.Reclaimable != 40 || len(images.Items) != 2 {
		t.Fatalf("image usage = %+v", images)
	}
	volumes := usage.Volumes
	if volumes.TotalCount != 2 || volumes.ActiveCount != 1 || volumes.TotalSize != 3<<30 || volumes.Reclaimable != 1<<30 {
		t.Fatalf("volume usage = %+v", volumes)
	}
	for _, volume := range volumes.Items {
		if volume.UsageData == nil || (volume.Name == "data") != (volume.UsageData.RefCount == 1) {
			t.Fatalf("volume %s usage data = %+v", volume.Name, volume.UsageData)
		}
	}
	if usage.Containers != nil {
		t.Fatalf("containers were not requested: %+v", usage.Containers)
	}
}
