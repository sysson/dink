package translator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (d *Docker) ContainerCreate(ctx context.Context, cfg backend.ContainerCreateConfig) (container.CreateResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return container.CreateResponse{}, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if cfg.Config == nil {
		return container.CreateResponse{}, InvalidArgument(fmt.Errorf("container configuration is required"))
	}
	if cfg.Name == "" {
		cfg.Name = generatedContainerName()
	}
	// A missing image is reported as not found, which makes the docker CLI pull and retry.
	image, imageConfig, err := d.podImage(ctx, id.Namespace, cfg.Config.Image)
	if err != nil {
		return container.CreateResponse{}, err
	}
	mergedConfig, err := mergeImageConfig(cfg.Config, imageConfig)
	if err != nil {
		return container.CreateResponse{}, InvalidArgument(err)
	}
	cfg.Config = mergedConfig
	resources, err := containerResources(cfg.HostConfig)
	if err != nil {
		return container.CreateResponse{}, InvalidArgument(err)
	}
	defaults, err := d.tenantResourceDefaults(ctx, id.Namespace)
	if err != nil {
		return container.CreateResponse{}, err
	}
	if err := applyResourceDefaults(&cfg, &resources, defaults); err != nil {
		return container.CreateResponse{}, InvalidArgument(err)
	}
	ports, serviceType, warnings, err := resolvePublishedPorts(cfg.Config, cfg.HostConfig)
	if err != nil {
		return container.CreateResponse{}, InvalidArgument(err)
	}
	readinessProbe, probeWarnings, err := dockerHealthProbe(cfg.Config.Healthcheck, cfg.Config.Shell)
	if err != nil {
		return container.CreateResponse{}, InvalidArgument(err)
	}
	warnings = append(warnings, probeWarnings...)
	volumes, volumeMounts, err := d.containerVolumes(ctx, cfg.Config, cfg.HostConfig)
	if err != nil {
		return container.CreateResponse{}, err
	}
	networkLabels, hostNetwork, err := d.containerNetworkLabels(ctx, cfg)
	if err != nil {
		return container.CreateResponse{}, err
	}
	if err := d.ensurePullSecret(ctx, id.Namespace); err != nil {
		return container.CreateResponse{}, err
	}
	annotations, err := containerAnnotations(cfg)
	if err != nil {
		return container.CreateResponse{}, InvalidArgument(fmt.Errorf("encoding container metadata: %w", err))
	}
	podLabels := map[string]string{"app": cfg.Name}
	maps.Copy(podLabels, networkLabels)
	deployment, err := d.k8s.AppsV1().Deployments(id.Namespace).Create(ctx,
		&appsv1.Deployment{
			Name: cfg.Name,
			Labels: map[string]string{
				"app": cfg.Name,
			},
			Annotations: annotations,
			Namespace:   id.Namespace,
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32(0)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": cfg.Name,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: podLabels,
					},
					Spec: corev1.PodSpec{
						HostNetwork:      hostNetwork,
						DNSPolicy:        podDNSPolicy(hostNetwork),
						Volumes:          volumes,
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: pullSecretName}},
						Containers: []corev1.Container{
							{
								Name:           cfg.Name,
								Image:          image,
								Ports:          podPorts(ports),
								Command:        cfg.Config.Entrypoint,
								Args:           cfg.Config.Cmd,
								Env:            containerEnv(cfg.Config.Env),
								WorkingDir:     cfg.Config.WorkingDir,
								TTY:            cfg.Config.Tty,
								Stdin:          cfg.Config.OpenStdin,
								Resources:      resources,
								VolumeMounts:   volumeMounts,
								ReadinessProbe: readinessProbe,
								// Checks the credential on every start, so a node's cached copy is not shared across namespaces.
								ImagePullPolicy: corev1.PullAlways,
							},
						},
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return container.CreateResponse{}, kubeError(err)
	}
	dnsService := containerDNSService(deployment, ports)
	if _, err := d.k8s.CoreV1().Services(id.Namespace).Create(ctx, dnsService, metav1.CreateOptions{}); err != nil {
		_ = d.k8s.AppsV1().Deployments(id.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &deployment.UID}})
		return container.CreateResponse{}, kubeError(err)
	}
	if serviceType != "" {
		service := publishedPortsService(deployment, serviceType, ports)
		if _, err := d.k8s.CoreV1().Services(id.Namespace).Create(ctx, service, metav1.CreateOptions{}); err != nil {
			_ = d.k8s.CoreV1().Services(id.Namespace).Delete(ctx, dnsService.Name, metav1.DeleteOptions{})
			_ = d.k8s.AppsV1().Deployments(id.Namespace).Delete(ctx, deployment.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &deployment.UID}})
			return container.CreateResponse{}, kubeError(err)
		}
	}
	return container.CreateResponse{
		ID:       identity.DockerIDFromUID(deployment.UID),
		Warnings: warnings,
	}, nil
}

