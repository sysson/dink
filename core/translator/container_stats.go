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
	deployment, err := d.findContainer(ctx, name)
	if err != nil {
		return err
	}
	var previous container.StatsResponse
	var previousPod string
	var encoder *json.Encoder
	var output io.Writer
	interval := time.NewTicker(2 * time.Second)
	defer interval.Stop()
	for {
		current, err := d.refreshWorkload(ctx, deployment)
		if err != nil {
			return kubeError(err)
		}
		deployment = current
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
		podContainer, err := namedContainer(&pod.Spec, deployment.Name)
		if err != nil {
			return err
		}
		read := time.Now().UTC()
		window := time.Second
		sample := corev1.ResourceList{}
		if d.k8s.MetricsV1beta1Interface != nil {
			metrics, err := d.k8s.PodMetricses(deployment.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsServiceUnavailable(err) {
				return kubeError(err)
			}
			if err == nil {
				containerName := podContainer.Name
				for index := range metrics.Containers {
					if metrics.Containers[index].Name == containerName {
						sample = metrics.Containers[index].Usage
						break
					}
				}
				if (!config.Stream || config.OneShot) && !metrics.Timestamp.IsZero() {
					read = metrics.Timestamp.Time
				}
				if metrics.Window.Duration > 0 {
					window = metrics.Window.Duration
				}
			}
		}
		if pod.Name != previousPod {
			previous = container.StatsResponse{}
			previousPod = pod.Name
		}
		elapsed := window
		if !previous.Read.IsZero() {
			elapsed = read.Sub(previous.Read)
			if elapsed <= 0 {
				elapsed = window
			}
		}
		stats := container.StatsResponse{
			ID: identity.DockerIDFromUID(deployment.UID), Name: deployment.dockerName(), OSType: "linux",
			Read: read, PreRead: previous.Read, PreCPUStats: previous.CPUStats,
		}
		if cpu := sample.Cpu(); cpu != nil {
			stats.CPUStats.CPUUsage.TotalUsage = previous.CPUStats.CPUUsage.TotalUsage + uint64(float64(cpu.MilliValue())*1e6*elapsed.Seconds())
		}
		stats.CPUStats.SystemUsage = previous.CPUStats.SystemUsage + uint64(elapsed.Nanoseconds())
		stats.CPUStats.OnlineCPUs = 1
		if memory := sample.Memory(); memory != nil {
			stats.MemoryStats.Usage = uint64(memory.Value())
		}
		if limit := podContainer.Resources.Limits.Memory(); limit != nil {
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
