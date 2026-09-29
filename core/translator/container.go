package translator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

func (d *Docker) streamPod(ctx context.Context, namespace, podName string, options corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
	if d.podStream != nil {
		return d.podStream(ctx, namespace, podName, options, streams, attach)
	}
	config := d.k8s.RESTConfig()
	if config == nil {
		return Unavailable(fmt.Errorf("kubernetes streaming config is unavailable"))
	}
	subresource := "exec"
	var params runtime.Object = &options
	if attach {
		subresource = "attach"
		params = &corev1.PodAttachOptions{Container: options.Container, Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr, TTY: options.TTY}
	}
	request := d.k8s.CoreV1().RESTClient().Post().Namespace(namespace).Resource("pods").Name(podName).SubResource(subresource).VersionedParams(params, scheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(config, "POST", request.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(ctx, streams)
}

func (d *Docker) containerPods(ctx context.Context, deployment *appsv1.Deployment) ([]corev1.Pod, error) {
	pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
	if err != nil {
		return nil, kubeError(err)
	}
	var owned []corev1.Pod
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if len(pod.OwnerReferences) == 0 {
			if deployment.CreationTimestamp.IsZero() && pod.CreationTimestamp.IsZero() {
				owned = append(owned, pod)
			}
			continue
		}
		for _, owner := range pod.OwnerReferences {
			if owner.Kind != "ReplicaSet" {
				continue
			}
			replicaSet, err := d.k8s.AppsV1().ReplicaSets(deployment.Namespace).Get(ctx, owner.Name, metav1.GetOptions{})
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
				if parent.Kind == "Deployment" && parent.UID == deployment.UID {
					owned = append(owned, pod)
					break
				}
			}
			break
		}
	}
	return owned, nil
}

func (d *Docker) ContainerArchivePath(context.Context, string, string) (io.ReadCloser, *container.PathStat, error) {
	return nil, nil, ErrNotImplemented
}

func (d *Docker) ContainerExport(context.Context, string, io.Writer) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerExtractToDir(context.Context, string, string, bool, bool, io.Reader) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerStatPath(context.Context, string, string) (*container.PathStat, error) {
	return nil, ErrNotImplemented
}

const tenantResourceDefaultsConfigMap = "dink-resource-defaults"

func (d *Docker) ContainerChanges(context.Context, string) ([]container.FilesystemChange, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) ContainerTop(context.Context, string, string) (*container.TopResponse, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) CreateImageFromContainer(context.Context, string, *backend.CreateImageConfig) (string, error) {
	return "", ErrNotImplemented
}

func (d *Docker) findDeployment(ctx context.Context, nameOrID string) (*appsv1.Deployment, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if nameOrID == "" {
		return nil, InvalidArgument(fmt.Errorf("container name or ID is required"))
	}
	deployments := d.k8s.AppsV1().Deployments(id.Namespace)
	if deployment, err := deployments.Get(ctx, nameOrID, metav1.GetOptions{}); err == nil {
		return deployment, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}

	list, err := deployments.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	var match *appsv1.Deployment
	for index := range list.Items {
		deployment := &list.Items[index]
		dockerID := identity.DockerIDFromUID(deployment.UID)
		if dockerID == "" || !strings.HasPrefix(dockerID, nameOrID) {
			continue
		}
		if match != nil {
			return nil, Conflict(fmt.Errorf("container ID %s is ambiguous", nameOrID))
		}
		match = deployment
	}
	if match == nil {
		return nil, NotFound(fmt.Errorf("container %s not found", nameOrID))
	}
	return match, nil
}

// pullSecretName is the dockerconfigjson Secret, one per tenant namespace,
// holding the namespace's dinki pull credential.
const pullSecretName = "dinki-pull"

const (
	containerConfigAnnotation     = "dink.io/docker-config"
	containerHostConfigAnnotation = "dink.io/docker-host-config"
)

