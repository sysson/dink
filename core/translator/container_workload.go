package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// containerWorkload is the Kubernetes object behind a Docker container: a
// Deployment, or a Job for one-shot --rm containers.
type containerWorkload struct {
	metav1.ObjectMeta
	OneShot  bool
	Template corev1.PodTemplateSpec
	// Started is whether Kubernetes has been asked to run the container.
	Started bool
	// Finished is set once a Job's Pod has exited.
	Finished bool
}

// oneShotRemovalDelay keeps a finished --rm Job long enough for an attached client to read its exit code.
const oneShotRemovalDelay = int32(10)

func deploymentWorkload(deployment *appsv1.Deployment) *containerWorkload {
	return &containerWorkload{
		ObjectMeta: deployment.ObjectMeta,
		Template:   deployment.Spec.Template,
		Started:    deployment.Spec.Replicas == nil || *deployment.Spec.Replicas > 0,
	}
}

func jobWorkload(job *batchv1.Job) *containerWorkload {
	finished := false
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			finished = true
		}
	}
	return &containerWorkload{
		ObjectMeta: job.ObjectMeta,
		OneShot:    true,
		Template:   job.Spec.Template,
		Started:    (job.Spec.Suspend == nil || !*job.Spec.Suspend) && job.DeletionTimestamp == nil,
		Finished:   finished,
	}
}

func (w *containerWorkload) ownerReference() metav1.OwnerReference {
	reference := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: w.Name, UID: w.UID, Controller: new(true)}
	if w.OneShot {
		reference.APIVersion, reference.Kind = "batch/v1", "Job"
	}
	return reference
}

func listWorkloads(ctx context.Context, client kubernetes.Interface, namespace string) ([]*containerWorkload, error) {
	deployments, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	jobs, err := client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	workloads := make([]*containerWorkload, 0, len(deployments.Items)+len(jobs.Items))
	for index := range deployments.Items {
		workloads = append(workloads, deploymentWorkload(&deployments.Items[index]))
	}
	for index := range jobs.Items {
		if isContainerJob(&jobs.Items[index]) {
			workloads = append(workloads, jobWorkload(&jobs.Items[index]))
		}
	}
	return workloads, nil
}

// isContainerJob skips Jobs in the namespace that Dink did not create.
func isContainerJob(job *batchv1.Job) bool {
	return job.Annotations[containerConfigAnnotation] != ""
}

func (d *Docker) findContainer(ctx context.Context, nameOrID string) (*containerWorkload, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if nameOrID == "" {
		return nil, InvalidArgument(fmt.Errorf("container name or ID is required"))
	}
	if deployment, err := d.k8s.AppsV1().Deployments(id.Namespace).Get(ctx, nameOrID, metav1.GetOptions{}); err == nil {
		return deploymentWorkload(deployment), nil
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}
	if job, err := d.k8s.BatchV1().Jobs(id.Namespace).Get(ctx, nameOrID, metav1.GetOptions{}); err == nil && isContainerJob(job) {
		return jobWorkload(job), nil
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}

	workloads, err := listWorkloads(ctx, d.k8s, id.Namespace)
	if err != nil {
		return nil, err
	}
	var match *containerWorkload
	for _, workload := range workloads {
		dockerID := identity.DockerIDFromUID(workload.UID)
		if dockerID == "" || !strings.HasPrefix(dockerID, nameOrID) {
			continue
		}
		if match != nil {
			return nil, Conflict(fmt.Errorf("container ID %s is ambiguous", nameOrID))
		}
		match = workload
	}
	if match == nil {
		return nil, NotFound(fmt.Errorf("container %s not found", nameOrID))
	}
	return match, nil
}

// refreshWorkload re-reads w, returning a Kubernetes not-found error once it is gone or replaced.
func (d *Docker) refreshWorkload(ctx context.Context, w *containerWorkload) (*containerWorkload, error) {
	var current *containerWorkload
	if w.OneShot {
		job, err := d.k8s.BatchV1().Jobs(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		current = jobWorkload(job)
	} else {
		deployment, err := d.k8s.AppsV1().Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		current = deploymentWorkload(deployment)
	}
	if current.UID != w.UID {
		return nil, apierrors.NewNotFound(appsv1.Resource("deployments"), w.Name)
	}
	return current, nil
}

func (d *Docker) deleteWorkload(ctx context.Context, w *containerWorkload, force bool) error {
	if w.DeletionTimestamp != nil {
		return nil
	}
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &w.UID}}
	if force {
		options.GracePeriodSeconds = new(int64(0))
	}
	var err error
	if w.OneShot {
		// Jobs orphan their Pods by default.
		options.PropagationPolicy = new(metav1.DeletePropagationBackground)
		err = d.k8s.BatchV1().Jobs(w.Namespace).Delete(ctx, w.Name, options)
	} else {
		err = d.k8s.AppsV1().Deployments(w.Namespace).Delete(ctx, w.Name, options)
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	return nil
}

func (d *Docker) containerPods(ctx context.Context, w *containerWorkload) ([]corev1.Pod, error) {
	pods, err := d.k8s.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + w.Name})
	if err != nil {
		return nil, kubeError(err)
	}
	var owned []corev1.Pod
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if len(pod.OwnerReferences) == 0 {
			if w.CreationTimestamp.IsZero() && pod.CreationTimestamp.IsZero() {
				owned = append(owned, pod)
			}
			continue
		}
		for _, owner := range pod.OwnerReferences {
			if w.OneShot {
				if owner.Kind == "Job" && owner.UID == w.UID {
					owned = append(owned, pod)
					break
				}
				continue
			}
			if owner.Kind != "ReplicaSet" {
				continue
			}
			replicaSet, err := d.k8s.AppsV1().ReplicaSets(w.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, kubeError(err)
			}
			if replicaSet.UID != owner.UID {
				continue
			}
			for _, parent := range replicaSet.OwnerReferences {
				if parent.Kind == "Deployment" && parent.UID == w.UID {
					owned = append(owned, pod)
					break
				}
			}
			break
		}
	}
	return owned, nil
}

func containerMetadata(w *containerWorkload) (*container.Config, *container.HostConfig, error) {
	config := &container.Config{}
	if value := w.Annotations[containerConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), config); err != nil {
			return nil, nil, fmt.Errorf("decoding container config for %s: %w", w.Name, err)
		}
	}
	hostConfig := &container.HostConfig{}
	if value := w.Annotations[containerHostConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), hostConfig); err != nil {
			return nil, nil, fmt.Errorf("decoding host config for %s: %w", w.Name, err)
		}
	}
	return config, hostConfig, nil
}
