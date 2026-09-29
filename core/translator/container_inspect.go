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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (d *Docker) ContainerInspect(ctx context.Context, name string, _ backend.ContainerInspectOptions) (*container.InspectResponse, network.HardwareAddr, error) {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	response, err := d.inspectDeployment(ctx, deployment)
	return response, nil, err
}

func (d *Docker) inspectDeployment(ctx context.Context, deployment *appsv1.Deployment) (*container.InspectResponse, error) {
	config, hostConfig, err := containerMetadata(deployment)
	if err != nil {
		return nil, err
	}
	pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
	if err != nil {
		return nil, kubeError(err)
	}
	state := deploymentState(deployment, pods.Items)
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
		if len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].ImageID != "" {
			imageID = dockerImageID(pod.Status.ContainerStatuses[0].ImageID, config.Image)
		}
	}
	if !actualPodResources && len(deployment.Spec.Template.Spec.Containers) > 0 {
		reconcileInspectResources(hostConfig, deployment.Spec.Template.Spec.Containers[0].Resources)
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
		ID:         identity.DockerIDFromUID(deployment.UID),
		Created:    deployment.CreationTimestamp.UTC().Format(time.RFC3339Nano),
		Path:       firstString(command),
		Args:       remainingStrings(command),
		State:      state,
		Image:      imageID,
		Name:       "/" + deployment.Name,
		HostConfig: hostConfig,
		Config:     config,
		NetworkSettings: &container.NetworkSettings{
			Ports:    portBindings,
			Networks: networks,
		},
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

func deploymentState(deployment *appsv1.Deployment, pods []corev1.Pod) *container.State {
	state := &container.State{Status: container.StateCreated}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == 0 {
		state.Status = container.StateExited
		return state
	}
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodRunning {
			state.Status = container.StateRunning
			state.Running = true
			if pod.Status.StartTime != nil {
				state.StartedAt = pod.Status.StartTime.UTC().Format(time.RFC3339Nano)
			}
			return state
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			state.Status = container.StateExited
			if pod.Status.ContainerStatuses != nil {
				for _, status := range pod.Status.ContainerStatuses {
					if status.State.Terminated != nil {
						state.ExitCode = int(status.State.Terminated.ExitCode)
						state.Error = status.State.Terminated.Message
						state.FinishedAt = status.State.Terminated.FinishedAt.UTC().Format(time.RFC3339Nano)
						break
					}
				}
			}
			return state
		}
	}
	return state
}

func (d *Docker) containerPortBindings(ctx context.Context, deployment *appsv1.Deployment) (network.PortMap, error) {
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

func (d *Docker) containerNetworkState(ctx context.Context, deployment *appsv1.Deployment, podIP string) (map[string]*network.EndpointSettings, error) {
	networks := make(map[string]*network.EndpointSettings)
	if deployment.Spec.Template.Spec.HostNetwork {
		return networks, nil
	}
	var networkObjects *unstructured.UnstructuredList
	if d.k8s.Dynamic != nil {
		list, err := d.networkList(ctx)
		if err != nil {
			return nil, err
		}
		networkObjects = list
	}
	for key := range deployment.Spec.Template.Labels {
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
