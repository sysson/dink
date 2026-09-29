package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) ContainerStats(ctx context.Context, name string, config *backend.ContainerStatsConfig) error {
	if config == nil || config.OutStream == nil {
		return InvalidArgument(fmt.Errorf("stats output is required"))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	if d.k8s.MetricsV1Interface == nil {
		return Unavailable(fmt.Errorf("kubernetes metrics API is unavailable"))
	}
	var previous container.StatsResponse
	var previousPod string
	var encoder *json.Encoder
	var output io.Writer
	interval := time.NewTicker(2 * time.Second)
	defer interval.Stop()
	for {
		pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
		if err != nil {
			return kubeError(err)
		}
		var pod *corev1.Pod
		for index := range pods.Items {
			if pods.Items[index].Status.Phase == corev1.PodRunning {
				pod = &pods.Items[index]
				break
			}
		}
		if pod == nil || len(pod.Spec.Containers) == 0 {
			return NotFound(fmt.Errorf("no running pod found for container %s", name))
		}
		metrics, err := d.k8s.PodMetricses(deployment.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) {
				return Unavailable(fmt.Errorf("pod metrics unavailable for %s: %w", name, err))
			}
			return kubeError(err)
		}
		containerName := pod.Spec.Containers[0].Name
		var sample *corev1.ResourceList
		for index := range metrics.Containers {
			if metrics.Containers[index].Name == containerName {
				sample = &metrics.Containers[index].Usage
				break
			}
		}
		if sample == nil {
			return Unavailable(fmt.Errorf("metrics unavailable for container %s", containerName))
		}
		if pod.Name != previousPod {
			previous = container.StatsResponse{}
			previousPod = pod.Name
		}
		if config.Stream && !config.OneShot && !previous.Read.IsZero() && !metrics.Timestamp.After(previous.Read) {
			select {
			case <-ctx.Done():
				return nil
			case <-interval.C:
				continue
			}
		}
		window := metrics.Window.Duration
		if window <= 0 {
			window = time.Second
		}
		elapsed := window
		if !previous.Read.IsZero() {
			elapsed = metrics.Timestamp.Sub(previous.Read)
			if elapsed <= 0 {
				elapsed = window
			}
		}
		stats := container.StatsResponse{
			ID: identity.DockerIDFromUID(deployment.UID), Name: deployment.Name, OSType: "linux",
			Read: metrics.Timestamp.Time, PreRead: previous.Read, PreCPUStats: previous.CPUStats,
		}
		if cpu := (*sample).Cpu(); cpu != nil {
			stats.CPUStats.CPUUsage.TotalUsage = previous.CPUStats.CPUUsage.TotalUsage + uint64(float64(cpu.MilliValue())*1e6*elapsed.Seconds())
			stats.CPUStats.SystemUsage = previous.CPUStats.SystemUsage + uint64(elapsed.Nanoseconds())
			stats.CPUStats.OnlineCPUs = 1
		}
		if memory := (*sample).Memory(); memory != nil {
			stats.MemoryStats.Usage = uint64(memory.Value())
		}
		if limit := pod.Spec.Containers[0].Resources.Limits.Memory(); limit != nil {
			stats.MemoryStats.Limit = uint64(limit.Value())
		}
		if encoder == nil {
			output = config.OutStream()
			encoder = json.NewEncoder(output)
		}
		if err := encoder.Encode(stats); err != nil {
			return err
		}
		if flusher, ok := output.(interface{ Flush() }); ok {
			flusher.Flush()
		}
		previous = stats
		if !config.Stream || config.OneShot {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-interval.C:
		}
	}
}