var containerNameAdjectives = [...]string{
	"amber", "brisk", "calm", "clever", "crisp", "eager", "gentle", "happy", "keen",
	"lively", "mellow", "nimble", "quiet", "rapid", "steady", "sunny", "tidy", "vivid",
}

var containerNameNouns = [...]string{
	"arch", "beacon", "birch", "brook", "canyon", "cedar", "comet", "delta", "ember", "grove",
	"harbor", "meadow", "mesa", "orbit", "quartz", "ridge", "river", "summit", "willow", "zenith",
}

func generatedContainerName() string {
	return fmt.Sprintf("%s-%s-%08d",
		containerNameAdjectives[rand.IntN(len(containerNameAdjectives))],
		containerNameNouns[rand.IntN(len(containerNameNouns))],
		rand.IntN(100_000_000),
	)
}

func (d *Docker) tenantResourceDefaults(ctx context.Context, namespace string) (config.ResourceDefaults, error) {
	defaults := d.defaultResources
	configMap, err := d.k8s.CoreV1().ConfigMaps(namespace).Get(ctx, tenantResourceDefaultsConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return defaults, nil
	}
	if err != nil {
		return defaults, kubeError(err)
	}
	data, ok := configMap.Data["resources.json"]
	if !ok {
		return defaults, fmt.Errorf("%s/%s is missing resources.json", namespace, tenantResourceDefaultsConfigMap)
	}
	var override config.ResourceDefaults
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&override); err != nil {
		return defaults, fmt.Errorf("decoding resource defaults for %s: %w", namespace, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return defaults, fmt.Errorf("resource defaults for %s must contain one JSON object", namespace)
	}
	if override.Limits.CPU != "" {
		defaults.Limits.CPU = override.Limits.CPU
	}
	if override.Limits.Memory != "" {
		defaults.Limits.Memory = override.Limits.Memory
	}
	if override.Requests.CPU != "" {
		defaults.Requests.CPU = override.Requests.CPU
	}
	if override.Requests.Memory != "" {
		defaults.Requests.Memory = override.Requests.Memory
	}
	if err := defaults.Validate(); err != nil {
		return defaults, fmt.Errorf("invalid resource defaults for %s: %w", namespace, err)
	}
	return defaults, nil
}

