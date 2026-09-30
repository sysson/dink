package translator

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestSubscribeToEventsIsScopedToIdentityNamespace(t *testing.T) {
	client := kubernetesfake.NewClientset()
	other := &appsv1.Deployment{Name: "outside", Namespace: "tenant-b"}
	if _, err := client.AppsV1().Deployments("tenant-b").Create(context.Background(), other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	translator := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant-a"})
	_, stream, err := translator.SubscribeToEvents(ctx, time.Time{}, time.Time{}, filters.NewArgs())
	if err != nil {
		t.Fatal(err)
	}

	outside := other.DeepCopy()
	outside.Name = "outside-after-subscribe"
	if _, err := client.AppsV1().Deployments("tenant-b").Create(context.Background(), outside, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-stream:
		t.Fatalf("received another namespace event: %#v", value)
	case <-time.After(25 * time.Millisecond):
	}

	inside := &appsv1.Deployment{
		Name: "inside", Namespace: "tenant-a",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "inside", Image: "example/image:v1"}},
		}}},
	}
	if _, err := client.AppsV1().Deployments("tenant-a").Create(context.Background(), inside, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case value, open := <-stream:
		if !open {
			t.Fatal("event stream closed before tenant event")
		}
		message, ok := value.(events.Message)
		if !ok || message.Type != events.ContainerEventType || message.Actor.Attributes["name"] != "inside" {
			t.Fatalf("event = %#v, want tenant-a container event", value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tenant namespace event")
	}

	if err := translator.UnsubscribeFromEvents(context.Background(), stream); err != nil {
		t.Fatal(err)
	}
	select {
	case _, open := <-stream:
		if open {
			t.Fatal("event stream remained open after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("event stream did not close after unsubscribe")
	}
}

func TestPodTransitionEventsReportRestartLoopAndHealth(t *testing.T) {
	now := time.Now()
	deployment := &appsv1.Deployment{
		Name: "db",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "db", Image: "postgres", ReadinessProbe: &corev1.Probe{PeriodSeconds: 1, FailureThreshold: 1}}},
		}}},
	}
	pod := func(status corev1.ContainerStatus) *corev1.Pod {
		status.Name = "db"
		return &corev1.Pod{
			Name: "db-1", Labels: map[string]string{"app": "db"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{status}},
		}
	}
	running := func(id string, started time.Time, ready bool, restarts int32, last *corev1.ContainerStateTerminated) *corev1.Pod {
		return pod(corev1.ContainerStatus{
			ContainerID: id, Ready: ready, RestartCount: restarts,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(started)}},
			LastTerminationState: corev1.ContainerState{Terminated: last},
		})
	}
	exit := &corev1.ContainerStateTerminated{ContainerID: "c1", ExitCode: 1, FinishedAt: metav1.NewTime(now)}
	crashLoop := pod(corev1.ContainerStatus{
		ContainerID: "c1", RestartCount: 1,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: exit},
	})

	steps := []struct {
		pod  *corev1.Pod
		at   time.Time
		want []string
	}{
		{running("c1", now, false, 0, nil), now, []string{"start"}},
		{running("c1", now, true, 0, nil), now, []string{"health_status: healthy"}},
		{crashLoop, now, []string{"die"}},
		{crashLoop, now, nil},
		{running("c2", now, false, 1, exit), now, []string{"start"}},
		{running("c2", now, false, 1, exit), now.Add(5 * time.Second), []string{"health_status: unhealthy"}},
	}
	var previous podEventState
	for index, step := range steps {
		current := observePodEventState(deploymentWorkload(deployment), step.pod, step.at)
		var got []string
		for _, message := range podTransitionEvents(deploymentWorkload(deployment), previous, current, step.at) {
			got = append(got, string(message.Action))
			if message.Action == events.ActionDie && message.Actor.Attributes["exitCode"] != "1" {
				t.Fatalf("step %d die exitCode = %q, want 1", index, message.Actor.Attributes["exitCode"])
			}
		}
		if len(got) != len(step.want) || (len(got) > 0 && got[0] != step.want[0]) {
			t.Fatalf("step %d events = %v, want %v", index, got, step.want)
		}
		previous = current
	}

	state, restarts := containerState(deploymentWorkload(deployment), []corev1.Pod{*crashLoop}, now)
	if state.Status != "restarting" || !state.Restarting || state.ExitCode != 1 || restarts != 1 {
		t.Fatalf("crash loop state = %+v restarts %d, want restarting with exit code 1", state, restarts)
	}
}

func TestSubscribeToEventsRequiresIdentityNamespace(t *testing.T) {
	translator := &Docker{}
	for _, ctx := range []context.Context{
		context.Background(),
		identity.NewContext(context.Background(), identity.Identity{}),
	} {
		if _, _, err := translator.SubscribeToEvents(ctx, time.Time{}, time.Time{}, filters.NewArgs()); !IsKind(err, KindUnauthenticated) {
			t.Fatalf("SubscribeToEvents without namespace = %v, want unauthenticated", err)
		}
	}
}