func containerResources(hostConfig *container.HostConfig) (corev1.ResourceRequirements, error) {
	result := corev1.ResourceRequirements{}
	if hostConfig == nil {
		return result, nil
	}
	options := hostConfig.Resources
	if options.Memory < 0 || options.MemoryReservation < 0 || options.NanoCPUs < 0 || options.CPUQuota < -1 || options.CPUPeriod < 0 {
		return result, fmt.Errorf("invalid negative container resource value")
	}
	if options.Memory > 0 {
		result.Limits = corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(options.Memory, resource.BinarySI)}
	}
	if options.MemoryReservation > 0 {
		if options.Memory > 0 && options.MemoryReservation > options.Memory {
			return result, fmt.Errorf("memory reservation exceeds memory limit")
		}
		result.Requests = corev1.ResourceList{corev1.ResourceMemory: *resource.NewQuantity(options.MemoryReservation, resource.BinarySI)}
	}
	if options.NanoCPUs > 0 && options.CPUQuota > 0 {
		return result, fmt.Errorf("NanoCPUs and CPUQuota cannot be combined")
	}
	if options.NanoCPUs > 0 {
		if options.NanoCPUs%1_000_000 != 0 {
			return result, fmt.Errorf("CPU limit must be a whole number of millicores")
		}
		if result.Limits == nil {
			result.Limits = corev1.ResourceList{}
		}
		result.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(options.NanoCPUs/1_000_000, resource.DecimalSI)
	}
	if options.CPUQuota > 0 {
		period := options.CPUPeriod
		if period == 0 {
			period = 100_000
		}
		if options.CPUQuota > math.MaxInt64/1000 || options.CPUQuota*1000%period != 0 {
			return result, fmt.Errorf("CPU quota cannot be represented in whole millicores")
		}
		if result.Limits == nil {
			result.Limits = corev1.ResourceList{}
		}
		result.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(options.CPUQuota*1000/period, resource.DecimalSI)
	} else if options.CPUPeriod != 0 {
		return result, fmt.Errorf("CPU period without a quota is not supported")
	}
	options.Memory, options.MemoryReservation, options.NanoCPUs, options.CPUQuota, options.CPUPeriod = 0, 0, 0, 0, 0
	if options.MemorySwappiness != nil && *options.MemorySwappiness == -1 {
		options.MemorySwappiness = nil
	}
	if options.OomKillDisable != nil && !*options.OomKillDisable {
		options.OomKillDisable = nil
	}
	if options.PidsLimit != nil && *options.PidsLimit == 0 {
		options.PidsLimit = nil
	}
	fields := reflect.ValueOf(options)
	for index := 0; index < fields.NumField(); index++ {
		field := fields.Field(index)
		if (field.Kind() == reflect.Slice || field.Kind() == reflect.Map) && field.Len() == 0 {
			continue
		}
		if !field.IsZero() {
			return result, fmt.Errorf("unsupported container resource %s", fields.Type().Field(index).Name)
		}
	}
	return result, nil
}

func publishedPortsServiceName(deploymentName string) string {
	const suffix = "-published"
	if len(deploymentName)+len(suffix) <= 63 {
		return deploymentName + suffix
	}
	hash := sha256.Sum256([]byte(deploymentName))
	return fmt.Sprintf("%.44s-%x%s", deploymentName, hash[:4], suffix)
}

func containerMetadata(deployment *appsv1.Deployment) (*container.Config, *container.HostConfig, error) {
	config := &container.Config{}
	if value := deployment.Annotations[containerConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), config); err != nil {
			return nil, nil, fmt.Errorf("decoding container config for %s: %w", deployment.Name, err)
		}
	}
	hostConfig := &container.HostConfig{}
	if value := deployment.Annotations[containerHostConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), hostConfig); err != nil {
			return nil, nil, fmt.Errorf("decoding host config for %s: %w", deployment.Name, err)
		}
	}
	return config, hostConfig, nil
}
