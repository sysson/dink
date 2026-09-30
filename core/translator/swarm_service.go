package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/netip"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/mount"
	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

func (s *Swarm) GetServices(ctx context.Context, options swarmbackend.ServiceListOptions) ([]swarmtypes.Service, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateServiceFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	deployments, err := s.k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmServiceSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	networks := s.networksByLabel(ctx)
	result := make([]swarmtypes.Service, 0, len(deployments.Items))
	for index := range deployments.Items {
		service, err := s.serviceFromDeployment(ctx, &deployments.Items[index], options.Status, networks)
		if err != nil {
			return nil, err
		}
		if !matchesServiceFilters(options.Filters, service) {
			continue
		}
		result = append(result, service)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.Name < result[j].Spec.Name })
	return result, nil
}

func (s *Swarm) GetService(ctx context.Context, idOrName string, insertDefaults bool) (swarmtypes.Service, error) {
	deployment, err := s.findService(ctx, idOrName)
	if err != nil {
		return swarmtypes.Service{}, err
	}
	service, err := s.serviceFromDeployment(ctx, deployment, true, s.networksByLabel(ctx))
	if err != nil {
		return swarmtypes.Service{}, err
	}
	if insertDefaults {
		insertServiceDefaults(&service.Spec)
	}
	return service, nil
}

func (s *Swarm) CreateService(ctx context.Context, spec swarmtypes.ServiceSpec, _ string, _ bool) (*swarmtypes.ServiceCreateResponse, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	deployment, services, warnings, err := s.buildService(ctx, namespace, spec)
	if err != nil {
		return nil, err
	}
	created, err := s.k8s.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	if err := s.applyServiceEndpoints(ctx, created, services); err != nil {
		_ = s.k8s.AppsV1().Deployments(namespace).Delete(ctx, created.Name, metav1.DeleteOptions{})
		return nil, err
	}
	if err := s.applyServicePolicies(ctx, created); err != nil {
		_ = s.k8s.AppsV1().Deployments(namespace).Delete(ctx, created.Name, metav1.DeleteOptions{})
		return nil, err
	}
	return &swarmtypes.ServiceCreateResponse{ID: identity.DockerIDFromUID(created.UID), Warnings: warnings}, nil
}

func (s *Swarm) UpdateService(ctx context.Context, idOrName string, version uint64, spec swarmtypes.ServiceSpec, options swarmbackend.ServiceUpdateOptions, _ bool) (*swarmtypes.ServiceUpdateResponse, error) {
	if options.Rollback != "" && options.Rollback != "none" {
		return nil, Unsupported(fmt.Errorf("server-side rollback is not available: Kubernetes keeps its own rollout history, so use kubectl rollout undo instead"))
	}
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.findService(ctx, idOrName)
	if err != nil {
		return nil, err
	}
	if spec.Name != "" && spec.Name != current.Name {
		return nil, InvalidArgument(fmt.Errorf("renaming a service is not supported"))
	}
	spec.Name = current.Name
	deployment, services, warnings, err := s.buildService(ctx, namespace, spec)
	if err != nil {
		return nil, err
	}
	deployment.ResourceVersion = resourceVersionFor(version)
	updated, err := s.k8s.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	if err := s.applyServiceEndpoints(ctx, updated, services); err != nil {
		return nil, err
	}
	if err := s.applyServicePolicies(ctx, updated); err != nil {
		return nil, err
	}
	return &swarmtypes.ServiceUpdateResponse{Warnings: warnings}, nil
}

// applyServicePolicies opens the service's published ports; network membership
// is already covered by the per-network policies.
func (s *Swarm) applyServicePolicies(ctx context.Context, deployment *appsv1.Deployment) error {
	var ports []corev1.ContainerPort
	for _, container := range deployment.Spec.Template.Spec.Containers {
		ports = append(ports, container.Ports...)
	}
	owner := deploymentOwnerReference(deployment.Name, deployment.UID)
	return s.docker.applyPublishedPortsPolicy(ctx, deployment.Namespace, deployment.Name, owner, ports)
}

func (s *Swarm) RemoveService(ctx context.Context, idOrName string) error {
	deployment, err := s.findService(ctx, idOrName)
	if err != nil {
		return err
	}
	// The Services carry owner references, so Kubernetes removes them too.
	err = s.k8s.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &deployment.UID},
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	return nil
}

