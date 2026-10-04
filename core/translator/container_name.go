package translator

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const containerNameAnnotation = "dink.io/docker-name"

var dockerContainerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func normalizeContainerName(name string) (string, error) {
	name = strings.TrimPrefix(name, "/")
	if !dockerContainerNamePattern.MatchString(name) {
		return "", InvalidArgument(fmt.Errorf("invalid container name %q: must match [a-zA-Z0-9][a-zA-Z0-9_.-]*", name))
	}
	return name, nil
}

func (w *containerWorkload) dockerName() string {
	if name := w.Annotations[containerNameAnnotation]; name != "" {
		return name
	}
	return w.Name
}

// DNS-valid names keep their readable Service name. Other Docker names get a
// collision-resistant DNS key; explicit valid aliases provide readable DNS.
func containerServiceName(name string) string {
	if len(validation.IsDNS1035Label(name)) == 0 {
		return name
	}
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("container-%x", hash[:20])
}

// The initial internal name may be readable, but it is never changed by rename.
// Reusing an old Docker name must not reuse the renamed container's workload.
func (d *Docker) containerObjectName(ctx context.Context, namespace, name string) (string, error) {
	candidate := containerServiceName(name)
	if _, err := d.k8s.AppsV1().Deployments(namespace).Get(ctx, candidate, metav1.GetOptions{}); err == nil {
		return generatedContainerName(), nil
	} else if !apierrors.IsNotFound(err) {
		return "", kubeError(err)
	}
	if _, err := d.k8s.BatchV1().Jobs(namespace).Get(ctx, candidate, metav1.GetOptions{}); err == nil {
		return generatedContainerName(), nil
	} else if !apierrors.IsNotFound(err) {
		return "", kubeError(err)
	}
	return candidate, nil
}

func workloadOwnsService(w *containerWorkload, service *corev1.Service) bool {
	owner := w.ownerReference()
	for _, reference := range service.OwnerReferences {
		if reference.UID == owner.UID && reference.Kind == owner.Kind && reference.Name == owner.Name {
			return service.Spec.Selector["app"] == w.Name
		}
	}
	return false
}

func (d *Docker) ensureContainerNameAvailable(ctx context.Context, namespace, name string, except *containerWorkload) error {
	workloads, err := listWorkloads(ctx, d.k8s, namespace)
	if err != nil {
		return err
	}
	for _, workload := range workloads {
		if except != nil && workload.Name == except.Name && workload.OneShot == except.OneShot && workload.UID == except.UID {
			continue
		}
		if workload.dockerName() == name {
			return Conflict(fmt.Errorf("the container name %q is already in use", name))
		}
	}
	service, err := d.k8s.CoreV1().Services(namespace).Get(ctx, containerServiceName(name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return kubeError(err)
	}
	if except != nil && workloadOwnsService(except, service) {
		return nil
	}
	return Conflict(fmt.Errorf("the container name %q is already in use by a container or network alias", name))
}

func (d *Docker) deleteOwnedService(ctx context.Context, workload *containerWorkload, service *corev1.Service) error {
	if !workloadOwnsService(workload, service) {
		return Conflict(fmt.Errorf("service %s is not owned by container %s", service.Name, workload.dockerName()))
	}
	err := d.k8s.CoreV1().Services(workload.Namespace).Delete(ctx, service.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &service.UID, ResourceVersion: &service.ResourceVersion},
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return kubeError(err)
}

func (d *Docker) removeContainerServices(ctx context.Context, workload *containerWorkload) error {
	services, err := d.k8s.CoreV1().Services(workload.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + workload.Name})
	if err != nil {
		return kubeError(err)
	}
	for index := range services.Items {
		service := &services.Items[index]
		if workloadOwnsService(workload, service) {
			if err := d.deleteOwnedService(ctx, workload, service); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Docker) ContainerRename(ctx context.Context, name, newName string) error {
	newName, err := normalizeContainerName(newName)
	if err != nil {
		return err
	}
	workload, unlock, err := d.lockContainer(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	d.containerNameMu.Lock()
	defer d.containerNameMu.Unlock()
	if err := d.ensureContainerNameAvailable(ctx, workload.Namespace, newName, workload); err != nil {
		return err
	}
	ports, err := workloadPublishedPorts(workload)
	if err != nil {
		return err
	}
	aliases, err := decodeContainerAliases(workload.Annotations)
	if err != nil {
		return err
	}
	renamed := *workload
	renamed.Annotations = make(map[string]string, len(workload.Annotations)+1)
	maps.Copy(renamed.Annotations, workload.Annotations)
	renamed.Annotations[containerNameAnnotation] = newName
	service := containerDNSService(&renamed, ports)
	services := d.k8s.CoreV1().Services(workload.Namespace)
	created, err := services.Create(ctx, service, metav1.CreateOptions{})
	createdNew := err == nil
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return kubeError(err)
	}
	if apierrors.IsAlreadyExists(err) {
		current, err := services.Get(ctx, service.Name, metav1.GetOptions{})
		if err != nil {
			return kubeError(err)
		}
		if !workloadOwnsService(workload, current) {
			return Conflict(fmt.Errorf("the container name %q is already in use", newName))
		}
	}
	oldName := workload.dockerName()
	// Compare the logical name too: resourceVersion retries must not overwrite a
	// rename that another Dink replica already completed.
	err = d.setContainerName(ctx, workload, oldName, newName)
	if err != nil {
		if createdNew {
			return errors.Join(err, d.deleteOwnedService(ctx, workload, created))
		}
		return err
	}
	current, err := services.Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil {
		return kubeError(err)
	}
	if !workloadOwnsService(workload, current) {
		return Conflict(fmt.Errorf("the container name %q is already in use", newName))
	}
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	current.Annotations[containerNameAnnotation] = newName
	delete(current.Labels, aliasOfLabel)
	if _, err := services.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	if err := d.applyAliasServices(ctx, &renamed, distinctAliases(aliases), ports); err != nil {
		return err
	}
	owned, err := services.List(ctx, metav1.ListOptions{LabelSelector: "app=" + workload.Name})
	if err != nil {
		return kubeError(err)
	}
	for index := range owned.Items {
		stale := &owned.Items[index]
		primary := stale.Annotations[containerNameAnnotation] != "" ||
			stale.Name == workload.Name && stale.Labels[aliasOfLabel] == ""
		if stale.Name != service.Name && primary &&
			workloadOwnsService(workload, stale) && !slices.Contains(distinctAliases(aliases), stale.Name) {
			if err := d.deleteOwnedService(ctx, workload, stale); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Docker) setContainerName(ctx context.Context, workload *containerWorkload, oldName, newName string) error {
	mutate := func(meta *metav1.ObjectMeta) error {
		currentName := meta.Annotations[containerNameAnnotation]
		if currentName == "" {
			currentName = meta.Name
		}
		if currentName == newName {
			return nil
		}
		if currentName != oldName {
			return Conflict(fmt.Errorf("container %s was concurrently renamed to %s", oldName, currentName))
		}
		if meta.Annotations == nil {
			meta.Annotations = map[string]string{}
		}
		meta.Annotations[containerNameAnnotation] = newName
		return nil
	}
	if workload.OneShot {
		return d.updateContainerJob(ctx, workload, func(current *batchv1.Job) error {
			return mutate(&current.ObjectMeta)
		})
	}
	_, err := d.updateContainerDeployment(ctx, workload, func(current *appsv1.Deployment) error {
		return mutate(&current.ObjectMeta)
	})
	return err
}
