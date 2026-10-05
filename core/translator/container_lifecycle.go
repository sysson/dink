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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const containerSignalExitCodeAnnotation = "dink.io/signal-exit-code"

func (d *Docker) ContainerKill(ctx context.Context, name, signal string) error {
	if signal != "" && signal != "KILL" && signal != "SIGKILL" && signal != "INT" && signal != "SIGINT" {
		return InvalidArgument(fmt.Errorf("container signal %q is not supported by the Kubernetes backend", signal))
	}
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	code := "137"
	if signal == "INT" || signal == "SIGINT" {
		code = "130"
	}
	if deployment.OneShot {
		return d.stopOneShot(ctx, deployment, code)
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

func (d *Docker) ContainerRestart(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if deployment.OneShot {
		return Unsupported(fmt.Errorf("restarting a --rm container is not supported by the Kubernetes backend"))
	}
	if err := d.setContainerReplicas(ctx, deployment, 0, ""); err != nil {
		return err
	}
	if err := d.waitContainerStopped(ctx, deployment); err != nil {
		return err
	}
	if err := d.claimContainerAliases(ctx, deployment); err != nil {
		return err
	}
	return d.setContainerReplicas(ctx, deployment, 1, time.Now().UTC().Format(time.RFC3339Nano))
}

func (d *Docker) ContainerRm(ctx context.Context, name string, config *backend.ContainerRmConfig) error {
	if config != nil && config.RemoveLink {
		return InvalidArgument(fmt.Errorf("link removal is not supported by the Kubernetes backend"))
	}
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	force := config != nil && config.ForceRemove
	if !force && deployment.Started && !deployment.Finished {
		return Conflict(fmt.Errorf("cannot remove container %s: container is running: stop the container before removing or force remove", name))
	}
	if err := d.deleteWorkload(ctx, deployment, force); err != nil {
		return err
	}
	if err := d.waitContainerStopped(ctx, deployment); err != nil {
		return err
	}
	d.containerNameMu.Lock()
	defer d.containerNameMu.Unlock()
	if err := d.removeContainerServices(ctx, deployment); err != nil {
		return err
	}
	d.execMu.Lock()
	for execID, entry := range d.execs {
		if entry.namespace == deployment.Namespace && entry.containerID == identity.DockerIDFromUID(deployment.UID) {
			delete(d.execs, execID)
		}
	}
	d.execMu.Unlock()
	if config != nil && config.RemoveVolume {
		return d.removeAnonymousVolumes(ctx, deployment)
	}
	return nil
}

// removeAnonymousVolumes mirrors Docker: only anonymous volumes not used by another container are removed.
func (d *Docker) removeAnonymousVolumes(ctx context.Context, removed *containerWorkload) error {
	workloads, err := listWorkloads(ctx, d.k8s, removed.Namespace)
	if err != nil {
		return err
	}
	inUse := make(map[string]bool)
	for _, workload := range workloads {
		if workload.UID == removed.UID {
			continue
		}
		for _, volume := range workload.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				inUse[volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	claims := d.k8s.CoreV1().PersistentVolumeClaims(removed.Namespace)
	for _, volume := range removed.Template.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil || inUse[volume.PersistentVolumeClaim.ClaimName] {
			continue
		}
		pvc, err := claims.Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return kubeError(err)
		}
		if pvc.Labels[volumeManagedLabel] != volumeManagedValue {
			continue
		}
		dockerVolume, err := volumeFromPVC(pvc)
		if err != nil {
			return err
		}
		if _, anonymous := dockerVolume.Labels[anonymousVolumeLabel]; !anonymous {
			continue
		}
		// The PVC protection finalizer defers deletion until the terminating Pod releases it.
		if err := claims.Delete(ctx, pvc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return kubeError(err)
		}
	}
	return nil
}

func (d *Docker) ContainerStart(ctx context.Context, name, checkpoint, checkpointDir string) error {
	if checkpoint != "" || checkpointDir != "" {
		return InvalidArgument(fmt.Errorf("container checkpoints are not supported by the Kubernetes backend"))
	}
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if !deployment.Started {
		if err := d.waitContainerStopped(ctx, deployment); err != nil {
			return err
		}
	}
	if err := d.claimContainerAliases(ctx, deployment); err != nil {
		return err
	}
	if deployment.OneShot {
		return d.updateContainerJob(ctx, deployment, func(current *batchv1.Job) error {
			current.Spec.Suspend = new(false)
			return nil
		})
	}
	return d.setContainerReplicas(ctx, deployment, 1, "")
}

func (d *Docker) ContainerStop(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if deployment.OneShot {
		return d.stopOneShot(ctx, deployment, "143")
	}
	if err := d.setContainerReplicas(ctx, deployment, 0, ""); err != nil {
		return err
	}
	return d.waitContainerStopped(ctx, deployment)
}

// stopOneShot removes a --rm container, as Docker does when one stops, keeping the exit code visible to waiters while its Pod terminates.
func (d *Docker) stopOneShot(ctx context.Context, w *containerWorkload, exitCode string) error {
	if !w.Started {
		return nil
	}
	if err := d.updateContainerJob(ctx, w, func(current *batchv1.Job) error {
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[containerSignalExitCodeAnnotation] = exitCode
		return nil
	}); err != nil {
		return err
	}
	err := d.k8s.BatchV1().Jobs(w.Namespace).Delete(ctx, w.Name, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &w.UID},
		PropagationPolicy: new(metav1.DeletePropagationForeground),
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	return d.waitContainerStopped(ctx, w)
}

func (d *Docker) waitContainerStopped(ctx context.Context, workload *containerWorkload) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		controllerStopped := workload.OneShot
		if !workload.OneShot {
			current, err := d.k8s.AppsV1().Deployments(workload.Namespace).Get(ctx, workload.Name, metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return kubeError(err)
			}
			controllerStopped = apierrors.IsNotFound(err)
			if err == nil {
				if current.UID != workload.UID {
					return Conflict(fmt.Errorf("container %s was replaced while stopping", workload.dockerName()))
				}
				controllerStopped = (current.Spec.Replicas == nil || *current.Spec.Replicas == 0) &&
					current.Status.ObservedGeneration >= current.Generation && current.Status.Replicas == 0
			}
		}
		pods, err := d.k8s.CoreV1().Pods(workload.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + workload.Name})
		if err != nil {
			return kubeError(err)
		}
		active := false
		for _, pod := range pods.Items {
			if pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				active = true
				break
			}
		}
		if controllerStopped && !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for container %s to stop: %w", workload.dockerName(), ctx.Err())
		case <-ticker.C:
		}
	}
}