func (s *Swarm) findService(ctx context.Context, idOrName string) (*appsv1.Deployment, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if idOrName == "" {
		return nil, InvalidArgument(fmt.Errorf("service ID or name is required"))
	}
	if deployment, err := s.k8s.AppsV1().Deployments(namespace).Get(ctx, idOrName, metav1.GetOptions{}); err == nil {
		if deployment.Labels[swarmKindLabel] == swarmServiceKind {
			return deployment, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}
	deployments, err := s.k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmServiceSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	var match *appsv1.Deployment
	for index := range deployments.Items {
		if !strings.HasPrefix(identity.DockerIDFromUID(deployments.Items[index].UID), idOrName) {
			continue
		}
		if match != nil {
			return nil, Conflict(fmt.Errorf("service %s is ambiguous", idOrName))
		}
		match = &deployments.Items[index]
	}
	if match == nil {
		return nil, NotFound(fmt.Errorf("service %s not found", idOrName))
	}
	return match, nil
}

func (s *Swarm) serviceFromDeployment(ctx context.Context, deployment *appsv1.Deployment, withStatus bool, networks map[string]networktypes.Network) (swarmtypes.Service, error) {
	var spec swarmtypes.ServiceSpec
	if encoded := deployment.Annotations[swarmSpecAnnotation]; encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
			return swarmtypes.Service{}, fmt.Errorf("decoding service spec for %s: %w", deployment.Name, err)
		}
	}
	spec.Name = deployment.Name
	service := swarmtypes.Service{
		ID:       identity.DockerIDFromUID(deployment.UID),
		Meta:     swarmMeta(deployment),
		Spec:     spec,
		Endpoint: s.serviceEndpoint(ctx, deployment, spec, networks),
	}
	if withStatus {
		desired := uint64(0)
		if deployment.Spec.Replicas != nil {
			desired = uint64(*deployment.Spec.Replicas)
		}
		service.ServiceStatus = &swarmtypes.ServiceStatus{
			RunningTasks: uint64(deployment.Status.ReadyReplicas),
			DesiredTasks: desired,
		}
	}
	return service, nil
}

// serviceEndpoint reports the endpoint Kubernetes actually allocated, so an
// auto-assigned NodePort is visible instead of the requested port.
func (s *Swarm) serviceEndpoint(ctx context.Context, deployment *appsv1.Deployment, spec swarmtypes.ServiceSpec, networks map[string]networktypes.Network) swarmtypes.Endpoint {
	endpoint := swarmtypes.Endpoint{}
	if spec.EndpointSpec != nil {
		endpoint.Spec = *spec.EndpointSpec
	}
	attached := attachedNetworks(deployment.Spec.Template.Labels, networks)
	if vip, err := s.k8s.CoreV1().Services(deployment.Namespace).Get(ctx, deployment.Name, metav1.GetOptions{}); err == nil {
		if address, parseErr := netip.ParseAddr(vip.Spec.ClusterIP); parseErr == nil {
			virtualIP := swarmtypes.EndpointVirtualIP{Addr: netip.PrefixFrom(address, address.BitLen())}
			if len(attached) > 0 {
				virtualIP.NetworkID = attached[0].ID
			}
			endpoint.VirtualIPs = []swarmtypes.EndpointVirtualIP{virtualIP}
		}
	}
	if published, err := s.k8s.CoreV1().Services(deployment.Namespace).Get(ctx, publishedPortsServiceName(deployment.Name), metav1.GetOptions{}); err == nil {
		for _, port := range published.Spec.Ports {
			publishedPort := uint32(port.Port)
			if port.NodePort != 0 {
				publishedPort = uint32(port.NodePort)
			}
			endpoint.Ports = append(endpoint.Ports, swarmtypes.PortConfig{
				Protocol:      networktypes.IPProtocol(strings.ToLower(string(port.Protocol))),
				TargetPort:    uint32(port.TargetPort.IntValue()),
				PublishedPort: publishedPort,
				PublishMode:   swarmtypes.PortConfigPublishModeIngress,
			})
		}
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, port := range container.Ports {
			if port.HostPort == 0 {
				continue
			}
			endpoint.Ports = append(endpoint.Ports, swarmtypes.PortConfig{
				Protocol:      networktypes.IPProtocol(strings.ToLower(string(port.Protocol))),
				TargetPort:    uint32(port.ContainerPort),
				PublishedPort: uint32(port.HostPort),
				PublishMode:   swarmtypes.PortConfigPublishModeHost,
			})
		}
	}
	slices.SortFunc(endpoint.Ports, swarmtypes.PortConfig.Compare)
	return endpoint
}

// networksByLabel indexes the tenant's Docker networks by the workload label
// that records membership.
func (s *Swarm) networksByLabel(ctx context.Context) map[string]networktypes.Network {
	list, err := s.docker.networkList(ctx)
	if err != nil {
		return nil
	}
	networks := make(map[string]networktypes.Network, len(list.Items))
	for index := range list.Items {
		object := &list.Items[index]
		networks[networkLabelPrefix+object.GetName()] = networkFromObject(object)
	}
	return networks
}

func attachedNetworks(labels map[string]string, networks map[string]networktypes.Network) []networktypes.Network {
	var attached []networktypes.Network
	for label := range labels {
		if network, ok := networks[label]; ok {
			attached = append(attached, network)
		}
	}
	slices.SortFunc(attached, func(a, b networktypes.Network) int { return strings.Compare(a.Name, b.Name) })
	return attached
}

