package translator

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/version"
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

func (d *Docker) SystemDiskUsage(context.Context, backend.DiskUsageOptions) (*backend.DiskUsage, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) AuthenticateToRegistry(ctx context.Context, auth *registry.AuthConfig) (string, error) {
	return d.registry.Authenticate(ctx, auth)
}

func (s *Swarm) Info(context.Context) (swarm.Info, error) {
	return swarm.Info{}, ErrNotImplemented
}

func (b *Builder) DiskUsage(context.Context, buildbackend.DiskUsageOptions) (*buildbackend.DiskUsage, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) Status(context.Context) (string, error) {
	return "", ErrNotImplemented
}
