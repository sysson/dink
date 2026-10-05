package translator

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func newOneShotTestDocker(t *testing.T) (context.Context, *kubernetesfake.Clientset, *Docker) {
	t.Helper()
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	client.PrependReactor("create", "jobs", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*batchv1.Job).UID = "12345678-1234-1234-1234-123456789abc"
		return false, nil, nil
	})
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"postgres": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:       "once",
		Config:     &container.Config{Image: "postgres", Volumes: map[string]struct{}{"/data": {}}},
		HostConfig: &container.HostConfig{AutoRemove: true, Binds: []string{"named:/named"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, client, docker
}

func TestAutoRemoveContainerIsSuspendedJob(t *testing.T) {
	ctx, client, docker := newOneShotTestDocker(t)

	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "once", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("--rm container created a Deployment: err = %v", err)
	}
	job, err := client.BatchV1().Jobs("tenant").Get(ctx, "once", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := job.Spec
	if spec.Suspend == nil || !*spec.Suspend || spec.BackoffLimit == nil || *spec.BackoffLimit != 0 ||
		spec.TTLSecondsAfterFinished == nil || *spec.TTLSecondsAfterFinished != oneShotRemovalDelay ||
		spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Fatalf("Job spec = %+v, want suspended, run-once and self-removing", spec)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, "once", metav1.GetOptions{})
	if err != nil || len(service.OwnerReferences) != 1 || service.OwnerReferences[0].Kind != "Job" || service.OwnerReferences[0].UID != job.UID {
		t.Fatalf("Service = %+v, err = %v; want owned by the Job", service, err)
	}
	for _, volume := range spec.Template.Spec.Volumes {
		pvc, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ownedByJob := len(pvc.OwnerReferences) == 1 && pvc.OwnerReferences[0].UID == job.UID
		if ownedByJob != (volume.Name != "named") {
			t.Fatalf("volume %s owners = %+v; only anonymous volumes should be owned by the Job", volume.Name, pvc.OwnerReferences)
		}
	}

	inspected, _, err := docker.ContainerInspect(ctx, "once", backend.ContainerInspectOptions{})
	if err != nil || inspected.State.Status != container.StateCreated {
		t.Fatalf("inspect = %+v, err = %v; want created", inspected, err)
	}
	listed, err := docker.Containers(ctx, &backend.ContainerListOptions{All: true})
	if err != nil || len(listed) != 1 || listed[0].Names[0] != "/once" {
		t.Fatalf("list = %+v, err = %v", listed, err)
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "once", Config: &container.Config{Image: "postgres"}}); !IsKind(err, KindConflict) {
		t.Fatalf("Deployment with a Job's name: err = %v, want conflict", err)
	}
}

func TestAutoRemoveContainerLifecycle(t *testing.T) {
	ctx, client, docker := newOneShotTestDocker(t)

	if err := docker.ContainerStart(ctx, "once", "", ""); err != nil {
		t.Fatal(err)
	}
	job, err := client.BatchV1().Jobs("tenant").Get(ctx, "once", metav1.GetOptions{})
	if err != nil || job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Fatalf("started Job = %+v, err = %v; want resumed", job, err)
	}
	if err := docker.ContainerRestart(ctx, "once", backend.ContainerStopOptions{}); !IsKind(err, KindUnsupported) {
		t.Fatalf("restart err = %v, want unsupported", err)
	}
	if _, err := docker.ContainerUpdate(ctx, "once", &container.UpdateConfig{}); !IsKind(err, KindUnsupported) {
		t.Fatalf("update err = %v, want unsupported", err)
	}

	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "once-pod", Namespace: "tenant", Labels: map[string]string{"app": "once"},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "once", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 3}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, _, err := docker.ContainerInspect(ctx, "once", backend.ContainerInspectOptions{})
	if err != nil || inspected.State.Status != container.StateExited || inspected.State.ExitCode != 3 {
		t.Fatalf("inspect = %+v, err = %v; want exited 3", inspected.State, err)
	}
	response, err := docker.ContainerWait(ctx, "once", container.WaitConditionRemoved)
	if err != nil || response.StatusCode != 3 {
		t.Fatalf("wait = %+v, err = %v; want 3", response, err)
	}
	if _, err := client.BatchV1().Jobs("tenant").Get(ctx, "once", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Job after --rm exit: err = %v, want removed", err)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, "named", metav1.GetOptions{}); err != nil {
		t.Fatalf("named volume removed with --rm container: %v", err)
	}
}

func TestAutoRemoveContainerStopRemovesIt(t *testing.T) {
	ctx, client, docker := newOneShotTestDocker(t)
	if err := docker.ContainerStart(ctx, "once", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerStop(ctx, "once", backend.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BatchV1().Jobs("tenant").Get(ctx, "once", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("Job after stop: err = %v, want removed", err)
	}
}
