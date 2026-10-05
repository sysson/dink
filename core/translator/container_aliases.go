package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	networktypes "github.com/moby/moby/api/types/network"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// containerAliasAnnotation records the Docker network aliases of a workload,
	// keyed by network name, so inspect can report them per endpoint.
	containerAliasAnnotation = "dink.io/network-aliases"
	// aliasOfLabel ties an alias Service back to the workload it resolves to.
	aliasOfLabel = "dink.io/alias-of"
)

// Docker scopes aliases to a network, but Kubernetes Service names are unique
// per namespace, so an alias resolves tenant-wide and may only be claimed once.
func aliasesFromEndpoints(endpoints map[string]*networktypes.EndpointSettings) (map[string][]string, error) {
	aliases := make(map[string][]string, len(endpoints))
	for name, endpoint := range endpoints {
		if endpoint == nil {
			continue
		}
		names := slices.Concat(endpoint.Aliases, endpoint.DNSNames)
		if len(names) == 0 {
			continue
		}
		slices.Sort(names)
		names = slices.Compact(names)
		for _, alias := range names {
			if errs := validation.IsDNS1035Label(alias); len(errs) > 0 {
				return nil, InvalidArgument(fmt.Errorf("network alias %q is not usable as a Kubernetes Service name: %s", alias, errs[0]))
			}
		}
		aliases[name] = names
	}
	if len(aliases) == 0 {
		return nil, nil
	}
	return aliases, nil
}

func decodeContainerAliases(annotations map[string]string) (map[string][]string, error) {
	encoded := annotations[containerAliasAnnotation]
	if encoded == "" {
		return nil, nil
	}
	var aliases map[string][]string
	if err := json.Unmarshal([]byte(encoded), &aliases); err != nil {
		return nil, fmt.Errorf("decoding network aliases: %w", err)
	}
	return aliases, nil
}

func encodeContainerAliases(aliases map[string][]string) (string, error) {
	if len(aliases) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(aliases)
	if err != nil {
		return "", InvalidArgument(fmt.Errorf("encoding network aliases: %w", err))
	}
	return string(encoded), nil
}