func applyResourceDefaults(cfg *backend.ContainerCreateConfig, resources *corev1.ResourceRequirements, defaults config.ResourceDefaults) error {
	hostConfig := &container.HostConfig{}
	if cfg.HostConfig != nil {
		*hostConfig = *cfg.HostConfig
	}
	if hostConfig.Annotations != nil {
		hostConfig.Annotations = maps.Clone(hostConfig.Annotations)
	}
	for _, entry := range []struct {
		name  corev1.ResourceName
		value string
	}{
		{corev1.ResourceCPU, defaults.Limits.CPU},
		{corev1.ResourceMemory, defaults.Limits.Memory},
	} {
		if _, exists := resources.Limits[entry.name]; exists || entry.value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(entry.value)
		if err != nil {
			return err
		}
		if resources.Limits == nil {
			resources.Limits = corev1.ResourceList{}
		}
		resources.Limits[entry.name] = quantity
		if entry.name == corev1.ResourceCPU {
			hostConfig.NanoCPUs = quantity.MilliValue() * 1_000_000
		} else {
			hostConfig.Memory = quantity.Value()
		}
	}
	for _, entry := range []struct {
		name  corev1.ResourceName
		value string
	}{
		{corev1.ResourceCPU, defaults.Requests.CPU},
		{corev1.ResourceMemory, defaults.Requests.Memory},
	} {
		if _, exists := resources.Requests[entry.name]; exists || entry.value == "" {
			continue
		}
		quantity, err := resource.ParseQuantity(entry.value)
		if err != nil {
			return err
		}
		if limit, exists := resources.Limits[entry.name]; exists && quantity.Cmp(limit) > 0 {
			quantity = limit.DeepCopy()
		}
		if resources.Requests == nil {
			resources.Requests = corev1.ResourceList{}
		}
		resources.Requests[entry.name] = quantity
		if entry.name == corev1.ResourceMemory {
			hostConfig.MemoryReservation = quantity.Value()
		}
	}
	for name, requested := range resources.Requests {
		if limit, exists := resources.Limits[name]; exists && requested.Cmp(limit) > 0 {
			return fmt.Errorf("%s request exceeds effective limit", name)
		}
	}
	if cpu, exists := resources.Requests[corev1.ResourceCPU]; exists {
		if hostConfig.Annotations == nil {
			hostConfig.Annotations = map[string]string{}
		}
		hostConfig.Annotations["dink.io/requests.cpu"] = cpu.String()
	}
	cfg.HostConfig = hostConfig
	return nil
}

func (d *Docker) containerNetworkLabels(ctx context.Context, cfg backend.ContainerCreateConfig) (map[string]string, bool, error) {
	mode := ""
	if cfg.HostConfig != nil {
		mode = string(cfg.HostConfig.NetworkMode)
	}
	endpoints := map[string]*network.EndpointSettings(nil)
	if cfg.NetworkingConfig != nil {
		endpoints = cfg.NetworkingConfig.EndpointsConfig
	}
	if mode == "host" {
		if len(endpoints) != 0 {
			return nil, false, InvalidArgument(fmt.Errorf("host network mode cannot be combined with network endpoints"))
		}
		return nil, true, nil
	}
	if strings.HasPrefix(mode, "container:") {
		return nil, false, InvalidArgument(fmt.Errorf("container network mode is not supported by the Kubernetes backend"))
	}

	networkNames := make([]string, 0, len(endpoints)+1)
	switch mode {
	case "none":
		if len(endpoints) != 0 {
			return nil, false, InvalidArgument(fmt.Errorf("none network mode cannot be combined with network endpoints"))
		}
		networkNames = append(networkNames, "none")
	case "", "default", "bridge":
		if len(endpoints) == 0 {
			networkNames = append(networkNames, "bridge")
		} else {
			for name := range endpoints {
				if name == network.NetworkDefault {
					name = network.NetworkBridge
				}
				networkNames = append(networkNames, name)
			}
		}
	default:
		if len(endpoints) == 0 {
			networkNames = append(networkNames, mode)
		} else {
			for name := range endpoints {
				if name == network.NetworkDefault {
					name = network.NetworkBridge
				}
				networkNames = append(networkNames, name)
			}
		}
	}
	sort.Strings(networkNames)
	labels := make(map[string]string, len(networkNames))
	for _, name := range networkNames {
		objectName := networkObjectName(name)
		if _, builtin := builtinNetworkDrivers[name]; !builtin {
			obj, err := d.findNetwork(ctx, name)
			if err != nil {
				return nil, false, err
			}
			objectName = obj.GetName()
		}
		labels[networkLabelPrefix+objectName] = "true"
	}
	return labels, false, nil
}

func podDNSPolicy(hostNetwork bool) corev1.DNSPolicy {
	if hostNetwork {
		return corev1.DNSClusterFirstWithHostNet
	}
	return corev1.DNSClusterFirst
}

