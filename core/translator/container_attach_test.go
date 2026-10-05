package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func TestContainerAttach(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "injected-sidecar"}, {Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "injected-sidecar"}, {Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	var output bytes.Buffer
	docker.podStream = func(_ context.Context, namespace, podName string, options corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach || options.Container != "web" || !options.Stdout || options.Stdin || namespace != "tenant" || podName != "web-pod" {
			t.Errorf("unexpected attach options: %+v, attach = %v", options, attach)
		}
		_, _ = streams.Stdout.Write([]byte("attached\n"))
		return nil
	}
	config := &backend.ContainerAttachConfig{Stream: true, UseStdout: true, GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
		return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
	}}
	if err := docker.ContainerAttach(ctx, "web", config); err != nil || output.String() != "attached\n" {
		t.Fatalf("attach output = %q, err = %v", output.String(), err)
	}
	config.Logs = true
	if err := docker.ContainerAttach(ctx, "web", config); err == nil {
		t.Fatal("historical attach logs silently accepted")
	}
	config.Logs = false
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		return errors.New("attach transport failed")
	}
	if err := docker.ContainerAttach(ctx, "web", config); err == nil || !strings.Contains(err.Error(), "attach transport failed") {
		t.Fatalf("running container attach error = %v", err)
	}
}

func TestContainerAttachReplaysNonInteractiveOutput(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(fmt.Sprintf("exited=%t", exited), func(t *testing.T) {
			phase := corev1.PodRunning
			state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
			if exited {
				phase = corev1.PodSucceeded
				state = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/apps/v1/namespaces/tenant/deployments/web":
					_ = json.NewEncoder(w).Encode(&appsv1.Deployment{
						APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "tenant",
						Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
					})
				case "/api/v1/namespaces/tenant/pods":
					_ = json.NewEncoder(w).Encode(&corev1.PodList{
						APIVersion: "v1", Kind: "PodList", Items: []corev1.Pod{{
							Name: "web-pod", Namespace: "tenant", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
							Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: state}}},
						}},
					})
				case "/api/v1/namespaces/tenant/pods/web-pod/log":
					if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("container") != "web" || r.URL.Query().Has("tailLines") {
						t.Errorf("log query = %s", r.URL.RawQuery)
					}
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = io.WriteString(w, "startup output\n")
					w.(http.Flusher).Flush()
					_, _ = io.WriteString(w, "later output\n")
				default:
					t.Errorf("unexpected Kubernetes request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
			ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
			var output bytes.Buffer
			if err := docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
				Stream: true, UseStdout: true, UseStderr: true,
				GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
					return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
				},
			}); err != nil || output.String() != "startup output\nlater output\n" {
				t.Fatalf("non-interactive attach output = %q, err = %v", output.String(), err)
			}
		})
	}
}

func TestContainerAttachBeforeStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach {
			t.Error("expected pod attach")
		}
		_, err := streams.Stdout.Write([]byte("started\n"))
		return err
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream before start")
	}
	select {
	case err := <-result:
		t.Fatalf("attach returned before start: %v", err)
	default:
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil || output.String() != "started\n" {
			t.Fatalf("attach output = %q, err = %v", output.String(), err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not connect after start")
	}
}

func TestContainerAttachTTYResize(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
		Spec: appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	sizes := make(chan remotecommand.TerminalSize, 2)
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach || !streams.Tty || streams.TerminalSizeQueue == nil {
			t.Error("TTY attach has no terminal size queue")
			return nil
		}
		sizes <- *streams.TerminalSizeQueue.Next()
		sizes <- *streams.TerminalSizeQueue.Next()
		return nil
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("TTY attach did not connect")
	}
	if err := docker.ContainerResize(ctx, identity.DockerIDFromUID("12345678-1234-1234-1234-123456789abc"), 17, 232); err != nil {
		t.Fatalf("resize before pod starts: %v", err)
	}
	if err := docker.ContainerResize(ctx, "web", 19, 200); err != nil {
		t.Fatalf("updated resize before pod starts: %v", err)
	}
	otherTenant := identity.NewContext(ctx, identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerResize(otherTenant, "web", 17, 232); err == nil {
		t.Fatal("resize crossed tenant namespace")
	}
	if err := docker.ContainerResize(ctx, "web", 65536, 232); err == nil {
		t.Fatal("resize accepted dimensions exceeding Kubernetes limits")
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-sizes:
		if size.Height != 19 || size.Width != 200 {
			t.Fatalf("TTY size = %+v", size)
		}
	case <-ctx.Done():
		t.Fatal("TTY resize did not reach the pod stream")
	}
	if err := docker.ContainerResize(ctx, "web", 18, 80); err != nil {
		t.Fatalf("resize running attach: %v", err)
	}
	select {
	case size := <-sizes:
		if size.Height != 18 || size.Width != 80 {
			t.Fatalf("updated TTY size = %+v", size)
		}
	case <-ctx.Done():
		t.Fatal("running resize did not reach the pod stream")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerResize(ctx, "web", 18, 80); err == nil {
		t.Fatal("resized an inactive attach")
	}
}

func TestContainerAttachIgnoresPreviousDeploymentPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	currentUID := types.UID("12345678-1234-1234-1234-123456789abc")
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: currentUID,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, replicaSet := range []struct {
		name string
		uid  types.UID
		root types.UID
	}{
		{"old-rs", "old-rs-uid", "previous-deployment-uid"},
		{"new-rs", "new-rs-uid", currentUID},
	} {
		if _, err := client.AppsV1().ReplicaSets("tenant").Create(ctx, &appsv1.ReplicaSet{
			Name: replicaSet.name, Namespace: "tenant", UID: replicaSet.uid,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: replicaSet.root}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	newPod := func(name string, replicaSetUID types.UID) *corev1.Pod {
		return &corev1.Pod{Name: name, Namespace: "tenant", Labels: map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: strings.TrimSuffix(name, "-pod") + "-rs", UID: replicaSetUID}},
			Spec:            corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
			Status:          corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, newPod("old-pod", "old-rs-uid"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(_ context.Context, _, podName string, _ corev1.PodExecOptions, _ remotecommand.StreamOptions, _ bool) error {
		if podName != "new-pod" {
			return fmt.Errorf("attached to previous deployment pod %s", podName)
		}
		return nil
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not connect")
	}
	select {
	case err := <-result:
		t.Fatalf("attach selected previous pod: %v", err)
	case <-time.After(350 * time.Millisecond):
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, newPod("new-pod", "new-rs-uid"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not reach current pod")
	}
}

func TestContainerAttachExitedBeforeStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 2*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "web", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		t.Error("tried attaching to an exited container")
		return errors.New("container web not found in pod")
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream")
	}
	if err := client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("attach after auto-remove = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not finish after auto-remove")
	}
}

func TestContainerAttachExitsDuringStream(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		pod, err := client.CoreV1().Pods("tenant").Get(ctx, "web-pod", metav1.GetOptions{})
		if err != nil {
			return err
		}
		pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
		if _, err := client.CoreV1().Pods("tenant").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return errors.New("unable to upgrade connection: container web not found in pod")
	}
	var output bytes.Buffer
	if err := docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
		Stream: true, UseStdout: true,
		GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
			return io.NopCloser(strings.NewReader("")), &output, &output, nil
		},
	}); err != nil || output.Len() != 0 {
		t.Fatalf("attach after exit output = %q, err = %v", output.String(), err)
	}
}

func TestContainerAttachWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}))
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("attach did not establish its stream")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attach did not stop after cancellation")
	}
}

func TestContainerAttachRemovedWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	var output bytes.Buffer
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, &output, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream")
	}
	if err := client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(output.String(), "no longer exists") {
			t.Fatalf("attach output = %q, err = %v", output.String(), err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not exit after container removal")
	}
}
