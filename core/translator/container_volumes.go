package translator

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	volumetypes "github.com/moby/moby/api/types/volume"
	corev1 "k8s.io/api/core/v1"
)

// containerVolumes also returns the claims it created as anonymous volumes.
func (d *Docker) containerVolumes(ctx context.Context, config *container.Config, hostConfig *container.HostConfig) ([]corev1.Volume, []corev1.VolumeMount, []string, error) {
	defaultDriver := ""
	if hostConfig != nil {
		defaultDriver = hostConfig.VolumeDriver
		if len(hostConfig.Tmpfs) > 0 || len(hostConfig.VolumesFrom) > 0 {
			return nil, nil, nil, Unsupported(fmt.Errorf("tmpfs and volumes-from mounts are not supported"))
		}
	}
	volumesByName := make(map[string]corev1.Volume)
	mountsByTarget := make(map[string]corev1.VolumeMount)
	var anonymous []string
	addMount := func(target, source string, readOnly bool, subPath, driver string, driverOpts map[string]string) error {
		if !path.IsAbs(target) {
			return InvalidArgument(fmt.Errorf("volume mount target %q must be an absolute path", target))
		}
		pvcName, err := d.ensureContainerVolume(ctx, source, driver, driverOpts)
		if err != nil {
			return err
		}
		if source == "" {
			anonymous = append(anonymous, pvcName)
		}
		volumesByName[pvcName] = corev1.Volume{
			Name: pvcName,
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		}
		mountsByTarget[target] = corev1.VolumeMount{Name: pvcName, MountPath: target, ReadOnly: readOnly, SubPath: subPath}
		return nil
	}

	if hostConfig != nil {
		for _, requested := range hostConfig.Mounts {
			mountType := requested.Type
			if mountType == "" {
				mountType = mount.TypeVolume
			}
			switch mountType {
			case mount.TypeVolume:
				driver, driverOpts := defaultDriver, map[string]string(nil)
				if requested.VolumeOptions != nil && requested.VolumeOptions.DriverConfig != nil {
					if requested.VolumeOptions.DriverConfig.Name != "" {
						driver = requested.VolumeOptions.DriverConfig.Name
					}
					driverOpts = requested.VolumeOptions.DriverConfig.Options
				}
				subPath := ""
				if requested.VolumeOptions != nil {
					if requested.VolumeOptions.NoCopy {
						return nil, nil, nil, Unsupported(fmt.Errorf("volume no-copy behavior is not supported"))
					}
					subPath = requested.VolumeOptions.Subpath
				}
				if err := addMount(requested.Target, requested.Source, requested.ReadOnly, subPath, driver, driverOpts); err != nil {
					return nil, nil, nil, err
				}
			case mount.TypeBind:
				return nil, nil, nil, Unsupported(fmt.Errorf("host-path bind mounts are not supported"))
			default:
				return nil, nil, nil, Unsupported(fmt.Errorf("mount type %q is not supported", mountType))
			}
		}
		for _, bind := range hostConfig.Binds {
			parts := strings.Split(bind, ":")
			if len(parts) < 2 || len(parts) > 3 {
				return nil, nil, nil, InvalidArgument(fmt.Errorf("invalid bind mount %q", bind))
			}
			source, target := parts[0], parts[1]
			if path.IsAbs(source) || source == "." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
				return nil, nil, nil, Unsupported(fmt.Errorf("host-path bind mounts are not supported"))
			}
			readOnly := false
			if len(parts) == 3 {
				for option := range strings.SplitSeq(parts[2], ",") {
					switch option {
					case "ro":
						readOnly = true
					case "rw", "":
					default:
						return nil, nil, nil, Unsupported(fmt.Errorf("bind option %q is not supported", option))
					}
				}
			}
			if err := addMount(target, source, readOnly, "", defaultDriver, nil); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	for target := range config.Volumes {
		if _, overridden := mountsByTarget[target]; overridden {
			continue
		}
		if err := addMount(target, "", false, "", defaultDriver, nil); err != nil {
			return nil, nil, nil, err
		}
	}

	volumeNames := make([]string, 0, len(volumesByName))
	for name := range volumesByName {
		volumeNames = append(volumeNames, name)
	}
	sort.Strings(volumeNames)
	volumes := make([]corev1.Volume, 0, len(volumeNames))
	for _, name := range volumeNames {
		volumes = append(volumes, volumesByName[name])
	}
	targets := make([]string, 0, len(mountsByTarget))
	for target := range mountsByTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	volumeMounts := make([]corev1.VolumeMount, 0, len(targets))
	for _, target := range targets {
		volumeMounts = append(volumeMounts, mountsByTarget[target])
	}
	return volumes, volumeMounts, anonymous, nil
}

// ensureContainerVolume reuses a named volume or creates it with driver, as Docker does on first mount.
func (d *Docker) ensureContainerVolume(ctx context.Context, name, driver string, driverOpts map[string]string) (string, error) {
	var labels map[string]string
	if name == "" {
		name = generatedContainerName()
		labels = map[string]string{anonymousVolumeLabel: ""}
	}
	volume, err := d.GetVolume(ctx, name)
	if err == nil {
		return volumeResourceName(volume.Name)
	}
	if !IsKind(err, KindNotFound) {
		return "", err
	}
	_, err = d.CreateVolume(ctx, volumetypes.CreateRequest{Name: name, Labels: labels, Driver: driver, DriverOpts: driverOpts})
	if err != nil {
		if !IsKind(err, KindConflict) {
			return "", err
		}
	}
	volume, err = d.GetVolume(ctx, name)
	if err != nil {
		return "", err
	}
	return volumeResourceName(volume.Name)
}
