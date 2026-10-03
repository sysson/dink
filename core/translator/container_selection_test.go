package translator

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNamedContainer(t *testing.T) {
	for _, test := range []struct {
		name       string
		containers []corev1.Container
		wantError  bool
	}{
		{"single", []corev1.Container{{Name: "web"}}, false},
		{"prepended sidecar", []corev1.Container{{Name: "sidecar"}, {Name: "web", TTY: true}}, false},
		{"missing application", []corev1.Container{{Name: "sidecar"}}, true},
		{"empty", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, err := namedContainer(&corev1.PodSpec{Containers: test.containers}, "web")
			if test.wantError {
				if !IsKind(err, KindNotFound) {
					t.Fatalf("error = %v, want not found", err)
				}
				return
			}
			if err != nil || selected.Name != "web" {
				t.Fatalf("selected = %+v, error = %v", selected, err)
			}
		})
	}
}

func TestMissingApplicationContainerRejectsExecAndLogs(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)
	if _, err := swarm.docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web", Config: &container.Config{Image: "nginx"},
	}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := swarm.docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"id"}}); !IsKind(err, KindNotFound) {
		t.Fatalf("exec error = %v, want not found", err)
	}
	options := &backend.ContainerLogsOptions{ShowStdout: true, ShowStderr: true}
	if _, _, err := swarm.docker.ContainerLogs(ctx, "web", options); !IsKind(err, KindNotFound) {
		t.Fatalf("logs error = %v, want not found", err)
	}
	if _, err := swarm.docker.podLogs(ctx, "tenant", pod, "web", options); !IsKind(err, KindNotFound) {
		t.Fatalf("shared Pod logs error = %v, want not found", err)
	}
	if len(swarm.docker.execs) != 0 {
		t.Fatal("missing application left an exec record")
	}
}

func TestApplicationStatusIgnoresSidecars(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar"}, {Name: "web"}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "sidecar", ContainerID: "containerd://sidecar", RestartCount: 99,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
				{Name: "web", ContainerID: "containerd://app",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 7}}},
			},
		},
	}
	status := podTaskStatus(pod, "web")
	if status.ContainerStatus == nil || status.ContainerStatus.ContainerID != "app" ||
		status.ContainerStatus.ExitCode != 7 || status.State != swarmtypes.TaskStateFailed {
		t.Fatalf("Swarm task selected sidecar status: %+v", status)
	}
	if selected := podContainerStatus("web", pod); selected == nil || selected.Name != "web" {
		t.Fatalf("selected status = %+v", selected)
	}
	pod.Status.ContainerStatuses = pod.Status.ContainerStatuses[:1]
	if selected := podContainerStatus("web", pod); selected != nil {
		t.Fatalf("missing application fell back to sidecar: %+v", selected)
	}
	status = podTaskStatus(pod, "web")
	if status.ContainerStatus != nil {
		t.Fatalf("missing application task fell back to sidecar: %+v", status)
	}
}
