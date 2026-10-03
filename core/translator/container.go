package translator

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"reflect"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func namedContainer(spec *corev1.PodSpec, name string) (*corev1.Container, error) {
	for index := range spec.Containers {
		if spec.Containers[index].Name == name {
			return &spec.Containers[index], nil
		}
	}
	return nil, NotFound(fmt.Errorf("application container %q not found in pod spec", name))
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
