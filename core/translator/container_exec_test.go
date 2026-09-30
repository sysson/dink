package translator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/remotecommand"
	kubeexec "k8s.io/client-go/util/exec"
)

func TestContainerExecCreateInspect(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"echo", "hello"}, AttachStdout: true})
	if err != nil || execID == "" {
		t.Fatalf("exec create ID = %q, err = %v", execID, err)
	}
	inspect, err := docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.ID != execID || inspect.ProcessConfig.Entrypoint != "echo" || inspect.Running || inspect.ExitCode != nil {
		t.Fatalf("exec inspect = %+v, err = %v", inspect, err)
	}
	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if _, err := docker.ContainerExecInspect(otherTenant, execID); err == nil {
		t.Fatal("exec ID leaked across tenants")
	}
	if _, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"id"}, User: "root"}); err == nil {
		t.Fatal("unsupported exec user accepted")
	}
	var stdout bytes.Buffer
	docker.podStream = func(_ context.Context, namespace, podName string, options corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if namespace != "tenant" || podName != "web-pod" || attach || len(options.Command) != 2 || options.Command[0] != "echo" || !options.Stdout {
			t.Errorf("unexpected exec options: namespace = %q, pod = %q, options = %+v, attach = %v", namespace, podName, options, attach)
		}
		_, _ = streams.Stdout.Write([]byte("hello\n"))
		return kubeexec.CodeExitError{Err: errors.New("exit status 7"), Code: 7}
	}
	if err := docker.ContainerExecStart(otherTenant, execID, backend.ExecStartConfig{Stdout: &stdout}); err == nil {
		t.Fatal("cross-tenant exec start succeeded")
	}
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	inspect, err = docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.Running || inspect.ExitCode == nil || *inspect.ExitCode != 7 || stdout.String() != "hello\n" {
		t.Fatalf("completed exec = %+v, output = %q, err = %v", inspect, stdout.String(), err)
	}
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: &stdout}); err == nil {
		t.Fatal("completed exec restarted")
	}
	if err := docker.ContainerRm(ctx, "web", &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerExecInspect(ctx, execID); err == nil {
		t.Fatal("removed container left an inspectable exec ID")
	}
}

func TestContainerExecConsoleSizeAndDetachKeys(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{
		Cmd: []string{"sh"}, Tty: true, AttachStdin: true, AttachStdout: true,
		ConsoleSize: &[2]uint{24, 80}, DetachKeys: "ctrl-p,ctrl-q",
	})
	if err != nil {
		t.Fatal(err)
	}
	var size *remotecommand.TerminalSize
	docker.podStream = func(streamCtx context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, _ bool) error {
		size = streams.TerminalSizeQueue.Next()
		if _, err := io.Copy(io.Discard, streams.Stdin); err != nil {
			return err
		}
		<-streamCtx.Done()
		return streamCtx.Err()
	}
	// ctrl-p ctrl-q is the detach sequence, so the stream must end cleanly.
	stdin := bytes.NewReader([]byte{0x10, 0x11})
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdin: io.NopCloser(stdin), Stdout: io.Discard}); err != nil {
		t.Fatalf("detached exec returned %v", err)
	}
	if size == nil || size.Height != 24 || size.Width != 80 {
		t.Fatalf("initial terminal size = %+v", size)
	}
	inspect, err := docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.Running || inspect.ExitCode != nil {
		t.Fatalf("detached exec inspect = %+v, err = %v", inspect, err)
	}
}

func TestContainerExecResize(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{Name: "web", Namespace: "tenant"}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"sh"}, Tty: true, AttachStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, _ bool) error {
		close(started)
		size := streams.TerminalSizeQueue.Next()
		if size == nil || size.Height != 25 || size.Width != 80 {
			return fmt.Errorf("unexpected terminal size: %+v", size)
		}
		return nil
	}
	go func() {
		finished <- docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: io.Discard})
	}()
	<-started
	if err := docker.ContainerExecResize(ctx, execID, 25, 80); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerExecResize(ctx, execID, 40, 100); err == nil {
		t.Fatal("resized a completed exec")
	}
}