func containerAnnotations(cfg backend.ContainerCreateConfig) (map[string]string, error) {
	configJSON, err := json.Marshal(cfg.Config)
	if err != nil {
		return nil, err
	}
	hostConfig := cfg.HostConfig
	if hostConfig == nil {
		hostConfig = &container.HostConfig{}
	}
	hostConfigJSON, err := json.Marshal(hostConfig)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		containerConfigAnnotation:     string(configJSON),
		containerHostConfigAnnotation: string(hostConfigJSON),
	}, nil
}

func containerEnv(env []string) []corev1.EnvVar {
	result := make([]corev1.EnvVar, 0, len(env))
	for _, value := range env {
		name, val, found := strings.Cut(value, "=")
		if !found {
			val = ""
		}
		result = append(result, corev1.EnvVar{Name: name, Value: val})
	}
	return result
}

type publishedPort struct {
	containerPort uint16
	hostPort      uint16
	protocol      corev1.Protocol
}

func resolvePublishedPorts(config *container.Config, hostConfig *container.HostConfig) ([]publishedPort, corev1.ServiceType, []string, error) {
	if hostConfig == nil {
		hostConfig = &container.HostConfig{}
	}
	var ports []publishedPort
	var warnings []string
	serviceType := corev1.ServiceType("")
	if hostConfig.PublishAllPorts {
		serviceType = corev1.ServiceTypeNodePort
		for port := range config.ExposedPorts {
			ports = append(ports, publishedPort{containerPort: port.Num(), protocol: corev1.Protocol(strings.ToUpper(string(port.Proto())))})
		}
	} else {
		for port, bindings := range hostConfig.PortBindings {
			for _, binding := range bindings {
				hostPort := uint16(0)
				if binding.HostPort != "" {
					parsed, err := strconv.ParseUint(binding.HostPort, 10, 16)
					if err != nil {
						return nil, "", nil, fmt.Errorf("invalid published port %q: %w", binding.HostPort, err)
					}
					hostPort = uint16(parsed)
				}
				if binding.HostIP.IsValid() && !binding.HostIP.IsUnspecified() {
					warnings = append(warnings, fmt.Sprintf("host IP %q has no Kubernetes Service equivalent and was ignored", binding.HostIP))
				}
				ports = append(ports, publishedPort{containerPort: port.Num(), hostPort: hostPort, protocol: corev1.Protocol(strings.ToUpper(string(port.Proto())))})
			}
		}
		if len(ports) > 0 {
			serviceType = corev1.ServiceTypeLoadBalancer
		}
	}
	if len(ports) == 0 {
		serviceType = ""
	}
	for _, port := range ports {
		if port.containerPort == 0 {
			return nil, "", nil, fmt.Errorf("container port must be between 1 and 65535")
		}
		switch port.protocol {
		case corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
		default:
			return nil, "", nil, fmt.Errorf("unsupported port protocol %q", port.protocol)
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].containerPort != ports[j].containerPort {
			return ports[i].containerPort < ports[j].containerPort
		}
		if ports[i].protocol != ports[j].protocol {
			return ports[i].protocol < ports[j].protocol
		}
		return ports[i].hostPort < ports[j].hostPort
	})
	return ports, serviceType, warnings, nil
}

func podPorts(ports []publishedPort) []corev1.ContainerPort {
	result := make([]corev1.ContainerPort, 0, len(ports))
	seen := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		key := fmt.Sprintf("%d/%s", port.containerPort, port.protocol)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, corev1.ContainerPort{ContainerPort: int32(port.containerPort), Protocol: port.protocol})
	}
	return result
}

func containerDNSService(deployment *appsv1.Deployment, ports []publishedPort) *corev1.Service {
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	for index, port := range ports {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", index),
			Port:       int32(port.containerPort),
			TargetPort: intstr.FromInt32(int32(port.containerPort)),
			Protocol:   port.protocol,
		})
	}
	spec := corev1.ServiceSpec{
		Type:     corev1.ServiceTypeClusterIP,
		Selector: map[string]string{"app": deployment.Name},
		Ports:    servicePorts,
	}
	if len(servicePorts) == 0 {
		spec.ClusterIP = corev1.ClusterIPNone
	}
	return &corev1.Service{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
		Labels:    map[string]string{"app": deployment.Name},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       deployment.Name,
			UID:        deployment.UID,
			Controller: new(true),
		}},
		Spec: spec,
	}
}