// insertServiceDefaults fills the fields Docker's daemon would have defaulted,
// so `docker service inspect --pretty` has something to print.
func insertServiceDefaults(spec *swarmtypes.ServiceSpec) {
	if spec.Mode.Replicated == nil && spec.Mode.Global == nil {
		spec.Mode.Replicated = &swarmtypes.ReplicatedService{Replicas: new(uint64(1))}
	}
	if spec.TaskTemplate.RestartPolicy == nil {
		spec.TaskTemplate.RestartPolicy = &swarmtypes.RestartPolicy{Condition: swarmtypes.RestartPolicyConditionAny}
	}
	if spec.EndpointSpec == nil {
		spec.EndpointSpec = &swarmtypes.EndpointSpec{Mode: swarmtypes.ResolutionModeVIP}
	}
}

// buildService translates a Docker ServiceSpec into a Deployment plus the
// Services that publish it.
func (s *Swarm) buildService(ctx context.Context, namespace string, spec swarmtypes.ServiceSpec) (*appsv1.Deployment, []*corev1.Service, []string, error) {
	if err := validateSwarmName("service", spec.Name); err != nil {
		return nil, nil, nil, err
	}
	replicas, err := serviceReplicas(spec.Mode)
	if err != nil {
		return nil, nil, nil, err
	}
	containerSpec := spec.TaskTemplate.ContainerSpec
	if containerSpec == nil {
		return nil, nil, nil, InvalidArgument(fmt.Errorf("service task template requires a container spec"))
	}
	switch spec.TaskTemplate.Runtime {
	case "", swarmtypes.RuntimeContainer:
	default:
		return nil, nil, nil, Unsupported(fmt.Errorf("task runtime %q is not supported; only container tasks map to Kubernetes Pods", spec.TaskTemplate.Runtime))
	}
	image, imageConfig, err := s.docker.podImage(ctx, namespace, containerSpec.Image)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := s.docker.ensurePullSecret(ctx, namespace); err != nil {
		return nil, nil, nil, err
	}
	if err := s.docker.ensureNamespaceIsolation(ctx, namespace); err != nil {
		return nil, nil, nil, err
	}
	warnings := serviceWarnings(spec)

	volumes, mounts, err := s.serviceMounts(ctx, containerSpec)
	if err != nil {
		return nil, nil, nil, err
	}
	resources, err := serviceResources(spec.TaskTemplate.Resources)
	if err != nil {
		return nil, nil, nil, err
	}
	defaults, err := s.docker.tenantResourceDefaults(ctx, namespace)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := applyServiceResourceDefaults(&resources, defaults); err != nil {
		return nil, nil, nil, InvalidArgument(err)
	}
	shell := []string(nil)
	if imageConfig != nil {
		shell = imageConfig.Shell
	}
	probe, probeWarnings, err := dockerHealthProbe(containerSpec.Healthcheck, shell)
	if err != nil {
		return nil, nil, nil, InvalidArgument(err)
	}
	warnings = append(warnings, probeWarnings...)
	ports, portWarnings, err := servicePorts(spec.EndpointSpec)
	if err != nil {
		return nil, nil, nil, err
	}
	warnings = append(warnings, portWarnings...)
	networkLabels, err := s.serviceNetworkLabels(ctx, spec, hasIngressPorts(ports))
	if err != nil {
		return nil, nil, nil, err
	}
	aliases, err := serviceAliases(spec)
	if err != nil {
		return nil, nil, nil, err
	}
	if slices.Contains(aliases, spec.Name) {
		return nil, nil, nil, InvalidArgument(fmt.Errorf("network alias %q duplicates the service name", spec.Name))
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, nil, InvalidArgument(fmt.Errorf("encoding service spec: %w", err))
	}

	labels := map[string]string{"app": spec.Name, swarmKindLabel: swarmServiceKind}
	podLabels := dinkPodLabels(spec.Name)
	podLabels[swarmKindLabel] = swarmServiceKind
	maps.Copy(podLabels, networkLabels)
	container := corev1.Container{
		Name:            spec.Name,
		Image:           image,
		Command:         containerSpec.Command,
		Args:            containerSpec.Args,
		Env:             containerEnv(containerSpec.Env),
		WorkingDir:      containerSpec.Dir,
		TTY:             containerSpec.TTY,
		Stdin:           containerSpec.OpenStdin,
		Ports:           serviceContainerPorts(ports),
		Resources:       resources,
		VolumeMounts:    mounts,
		ReadinessProbe:  probe,
		ImagePullPolicy: corev1.PullAlways,
	}
	if containerSpec.ReadOnly || len(containerSpec.CapabilityAdd) > 0 || len(containerSpec.CapabilityDrop) > 0 {
		container.SecurityContext = &corev1.SecurityContext{}
		if containerSpec.ReadOnly {
			container.SecurityContext.ReadOnlyRootFilesystem = new(true)
		}
		if len(containerSpec.CapabilityAdd) > 0 || len(containerSpec.CapabilityDrop) > 0 {
			container.SecurityContext.Capabilities = &corev1.Capabilities{
				Add:  capabilities(containerSpec.CapabilityAdd),
				Drop: capabilities(containerSpec.CapabilityDrop),
			}
		}
	}
	template := corev1.PodTemplateSpec{
		Labels: podLabels,
		Spec: corev1.PodSpec{
			Hostname:         hostnameFor(containerSpec.Hostname),
			Volumes:          volumes,
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: pullSecretName}},
			Containers:       []corev1.Container{container},
			HostAliases:      serviceHostAliases(containerSpec.Hosts),
			DNSConfig:        serviceDNSConfig(containerSpec.DNSConfig),
		},
	}
	if containerSpec.DNSConfig != nil && len(containerSpec.DNSConfig.Nameservers) > 0 {
		template.Spec.DNSPolicy = corev1.DNSNone
	}
	if containerSpec.StopGracePeriod != nil {
		seconds := int64(containerSpec.StopGracePeriod.Seconds())
		template.Spec.TerminationGracePeriodSeconds = &seconds
	}
	if len(containerSpec.Sysctls) > 0 {
		template.Spec.SecurityContext = &corev1.PodSecurityContext{Sysctls: podSysctls(containerSpec.Sysctls)}
	}
	constraints, err := placementRequirements(spec.TaskTemplate.Placement)
	if err != nil {
		return nil, nil, nil, err
	}
	applyNodePlacement(&template.Spec, s.docker.nodePlacement, namespace)
	appendNodeSelectorRequirements(&template.Spec, constraints)

	deployment := &appsv1.Deployment{
		Name:        spec.Name,
		Namespace:   namespace,
		Labels:      labels,
		Annotations: map[string]string{swarmSpecAnnotation: string(specJSON)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": spec.Name, swarmKindLabel: swarmServiceKind}},
			Strategy: serviceStrategy(spec.UpdateConfig),
			Template: template,
		},
	}
	return deployment, serviceEndpointServices(namespace, spec.Name, ports, aliases), warnings, nil
}

