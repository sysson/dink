package translator

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestDefaultVolumeCreatesPVC(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	created, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{
		Name:   "cache_data",
		Labels: map[string]string{"app": "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "cache_data" || created.Driver != "local" || created.Scope != "local" || created.Labels["app"] != "web" || created.Options == nil {
		t.Fatalf("created volume = %+v", created)
	}
	recreated, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "cache_data"})
	if err != nil || recreated.Name != created.Name || recreated.Labels["app"] != "web" {
		t.Fatalf("repeated CreateVolume() = %+v, %v", recreated, err)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, "cache-data", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pvc.Labels[volumeManagedLabel] != volumeManagedValue || pvc.Spec.StorageClassName != nil || len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Fatalf("PVC spec = %+v", pvc.Spec)
	}
	if got := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; got.Cmp(resource.MustParse(defaultVolumeStorageSize)) != 0 {
		t.Fatalf("PVC size = %s, want %s", got.String(), defaultVolumeStorageSize)
	}
	inspected, err := docker.GetVolume(ctx, "cache_data")
	if err != nil || inspected.Name != created.Name || inspected.Labels["app"] != "web" {
		t.Fatalf("GetVolume() = %+v, %v", inspected, err)
	}
	listed, warnings, err := docker.ListVolumes(ctx, filters.NewArgs(filters.Arg("label", "app=web")))
	if err != nil || len(warnings) != 0 || len(listed) != 1 || listed[0].Name != created.Name {
		t.Fatalf("ListVolumes() = %+v, warnings %v, err %v", listed, warnings, err)
	}
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "unsupported", Driver: "nfs"}); err == nil {
		t.Fatal("accepted unsupported volume driver")
	} else if !IsKind(err, KindUnsupported) {
		t.Fatalf("unsupported driver error = %v, want unsupported", err)
	}
}

func TestVolumeRemovalAndPruneRespectWorkloadReferences(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	for _, name := range []string{"used", "unused"} {
		if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Volumes: []corev1.Volume{{Name: "data", PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "used"}}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := docker.RemoveVolume(ctx, "used", false); err == nil {
		t.Fatal("removed volume referenced by Deployment")
	} else if !IsKind(err, KindConflict) {
		t.Fatalf("remove in-use volume error = %v, want conflict", err)
	}
	report, err := docker.PruneVolumes(ctx, filters.NewArgs())
	if err != nil || len(report.VolumesDeleted) != 0 {
		t.Fatalf("default PruneVolumes() = %+v, %v; want named volumes kept", report, err)
	}
	report, err = docker.PruneVolumes(ctx, filters.NewArgs(filters.Arg("all", "true")))
	if err != nil || len(report.VolumesDeleted) != 1 || report.VolumesDeleted[0] != "unused" {
		t.Fatalf("PruneVolumes(all) = %+v, %v", report, err)
	}
	if err := docker.RemoveVolume(ctx, "used", true); err != nil {
		t.Fatalf("forced RemoveVolume() error = %v", err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, "used", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("forced volume removal error = %v, want not found", err)
	}
}

func TestContainerCreateMountsLocalVolumes(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image:   "nginx",
			Volumes: map[string]struct{}{"/data": {}, "/image-data": {}},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "cache_data", Target: "/data", ReadOnly: true}},
			Binds:  []string{"logs:/logs:ro"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	if len(pod.Volumes) != 3 || len(pod.Containers[0].VolumeMounts) != 3 {
		t.Fatalf("Pod volumes = %+v, mounts = %+v", pod.Volumes, pod.Containers[0].VolumeMounts)
	}
	mounts := make(map[string]corev1.VolumeMount, len(pod.Containers[0].VolumeMounts))
	for _, volumeMount := range pod.Containers[0].VolumeMounts {
		mounts[volumeMount.MountPath] = volumeMount
	}
	if !mounts["/data"].ReadOnly || mounts["/data"].Name != "cache-data" || !mounts["/logs"].ReadOnly || mounts["/logs"].Name != "logs" {
		t.Fatalf("named volume mounts = %+v", mounts)
	}
	if _, ok := mounts["/image-data"]; !ok {
		t.Fatalf("image-declared anonymous mount missing: %+v", mounts)
	}
	inspected, _, err := docker.ContainerInspect(ctx, "web", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected.Mounts) != 3 {
		t.Fatalf("inspect mounts = %+v", inspected.Mounts)
	}
	for _, inspectedMount := range inspected.Mounts {
		if inspectedMount.Destination == "/data" && (inspectedMount.Name != "cache_data" || inspectedMount.Driver != "local" || inspectedMount.RW) {
			t.Fatalf("inspect named mount = %+v", inspectedMount)
		}
	}
	for _, claim := range []string{"cache-data", "logs", mounts["/image-data"].Name} {
		pvc, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, claim, metav1.GetOptions{})
		if err != nil || pvc.Labels[volumeManagedLabel] != volumeManagedValue {
			t.Fatalf("PVC %q = %+v, err = %v", claim, pvc, err)
		}
	}

	if err := docker.ContainerRm(ctx, "web", &backend.ContainerRmConfig{ForceRemove: true, RemoveVolume: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, mounts["/image-data"].Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("anonymous volume after rm -v: err = %v, want not found", err)
	}
	for _, claim := range []string{"cache-data", "logs"} {
		if _, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, claim, metav1.GetOptions{}); err != nil {
			t.Fatalf("named volume %q removed by rm -v: %v", claim, err)
		}
	}
}
