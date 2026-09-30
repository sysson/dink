// Package volumes lets a plugin back `docker volume create --driver <plugin>`.
//
// The plugin only describes the claim; dink creates and owns the
// PersistentVolumeClaim, so a plugin never needs Kubernetes access.
package volumes

import (
	"context"

	"github.com/sysson/dink/sdk/plugin"
	volumesv1 "github.com/sysson/dink/sdk/volumes/v1"
	"github.com/sysson/dink/sdk/volumes/v1/volumesconnect"
)

type Request struct {
	IdentityName string
	Namespace    string
	Name         string
	Options      map[string]string
	Labels       map[string]string
}

type Claim struct {
	StorageClassName string
	// Size is a Kubernetes quantity such as "10Gi"; empty uses dink's default.
	Size string
	// AccessModes such as "ReadWriteMany"; empty means ReadWriteOnce.
	AccessModes []string
	Annotations map[string]string
}

type Driver interface {
	// Create validates the options and describes the claim to create. Return an error to reject the volume.
	Create(ctx context.Context, req *Request) (Claim, error)
	// Remove releases any state held outside Kubernetes before the claim is deleted.
	Remove(ctx context.Context, req *Request) error
}

// Plugin serves d as the plugin's volume driver.
func Plugin(d Driver) plugin.Option {
	path, h := volumesconnect.NewVolumePluginServiceHandler(handler{d: d})
	return plugin.WithService(plugin.TypeVolumes, path, h)
}

type handler struct {
	d Driver
}

func (h handler) Create(ctx context.Context, in *volumesv1.CreateRequest) (*volumesv1.CreateResponse, error) {
	claim, err := h.d.Create(ctx, &Request{
		IdentityName: in.GetIdentityName(),
		Namespace:    in.GetNamespace(),
		Name:         in.GetName(),
		Options:      in.GetOptions(),
		Labels:       in.GetLabels(),
	})
	if err != nil {
		return nil, err
	}
	return &volumesv1.CreateResponse{
		StorageClassName: claim.StorageClassName,
		Size:             claim.Size,
		AccessModes:      claim.AccessModes,
		Annotations:      claim.Annotations,
	}, nil
}

func (h handler) Remove(ctx context.Context, in *volumesv1.RemoveRequest) (*volumesv1.RemoveResponse, error) {
	err := h.d.Remove(ctx, &Request{
		IdentityName: in.GetIdentityName(),
		Namespace:    in.GetNamespace(),
		Name:         in.GetName(),
		Options:      in.GetOptions(),
	})
	if err != nil {
		return nil, err
	}
	return &volumesv1.RemoveResponse{}, nil
}