// serviceAliases collects the network aliases a service asks for. Kubernetes
// Service names are namespace-unique, so an alias is not scoped to one network.
func serviceAliases(spec swarmtypes.ServiceSpec) ([]string, error) {
	var aliases []string
	for _, attachment := range spec.TaskTemplate.Networks {
		aliases = append(aliases, attachment.Aliases...)
	}
	slices.Sort(aliases)
	aliases = slices.Compact(aliases)
	for _, alias := range aliases {
		if errs := validation.IsDNS1035Label(alias); len(errs) > 0 {
			return nil, InvalidArgument(fmt.Errorf("network alias %q is not usable as a Kubernetes Service name: %s", alias, errs[0]))
		}
	}
	return aliases, nil
}

func serviceReplicas(mode swarmtypes.ServiceMode) (int32, error) {
	switch {
	case mode.Global != nil:
		return 0, Unsupported(fmt.Errorf("global services are not supported; use a replicated service, or run a DaemonSet with kubectl"))
	case mode.ReplicatedJob != nil, mode.GlobalJob != nil:
		return 0, Unsupported(fmt.Errorf("job-mode services are not supported; use docker run --rm, which Dink runs as a Kubernetes Job"))
	case mode.Replicated == nil || mode.Replicated.Replicas == nil:
		return 1, nil
	case *mode.Replicated.Replicas > uint64(1<<31-1):
		return 0, InvalidArgument(fmt.Errorf("replica count %d is too large", *mode.Replicated.Replicas))
	default:
		return int32(*mode.Replicated.Replicas), nil
	}
}

// serviceWarnings reports the spec fields Kubernetes cannot honour, rather than
// failing a deployment that is otherwise valid.
func serviceWarnings(spec swarmtypes.ServiceSpec) []string {
	var warnings []string
	containerSpec := spec.TaskTemplate.ContainerSpec
	if containerSpec.User != "" || len(containerSpec.Groups) > 0 {
		warnings = append(warnings, "user and group settings are ignored: Kubernetes needs numeric UIDs in the Pod security context")
	}
	if containerSpec.Privileges != nil {
		warnings = append(warnings, "privilege settings are ignored: SELinux, seccomp, and AppArmor are Kubernetes Pod security policy concerns")
	}
	if containerSpec.Init != nil || containerSpec.Isolation != "" || len(containerSpec.Ulimits) > 0 || containerSpec.OomScoreAdj != 0 {
		warnings = append(warnings, "init, isolation, ulimit, and OOM score settings have no Kubernetes equivalent and were ignored")
	}
	if containerSpec.StopSignal != "" {
		warnings = append(warnings, "stop signal is ignored: Kubernetes always sends SIGTERM before SIGKILL")
	}
	if spec.TaskTemplate.RestartPolicy != nil && spec.TaskTemplate.RestartPolicy.Condition != "" && spec.TaskTemplate.RestartPolicy.Condition != swarmtypes.RestartPolicyConditionAny {
		warnings = append(warnings, "restart policy is ignored: Deployment Pods always restart")
	}
	if spec.RollbackConfig != nil {
		warnings = append(warnings, "rollback configuration is ignored: Kubernetes manages its own rollout history")
	}
	if spec.EndpointSpec != nil && spec.EndpointSpec.Mode == swarmtypes.ResolutionModeDNSRR {
		warnings = append(warnings, "dnsrr endpoint mode is approximated by a Kubernetes ClusterIP Service")
	}
	return warnings
}

