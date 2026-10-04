package translator

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (d *Docker) ContainerInspect(ctx context.Context, name string, _ backend.ContainerInspectOptions) (*container.InspectResponse, network.HardwareAddr, error) {
	deployment, err := d.findContainer(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	response, err := d.inspectContainer(ctx, deployment)
	return response, nil, err
}

func (d *Docker) inspectContainer(ctx context.Context, deployment *containerWorkload) (*container.InspectResponse, error) {
	config, hostConfig, err := containerMetadata(deployment)
	if err != nil {
		return nil, err
	}
	pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
	if err != nil {
		return nil, kubeError(err)
	}
	state, restartCount := containerState(deployment, pods.Items, time.Now())
	imageID := config.Image
	var podIP string
	actualPodResources := false
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && !actualPodResources {
			podIP = pod.Status.PodIP
			for _, podContainer := range pod.Spec.Containers {
				if podContainer.Name == deployment.Name {
					reconcileInspectResources(hostConfig, podContainer.Resources)
					actualPodResources = true
					break
				}
			}
		}
		if status := podContainerStatus(deployment.Name, &pod); status != nil && status.ImageID != "" {
			imageID = dockerImageID(status.ImageID, config.Image)
		}
	}
	if !actualPodResources && len(deployment.Template.Spec.Containers) > 0 {
		podContainer, err := namedContainer(&deployment.Template.Spec, deployment.Name)
		if err != nil {
			return nil, err
		}
		reconcileInspectResources(hostConfig, podContainer.Resources)
	}
	portBindings, err := d.containerPortBindings(ctx, deployment)
	if err != nil {
		return nil, err
	}
	hostConfig.PortBindings = portBindings
	networks, err := d.containerNetworkState(ctx, deployment, podIP)
	if err != nil {
		return nil, err
	}
	if hostConfig.NetworkMode == "" {
		hostConfig.NetworkMode = container.NetworkMode(network.NetworkBridge)
	}
	command := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	response := &container.InspectResponse{
		ID:           identity.DockerIDFromUID(deployment.UID),
		Created:      deployment.CreationTimestamp.UTC().Format(time.RFC3339Nano),
		Path:         firstString(command),
		Args:         remainingStrings(command),
		State:        state,
		RestartCount: restartCount,
		Image:        imageID,
		Name:         "/" + deployment.dockerName(),
		HostConfig:   hostConfig,
		Config:       config,
		NetworkSettings: &container.NetworkSettings{
			Ports:    portBindings,
			Networks: networks,
		},
	}
	for _, podVolume := range deployment.Template.Spec.Volumes {
		if podVolume.PersistentVolumeClaim == nil {
			continue
		}
		claimName := podVolume.PersistentVolumeClaim.ClaimName
		pvc, err := d.k8s.CoreV1().PersistentVolumeClaims(deployment.Namespace).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			return nil, kubeError(err)
		}
		name := pvc.Annotations[volumeNameAnnotation]
		if name == "" {
			name = claimName
		}
		for _, podContainer := range deployment.Template.Spec.Containers {
			for _, volumeMount := range podContainer.VolumeMounts {
				if volumeMount.Name != podVolume.Name {
					continue
				}
				mode := "rw"
				if volumeMount.ReadOnly {
					mode = "ro"
				}
				response.Mounts = append(response.Mounts, container.MountPoint{
					Type:        "volume",
					Name:        name,
					Source:      volumeMountpointBase + "/" + claimName,
					Destination: volumeMount.MountPath,
					Driver:      "local",
					Mode:        mode,
					RW:          !volumeMount.ReadOnly,
				})
			}
		}
	}
	return response, nil
}

