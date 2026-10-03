package translator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestContainerLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/apps/v1/namespaces/tenant/deployments/web":
			_ = json.NewEncoder(w).Encode(&appsv1.Deployment{
				APIVersion: "apps/v1", Kind: "Deployment",
				Name: "web", Namespace: "tenant",
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "injected-sidecar", TTY: true}, {Name: "web"}}}}},
			})
		case "/api/v1/namespaces/tenant/pods":
			if r.URL.Query().Get("labelSelector") != "app=web" {
				t.Errorf("label selector = %q", r.URL.Query().Get("labelSelector"))
			}
			_ = json.NewEncoder(w).Encode(&corev1.PodList{
				APIVersion: "v1", Kind: "PodList",
				Items: []corev1.Pod{{Name: "web-pod", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "injected-sidecar", TTY: true}, {Name: "web"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}},
			})
		case "/api/v1/namespaces/tenant/pods/web-pod/log":
			if r.URL.Query().Get("container") != "web" || r.URL.Query().Get("tailLines") != "2" || r.URL.Query().Get("timestamps") != "true" {
				t.Errorf("log query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("2026-01-01T00:00:00Z hello\n"))
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
	messages, tty, err := docker.ContainerLogs(ctx, "web", &backend.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "2"})
	if err != nil {
		t.Fatal(err)
	}
	message, ok := <-messages
	if !ok || tty || message.Source != "stdout" || string(message.Line) != "hello\n" {
		t.Fatalf("message = %+v, ok = %v, tty = %v", message, ok, tty)
	}
	if _, ok := <-messages; ok {
		t.Fatal("unexpected second log message")
	}
}