func (s *Swarm) serviceMounts(ctx context.Context, spec *swarmtypes.ContainerSpec) ([]corev1.Volume, []corev1.VolumeMount, error) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	for index, requested := range spec.Mounts {
		if !path.IsAbs(requested.Target) {
			return nil, nil, InvalidArgument(fmt.Errorf("mount target %q must be an absolute path", requested.Target))
		}
		mountType := requested.Type
		if mountType == "" {
			mountType = mount.TypeVolume
		}
		switch mountType {
		case mount.TypeVolume:
			claim, err := s.docker.ensureContainerVolume(ctx, requested.Source)
			if err != nil {
				return nil, nil, err
			}
			volumes = append(volumes, corev1.Volume{
				Name:                  claim,
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			})
			mounts = append(mounts, corev1.VolumeMount{Name: claim, MountPath: requested.Target, ReadOnly: requested.ReadOnly})
		case mount.TypeTmpfs:
			name := fmt.Sprintf("tmpfs-%d", index)
			source := &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}
			if requested.TmpfsOptions != nil && requested.TmpfsOptions.SizeBytes > 0 {
				source.SizeLimit = resource.NewQuantity(requested.TmpfsOptions.SizeBytes, resource.BinarySI)
			}
			volumes = append(volumes, corev1.Volume{Name: name, EmptyDir: source})
			mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: requested.Target})
		default:
			return nil, nil, Unsupported(fmt.Errorf("mount type %q is not supported by the Kubernetes backend", mountType))
		}
	}
	for index, reference := range spec.Secrets {
		if reference == nil || reference.File == nil {
			return nil, nil, InvalidArgument(fmt.Errorf("secret references require a file target"))
		}
		name := fmt.Sprintf("secret-%d", index)
		target := path.Join("/run/secrets", reference.File.Name)
		volumes = append(volumes, corev1.Volume{Name: name,
			Secret: &corev1.SecretVolumeSource{
				SecretName:  reference.SecretName,
				Items:       []corev1.KeyToPath{{Key: swarmPayloadKey, Path: path.Base(target)}},
				DefaultMode: new(int32(reference.File.Mode.Perm())),
			}})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: target, SubPath: path.Base(target), ReadOnly: true})
	}
	for index, reference := range spec.Configs {
		if reference == nil || reference.File == nil {
			return nil, nil, InvalidArgument(fmt.Errorf("config references require a file target"))
		}
		name := fmt.Sprintf("config-%d", index)
		target := reference.File.Name
		if !path.IsAbs(target) {
			target = "/" + target
		}
		volumes = append(volumes, corev1.Volume{Name: name,
			ConfigMap: &corev1.ConfigMapVolumeSource{
				Name:        reference.ConfigName,
				Items:       []corev1.KeyToPath{{Key: swarmPayloadKey, Path: path.Base(target)}},
				DefaultMode: new(int32(reference.File.Mode.Perm())),
			}})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: target, SubPath: path.Base(target), ReadOnly: true})
	}
	return volumes, mounts, nil
}

func serviceResources(requirements *swarmtypes.ResourceRequirements) (corev1.ResourceRequirements, error) {
	result := corev1.ResourceRequirements{}
	if requirements == nil {
		return result, nil
	}
	if limits := requirements.Limits; limits != nil {
		if limits.NanoCPUs < 0 || limits.MemoryBytes < 0 {
			return result, InvalidArgument(fmt.Errorf("resource limits must not be negative"))
		}
		if limits.Pids > 0 {
			return result, Unsupported(fmt.Errorf("PID limits are not supported by the Kubernetes backend"))
		}
		result.Limits = corev1.ResourceList{}
		if limits.NanoCPUs > 0 {
			if limits.NanoCPUs%1_000_000 != 0 {
				return result, InvalidArgument(fmt.Errorf("CPU limit must be a whole number of millicores"))
			}
			result.Limits[corev1.ResourceCPU] = *resource.NewMilliQuantity(limits.NanoCPUs/1_000_000, resource.DecimalSI)
		}
		if limits.MemoryBytes > 0 {
			result.Limits[corev1.ResourceMemory] = *resource.NewQuantity(limits.MemoryBytes, resource.BinarySI)
		}
	}
	if reservations := requirements.Reservations; reservations != nil {
		if reservations.NanoCPUs < 0 || reservations.MemoryBytes < 0 {
			return result, InvalidArgument(fmt.Errorf("resource reservations must not be negative"))
		}
		if len(reservations.GenericResources) > 0 {
			return result, Unsupported(fmt.Errorf("generic resources are not supported; request Kubernetes extended resources instead"))
		}
		result.Requests = corev1.ResourceList{}
		if reservations.NanoCPUs > 0 {
			if reservations.NanoCPUs%1_000_000 != 0 {
				return result, InvalidArgument(fmt.Errorf("CPU reservation must be a whole number of millicores"))
			}
			result.Requests[corev1.ResourceCPU] = *resource.NewMilliQuantity(reservations.NanoCPUs/1_000_000, resource.DecimalSI)
		}
		if reservations.MemoryBytes > 0 {
			result.Requests[corev1.ResourceMemory] = *resource.NewQuantity(reservations.MemoryBytes, resource.BinarySI)
		}
	}
	return result, nil
}

