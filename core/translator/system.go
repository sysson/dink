package translator

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/dink/core/version"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) SystemInfo(ctx context.Context) (*system.Info, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	deployments, err := d.k8s.AppsV1().Deployments(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}

	v := version.Get()
	info := &system.Info{
		Name:            "dink",
		ServerVersion:   v.Version,
		OperatingSystem: "Kubernetes-backed Dink",
		OSType:          runtime.GOOS,
		Architecture:    runtime.GOARCH,
		NCPU:            runtime.NumCPU(),
		SystemTime:      time.Now().UTC().Format(time.RFC3339Nano),
		Warnings: []string{
			"Container counts are scoped to the authenticated namespace; Images counts distinct workload image references, not registry contents.",
			"Deployments with desired replicas but no ready Pods are not classified as running or stopped.",
			"Host, storage, runtime, and cgroup details are not Docker Engine properties in Dink.",
		},
	}
	images := make(map[string]struct{})
	addImage := func(image string) {
		if image != "" {
			images[image] = struct{}{}
		}
	}
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		info.Containers++
		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		if deployment.Status.ReadyReplicas > 0 {
			info.ContainersRunning++
		} else if replicas == 0 {
			info.ContainersStopped++
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			addImage(container.Image)
		}
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			addImage(container.Image)
		}
	}
	jobs, err := d.k8s.BatchV1().Jobs(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	for index := range jobs.Items {
		job := &jobs.Items[index]
		if !isContainerJob(job) {
			continue
		}
		info.Containers++
		if job.Status.Active > 0 {
			info.ContainersRunning++
		} else if workload := jobWorkload(job); workload.Finished {
			info.ContainersStopped++
		}
		for _, container := range job.Spec.Template.Spec.Containers {
			addImage(container.Image)
		}
	}
	info.Images = len(images)
	return info, nil
}

func (d *Docker) SystemVersion(context.Context) (system.VersionResponse, error) {
	v := version.Get()
	ver := system.VersionResponse{
		Platform: system.PlatformInfo{
			Name: "Dink Translation Layer",
		},
		Version:       v.Version,
		APIVersion:    v.APIVersion,
		MinAPIVersion: v.MinAPIVersion,
		Os:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Components: []system.ComponentVersion{
			{
				Name:    "Dink Translation Layer",
				Version: v.Version,
				Details: map[string]string{
					"GitCommit":     v.Commit,
					"ApiVersion":    v.APIVersion,
					"MinAPIVersion": v.MinAPIVersion,
					"GoVersion":     runtime.Version(),
					"Os":            runtime.GOOS,
					"Arch":          runtime.GOARCH,
					"BuildTime":     v.Date,
					"KernelVersion": "unknown",
					"Module":        "github.com/sysson/dink",
					"ModuleVersion": "moduleVersion()",
					"Experimental":  "false",
				},
			},
		},
	}
	return ver, nil
}

func (d *Docker) SystemDiskUsage(ctx context.Context, options backend.DiskUsageOptions) (*backend.DiskUsage, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	usage := &backend.DiskUsage{}
	if options.Containers {
		containers, err := d.Containers(ctx, &backend.ContainerListOptions{All: true})
		if err != nil {
			return nil, err
		}
		// Container writable layers live on nodes, so sizes are not available.
		usage.Containers = &backend.ContainerDiskUsage{TotalCount: int64(len(containers))}
		for _, summary := range containers {
			if summary.State != container.StateCreated && summary.State != container.StateExited {
				usage.Containers.ActiveCount++
			}
		}
		if options.Verbose {
			usage.Containers.Items = containers
		}
	}
	if options.Images {
		images, err := d.imageDiskUsage(ctx, id.Namespace, options.Verbose)
		if err != nil {
			return nil, err
		}
		usage.Images = images
	}
	if options.Volumes {
		volumes, err := d.volumeDiskUsage(ctx, id.Namespace, options.Verbose)
		if err != nil {
			return nil, err
		}
		usage.Volumes = volumes
	}
	return usage, nil
}

func (d *Docker) imageDiskUsage(ctx context.Context, namespace string, verbose bool) (*backend.ImageDiskUsage, error) {
	images, err := d.registry.Images(ctx, types.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	workloads, err := listWorkloads(ctx, d.k8s, namespace)
	if err != nil {
		return nil, err
	}
	usage := &backend.ImageDiskUsage{}
	seen := make(map[string]bool, len(images))
	for _, summary := range images {
		// The registry lists one summary per tag; count each image once.
		if seen[summary.ID] {
			continue
		}
		seen[summary.ID] = true
		summary.Containers = 0
		for _, workload := range workloads {
			if workloadUsesImage(workload, namespace, summary.RepoDigests) {
				summary.Containers++
			}
		}
		usage.TotalCount++
		usage.TotalSize += summary.Size
		if summary.Containers > 0 {
			usage.ActiveCount++
		} else {
			usage.Reclaimable += summary.Size
		}
		if verbose {
			usage.Items = append(usage.Items, summary)
		}
	}
	return usage, nil
}

func (d *Docker) volumeDiskUsage(ctx context.Context, namespace string, verbose bool) (*backend.VolumeDiskUsage, error) {
	pvcs, err := d.k8s.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: eventVolumeSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	used, err := d.volumeClaimsInUse(ctx, namespace)
	if err != nil {
		return nil, err
	}
	usage := &backend.VolumeDiskUsage{}
	for index := range pvcs.Items {
		pvc := &pvcs.Items[index]
		volume, err := volumeFromPVC(pvc)
		if err != nil {
			return nil, err
		}
		// Actual usage needs kubelet stats; report the provisioned capacity instead.
		capacity, ok := pvc.Status.Capacity[corev1.ResourceStorage]
		if !ok {
			capacity = pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		}
		size := capacity.Value()
		var refCount int64
		if used[pvc.Name] {
			refCount = 1
		}
		volume.UsageData = &volumetypes.UsageData{Size: size, RefCount: refCount}
		usage.TotalCount++
		usage.TotalSize += size
		if refCount > 0 {
			usage.ActiveCount++
		} else {
			usage.Reclaimable += size
		}
		if verbose {
			usage.Items = append(usage.Items, *volume)
		}
	}
	return usage, nil
}

func (d *Docker) AuthenticateToRegistry(ctx context.Context, auth *registry.AuthConfig) (string, error) {
	return d.registry.Authenticate(ctx, auth)
}

func (s *Swarm) Info(context.Context) (swarm.Info, error) {
	return swarm.Info{}, ErrNotImplemented
}

// DiskUsage reports an empty build cache because Dink does not build images.
func (b *Builder) DiskUsage(context.Context, buildbackend.DiskUsageOptions) (*buildbackend.DiskUsage, error) {
	return &buildbackend.DiskUsage{}, nil
}

func (d *Docker) Status(context.Context) (string, error) {
	return "", ErrNotImplemented
}
