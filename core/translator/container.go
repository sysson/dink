package translator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/netip"
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
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func (d *Docker) ContainerExecCreate(context.Context, string, *container.ExecCreateRequest) (string, error) {
	return "", ErrNotImplemented
}

func (d *Docker) ContainerExecInspect(context.Context, string) (*container.ExecInspectResponse, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) ContainerExecResize(context.Context, string, uint32, uint32) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerExecStart(context.Context, string, backend.ExecStartConfig) error {
	return ErrNotImplemented
}

func (d *Docker) ExecExists(context.Context, string) (bool, error) {
	return false, ErrNotImplemented
}

func (d *Docker) ContainerArchivePath(context.Context, string, string) (io.ReadCloser, *container.PathStat, error) {
	return nil, nil, ErrNotImplemented
}

func (d *Docker) ContainerExport(context.Context, string, io.Writer) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerExtractToDir(context.Context, string, string, bool, bool, io.Reader) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerStatPath(context.Context, string, string) (*container.PathStat, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) ContainerCreate(ctx context.Context, cfg backend.ContainerCreateConfig) (container.CreateResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return container.CreateResponse{}, httpx.Unauthorized(fmt.Errorf("missing identity in context"))
	}
	if cfg.Config == nil {
		return container.CreateResponse{}, httpx.BadRequest(fmt.Errorf("container configuration is required"))
	}
	// A missing image is reported as not found, which makes the docker CLI pull and retry.
	image, imageConfig, err := d.podImage(ctx, id.Namespace, cfg.Config.Image)
	if err != nil {
		return container.CreateResponse{}, err
	}
	mergedConfig, err := mergeImageConfig(cfg.Config, imageConfig)
	if err != nil {
		return container.CreateResponse{}, httpx.BadRequest(err)
	}
	cfg.Config = mergedConfig
	ports, serviceType, warnings, err := resolvePublishedPorts(cfg.Config, cfg.HostConfig)
	if err != nil {
		return container.CreateResponse{}, httpx.BadRequest(err)
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
		return container.CreateResponse{}, httpx.BadRequest(fmt.Errorf("encoding container metadata: %w", err))
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
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: pullSecretName}},
						Containers: []corev1.Container{
							{
								Name:       cfg.Name,
								Image:      image,
								Ports:      podPorts(ports),
								Command:    cfg.Config.Entrypoint,
								Args:       cfg.Config.Cmd,
								Env:        containerEnv(cfg.Config.Env),
								WorkingDir: cfg.Config.WorkingDir,
								TTY:        cfg.Config.Tty,
								Stdin:      cfg.Config.OpenStdin,
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
			return nil, false, httpx.BadRequest(fmt.Errorf("host network mode cannot be combined with network endpoints"))
		}
		return nil, true, nil
	}
	if strings.HasPrefix(mode, "container:") {
		return nil, false, httpx.BadRequest(fmt.Errorf("container network mode is not supported by the Kubernetes backend"))
	}

	networkNames := make([]string, 0, len(endpoints)+1)
	switch mode {
	case "none":
		if len(endpoints) != 0 {
			return nil, false, httpx.BadRequest(fmt.Errorf("none network mode cannot be combined with network endpoints"))
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

func (d *Docker) ContainerKill(ctx context.Context, name, signal string) error {
	if signal != "" && signal != "KILL" && signal != "SIGKILL" {
		return httpx.BadRequest(fmt.Errorf("container signal %q is not supported by the Kubernetes backend", signal))
	}
	return d.setContainerReplicas(ctx, name, 0, "")
}

func (d *Docker) ContainerPause(context.Context, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerRename(context.Context, string, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerResize(context.Context, string, uint32, uint32) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerRestart(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	return d.setContainerReplicas(ctx, name, 1, time.Now().UTC().Format(time.RFC3339Nano))
}

func (d *Docker) ContainerRm(ctx context.Context, name string, config *backend.ContainerRmConfig) error {
	if config != nil && (config.RemoveVolume || config.RemoveLink) {
		return httpx.BadRequest(fmt.Errorf("volume and link removal are not supported by the Kubernetes backend"))
	}
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	uid := deployment.UID
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
	if config != nil && config.ForceRemove {
		zero := int64(0)
		options.GracePeriodSeconds = &zero
	}
	if err := d.k8s.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, options); err != nil {
		return kubeError(err)
	}
	services := d.k8s.CoreV1().Services(deployment.Namespace)
	for _, serviceName := range []string{deployment.Name, publishedPortsServiceName(deployment.Name)} {
		if err := services.Delete(ctx, serviceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return kubeError(err)
		}
	}
	return nil
}

func (d *Docker) ContainerStart(ctx context.Context, name, checkpoint, checkpointDir string) error {
	if checkpoint != "" || checkpointDir != "" {
		return httpx.BadRequest(fmt.Errorf("container checkpoints are not supported by the Kubernetes backend"))
	}
	return d.setContainerReplicas(ctx, name, 1, "")
}

func (d *Docker) ContainerStop(ctx context.Context, name string, options backend.ContainerStopOptions) error {
	if err := validateStopOptions(options); err != nil {
		return err
	}
	return d.setContainerReplicas(ctx, name, 0, "")
}

func (d *Docker) ContainerUnpause(context.Context, string) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerUpdate(context.Context, string, *container.HostConfig) (container.UpdateResponse, error) {
	return container.UpdateResponse{}, ErrNotImplemented
}

func (d *Docker) ContainerWait(context.Context, string, container.WaitCondition) (container.WaitResponse, error) {
	return container.WaitResponse{}, ErrNotImplemented
}

func (d *Docker) ContainerAttach(context.Context, string, *backend.ContainerAttachConfig) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerChanges(context.Context, string) ([]container.FilesystemChange, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) ContainerInspect(ctx context.Context, name string, _ backend.ContainerInspectOptions) (*container.InspectResponse, network.HardwareAddr, error) {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	response, err := d.inspectDeployment(ctx, deployment)
	return response, nil, err
}

func (d *Docker) ContainerLogs(context.Context, string, *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, bool, error) {
	return nil, false, ErrNotImplemented
}

func (d *Docker) ContainerStats(context.Context, string, *backend.ContainerStatsConfig) error {
	return ErrNotImplemented
}

func (d *Docker) ContainerTop(context.Context, string, string) (*container.TopResponse, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) Containers(ctx context.Context, options *backend.ContainerListOptions) ([]container.Summary, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, httpx.Unauthorized(fmt.Errorf("missing identity in context"))
	}
	if options == nil {
		options = &backend.ContainerListOptions{}
	}
	if options.Limit < 0 {
		return nil, httpx.BadRequest(fmt.Errorf("container list limit must not be negative"))
	}
	if err := validateContainerListFilters(options.Filters); err != nil {
		return nil, httpx.BadRequest(err)
	}
	deployments, err := d.k8s.AppsV1().Deployments(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	sort.Slice(deployments.Items, func(i, j int) bool {
		return deployments.Items[i].CreationTimestamp.After(deployments.Items[j].CreationTimestamp.Time)
	})
	result := make([]container.Summary, 0, len(deployments.Items))
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		inspect, err := d.inspectDeployment(ctx, deployment)
		if err != nil {
			return nil, err
		}
		summary := containerSummary(deployment, inspect)
		if !matchesContainerListFilters(options.Filters, summary, inspect.Config) {
			continue
		}
		if !options.All && summary.State != container.StateRunning {
			continue
		}
		result = append(result, summary)
		if options.Limit > 0 && len(result) == options.Limit {
			break
		}
	}
	return result, nil
}

func (d *Docker) ContainerPrune(context.Context, filters.Args) (*container.PruneReport, error) {
	return nil, ErrNotImplemented
}

func (d *Docker) CreateImageFromContainer(context.Context, string, *backend.CreateImageConfig) (string, error) {
	return "", ErrNotImplemented
}

func (d *Docker) findDeployment(ctx context.Context, nameOrID string) (*appsv1.Deployment, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, httpx.Unauthorized(fmt.Errorf("missing identity in context"))
	}
	if nameOrID == "" {
		return nil, httpx.BadRequest(fmt.Errorf("container name or ID is required"))
	}
	deployments := d.k8s.AppsV1().Deployments(id.Namespace)
	if deployment, err := deployments.Get(ctx, nameOrID, metav1.GetOptions{}); err == nil {
		return deployment, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}

	list, err := deployments.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	var match *appsv1.Deployment
	for index := range list.Items {
		deployment := &list.Items[index]
		dockerID := identity.DockerIDFromUID(deployment.UID)
		if dockerID == "" || !strings.HasPrefix(dockerID, nameOrID) {
			continue
		}
		if match != nil {
			return nil, httpx.Conflict(fmt.Errorf("container ID %s is ambiguous", nameOrID))
		}
		match = deployment
	}
	if match == nil {
		return nil, httpx.NotFound(fmt.Errorf("container %s not found", nameOrID))
	}
	return match, nil
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
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && podIP == "" {
			podIP = pod.Status.PodIP
		}
		if len(pod.Status.ContainerStatuses) > 0 && pod.Status.ContainerStatuses[0].ImageID != "" {
			imageID = dockerImageID(pod.Status.ContainerStatuses[0].ImageID, config.Image)
		}
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

func containerMetadata(deployment *appsv1.Deployment) (*container.Config, *container.HostConfig, error) {
	config := &container.Config{}
	if value := deployment.Annotations[containerConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), config); err != nil {
			return nil, nil, fmt.Errorf("decoding container config for %s: %w", deployment.Name, err)
		}
	}
	hostConfig := &container.HostConfig{}
	if value := deployment.Annotations[containerHostConfigAnnotation]; value != "" {
		if err := json.Unmarshal([]byte(value), hostConfig); err != nil {
			return nil, nil, fmt.Errorf("decoding host config for %s: %w", deployment.Name, err)
		}
	}
	return config, hostConfig, nil
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

func containerSummary(deployment *appsv1.Deployment, inspect *container.InspectResponse) container.Summary {
	config := inspect.Config
	command := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	state := inspect.State.Status
	status := string(state)
	switch state {
	case container.StateRunning:
		status = "Up " + time.Since(deployment.CreationTimestamp.Time).Round(time.Second).String()
	case container.StateExited:
		status = fmt.Sprintf("Exited (%d)", inspect.State.ExitCode)
	}
	labels := config.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	ports := make([]container.PortSummary, 0)
	for port, bindings := range inspect.NetworkSettings.Ports {
		for _, binding := range bindings {
			publicPort, _ := strconv.ParseUint(binding.HostPort, 10, 16)
			ports = append(ports, container.PortSummary{PrivatePort: port.Num(), PublicPort: uint16(publicPort), Type: string(port.Proto()), IP: binding.HostIP})
		}
	}
	return container.Summary{
		ID:      inspect.ID,
		Names:   []string{inspect.Name},
		Image:   config.Image,
		ImageID: inspect.Image,
		Command: strings.Join(command, " "),
		Created: deployment.CreationTimestamp.Unix(),
		Ports:   ports,
		Labels:  labels,
		State:   state,
		Status:  status,
		HostConfig: struct {
			NetworkMode string            `json:",omitempty"`
			Annotations map[string]string `json:",omitempty"`
		}{NetworkMode: string(inspect.HostConfig.NetworkMode), Annotations: inspect.HostConfig.Annotations},
		NetworkSettings: &container.NetworkSettingsSummary{Networks: inspect.NetworkSettings.Networks},
		Mounts:          []container.MountPoint{},
	}
}

type containerListFilters interface {
	Validate(map[string]bool) error
	FuzzyMatch(string, string) bool
	Match(string, string) bool
	ExactMatch(string, string) bool
	MatchKVList(string, map[string]string) bool
}

func validateContainerListFilters(filters containerListFilters) error {
	allowed := map[string]bool{"ancestor": true, "id": true, "label": true, "name": true, "status": true}
	return filters.Validate(allowed)
}

func matchesContainerListFilters(filters containerListFilters, summary container.Summary, config *container.Config) bool {
	if !filters.FuzzyMatch("id", summary.ID) && !filters.FuzzyMatch("id", identity.Truncate(summary.ID)) {
		return false
	}
	if !filters.Match("name", strings.TrimPrefix(summary.Names[0], "/")) {
		return false
	}
	if !filters.ExactMatch("status", string(summary.State)) {
		return false
	}
	if !filters.MatchKVList("label", config.Labels) {
		return false
	}
	imageMatches := filters.FuzzyMatch("ancestor", summary.Image) || filters.FuzzyMatch("ancestor", summary.ImageID)
	return imageMatches
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

func (d *Docker) setContainerReplicas(ctx context.Context, name string, replicas int32, restartAt string) error {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return err
	}
	deployment.Spec.Replicas = &replicas
	if restartAt != "" {
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations["dink.io/restarted-at"] = restartAt
	}
	_, err = d.k8s.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	return kubeError(err)
}

func validateStopOptions(options backend.ContainerStopOptions) error {
	if options.Signal != "" || options.Timeout != nil {
		return httpx.BadRequest(fmt.Errorf("custom signal and timeout are not supported by the Kubernetes backend"))
	}
	return nil
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

func publishedPortsServiceName(deploymentName string) string {
	const suffix = "-published"
	if len(deploymentName)+len(suffix) <= 63 {
		return deploymentName + suffix
	}
	hash := sha256.Sum256([]byte(deploymentName))
	return fmt.Sprintf("%.44s-%x%s", deploymentName, hash[:4], suffix)
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

// pullSecretName is the dockerconfigjson Secret, one per tenant namespace,
// holding the namespace's dinki pull credential.
const pullSecretName = "dinki-pull"

const (
	containerConfigAnnotation     = "dink.io/docker-config"
	containerHostConfigAnnotation = "dink.io/docker-host-config"
)

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

// ensurePullSecret creates the namespace's pull Secret if it is missing. An
// existing Secret is kept: issuing again would replace the credential that
// running workloads already use.
func (d *Docker) ensurePullSecret(ctx context.Context, namespace string) error {
	d.pullSecretMu.Lock()
	defer d.pullSecretMu.Unlock()
	secrets := d.k8s.CoreV1().Secrets(namespace)
	if _, err := secrets.Get(ctx, pullSecretName, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading pull secret: %w", err)
	}
	username, password, err := d.registry.IssuePullCredential(ctx)
	if err != nil {
		return fmt.Errorf("issuing pull credential: %w", err)
	}
	config, err := dockerConfigJSON(d.pullHost+"/"+namespace, username, password)
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
