package translator

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const containerSignalExitCodeAnnotation = "dink.io/signal-exit-code"

func (d *Docker) ContainerKill(ctx context.Context, name, signal string) error {
	if signal != "" && signal != "KILL" && signal != "SIGKILL" && signal != "INT" && signal != "SIGINT" {
		return httpx.BadRequest(fmt.Errorf("container signal %q is not supported by the Kubernetes backend", signal))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	deployment.Spec.Replicas = new(int32(0))
	if deployment.Annotations == nil {
		deployment.Annotations = make(map[string]string)
	}
	code := "137"
	if signal == "INT" || signal == "SIGINT" {
		code = "130"
	}
	deployment.Annotations[containerSignalExitCodeAnnotation] = code
	_, err = d.k8s.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	return kubeError(err)
}

func (d *Docker) ContainerPause(context.Context, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerRename(context.Context, string, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerRestart(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	return d.setContainerReplicas(ctx, name, 1, time.Now().UTC().Format(time.RFC3339Nano))
}

func (d *Docker) ContainerRm(ctx context.Context, name string, config *backend.ContainerRmConfig) error {
	if config != nil && (config.RemoveVolume || config.RemoveLink) {
		return httpx.BadRequest(fmt.Errorf("volume and link removal are not supported by the Kubernetes backend"))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	if (config == nil || !config.ForceRemove) && (deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0) {
		return httpx.Conflict(fmt.Errorf("cannot remove container %s: container is running: stop the container before removing or force remove", name))
	}
	uid := deployment.UID
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	if config != nil && config.ForceRemove {
		zero := int64(0)
		options.GracePeriodSeconds = &zero
	}
	if err := d.k8s.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, options); err != nil {
		return kubeError(err)
	}
	services := d.k8s.CoreV1().Services(deployment.Namespace)
	for _, serviceName := range []string{deployment.Name, publishedPortsServiceName(deployment.Name)} {
		if err := services.Delete(ctx, serviceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return kubeError(err)
		}
	}
	d.execMu.Lock()
	for execID, entry := range d.execs {
		if entry.namespace == deployment.Namespace && entry.containerID == identity.DockerIDFromUID(deployment.UID) {
			delete(d.execs, execID)
		}
	}
	d.execMu.Unlock()
	return nil
}

func (d *Docker) ContainerStart(ctx context.Context, name, checkpoint, checkpointDir string) error {
	if checkpoint != "" || checkpointDir != "" {
		return httpx.BadRequest(fmt.Errorf("container checkpoints are not supported by the Kubernetes backend"))
	}
	return d.setContainerReplicas(ctx, name, 1, "")
}

func (d *Docker) ContainerStop(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	return d.setContainerReplicas(ctx, name, 0, "")
}

func (d *Docker) ContainerUnpause(context.Context, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerUpdate(context.Context, string, *container.HostConfig) (container.UpdateResponse, error) {
	return container.UpdateResponse{}, ErrNotImplemented
}

func (d *Docker) ContainerWait(ctx context.Context, name string, condition container.WaitCondition) (container.WaitResponse, error) {
	switch condition {
	case container.WaitConditionNotRunning, container.WaitConditionNextExit, container.WaitConditionRemoved:
	default:
		return container.WaitResponse{}, httpx.BadRequest(fmt.Errorf("invalid wait condition %q", condition))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return container.WaitResponse{}, err
	}
	autoRemove := false
	if condition == container.WaitConditionRemoved {
		_, hostConfig, err := containerMetadata(deployment)
		if err != nil {
			return container.WaitResponse{}, err
		}
		autoRemove = hostConfig.AutoRemove
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	seenRunning := false
	firstCheck := true
	var removedExitCode *int32
	for {
		current, err := d.k8s.AppsV1().Deployments(deployment.Namespace).Get(ctx, deployment.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if removedExitCode != nil {
				return container.WaitResponse{StatusCode: int64(*removedExitCode)}, nil
			}
			return container.WaitResponse{StatusCode: 0}, nil
		}
		if err != nil {
			return container.WaitResponse{}, kubeError(err)
		}
		if current.UID != deployment.UID {
			if removedExitCode != nil {
				return container.WaitResponse{StatusCode: int64(*removedExitCode)}, nil
			}
			return container.WaitResponse{StatusCode: 0}, nil
		}
		if removedExitCode == nil && (condition != container.WaitConditionRemoved || autoRemove) {
			pods, err := d.containerPods(ctx, deployment)
			if err != nil {
				return container.WaitResponse{}, err
			}
			running := false
			var exitCode *int32
			for index := range pods {
				pod := &pods[index]
				for _, status := range pod.Status.ContainerStatuses {
					if status.State.Terminated != nil && exitCode == nil {
						code := status.State.Terminated.ExitCode
						exitCode = &code
					} else if (status.State.Running == nil || autoRemove) && status.LastTerminationState.Terminated != nil && exitCode == nil {
						code := status.LastTerminationState.Terminated.ExitCode
						exitCode = &code
					}
					if status.State.Running != nil {
						running = true
					}
				}
				if pod.Status.Phase == corev1.PodRunning && len(pod.Status.ContainerStatuses) == 0 {
					running = true
				}
			}
			if running {
				seenRunning = true
			}
			if autoRemove && exitCode == nil && !running && current.Spec.Replicas != nil && *current.Spec.Replicas == 0 {
				if code, err := strconv.ParseInt(current.Annotations[containerSignalExitCodeAnnotation], 10, 32); err == nil {
					status := int32(code)
					exitCode = &status
				}
			}
			if autoRemove && exitCode != nil {
				if err := d.ContainerRm(ctx, deployment.Name, &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
					return container.WaitResponse{}, err
				}
				removedExitCode = exitCode
			}
			stopped := current.Spec.Replicas != nil && *current.Spec.Replicas == 0 && !running
			if condition == container.WaitConditionNotRunning && (exitCode != nil || stopped) ||
				condition == container.WaitConditionNextExit && !firstCheck && seenRunning && (exitCode != nil || stopped) {
				if exitCode == nil {
					return container.WaitResponse{}, httpx.NewHTTPError(501, fmt.Errorf("container exit status is no longer available from Kubernetes"))
				}
				return container.WaitResponse{StatusCode: int64(*exitCode)}, nil
			}
		}
		firstCheck = false
		select {
		case <-ctx.Done():
			return container.WaitResponse{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (d *Docker) setContainerReplicas(ctx context.Context, name string, replicas int32, restartAt string) error {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	deployment.Spec.Replicas = &replicas
	if replicas > 0 {
		delete(deployment.Annotations, containerSignalExitCodeAnnotation)
	}
	if restartAt != "" {
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations["dink.io/restarted-at"] = restartAt
	}
	_, err = d.k8s.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	return kubeError(err)
}

func validateStopOptions(options backend.ContainerStopOptions) error {
	if options.Signal != "" || options.Timeout != nil {
		return httpx.BadRequest(fmt.Errorf("custom signal and timeout are not supported by the Kubernetes backend"))
	}
	return nil
}