// distinctAliases flattens the per-network aliases into the Service names to create.
func distinctAliases(aliases map[string][]string) []string {
	var names []string
	for _, perNetwork := range aliases {
		names = append(names, perNetwork...)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// applyAliasServices reconciles a workload's alias Services to the desired set,
// so an alias resolves to the container the way Docker's embedded resolver does.
func (d *Docker) applyAliasServices(ctx context.Context, workload *containerWorkload, aliases []string, ports []publishedPort) error {
	services := d.k8s.CoreV1().Services(workload.Namespace)
	existing, err := services.List(ctx, metav1.ListOptions{LabelSelector: aliasOfLabel + "=" + workload.Name})
	if err != nil {
		return kubeError(err)
	}
	wanted := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		wanted[alias] = struct{}{}
	}
	for index := range existing.Items {
		service := &existing.Items[index]
		if _, keep := wanted[service.Name]; keep {
			continue
		}
		if service.Name == containerServiceName(workload.dockerName()) {
			continue
		}
		if err := d.deleteOwnedService(ctx, workload, service); err != nil {
			return err
		}
	}
	for _, alias := range aliases {
		if alias == containerServiceName(workload.dockerName()) {
			current, err := services.Get(ctx, alias, metav1.GetOptions{})
			if err != nil {
				return kubeError(err)
			}
			if !workloadOwnsService(workload, current) {
				return Conflict(fmt.Errorf("network alias %q is already in use in this namespace", alias))
			}
			// A self-alias is served by the primary DNS Service, never relabeled
			// as an alias that network disconnect would delete.
			continue
		}
		service := aliasService(workload, alias, ports)
		_, err := services.Create(ctx, service, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			current, getErr := services.Get(ctx, alias, metav1.GetOptions{})
			if getErr != nil {
				return kubeError(getErr)
			}
			if !workloadOwnsService(workload, current) {
				return Conflict(fmt.Errorf("network alias %q is already in use in this namespace", alias))
			}
			if current.Annotations[containerNameAnnotation] != "" {
				if current.Annotations[containerNameAnnotation] == workload.dockerName() {
					continue
				}
				delete(current.Annotations, containerNameAnnotation)
			}
			service.Annotations = current.Annotations
			service.ResourceVersion = current.ResourceVersion
			service.Spec.ClusterIP = current.Spec.ClusterIP
			if _, err := services.Update(ctx, service, metav1.UpdateOptions{}); err != nil {
				return kubeError(err)
			}
			continue
		}
		if err != nil {
			return kubeError(err)
		}
	}
	return nil
}

func aliasService(workload *containerWorkload, alias string, ports []publishedPort) *corev1.Service {
	service := containerDNSService(workload, ports)
	service.Name = alias
	service.Annotations = nil
	service.Labels = map[string]string{
		"app":          workload.Name,
		aliasOfLabel:   workload.Name,
		managedByLabel: managedByDink,
	}
	return service
}

func (d *Docker) claimContainerAliases(ctx context.Context, workload *containerWorkload) error {
	d.containerNameMu.Lock()
	defer d.containerNameMu.Unlock()
	aliases, err := decodeContainerAliases(workload.Annotations)
	if err != nil {
		return err
	}
	if len(aliases) == 0 {
		return nil
	}
	ports, err := workloadPublishedPorts(workload)
	if err != nil {
		return err
	}
	return d.applyAliasServices(ctx, workload, distinctAliases(aliases), ports)
}

// setContainerAliases rewrites the stored aliases and reconciles the Services
// that back them.
func (d *Docker) setContainerAliases(ctx context.Context, workload *containerWorkload, aliases map[string][]string) error {
	encoded, err := encodeContainerAliases(aliases)
	if err != nil {
		return err
	}
	ports, err := workloadPublishedPorts(workload)
	if err != nil {
		return err
	}
	if err := d.applyAliasServices(ctx, workload, distinctAliases(aliases), ports); err != nil {
		return err
	}
	return d.updateWorkloadAnnotations(ctx, workload, func(annotations map[string]string) {
		if encoded == "" {
			delete(annotations, containerAliasAnnotation)
			return
		}
		annotations[containerAliasAnnotation] = encoded
	})
}

// workloadPublishedPorts recovers the ports an alias Service must expose from
// the container the workload already runs.
func workloadPublishedPorts(workload *containerWorkload) ([]publishedPort, error) {
	if len(workload.Template.Spec.Containers) == 0 {
		return nil, nil
	}
	application, err := namedContainer(&workload.Template.Spec, workload.Name)
	if err != nil {
		return nil, err
	}
	containerPorts := application.Ports
	ports := make([]publishedPort, 0, len(containerPorts))
	for _, port := range containerPorts {
		if port.ContainerPort < 0 || port.ContainerPort > 65535 {
			return nil, InvalidArgument(fmt.Errorf("container port %d is out of range", port.ContainerPort))
		}
		ports = append(ports, publishedPort{containerPort: uint16(port.ContainerPort), protocol: port.Protocol})
	}
	return ports, nil
}

// mergeNetworkAliases returns the stored aliases with network replaced.
func mergeNetworkAliases(current map[string][]string, network string, aliases []string) map[string][]string {
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string][]string{}
	}
	if len(aliases) == 0 {
		delete(merged, network)
		return merged
	}
	merged[network] = aliases
	return merged
}

func (d *Docker) updateWorkloadAnnotations(ctx context.Context, workload *containerWorkload, mutate func(map[string]string)) error {
	if workload.OneShot {
		return d.updateContainerJob(ctx, workload, func(current *batchv1.Job) error {
			if current.Annotations == nil {
				current.Annotations = map[string]string{}
			}
			mutate(current.Annotations)
			return nil
		})
	}
	_, err := d.updateContainerDeployment(ctx, workload, func(current *appsv1.Deployment) error {
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		mutate(current.Annotations)
		return nil
	})
	return err
}
