package translator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	volumetypes "github.com/moby/moby/api/types/volume"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	volumesv1 "github.com/sysson/dink/sdk/volumes/v1"
)

var volumeAccessModes = map[string]corev1.PersistentVolumeAccessMode{
	string(corev1.ReadWriteOnce):    corev1.ReadWriteOnce,
	string(corev1.ReadOnlyMany):     corev1.ReadOnlyMany,
	string(corev1.ReadWriteMany):    corev1.ReadWriteMany,
	string(corev1.ReadWriteOncePod): corev1.ReadWriteOncePod,
}

// volumeClaim is what a volume driver asks for; dink validates it before building the PVC.
type volumeClaim struct {
	StorageClassName string
	Size             string
	AccessModes      []string
	Annotations      map[string]string
}

func (c volumeClaim) pvcSpec() (corev1.PersistentVolumeClaimSpec, map[string]string, error) {
	size := c.Size
	if size == "" {
		size = defaultVolumeStorageSize
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil || quantity.Sign() <= 0 {
		return corev1.PersistentVolumeClaimSpec{}, nil, InvalidArgument(fmt.Errorf("invalid volume size %q", size))
	}
	spec := corev1.PersistentVolumeClaimSpec{
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
		},
	}
	for _, m := range c.AccessModes {
		mode, ok := volumeAccessModes[m]
		if !ok {
			return corev1.PersistentVolumeClaimSpec{}, nil, InvalidArgument(fmt.Errorf("invalid volume access mode %q", m))
		}
		spec.AccessModes = append(spec.AccessModes, mode)
	}
	if len(spec.AccessModes) == 0 {
		spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	if c.StorageClassName != "" {
		if errs := validation.IsDNS1123Subdomain(c.StorageClassName); len(errs) > 0 {
			return corev1.PersistentVolumeClaimSpec{}, nil, InvalidArgument(fmt.Errorf("invalid storage class %q: %s", c.StorageClassName, strings.Join(errs, "; ")))
		}
		spec.StorageClassName = &c.StorageClassName
	}
	for key := range c.Annotations {
		// dink's own annotations record the Docker view of the volume and must not be overridden.
		if strings.HasPrefix(key, "dink.io/") {
			return corev1.PersistentVolumeClaimSpec{}, nil, InvalidArgument(fmt.Errorf("volume annotation %q is reserved", key))
		}
		if errs := validation.IsQualifiedName(key); len(errs) > 0 {
			return corev1.PersistentVolumeClaimSpec{}, nil, InvalidArgument(fmt.Errorf("invalid volume annotation %q: %s", key, strings.Join(errs, "; ")))
		}
	}
	return spec, c.Annotations, nil
}

func (d *Docker) volumePlugin(ctx context.Context, namespace, driver string) (*plugins.Plugin, error) {
	plugin, err := d.plugins.Lookup(ctx, namespace, driver, plugins.TypeVolumes)
	// Not NotFound: a 404 from container create makes the docker CLI try to pull the image.
	if errors.Is(err, plugins.ErrNotFound) {
		return nil, Unsupported(fmt.Errorf("volume driver %q is not installed", driver))
	}
	if err != nil {
		return nil, Unavailable(err)
	}
	return plugin, nil
}

func (d *Docker) pluginVolumeClaim(ctx context.Context, id identity.Identity, driver, name string, request volumetypes.CreateRequest) (volumeClaim, error) {
	plugin, err := d.volumePlugin(ctx, id.Namespace, driver)
	if err != nil {
		return volumeClaim{}, err
	}
	res, err := plugin.Volumes().Create(ctx, &volumesv1.CreateRequest{
		IdentityName: id.CommonName,
		Namespace:    id.Namespace,
		Name:         name,
		Options:      request.DriverOpts,
		Labels:       request.Labels,
	})
	plugin.Observe(err)
	if err != nil {
		return volumeClaim{}, volumePluginError(plugin, err)
	}
	return volumeClaim{
		StorageClassName: res.GetStorageClassName(),
		Size:             res.GetSize(),
		AccessModes:      res.GetAccessModes(),
		Annotations:      res.GetAnnotations(),
	}, nil
}

func (d *Docker) removePluginVolume(ctx context.Context, id identity.Identity, volume *volumetypes.Volume) error {
	plugin, err := d.volumePlugin(ctx, id.Namespace, volume.Driver)
	if err != nil {
		return err
	}
	_, err = plugin.Volumes().Remove(ctx, &volumesv1.RemoveRequest{
		IdentityName: id.CommonName,
		Namespace:    id.Namespace,
		Name:         volume.Name,
		Options:      volume.Options,
	})
	plugin.Observe(err)
	if err != nil {
		return volumePluginError(plugin, err)
	}
	return nil
}

// volumePluginError reports an unreachable plugin as unavailable and anything else as the driver rejecting the request.
func volumePluginError(plugin *plugins.Plugin, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return Unavailable(fmt.Errorf("volume driver %s: %w", plugin, err))
	default:
		return InvalidArgument(fmt.Errorf("volume driver %s: %w", plugin, err))
	}
}