func publishedPortsService(deployment *appsv1.Deployment, serviceType corev1.ServiceType, ports []publishedPort) *corev1.Service {
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	for index, port := range ports {
		servicePort := port.hostPort
		if servicePort == 0 {
			servicePort = port.containerPort
		}
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", index),
			Port:       int32(servicePort),
			TargetPort: intstr.FromInt32(int32(port.containerPort)),
			Protocol:   port.protocol,
		})
	}
	return &corev1.Service{
		Name:      publishedPortsServiceName(deployment.Name),
		Namespace: deployment.Namespace,
		Labels:    map[string]string{"app": deployment.Name},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       deployment.Name,
			UID:        deployment.UID,
			Controller: new(true),
		}},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: map[string]string{"app": deployment.Name},
			Ports:    servicePorts,
		},
	}
}

// podImage returns the reference nodes pull name from: the namespace's
// repository on pullHost, pinned to the digest name resolves to now.
func (d *Docker) podImage(ctx context.Context, namespace, name string) (string, *container.Config, error) {
	image, err := d.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{})
	if err != nil {
		return "", nil, err
	}
	if len(image.RepoDigests) == 0 {
		return "", nil, fmt.Errorf("image %s has no repository digest", name)
	}
	return d.pullHost + "/" + namespace + "/" + image.RepoDigests[0], containerConfigFromImage(image.Config), nil
}

func containerConfigFromImage(image *dockerspec.DockerOCIImageConfig) *container.Config {
	if image == nil {
		return nil
	}
	config := image.ImageConfig
	result := &container.Config{
		User:       config.User,
		Env:        slices.Clone(config.Env),
		Cmd:        slices.Clone(config.Cmd),
		Entrypoint: slices.Clone(config.Entrypoint),
		WorkingDir: config.WorkingDir,
		Volumes:    maps.Clone(config.Volumes),
		Labels:     maps.Clone(config.Labels),
		StopSignal: config.StopSignal,
		OnBuild:    slices.Clone(image.OnBuild),
		Shell:      slices.Clone(image.Shell),
	}
	if image.Healthcheck != nil {
		healthcheck := *image.Healthcheck
		healthcheck.Test = slices.Clone(image.Healthcheck.Test)
		result.Healthcheck = &healthcheck
	}
	if len(config.ExposedPorts) > 0 {
		result.ExposedPorts = make(network.PortSet, len(config.ExposedPorts))
		for value := range config.ExposedPorts {
			port, err := network.ParsePort(value)
			if err != nil {
				continue
			}
			result.ExposedPorts[port] = struct{}{}
		}
	}
	return result
}

