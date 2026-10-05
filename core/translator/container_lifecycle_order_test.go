package translator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestContainerRestartWaitsForTerminatingPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	now := metav1.Now()
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Replicas: new(int32(1))},
	}, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		DeletionTimestamp: &now, Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	stopRequested := make(chan struct{})
	client.PrependReactor("update", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		deployment := action.(ktesting.UpdateAction).GetObject().(*appsv1.Deployment)
		if *deployment.Spec.Replicas == 0 {
			close(stopRequested)
		}
		return false, nil, nil
	})
	result := make(chan error, 1)
	go func() { result <- docker.ContainerRestart(ctx, "web", backend.ContainerStopOptions{}) }()
	select {
	case <-stopRequested:
	case <-ctx.Done():
		t.Fatal("restart never requested stop")
	}
	select {
	case err := <-result:
		t.Fatalf("restart returned before the old Pod stopped: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replacement started before old Pod stopped: %+v, %v", deployment, err)
	}
	if err := client.CoreV1().Pods("tenant").Delete(ctx, "web-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("restart never resumed after the Pod stopped")
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("restart strategy/replicas: %+v, %v", deployment, err)
	}
}

func TestContainerStopsRunConcurrentlyButStartWaitsForSameContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	for _, name := range []string{"web", "client"} {
		_, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
			Name: name, Namespace: "tenant", UID: types.UID(name + "-uid"),
			Spec: appsv1.DeploymentSpec{Replicas: new(int32(1))},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
			Name: name + "-pod", Namespace: "tenant", Labels: map[string]string{"app": name},
			Status: corev1.PodStatus{Phase: corev1.PodRunning},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	stops := make(chan string, 2)
	client.PrependReactor("update", "deployments", func(action ktesting.Action) (bool, runtime.Object, error) {
		deployment := action.(ktesting.UpdateAction).GetObject().(*appsv1.Deployment)
		if *deployment.Spec.Replicas == 0 {
			stops <- deployment.Name
		}
		return false, nil, nil
	})
	results := make(chan error, 2)
	for _, name := range []string{"web", "client"} {
		go func() { results <- docker.ContainerStop(ctx, name, backend.ContainerStopOptions{}) }()
	}
	for range 2 {
		select {
		case <-stops:
		case <-ctx.Done():
			t.Fatal("unrelated container stops were serialized")
		}
	}
	// An ID and a name must share the same lock, and waiting must honor cancellation.
	startCtx, startCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer startCancel()
	if err := docker.ContainerStart(startCtx, identity.DockerIDFromUID(types.UID("web-uid")), "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start did not wait for its in-progress stop: %v", err)
	}
	for _, name := range []string{"web", "client"} {
		deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, name, metav1.GetOptions{})
		if err != nil || *deployment.Spec.Replicas != 0 {
			t.Fatalf("container restarted while stopping: %+v, %v", deployment, err)
		}
		if err := client.CoreV1().Pods("tenant").Delete(ctx, name+"-pod", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("stop never completed")
		}
	}
	docker.containerLocksMu.Lock()
	defer docker.containerLocksMu.Unlock()
	if len(docker.containerLocks) != 0 {
		t.Fatalf("finished operations leaked locks: %v", docker.containerLocks)
	}
}

func TestContainerRestartTimeoutLeavesReplacementStopped(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"}), 50*time.Millisecond)
	defer cancel()
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Replicas: new(int32(1))},
	}, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if err := docker.ContainerRestart(ctx, "web", backend.ContainerStopOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restart timeout was not reported: %v", err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(t.Context(), "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 || deployment.Spec.Template.Annotations["dink.io/restarted-at"] != "" {
		t.Fatalf("timed-out restart started replacement: %+v, %v", deployment, err)
	}
}

func TestContainerStopWaitsForControllerObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant", Generation: 2,
		Spec: appsv1.DeploymentSpec{Replicas: new(int32(1))},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	result := make(chan error, 1)
	go func() { result <- docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}) }()
	select {
	case err := <-result:
		t.Fatalf("stop returned before controller observed the update: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("stop request not applied: %+v, %v", deployment, err)
	}
	deployment.Status.ObservedGeneration = deployment.Generation
	if _, err := client.AppsV1().Deployments("tenant").UpdateStatus(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stop never completed after controller observation")
	}
}

func TestStoppedReplacementDefersAliasClaims(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	createAliasedContainer(t, ctx, docker, "web", "web", "frontend")
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "replacement_Web", Config: &container.Config{Image: "nginx"},
		NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
			"bridge": {Aliases: []string{"web", "frontend"}},
		}},
	}); err != nil {
		t.Fatalf("stopped replacement cannot be created: %v", err)
	}
	if err := docker.ContainerStart(ctx, "replacement_Web", "", ""); !IsKind(err, KindConflict) {
		t.Fatalf("replacement started while aliases belonged to old container: %v", err)
	}
	replacement, err := docker.findContainer(ctx, "replacement_Web")
	if err != nil || replacement.Started {
		t.Fatalf("conflicting start changed running state: %+v, %v", replacement, err)
	}
	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerRm(ctx, "web", &backend.ContainerRmConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerRename(ctx, "replacement_Web", "web"); err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, "frontend", metav1.GetOptions{})
	if err != nil || service.Spec.Selector["app"] != replacement.Name {
		t.Fatalf("alias did not transfer to replacement's stable identity: %+v, %v", service, err)
	}
}

func TestSwarmServiceUpdateOrder(t *testing.T) {
	for _, test := range []struct {
		name        string
		update      *swarmtypes.UpdateConfig
		surge       int
		unavailable int
	}{
		{"default", nil, 0, 1},
		{"empty", &swarmtypes.UpdateConfig{}, 0, 1},
		{"stop-first", &swarmtypes.UpdateConfig{Order: swarmtypes.UpdateOrderStopFirst, Parallelism: 2}, 0, 2},
		{"start-first", &swarmtypes.UpdateConfig{Order: swarmtypes.UpdateOrderStartFirst, Parallelism: 2}, 2, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			strategy := serviceStrategy(test.update)
			if strategy.Type != appsv1.RollingUpdateDeploymentStrategyType ||
				strategy.RollingUpdate.MaxSurge.IntValue() != test.surge ||
				strategy.RollingUpdate.MaxUnavailable.IntValue() != test.unavailable {
				t.Fatalf("strategy = %+v, want surge=%d unavailable=%d", strategy, test.surge, test.unavailable)
			}
		})
	}
}
