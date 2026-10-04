package translator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func simulateContainerPodGC(client *kubernetesfake.Clientset) {
	client.PrependReactor("delete", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		err := client.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), "tenant", "web-pod")
		if err != nil && !apierrors.IsNotFound(err) {
			return true, nil, err
		}
		return false, nil, nil
	})
}

func TestContainerUpdateResources(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Annotations: map[string]string{
			containerHostConfigAnnotation: `{"NetworkMode":"bridge"}`,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}},
		},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	response, err := docker.ContainerUpdate(ctx, "web", &container.UpdateConfig{
		NanoCPUs: 500_000_000, Memory: 128 << 20, MemoryReservation: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Warnings) != 1 {
		t.Fatalf("update warnings = %v, want Pod replacement warning", response.Warnings)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resources := deployment.Spec.Template.Spec.Containers[0].Resources
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || deployment.Spec.Strategy.RollingUpdate != nil {
		t.Fatalf("legacy template update did not enforce stop-first: %+v", deployment.Spec.Strategy)
	}
	if resources.Limits.Cpu().MilliValue() != 500 || resources.Limits.Memory().Value() != 128<<20 || resources.Requests.Memory().Value() != 64<<20 {
		t.Fatalf("updated Pod resources = %+v", resources)
	}
	_, hostConfig, err := containerMetadata(deploymentWorkload(deployment))
	if err != nil {
		t.Fatal(err)
	}
	if hostConfig.NetworkMode != "bridge" || hostConfig.NanoCPUs != 500_000_000 || hostConfig.Memory != 128<<20 || hostConfig.MemoryReservation != 64<<20 {
		t.Fatalf("updated host config = %+v", hostConfig)
	}
}

func TestContainerStartRetriesOnConflict(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
	})
	updates := 0
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatalf("ContainerStart() error = %v", err)
	}
	if updates != 2 {
		t.Fatalf("Deployment update attempts = %d, want 2", updates)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v, want 1", deployment.Spec.Replicas)
	}
}

func TestContainerKillRetriesOnConflict(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
	})
	updates := 0
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if err := docker.ContainerKill(ctx, "web", "SIGINT"); err != nil {
		t.Fatalf("ContainerKill() error = %v", err)
	}
	if updates != 2 {
		t.Fatalf("Deployment update attempts = %d, want 2", updates)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 || deployment.Annotations[containerSignalExitCodeAnnotation] != "130" {
		t.Fatalf("killed Deployment = replicas %v, annotations %+v", deployment.Spec.Replicas, deployment.Annotations)
	}
}

func TestContainerLifecycle(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(1)
	deployment, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dockerID := identity.DockerIDFromUID(deployment.UID)

	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas after stop = %v, err = %v; want 0", deployment.Spec.Replicas, err)
	}

	if err := docker.ContainerStart(ctx, dockerID[:12], "", ""); err != nil {
		t.Fatalf("ContainerStart by ID prefix: %v", err)
	}
	if err := docker.ContainerRestart(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerRestart: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Annotations["dink.io/restarted-at"] == "" {
		t.Fatalf("deployment after restart = %+v, err = %v", deployment.Spec, err)
	}
	if err := docker.ContainerKill(ctx, "web", "INT"); err != nil {
		t.Fatalf("ContainerKill INT: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas after INT = %v, err = %v; want 0", deployment.Spec.Replicas, err)
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || deployment.Annotations[containerSignalExitCodeAnnotation] != "" {
		t.Fatalf("signal exit status survived start: %+v, err = %v", deployment.Annotations, err)
	}
	if err := docker.ContainerKill(ctx, "web", "HUP"); err == nil {
		t.Fatal("unsupported signal accepted")
	}

	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerStop(otherTenant, "web", backend.ContainerStopOptions{}); err == nil {
		t.Fatal("ContainerStop crossed tenant namespace")
	}
	for _, serviceName := range []string{"web", publishedPortsServiceName("web")} {
		service := containerDNSService(deploymentWorkload(deployment), nil)
		service.Name = serviceName
		if _, err := client.CoreV1().Services("tenant").Create(ctx, service, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{}); err == nil {
		t.Fatal("running container removed without force")
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
		t.Fatalf("ContainerRm by ID: %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("published service remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("DNS service remains after ContainerRm")
	}
}

func TestContainerWait(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	response, err := docker.ContainerWait(ctx, "web", container.WaitConditionNotRunning)
	if err != nil || response.StatusCode != 42 {
		t.Fatalf("wait result = %+v, err = %v", response, err)
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := docker.ContainerWait(deadline, "web", container.WaitConditionRemoved); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancellation = %v", err)
	}
	timer := time.AfterFunc(50*time.Millisecond, func() {
		_ = client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{})
	})
	defer timer.Stop()
	response, err = docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err != nil || response.StatusCode != 0 {
		t.Fatalf("wait for removal = %+v, err = %v", response, err)
	}
	response, err = docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err == nil {
		t.Fatalf("wait on unknown container = %+v, want not found", response)
	}
}

func TestContainerWaitAutoRemove(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 2*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	simulateContainerPodGC(client)
	zero := int32(0)
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &zero},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	timer := time.AfterFunc(40*time.Millisecond, func() {
		_ = docker.ContainerStart(ctx, "web", "", "")
		_, _ = client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
			Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}},
			}}},
		}, metav1.CreateOptions{})
	})
	defer timer.Stop()
	response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err != nil || response.StatusCode != 42 {
		t.Fatalf("auto-remove wait = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("auto-removed container still exists")
	}
}

func TestContainerWaitAutoRemoveAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	simulateContainerPodGC(client)
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved); err != nil || response.StatusCode != 137 {
		t.Fatalf("restarted container auto-remove wait = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err == nil {
		t.Fatal("restarted container remains after its first exit")
	}
}

func TestContainerWaitAutoRemoveWaitsForDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deleteRequested := make(chan struct{})
	client.PrependReactor("delete", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		close(deleteRequested)
		return true, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	result := make(chan error, 1)
	go func() {
		_, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
		result <- err
	}()
	select {
	case <-deleteRequested:
	case <-ctx.Done():
		t.Fatal("auto-remove did not request deployment deletion")
	}
	select {
	case err := <-result:
		t.Fatalf("wait returned before deployment deletion completed: %v", err)
	default:
	}
	if err := client.Tracker().Delete(appsv1.SchemeGroupVersion.WithResource("deployments"), "tenant", "web"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("wait did not finish when deployment was deleted")
	}
}

func TestContainerWaitIgnoresPreviousDeploymentPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 400*time.Millisecond)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "current-deployment", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().ReplicaSets("tenant").Create(ctx, &appsv1.ReplicaSet{
		Name: "old-rs", Namespace: "tenant", UID: "old-rs-uid",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: "previous-deployment"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "old-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "old-rs", UID: "old-rs-uid"}},
		Status:          corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait on new deployment = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("new deployment was removed by old pod: %v", err)
	}
}

func TestContainerWaitAutoRemoveAfterSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	result := make(chan struct {
		response container.WaitResponse
		err      error
	}, 1)
	go func() {
		response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
		result <- struct {
			response container.WaitResponse
			err      error
		}{response, err}
	}()
	if err := docker.ContainerKill(ctx, "web", "INT"); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-result:
		if item.err != nil || item.response.StatusCode != 130 {
			t.Fatalf("wait after INT = %+v, err = %v", item.response, item.err)
		}
	case <-ctx.Done():
		t.Fatal("wait did not finish after INT removed its pod")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err == nil {
		t.Fatal("auto-remove left a stopped Deployment behind")
	}
}