func mergeImageConfig(request, image *container.Config) (*container.Config, error) {
	if request == nil {
		return nil, fmt.Errorf("container configuration is required")
	}
	merged := *request
	merged.Env = slices.Clone(request.Env)
	merged.Cmd = slices.Clone(request.Cmd)
	merged.Entrypoint = slices.Clone(request.Entrypoint)
	merged.Shell = slices.Clone(request.Shell)
	merged.ExposedPorts = maps.Clone(request.ExposedPorts)
	merged.Labels = maps.Clone(request.Labels)
	merged.Volumes = maps.Clone(request.Volumes)
	if merged.ExposedPorts == nil {
		merged.ExposedPorts = make(network.PortSet)
	}
	if merged.Labels == nil {
		merged.Labels = make(map[string]string)
	}
	if merged.Volumes == nil {
		merged.Volumes = make(map[string]struct{})
	}
	if image == nil {
		return &merged, nil
	}

	if merged.User == "" {
		merged.User = image.User
	}
	if len(merged.Shell) == 0 {
		merged.Shell = slices.Clone(image.Shell)
	}
	for port := range image.ExposedPorts {
		merged.ExposedPorts[port] = struct{}{}
	}
	imageEnvKeys := make(map[string]struct{}, len(merged.Env))
	for _, env := range merged.Env {
		key, _, _ := strings.Cut(env, "=")
		imageEnvKeys[key] = struct{}{}
	}
	for _, env := range image.Env {
		key, _, _ := strings.Cut(env, "=")
		if _, overridden := imageEnvKeys[key]; !overridden {
			merged.Env = append(merged.Env, env)
		}
	}
	for key, value := range image.Labels {
		if _, overridden := merged.Labels[key]; !overridden {
			merged.Labels[key] = value
		}
	}
	if len(merged.Entrypoint) == 0 {
		if len(merged.Cmd) == 0 {
			merged.Cmd = slices.Clone(image.Cmd)
		}
		if request.Entrypoint == nil {
			merged.Entrypoint = slices.Clone(image.Entrypoint)
		}
	}
	if image.Healthcheck != nil {
		if merged.Healthcheck == nil {
			healthcheck := *image.Healthcheck
			merged.Healthcheck = &healthcheck
			if healthcheck.Test != nil {
				merged.Healthcheck.Test = append([]string(nil), healthcheck.Test...)
			}
		} else {
			if len(merged.Healthcheck.Test) == 0 {
				merged.Healthcheck.Test = append([]string(nil), image.Healthcheck.Test...)
			}
			if merged.Healthcheck.Interval == 0 {
				merged.Healthcheck.Interval = image.Healthcheck.Interval
			}
			if merged.Healthcheck.Timeout == 0 {
				merged.Healthcheck.Timeout = image.Healthcheck.Timeout
			}
			if merged.Healthcheck.StartPeriod == 0 {
				merged.Healthcheck.StartPeriod = image.Healthcheck.StartPeriod
			}
			if merged.Healthcheck.StartInterval == 0 {
				merged.Healthcheck.StartInterval = image.Healthcheck.StartInterval
			}
			if merged.Healthcheck.Retries == 0 {
				merged.Healthcheck.Retries = image.Healthcheck.Retries
			}
		}
	}
	if merged.WorkingDir == "" {
		merged.WorkingDir = image.WorkingDir
	}
	for volume := range image.Volumes {
		merged.Volumes[volume] = struct{}{}
	}
	if merged.StopSignal == "" {
		merged.StopSignal = image.StopSignal
	}
	return &merged, nil
}