func applyServiceResourceDefaults(resources *corev1.ResourceRequirements, defaults config.ResourceDefaults) error {
	for _, entry := range []struct {
		list  *corev1.ResourceList
		name  corev1.ResourceName
		value string
	}{
		{&resources.Limits, corev1.ResourceCPU, defaults.Limits.CPU},
		{&resources.Limits, corev1.ResourceMemory, defaults.Limits.Memory},
		{&resources.Requests, corev1.ResourceCPU, defaults.Requests.CPU},
		{&resources.Requests, corev1.ResourceMemory, defaults.Requests.Memory},
	} {
		if entry.value == "" {
			continue
		}
		if _, exists := (*entry.list)[entry.name]; exists {
			continue
		}
		quantity, err := resource.ParseQuantity(entry.value)
		if err != nil {
			return err
		}
		if *entry.list == nil {
			*entry.list = corev1.ResourceList{}
		}
		(*entry.list)[entry.name] = quantity
	}
	for name, requested := range resources.Requests {
		if limit, exists := resources.Limits[name]; exists && requested.Cmp(limit) > 0 {
			resources.Requests[name] = limit.DeepCopy()
		}
	}
	return nil
}

// servicePort keeps Docker's publish mode, which decides whether a port is
// published through a Kubernetes Service or bound on the task's own node.
type servicePort struct {
	target    uint16
	published uint16
	protocol  corev1.Protocol
	hostMode  bool
}

func servicePorts(spec *swarmtypes.EndpointSpec) ([]servicePort, []string, error) {
	if spec == nil {
		return nil, nil, nil
	}
	var warnings []string
	ports := make([]servicePort, 0, len(spec.Ports))
	for _, port := range spec.Ports {
		if port.TargetPort == 0 || port.TargetPort > 65535 {
			return nil, nil, InvalidArgument(fmt.Errorf("target port must be between 1 and 65535"))
		}
		if port.PublishedPort > 65535 {
			return nil, nil, InvalidArgument(fmt.Errorf("published port must be between 0 and 65535"))
		}
		protocol := corev1.Protocol(strings.ToUpper(string(port.Protocol)))
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		switch protocol {
		case corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
		default:
			return nil, nil, InvalidArgument(fmt.Errorf("unsupported port protocol %q", port.Protocol))
		}
		entry := servicePort{
			target:    uint16(port.TargetPort),
			published: uint16(port.PublishedPort),
			protocol:  protocol,
			hostMode:  port.PublishMode == swarmtypes.PortConfigPublishModeHost,
		}
		if entry.hostMode && entry.published == 0 {
			entry.published = entry.target
			warnings = append(warnings, fmt.Sprintf("host-mode port %d has no published port; Kubernetes hostPort cannot be allocated dynamically, so the target port was used", entry.target))
		}
		ports = append(ports, entry)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].target < ports[j].target })
	return ports, warnings, nil
}

func hasIngressPorts(ports []servicePort) bool {
	for _, port := range ports {
		if !port.hostMode {
			return true
		}
	}
	return false
}

// serviceContainerPorts binds host-mode ports on the node running the task,
// which is the closest Kubernetes equivalent to Swarm's host publish mode.
func serviceContainerPorts(ports []servicePort) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(ports))
	seen := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		key := fmt.Sprintf("%d/%s", port.target, port.protocol)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entry := corev1.ContainerPort{ContainerPort: int32(port.target), Protocol: port.protocol}
		if port.hostMode {
			entry.HostPort = int32(port.published)
		}
		result = append(result, entry)
	}
	return result
}

// serviceNetworkLabels records the Docker networks a service belongs to.
// Publishing a port joins the ingress network, as it does in Swarm.
func (s *Swarm) serviceNetworkLabels(ctx context.Context, spec swarmtypes.ServiceSpec, published bool) (map[string]string, error) {
	names := make([]string, 0, len(spec.TaskTemplate.Networks)+1)
	for _, attachment := range spec.TaskTemplate.Networks {
		if attachment.Target != "" {
			names = append(names, attachment.Target)
		}
	}
	if published {
		names = append(names, ingressNetworkName)
	}
	if len(names) == 0 {
		names = append(names, networktypes.NetworkBridge)
	}
	sort.Strings(names)
	return s.docker.networkMembershipLabels(ctx, slices.Compact(names))
}

