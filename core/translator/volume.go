package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	volumetypes "github.com/moby/moby/api/types/volume"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	volumeManagedLabel       = "dink.io/managed-by"
	volumeManagedValue       = "dink"
	volumeNameAnnotation     = "dink.io/docker-volume-name"
	volumeLabelsAnnotation   = "dink.io/docker-volume-labels"
	volumeOptionsAnnotation  = "dink.io/docker-volume-options"
	volumeMountpointBase     = "/var/lib/dink/volumes"
	defaultVolumeStorageSize = "1Gi"
)

var invalidVolumeNameCharacters = regexp.MustCompile(`[^a-z0-9.-]+`)

func (d *Docker) ListVolumes(ctx context.Context, volumeFilters filters.Args) ([]volumetypes.Volume, []string, error) {
	identity, ok := identity.FromContext(ctx)
	if !ok {
		return nil, nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	pvcs, err := d.k8s.CoreV1().PersistentVolumeClaims(identity.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: volumeManagedLabel + "=" + volumeManagedValue,
	})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	used, err := d.volumeClaimsInUse(ctx, identity.Namespace)
	if err != nil {
		return nil, nil, err
	}
	danglingValues := volumeFilters.Get("dangling")
	dangling, err := volumeFilters.GetBoolOrDefault("dangling", false)
	if err != nil {
		return nil, nil, InvalidArgument(err)
	}
	volumes := make([]volumetypes.Volume, 0, len(pvcs.Items))
	for index := range pvcs.Items {
		volume, err := volumeFromPVC(&pvcs.Items[index])
		if err != nil {
			return nil, nil, err
		}
		if !volumeFilters.Match("name", volume.Name) || !volumeFilters.Match("driver", volume.Driver) ||
			!volumeFilters.MatchKVList("label", volume.Labels) {
			continue
		}
		if len(danglingValues) > 0 && used[pvcs.Items[index].Name] == dangling {
			continue
		}
		volumes = append(volumes, *volume)
	}
	return volumes, []string{}, nil
}

func (d *Docker) GetVolume(ctx context.Context, name string) (*volumetypes.Volume, error) {
	identity, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	pvcName, err := volumeResourceName(name)
	if err != nil {
		return nil, err
	}
	pvc, err := d.k8s.CoreV1().PersistentVolumeClaims(identity.Namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, NotFound(fmt.Errorf("volume %q not found", name))
	}
	if err != nil {
		return nil, kubeError(err)
	}
	if pvc.Labels[volumeManagedLabel] != volumeManagedValue || pvc.Annotations[volumeNameAnnotation] != name {
		return nil, NotFound(fmt.Errorf("volume %q not found", name))
	}
	return volumeFromPVC(pvc)
}