func dockerHealthProbe(healthcheck *container.HealthConfig, shell []string) (*corev1.Probe, []string, error) {
	if healthcheck == nil || len(healthcheck.Test) == 0 {
		return nil, nil, nil
	}

	var command []string
	switch healthcheck.Test[0] {
	case "NONE":
		if len(healthcheck.Test) != 1 {
			return nil, nil, fmt.Errorf("Docker healthcheck NONE cannot include a command")
		}
		return nil, nil, nil
	case "CMD":
		if len(healthcheck.Test) < 2 {
			return nil, nil, fmt.Errorf("Docker healthcheck CMD requires a command")
		}
		command = slices.Clone(healthcheck.Test[1:])
	case "CMD-SHELL":
		if len(healthcheck.Test) < 2 {
			return nil, nil, fmt.Errorf("Docker healthcheck CMD-SHELL requires a command")
		}
		if len(shell) == 0 {
			shell = []string{"/bin/sh"}
		}
		command = append(slices.Clone(shell), "-c", strings.Join(healthcheck.Test[1:], " "))
	default:
		return nil, nil, fmt.Errorf("unsupported Docker healthcheck test %q", healthcheck.Test[0])
	}

	interval, err := dockerProbeSeconds("interval", healthcheck.Interval, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	timeout, err := dockerProbeSeconds("timeout", healthcheck.Timeout, 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	initialDelay, err := dockerProbeSeconds("start period", healthcheck.StartPeriod, 0)
	if err != nil {
		return nil, nil, err
	}
	retries := healthcheck.Retries
	if retries == 0 {
		retries = 3
	}
	if retries < 0 || int64(retries) > int64(1<<31-1) {
		return nil, nil, fmt.Errorf("Docker healthcheck retries must be between 1 and %d", int64(1<<31-1))
	}
	probe := &corev1.Probe{
		Exec:             &corev1.ExecAction{Command: command},
		PeriodSeconds:    interval,
		TimeoutSeconds:   timeout,
		FailureThreshold: int32(retries),
		SuccessThreshold: 1,
	}
	if healthcheck.StartPeriod > 0 {
		probe.InitialDelaySeconds = initialDelay
	}
	warnings := make([]string, 0, 3)
	if healthcheck.StartPeriod > 0 {
		warnings = append(warnings, "Docker healthcheck start period is approximated with Kubernetes readiness-probe initial delay.")
	}
	if healthcheck.Interval%time.Second != 0 || healthcheck.Timeout%time.Second != 0 || healthcheck.StartPeriod%time.Second != 0 {
		warnings = append(warnings, "Sub-second Docker healthcheck timings are rounded up to whole seconds for Kubernetes probes.")
	}
	if healthcheck.StartInterval < 0 {
		return nil, nil, fmt.Errorf("Docker healthcheck start interval cannot be negative")
	}
	if healthcheck.StartInterval > 0 {
		warnings = append(warnings, "Docker healthcheck start interval is not supported by Kubernetes probes; the regular interval is used during startup.")
	}
	return probe, warnings, nil
}

func dockerProbeSeconds(name string, duration, defaultDuration time.Duration) (int32, error) {
	if duration == 0 {
		duration = defaultDuration
	}
	if duration < 0 {
		return 0, fmt.Errorf("Docker healthcheck %s cannot be negative", name)
	}
	if duration == 0 {
		return 0, nil
	}
	seconds := int64((duration-1)/time.Second + 1)
	if seconds > int64(1<<31-1) {
		return 0, fmt.Errorf("Docker healthcheck %s exceeds Kubernetes probe limits", name)
	}
	return int32(seconds), nil
}

// ensurePullSecret creates the namespace's pull Secret if it is missing. An
// existing Secret is kept: issuing again would replace the credential that
// running workloads already use. It is only replaced when it no longer covers
// the pull host, since kubelet would otherwise pull without credentials.
func (d *Docker) ensurePullSecret(ctx context.Context, namespace string) error {
	d.pullSecretMu.Lock()
	defer d.pullSecretMu.Unlock()
	scope := d.pullHost + "/" + namespace
	secrets := d.k8s.CoreV1().Secrets(namespace)
	if existing, err := secrets.Get(ctx, pullSecretName, metav1.GetOptions{}); err == nil {
		if dockerConfigCovers(existing.Data[corev1.DockerConfigJsonKey], scope) {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading pull secret: %w", err)
	}
	username, password, err := d.registry.IssuePullCredential(ctx)
	if err != nil {
		return fmt.Errorf("issuing pull credential: %w", err)
	}
	config, err := dockerConfigJSON(scope, username, password)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		Name:      pullSecretName,
		Namespace: namespace,
		Labels:    map[string]string{"app.kubernetes.io/managed-by": "dink"},
		Type:      corev1.SecretTypeDockerConfigJson,
		Data:      map[string][]byte{corev1.DockerConfigJsonKey: config},
	}
	if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating pull secret: %w", err)
		}
		// Ours is the credential dinki now holds, so it must win.
		if _, err := secrets.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating pull secret: %w", err)
		}
	}
	return nil
}

// dockerConfigCovers reports whether a dockerconfigjson payload already holds
// a credential for scope.
func dockerConfigCovers(config []byte, scope string) bool {
	var parsed struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if json.Unmarshal(config, &parsed) != nil {
		return false
	}
	_, ok := parsed.Auths[scope]
	return ok
}

// dockerConfigJSON scopes the credential to the namespace's repositories on
// the pull host, so kubelet only offers it for those images.
func dockerConfigJSON(scope, username, password string) ([]byte, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return json.Marshal(map[string]any{
		"auths": map[string]any{
			scope: map[string]string{"username": username, "password": password, "auth": auth},
		},
	})
}