// serviceEndpointServices returns the ClusterIP Service that stands in for the
// Swarm VIP, a LoadBalancer Service for ingress-published ports, and one
// Service per network alias.
func serviceEndpointServices(namespace, name string, ports []servicePort, aliases []string) []*corev1.Service {
	labels := map[string]string{"app": name, swarmKindLabel: swarmServiceKind}
	selector := map[string]string{"app": name, swarmKindLabel: swarmServiceKind}
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	published := make([]corev1.ServicePort, 0, len(ports))
	for index, port := range ports {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", index),
			Port:       int32(port.target),
			TargetPort: intstr.FromInt32(int32(port.target)),
			Protocol:   port.protocol,
		})
		if port.hostMode {
			continue
		}
		hostPort := port.published
		if hostPort == 0 {
			hostPort = port.target
		}
		published = append(published, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", index),
			Port:       int32(hostPort),
			TargetPort: intstr.FromInt32(int32(port.target)),
			Protocol:   port.protocol,
		})
	}
	vip := &corev1.Service{
		Name:      name,
		Namespace: namespace,
		Labels:    labels,
		Spec:      corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector, Ports: servicePorts},
	}
	if len(servicePorts) == 0 {
		vip.Spec.ClusterIP = corev1.ClusterIPNone
	}
	services := []*corev1.Service{vip}
	if len(published) > 0 {
		services = append(services, &corev1.Service{
			Name:      publishedPortsServiceName(name),
			Namespace: namespace,
			Labels:    labels,
			Spec:      corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Selector: selector, Ports: published},
		})
	}
	for _, alias := range aliases {
		aliasLabels := maps.Clone(labels)
		aliasLabels[aliasOfLabel] = name
		aliasLabels[managedByLabel] = managedByDink
		aliasService := &corev1.Service{
			Name:      alias,
			Namespace: namespace,
			Labels:    aliasLabels,
			Spec:      corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector, Ports: servicePorts},
		}
		if len(servicePorts) == 0 {
			aliasService.Spec.ClusterIP = corev1.ClusterIPNone
		}
		services = append(services, aliasService)
	}
	return services
}

// applyServiceEndpoints creates or updates the Services owned by a Deployment
// and removes the ones the current spec no longer needs.
func (s *Swarm) applyServiceEndpoints(ctx context.Context, deployment *appsv1.Deployment, services []*corev1.Service) error {
	owner := deploymentOwnerReference(deployment.Name, deployment.UID)
	wanted := make(map[string]struct{}, len(services))
	for _, service := range services {
		wanted[service.Name] = struct{}{}
		service.OwnerReferences = []metav1.OwnerReference{owner}
		existing, err := s.k8s.CoreV1().Services(deployment.Namespace).Get(ctx, service.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if _, err := s.k8s.CoreV1().Services(deployment.Namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
				return kubeError(err)
			}
			continue
		}
		if err != nil {
			return kubeError(err)
		}
		service.ResourceVersion = existing.ResourceVersion
		service.Spec.ClusterIP = existing.Spec.ClusterIP
		retainNodePorts(&service.Spec, existing.Spec)
		if _, err := s.k8s.CoreV1().Services(deployment.Namespace).Update(ctx, service, metav1.UpdateOptions{}); err != nil {
			return kubeError(err)
		}
	}
	stale := []string{deployment.Name, publishedPortsServiceName(deployment.Name)}
	aliases, err := s.k8s.CoreV1().Services(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: aliasOfLabel + "=" + deployment.Name})
	if err != nil {
		return kubeError(err)
	}
	for index := range aliases.Items {
		stale = append(stale, aliases.Items[index].Name)
	}
	for _, name := range stale {
		if _, keep := wanted[name]; keep {
			continue
		}
		err := s.k8s.CoreV1().Services(deployment.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return kubeError(err)
		}
	}
	return nil
}

// retainNodePorts keeps the node ports Kubernetes already allocated, so
// updating a service does not move its published ports.
func retainNodePorts(desired *corev1.ServiceSpec, existing corev1.ServiceSpec) {
	allocated := make(map[int32]int32, len(existing.Ports))
	for _, port := range existing.Ports {
		if port.NodePort != 0 {
			allocated[port.Port] = port.NodePort
		}
	}
	for index := range desired.Ports {
		if nodePort, ok := allocated[desired.Ports[index].Port]; ok {
			desired.Ports[index].NodePort = nodePort
		}
	}
}

func serviceStrategy(update *swarmtypes.UpdateConfig) appsv1.DeploymentStrategy {
	strategy := appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
	if update == nil {
		return strategy
	}
	rolling := &appsv1.RollingUpdateDeployment{}
	if update.Parallelism > 0 && update.Parallelism <= uint64(1<<31-1) {
		surge := intstr.FromInt32(int32(update.Parallelism))
		rolling.MaxSurge = &surge
	}
	if update.Order == swarmtypes.UpdateOrderStartFirst {
		unavailable := intstr.FromInt32(0)
		rolling.MaxUnavailable = &unavailable
	}
	strategy.RollingUpdate = rolling
	return strategy
}

// placementRequirements maps Swarm placement constraints onto node selector
// requirements. Only node and engine labels have a Kubernetes equivalent.
func placementRequirements(placement *swarmtypes.Placement) ([]corev1.NodeSelectorRequirement, error) {
	if placement == nil {
		return nil, nil
	}
	if len(placement.Preferences) > 0 {
		return nil, Unsupported(fmt.Errorf("placement preferences are not supported; Kubernetes spreads Pods with topology spread constraints"))
	}
	var requirements []corev1.NodeSelectorRequirement
	for _, constraint := range placement.Constraints {
		key, value, operator, err := parsePlacementConstraint(constraint)
		if err != nil {
			return nil, err
		}
		requirement := corev1.NodeSelectorRequirement{Key: key, Operator: operator}
		if operator == corev1.NodeSelectorOpIn || operator == corev1.NodeSelectorOpNotIn {
			requirement.Values = []string{value}
		}
		requirements = append(requirements, requirement)
	}
	return requirements, nil
}