func reconcileInspectResources(hostConfig *container.HostConfig, actual corev1.ResourceRequirements) {
	declared, err := containerResources(hostConfig)
	if err == nil {
		declaredCPU, declaredCPUSet := declared.Limits[corev1.ResourceCPU]
		actualCPU, actualCPUSet := actual.Limits[corev1.ResourceCPU]
		if declaredCPUSet != actualCPUSet || (actualCPUSet && declaredCPU.Cmp(actualCPU) != 0) {
			hostConfig.NanoCPUs, hostConfig.CPUQuota, hostConfig.CPUPeriod = 0, 0, 0
			if actualCPUSet {
				hostConfig.NanoCPUs = actualCPU.MilliValue() * 1_000_000
			}
		}
	}
	hostConfig.Memory = 0
	if memory, ok := actual.Limits[corev1.ResourceMemory]; ok {
		hostConfig.Memory = memory.Value()
	}
	hostConfig.MemoryReservation = 0
	if memory, ok := actual.Requests[corev1.ResourceMemory]; ok {
		hostConfig.MemoryReservation = memory.Value()
	}
	if cpu, ok := actual.Requests[corev1.ResourceCPU]; ok {
		if hostConfig.Annotations == nil {
			hostConfig.Annotations = map[string]string{}
		}
		hostConfig.Annotations["dink.io/requests.cpu"] = cpu.String()
	} else {
		delete(hostConfig.Annotations, "dink.io/requests.cpu")
	}
}

func containerState(deployment *containerWorkload, pods []corev1.Pod, now time.Time) (*container.State, int) {
	state := &container.State{Status: container.StateCreated}
	if !deployment.Started {
		// A stopped Deployment has run before; a suspended Job never has.
		if !deployment.OneShot {
			state.Status = container.StateExited
		}
		return state, 0
	}
	for index := range pods {
		pod := &pods[index]
		status := podContainerStatus(deployment.Name, pod)
		restartCount := 0
		if status != nil {
			restartCount = int(status.RestartCount)
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			state.Status = container.StateRunning
			state.Running = true
			switch {
			case status != nil && status.State.Running != nil:
				state.StartedAt = status.State.Running.StartedAt.UTC().Format(time.RFC3339Nano)
			case status != nil && lastTermination(status) != nil:
				// Kubernetes keeps the pod Running while the container is in a crash/restart loop.
				state.Status = container.StateRestarting
				state.Restarting = true
				setTerminatedState(state, lastTermination(status))
			case pod.Status.StartTime != nil:
				state.StartedAt = pod.Status.StartTime.UTC().Format(time.RFC3339Nano)
			}
			if health := containerHealthStatus(deployment, status, now); health != "" {
				state.Health = &container.Health{Status: health}
			}
			return state, restartCount
		case corev1.PodFailed, corev1.PodSucceeded:
			state.Status = container.StateExited
			if status != nil {
				if terminated := lastTermination(status); terminated != nil {
					setTerminatedState(state, terminated)
				}
			}
			return state, restartCount
		}
	}
	return state, 0
}

func setTerminatedState(state *container.State, terminated *corev1.ContainerStateTerminated) {
	state.ExitCode = int(terminated.ExitCode)
	state.Error = terminated.Message
	state.OOMKilled = terminated.Reason == "OOMKilled"
	state.FinishedAt = terminated.FinishedAt.UTC().Format(time.RFC3339Nano)
}

// podContainerStatus returns the status of the Docker container within the pod.
func podContainerStatus(name string, pod *corev1.Pod) *corev1.ContainerStatus {
	statuses := pod.Status.ContainerStatuses
	for index := range statuses {
		if statuses[index].Name == name {
			return &statuses[index]
		}
	}
	return nil
}

func lastTermination(status *corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if status.State.Terminated != nil {
		return status.State.Terminated
	}
	return status.LastTerminationState.Terminated
}