func (d *Docker) hasActivePod(ctx context.Context, w *containerWorkload) (bool, error) {
	pods, err := d.k8s.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + w.Name})
	if err != nil {
		return false, kubeError(err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning || pod.Status.Phase == corev1.PodPending {
			return true, nil
		}
	}
	return false, nil
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
	deployment, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return container.UpdateResponse{}, err
	}
	defer unlock()
	if deployment.OneShot {
		return container.UpdateResponse{}, Unsupported(fmt.Errorf("updating a --rm container is not supported by the Kubernetes backend"))
	}
	updated, err := d.updateContainerDeployment(ctx, deployment, func(current *appsv1.Deployment) error {
		if len(current.Spec.Template.Spec.Containers) == 0 {
			return Conflict(fmt.Errorf("container %s has no pod template", name))
		}
		_, hostConfig, err := containerMetadata(deploymentWorkload(current))
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
	deployment, err := d.findContainer(ctx, name)
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
		current, err := d.refreshWorkload(ctx, deployment)
		if apierrors.IsNotFound(err) {
			if removedExitCode != nil {
				return container.WaitResponse{StatusCode: int64(*removedExitCode)}, nil
			}
			return container.WaitResponse{StatusCode: 0}, nil
		}
		if err != nil {
			return container.WaitResponse{}, kubeError(err)
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
			if autoRemove && exitCode == nil && !running && !current.Started {
				if code, err := strconv.ParseInt(current.Annotations[containerSignalExitCodeAnnotation], 10, 32); err == nil {
					status := int32(code)
					exitCode = &status
				}
			}
			if autoRemove && exitCode != nil {
				if err := d.ContainerRm(ctx, identity.DockerIDFromUID(current.UID), &backend.ContainerRmConfig{ForceRemove: true, RemoveVolume: true}); err != nil {
					return container.WaitResponse{}, err
				}
				removedExitCode = exitCode
			}
			stopped := !current.Started && !running
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

func (d *Docker) setContainerReplicas(ctx context.Context, deployment *containerWorkload, replicas int32, restartAt string) error {
	_, err := d.updateContainerDeployment(ctx, deployment, func(current *appsv1.Deployment) error {
		current.Spec.Replicas = &replicas
		current.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
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

func (d *Docker) updateContainerDeployment(ctx context.Context, original *containerWorkload, mutate func(*appsv1.Deployment) error) (*appsv1.Deployment, error) {
	var updated *appsv1.Deployment
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := d.k8s.AppsV1().Deployments(original.Namespace).Get(ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != original.UID {
			return apierrors.NewNotFound(appsv1.Resource("deployments"), original.Name)
		}
		template := current.Spec.Template.DeepCopy()
		if err := mutate(current); err != nil {
			return err
		}
		if !apiequality.Semantic.DeepEqual(*template, current.Spec.Template) {
			current.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
		}
		updated, err = d.k8s.AppsV1().Deployments(current.Namespace).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return nil, kubeError(err)
	}
	return updated, nil
}

func (d *Docker) updateContainerJob(ctx context.Context, original *containerWorkload, mutate func(*batchv1.Job) error) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := d.k8s.BatchV1().Jobs(original.Namespace).Get(ctx, original.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.UID != original.UID {
			return apierrors.NewNotFound(batchv1.Resource("jobs"), original.Name)
		}
		if err := mutate(current); err != nil {
			return err
		}
		_, err = d.k8s.BatchV1().Jobs(current.Namespace).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	return kubeError(err)
}

func validateStopOptions(options backend.ContainerStopOptions) error {
	if options.Signal != "" || options.Timeout != nil {
		return InvalidArgument(fmt.Errorf("custom signal and timeout are not supported by the Kubernetes backend"))
	}
	return nil
}