const controlPlaneLabel = "node-role.kubernetes.io/control-plane"

func parsePlacementConstraint(constraint string) (string, string, corev1.NodeSelectorOperator, error) {
	operator := corev1.NodeSelectorOpIn
	key, value, found := strings.Cut(constraint, "==")
	if !found {
		key, value, found = strings.Cut(constraint, "!=")
		if !found {
			return "", "", "", InvalidArgument(fmt.Errorf("invalid placement constraint %q", constraint))
		}
		operator = corev1.NodeSelectorOpNotIn
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	switch {
	case key == "node.hostname":
		return corev1.LabelHostname, value, operator, nil
	case key == "node.role":
		// A manager is a control-plane node, so role becomes a label presence test.
		manager := value == string(swarmtypes.NodeRoleManager)
		if !manager && value != string(swarmtypes.NodeRoleWorker) {
			return "", "", "", InvalidArgument(fmt.Errorf("invalid node role %q in placement constraint", value))
		}
		if operator == corev1.NodeSelectorOpNotIn {
			manager = !manager
		}
		if manager {
			return controlPlaneLabel, "", corev1.NodeSelectorOpExists, nil
		}
		return controlPlaneLabel, "", corev1.NodeSelectorOpDoesNotExist, nil
	case strings.HasPrefix(key, "node.labels."):
		return strings.TrimPrefix(key, "node.labels."), value, operator, nil
	case strings.HasPrefix(key, "engine.labels."):
		return strings.TrimPrefix(key, "engine.labels."), value, operator, nil
	default:
		return "", "", "", Unsupported(fmt.Errorf("placement constraint %q has no Kubernetes equivalent; use node.hostname, node.role, node.labels.*, or engine.labels.*", constraint))
	}
}

// appendNodeSelectorRequirements ANDs requirements into every existing node
// selector term, preserving the tenant placement terms.
func appendNodeSelectorRequirements(spec *corev1.PodSpec, requirements []corev1.NodeSelectorRequirement) {
	if len(requirements) == 0 {
		return
	}
	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	selector := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if selector == nil || len(selector.NodeSelectorTerms) == 0 {
		spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: requirements}},
		}
		return
	}
	for index := range selector.NodeSelectorTerms {
		term := &selector.NodeSelectorTerms[index]
		term.MatchExpressions = append(term.MatchExpressions, requirements...)
	}
}

func capabilities(values []string) []corev1.Capability {
	result := make([]corev1.Capability, 0, len(values))
	for _, value := range values {
		result = append(result, corev1.Capability(strings.TrimPrefix(strings.ToUpper(value), "CAP_")))
	}
	return result
}

func hostnameFor(value string) string {
	if errs := validation.IsDNS1123Label(value); len(errs) > 0 {
		return ""
	}
	return value
}

func serviceHostAliases(hosts []string) []corev1.HostAlias {
	var aliases []corev1.HostAlias
	for _, host := range hosts {
		fields := strings.Fields(host)
		if len(fields) < 2 {
			continue
		}
		aliases = append(aliases, corev1.HostAlias{IP: fields[0], Hostnames: fields[1:]})
	}
	return aliases
}

func serviceDNSConfig(dns *swarmtypes.DNSConfig) *corev1.PodDNSConfig {
	if dns == nil || (len(dns.Nameservers) == 0 && len(dns.Search) == 0 && len(dns.Options) == 0) {
		return nil
	}
	config := &corev1.PodDNSConfig{Searches: dns.Search}
	for _, nameserver := range dns.Nameservers {
		config.Nameservers = append(config.Nameservers, nameserver.String())
	}
	for _, option := range dns.Options {
		name, value, found := strings.Cut(option, ":")
		entry := corev1.PodDNSConfigOption{Name: name}
		if found {
			entry.Value = &value
		}
		config.Options = append(config.Options, entry)
	}
	return config
}

func podSysctls(sysctls map[string]string) []corev1.Sysctl {
	names := make([]string, 0, len(sysctls))
	for name := range sysctls {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]corev1.Sysctl, 0, len(names))
	for _, name := range names {
		result = append(result, corev1.Sysctl{Name: name, Value: sysctls[name]})
	}
	return result
}

func validateServiceFilters(filters swarmFilters) error {
	return filters.Validate(map[string]bool{"id": true, "label": true, "mode": true, "name": true, "runtime": true})
}

func matchesServiceFilters(filters swarmFilters, service swarmtypes.Service) bool {
	if filters.Len() == 0 {
		return true
	}
	if !filters.FuzzyMatch("id", service.ID) {
		return false
	}
	if !filters.Match("name", service.Spec.Name) {
		return false
	}
	if !filters.ExactMatch("mode", "replicated") {
		return false
	}
	if !filters.ExactMatch("runtime", string(swarmtypes.RuntimeContainer)) {
		return false
	}
	return filters.MatchKVList("label", service.Spec.Labels)
}