// containerHealthStatus maps the readiness probe (translated from the Docker healthcheck) to a Docker health status.
func containerHealthStatus(deployment *containerWorkload, status *corev1.ContainerStatus, now time.Time) container.HealthStatus {
	var probe *corev1.Probe
	for _, podContainer := range deployment.Template.Spec.Containers {
		if podContainer.Name == deployment.Name {
			probe = podContainer.ReadinessProbe
			break
		}
	}
	if probe == nil {
		return ""
	}
	if status == nil {
		return container.Starting
	}
	if status.State.Running == nil {
		return container.Unhealthy
	}
	if status.Ready {
		return container.Healthy
	}
	// Like Docker, failures only count as unhealthy once the start period and retries have elapsed.
	period, failures := probe.PeriodSeconds, probe.FailureThreshold
	if period <= 0 {
		period = 10
	}
	if failures <= 0 {
		failures = 3
	}
	grace := time.Duration(probe.InitialDelaySeconds+period*failures) * time.Second
	if now.Before(status.State.Running.StartedAt.Add(grace)) {
		return container.Starting
	}
	return container.Unhealthy
}

func (d *Docker) containerPortBindings(ctx context.Context, deployment *containerWorkload) (network.PortMap, error) {
	bindings := make(network.PortMap)
	service, err := d.k8s.CoreV1().Services(deployment.Namespace).Get(ctx, publishedPortsServiceName(deployment.Name), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return bindings, nil
	}
	if err != nil {
		return nil, kubeError(err)
	}
	for _, servicePort := range service.Spec.Ports {
		targetPort := servicePort.TargetPort.IntVal
		if targetPort == 0 {
			targetPort = servicePort.Port
		}
		port, ok := network.PortFrom(uint16(targetPort), network.IPProtocol(strings.ToLower(string(servicePort.Protocol))))
		if !ok {
			continue
		}
		publishedPort := servicePort.Port
		if service.Spec.Type == corev1.ServiceTypeNodePort && servicePort.NodePort != 0 {
			publishedPort = servicePort.NodePort
		}
		bindings[port] = []network.PortBinding{{HostPort: strconv.Itoa(int(publishedPort))}}
	}
	return bindings, nil
}

func (d *Docker) containerNetworkState(ctx context.Context, deployment *containerWorkload, podIP string) (map[string]*network.EndpointSettings, error) {
	networks := make(map[string]*network.EndpointSettings)
	if deployment.Template.Spec.HostNetwork {
		return networks, nil
	}
	aliases, err := decodeContainerAliases(deployment.Annotations)
	if err != nil {
		return nil, err
	}
	var networkObjects *unstructured.UnstructuredList
	if d.k8s.Dynamic != nil {
		list, err := d.networkList(ctx)
		if err != nil {
			return nil, err
		}
		networkObjects = list
	}
	for key := range deployment.Template.Labels {
		if !strings.HasPrefix(key, networkLabelPrefix) {
			continue
		}
		objectName := strings.TrimPrefix(key, networkLabelPrefix)
		name := objectName
		id := objectName
		for builtin := range builtinNetworkDrivers {
			if networkObjectName(builtin) == objectName {
				name = builtin
				id = objectName
				break
			}
		}
		if networkObjects != nil {
			for index := range networkObjects.Items {
				obj := &networkObjects.Items[index]
				if obj.GetName() == objectName {
					network := networkFromObject(obj)
					name, id = network.Name, network.ID
					break
				}
			}
		}
		endpoint := &network.EndpointSettings{NetworkID: id}
		if names := aliases[name]; len(names) > 0 {
			endpoint.Aliases = names
			endpoint.DNSNames = names
		}
		if ip, err := netip.ParseAddr(podIP); err == nil {
			endpoint.IPAddress = ip
		}
		networks[name] = endpoint
	}
	return networks, nil
}

func commandPath(command []string) string {
	if len(command) == 0 {
		return ""
	}
	return command[0]
}

func commandArgs(command []string) []string {
	if len(command) < 2 {
		return []string{}
	}
	return command[1:]
}

func firstString(values []string) string {
	return commandPath(values)
}

func remainingStrings(values []string) []string {
	return commandArgs(values)
}

func dockerImageID(imageID, fallback string) string {
	if digest := strings.LastIndex(imageID, "@"); digest >= 0 {
		return imageID[digest+1:]
	}
	imageID = strings.TrimPrefix(imageID, "docker-pullable://")
	imageID = strings.TrimPrefix(imageID, "containerd://")
	if imageID == "" {
		return fallback
	}
	return imageID
}
