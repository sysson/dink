package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

type statsSampleStream chan []byte

func (w statsSampleStream) Write(value []byte) (int, error) {
	w <- append([]byte(nil), value...)
	return len(value), nil
}

func TestContainerStats(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	metricsClient := metricsfake.NewSimpleClientset()
	metric := &metricsv1beta1.PodMetrics{
		APIVersion: "metrics.k8s.io/v1beta1", Kind: "PodMetrics",
		Name: "web-pod", Namespace: "tenant",
		Timestamp: metav1.NewTime(time.Unix(100, 0)), Window: metav1.Duration{Duration: 2 * time.Second},
		Containers: []metricsv1beta1.ContainerMetrics{{Name: "web", Usage: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
		}}},
	}
	if err := metricsClient.Tracker().Create(metricsv1beta1.SchemeGroupVersion.WithResource("pods"), metric, "tenant"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client, MetricsV1beta1Interface: metricsClient.MetricsV1beta1()}}
	var output bytes.Buffer
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatal(err)
	}
	var stats container.StatsResponse
	if err := json.Unmarshal(output.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.CPUStats.CPUUsage.TotalUsage != 500_000_000 || stats.MemoryStats.Usage != 64<<20 || stats.MemoryStats.Limit != 128<<20 {
		t.Fatalf("unexpected CPU/memory stats: %+v", stats)
	}
	output.Reset()
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{Stream: true, OneShot: true, OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatalf("one-shot stats: %v", err)
	}
	if bytes.Count(output.Bytes(), []byte(`"read"`)) != 1 {
		t.Fatalf("one-shot stats output = %q", output.String())
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	samples := make(statsSampleStream, 2)
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- docker.ContainerStats(streamCtx, "web", &backend.ContainerStatsConfig{
			Stream: true, OutStream: func() io.Writer { return samples },
		})
	}()
	streamed := make([]container.StatsResponse, 0, 2)
	for range 2 {
		select {
		case sample := <-samples:
			var stats container.StatsResponse
			if err := json.Unmarshal(sample, &stats); err != nil {
				t.Fatal(err)
			}
			streamed = append(streamed, stats)
		case <-time.After(5 * time.Second):
			cancelStream()
			t.Fatal("stats stream did not emit a fresh sample for an unchanged Metrics Server timestamp")
		}
	}
	cancelStream()
	if err := <-streamDone; err != nil {
		t.Fatalf("cancelled stats stream: %v", err)
	}
	if !streamed[1].Read.After(streamed[0].Read) || streamed[1].CPUStats.CPUUsage.TotalUsage <= streamed[0].CPUStats.CPUUsage.TotalUsage {
		t.Fatalf("stats stream did not advance timestamps/counters: %+v then %+v", streamed[0], streamed[1])
	}
	output.Reset()
	docker.k8s.MetricsV1beta1Interface = nil
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatalf("missing metrics API returned an error: %v", err)
	}
	stats = container.StatsResponse{}
	if err := json.Unmarshal(output.Bytes(), &stats); err != nil {
		t.Fatalf("missing metrics API did not return a stats sample: %v", err)
	}
	if stats.MemoryStats.Usage != 0 || stats.CPUStats.CPUUsage.TotalUsage != 0 || stats.MemoryStats.Limit != 128<<20 {
		t.Fatalf("unexpected fallback stats: %+v", stats)
	}
	output.Reset()
	docker.k8s.MetricsV1beta1Interface = metricsfake.NewSimpleClientset().MetricsV1beta1()
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatalf("missing PodMetrics returned an error: %v", err)
	}
	stats = container.StatsResponse{}
	if err := json.Unmarshal(output.Bytes(), &stats); err != nil || stats.MemoryStats.Usage != 0 {
		t.Fatalf("missing PodMetrics fallback = %+v, err = %v", stats, err)
	}
}