func (d *Docker) CreateVolume(ctx context.Context, request volumetypes.CreateRequest) (*volumetypes.Volume, error) {
	identity, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if request.Driver != "" && request.Driver != "local" {
		return nil, Unsupported(fmt.Errorf("volume driver %q is not supported", request.Driver))
	}
	if request.ClusterVolumeSpec != nil {
		return nil, Unsupported(fmt.Errorf("cluster volumes are not supported"))
	}
	if len(request.DriverOpts) > 1 || (len(request.DriverOpts) == 1 && request.DriverOpts["size"] == "") {
		return nil, InvalidArgument(fmt.Errorf("only the local volume driver size option is supported"))
	}
	name := request.Name
	if name == "" {
		name = generatedContainerName()
	}
	pvcName, err := volumeResourceName(name)
	if err != nil {
		return nil, err
	}
	size := request.DriverOpts["size"]
	if size == "" {
		size = defaultVolumeStorageSize
	}
	quantity, err := resource.ParseQuantity(size)
	if err != nil || quantity.Sign() <= 0 {
		return nil, InvalidArgument(fmt.Errorf("invalid volume size %q", size))
	}
	labels, err := json.Marshal(request.Labels)
	if err != nil {
		return nil, fmt.Errorf("encoding volume labels: %w", err)
	}
	options, err := json.Marshal(request.DriverOpts)
	if err != nil {
		return nil, fmt.Errorf("encoding volume options: %w", err)
	}
	client := d.k8s.CoreV1().PersistentVolumeClaims(identity.Namespace)
	pvc, err := client.Create(ctx, &corev1.PersistentVolumeClaim{
		Name:      pvcName,
		Namespace: identity.Namespace,
		Labels:    map[string]string{volumeManagedLabel: volumeManagedValue},
		Annotations: map[string]string{
			volumeNameAnnotation:    name,
			volumeLabelsAnnotation:  string(labels),
			volumeOptionsAnnotation: string(options),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
		},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := client.Get(ctx, pvcName, metav1.GetOptions{})
		if getErr != nil {
			return nil, kubeError(getErr)
		}
		if existing.Labels[volumeManagedLabel] != volumeManagedValue || existing.Annotations[volumeNameAnnotation] != name {
			return nil, Conflict(fmt.Errorf("volume %q already exists", name))
		}
		return volumeFromPVC(existing)
	}
	if err != nil {
		return nil, kubeError(err)
	}
	return volumeFromPVC(pvc)
}

func (d *Docker) RemoveVolume(ctx context.Context, name string, force bool) error {
	identity, ok := identity.FromContext(ctx)
	if !ok {
		return Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	volume, err := d.GetVolume(ctx, name)
	if err != nil {
		return err
	}
	if !force {
		used, err := d.volumeClaimsInUse(ctx, identity.Namespace)
		if err != nil {
			return err
		}
		pvcName, err := volumeResourceName(volume.Name)
		if err != nil {
			return err
		}
		if used[pvcName] {
			return Conflict(fmt.Errorf("volume %q is in use", name))
		}
	}
	pvcName, err := volumeResourceName(volume.Name)
	if err != nil {
		return err
	}
	return kubeError(d.k8s.CoreV1().PersistentVolumeClaims(identity.Namespace).Delete(ctx, pvcName, metav1.DeleteOptions{}))
}

func (d *Docker) PruneVolumes(ctx context.Context, volumeFilters filters.Args) (*volumetypes.PruneReport, error) {
	volumes, _, err := d.ListVolumes(ctx, volumeFilters)
	if err != nil {
		return nil, err
	}
	report := &volumetypes.PruneReport{VolumesDeleted: []string{}}
	for _, volume := range volumes {
		if err := d.RemoveVolume(ctx, volume.Name, false); err != nil {
			if IsKind(err, KindConflict) {
				continue
			}
			return nil, err
		}
		report.VolumesDeleted = append(report.VolumesDeleted, volume.Name)
	}
	return report, nil
}

func volumeResourceName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = invalidVolumeNameCharacters.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-.")
	}
	if name == "" {
		return "", InvalidArgument(fmt.Errorf("volume name is required"))
	}
	return name, nil
}

func volumeFromPVC(pvc *corev1.PersistentVolumeClaim) (*volumetypes.Volume, error) {
	var labels, options map[string]string
	if raw := pvc.Annotations[volumeLabelsAnnotation]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &labels); err != nil {
			return nil, fmt.Errorf("decoding labels for volume %s: %w", pvc.Name, err)
		}
	}
	if raw := pvc.Annotations[volumeOptionsAnnotation]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			return nil, fmt.Errorf("decoding options for volume %s: %w", pvc.Name, err)
		}
	}
	if labels == nil {
		labels = map[string]string{}
	}
	if options == nil {
		options = map[string]string{}
	}
	name := pvc.Annotations[volumeNameAnnotation]
	return &volumetypes.Volume{
		Name:       name,
		Driver:     "local",
		Mountpoint: volumeMountpointBase + "/" + pvc.Name,
		CreatedAt:  pvc.CreationTimestamp.UTC().Format(time.RFC3339Nano),
		Labels:     labels,
		Options:    options,
		Scope:      "local",
	}, nil
}

func (d *Docker) volumeClaimsInUse(ctx context.Context, namespace string) (map[string]bool, error) {
	used := make(map[string]bool)
	deployments, err := d.k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	for _, deployment := range deployments.Items {
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				used[volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	pods, err := d.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				used[volume.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}
	return used, nil
}

func (s *Swarm) GetVolume(context.Context, string) (volumetypes.Volume, error) {
	return volumetypes.Volume{}, ErrNotImplemented
}

func (s *Swarm) GetVolumes(context.Context, filters.Args) ([]volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) CreateVolume(context.Context, volumetypes.CreateRequest) (*volumetypes.Volume, error) {
	return nil, ErrNotImplemented
}

func (s *Swarm) RemoveVolume(context.Context, string, bool) error {
	return ErrNotImplemented
}

func (s *Swarm) UpdateVolume(context.Context, string, uint64, *volumetypes.ClusterVolumeSpec) error {
	return ErrNotImplemented
}

func (s *Swarm) IsManager(context.Context) (bool, error) {
	return false, ErrNotImplemented
}
