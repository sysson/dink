package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const containerSignalExitCodeAnnotation = "dink.io/signal-exit-code"

func (d *Docker) ContainerKill(ctx context.Context, name, signal string) error {
	if signal != "" && signal != "KILL" && signal != "SIGKILL" && signal != "INT" && signal != "SIGINT" {
		return InvalidArgument(fmt.Errorf("container signal %q is not supported by the Kubernetes backend", signal))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	code := "137"
	if signal == "INT" || signal == "SIGINT" {
		code = "130"
	}
	_, err = d.updateContainerDeployment(ctx, deployment, func(current *appsv1.Deployment) error {
		current.Spec.Replicas = new(int32(0))
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[containerSignalExitCodeAnnotation] = code
		return nil
	})
	return err
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
		return InvalidArgument(fmt.Errorf("volume and link removal are not supported by the Kubernetes backend"))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	if (config == nil || !config.ForceRemove) && (deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0) {
		return Conflict(fmt.Errorf("cannot remove container %s: container is running: stop the container before removing or force remove", name))
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
		return InvalidArgument(fmt.Errorf("container checkpoints are not supported by the Kubernetes backend"))
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

func (d *Docker) ContainerUpdate(ctx context.Context, name string, update *container.UpdateConfig) (container.UpdateResponse, error) {
	if update == nil {
		return container.UpdateResponse{}, InvalidArgument(fmt.Errorf("container update configuration is required"))
	}
	if update.RestartPolicy.Name != "" || update.RestartPolicy.MaximumRetryCount != 0 {
		return container.UpdateResponse{}, InvalidArgument(fmt.Errorf("updating restart policy is not supported by the Kubernetes backend"))
	}
	resources, err := containerResources(&container.HostConfig{Resources: update.Resources})
	if err != nil {
		return container.UpdateResponse{}, InvalidArgument(err)
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return container.UpdateResponse{}, err
	}
	updated, err := d.updateContainerDeployment(ctx, deployment, func(current *appsv1.Deployment) error {
		if len(current.Spec.Template.Spec.Containers) == 0 {
			return Conflict(fmt.Errorf("container %s has no pod template", name))
		}
		_, hostConfig, err := containerMetadata(current)
		if err != nil {
			return err
		}
		hostConfig.Resources = update.Resources
		metadata, err := json.Marshal(hostConfig)
		if err != nil {
			return fmt.Errorf("encoding updated host config: %w", err)
		}
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[containerHostConfigAnnotation] = string(metadata)
		current.Spec.Template.Spec.Containers[0].Resources = resources
		return nil
	})
	if err != nil {
		return container.UpdateResponse{}, err
	}
	response := container.UpdateResponse{}
	if updated.Spec.Replicas != nil && *updated.Spec.Replicas > 0 {
		response.Warnings = []string{"Applying resource updates replaces the Kubernetes Pod."}
	}
	return response, nil
}

func (d *Docker) ContainerWait(ctx context.Context, name string, condition container.WaitCondition) (container.WaitResponse, error) {
	switch condition {
	case container.WaitConditionNotRunning, container.WaitConditionNextExit, container.WaitConditionRemoved:
	default:
		return container.WaitResponse{}, InvalidArgument(fmt.Errorf("invalid wait condition %q", condition))
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
					return container.WaitResponse{}, Unsupported(fmt.Errorf("container exit status is no longer available from Kubernetes"))
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
	_, err = d.updateContainerDeployment(ctx, deployment, func(current *appsv1.Deployment) error {
		current.Spec.Replicas = &replicas
		if replicas > 0 {
			delete(current.Annotations, containerSignalExitCodeAnnotation)
		}
		if restartAt != "" {
			if current.Spec.Template.Annotations == nil {
				current.Spec.Template.Annotations = make(map[string]string)
			}
			current.Spec.Template.Annotations["dink.io/restarted-at"] = restartAt
		}
		return nil
	})
	return err
}

func (d *Docker) updateContainerDeployment(ctx context.Context, original *appsv1.Deployment, mutate func(*appsv1.Deployment) error) (*appsv1.Deployment, error) {
	var updated *appsv1.Deployment
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := d.k8s.AppsV1().Deployments(original.Namespace).Get(ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != original.UID {
			return apierrors.NewNotFound(appsv1.Resource("deployments"), original.Name)
		}
		if err := mutate(current); err != nil {
			return err
		}
		updated, err = d.k8s.AppsV1().Deployments(current.Namespace).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, kubeError(err)
	}
	return updated, nil
}

func validateStopOptions(options backend.ContainerStopOptions) error {
	if options.Signal != "" || options.Timeout != nil {
		return InvalidArgument(fmt.Errorf("custom signal and timeout are not supported by the Kubernetes backend"))
	}
	return nil
}
