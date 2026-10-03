package translator

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"

	networktypes "github.com/moby/moby/api/types/network"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (s *Swarm) GetTasks(ctx context.Context, options swarmbackend.TaskListOptions) ([]swarmtypes.Task, error) {
	if err := validateTaskFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	// The CLI's convergence progress asks for "_up-to-date" tasks only: those
	// running the service's current spec rather than a superseded one.
	upToDate := len(options.Filters.Get("_up-to-date")) > 0
	tasks, err := s.tasks(ctx, upToDate)
	if err != nil {
		return nil, err
	}
	result := make([]swarmtypes.Task, 0, len(tasks))
	for _, task := range tasks {
		if matchesTaskFilters(options.Filters, task) {
			result = append(result, task)
		}
	}
	return result, nil
}

func (s *Swarm) GetTask(ctx context.Context, id string) (swarmtypes.Task, error) {
	if id == "" {
		return swarmtypes.Task{}, InvalidArgument(fmt.Errorf("task ID is required"))
	}
	tasks, err := s.tasks(ctx, false)
	if err != nil {
		return swarmtypes.Task{}, err
	}
	var match *swarmtypes.Task
	for index := range tasks {
		if !strings.HasPrefix(tasks[index].ID, id) {
			continue
		}
		if match != nil {
			return swarmtypes.Task{}, Conflict(fmt.Errorf("task %s is ambiguous", id))
		}
		match = &tasks[index]
	}
	if match == nil {
		return swarmtypes.Task{}, NotFound(fmt.Errorf("task %s not found", id))
	}
	return *match, nil
}

// tasks lists the Pods behind the namespace's Swarm services. With upToDate,
// Pods from a Deployment's superseded ReplicaSets are left out.
func (s *Swarm) tasks(ctx context.Context, upToDate bool) ([]swarmtypes.Task, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	services, err := s.GetServices(ctx, swarmbackend.ServiceListOptions{})
	if err != nil {
		return nil, err
	}
	byName := make(map[string]swarmtypes.Service, len(services))
	for _, service := range services {
		byName[service.Spec.Name] = service
	}
	pods, err := s.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmServiceSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	var currentHashes map[string]string
	if upToDate {
		if currentHashes, err = s.currentTemplateHashes(ctx, namespace); err != nil {
			return nil, err
		}
	}
	nodeIDs := s.nodeIDsByName(ctx)
	networks := s.networksByLabel(ctx)
	slots := make(map[string]int, len(byName))
	items := pods.Items
	sort.Slice(items, func(i, j int) bool { return items[i].CreationTimestamp.Before(&items[j].CreationTimestamp) })
	result := make([]swarmtypes.Task, 0, len(items))
	for index := range items {
		pod := &items[index]
		service, ok := byName[pod.Labels["app"]]
		if !ok {
			continue
		}
		if upToDate && (pod.DeletionTimestamp != nil || pod.Labels[appsv1.DefaultDeploymentUniqueLabelKey] != currentHashes[service.Spec.Name]) {
			continue
		}
		slots[service.Spec.Name]++
		task := swarmTask(pod, service, slots[service.Spec.Name], nodeIDs[pod.Spec.NodeName])
		task.NetworksAttachments = taskNetworkAttachments(pod, networks)
		result = append(result, task)
	}
	return result, nil
}

// deploymentRevisionAnnotation is the revision the Deployment controller stamps
// on each ReplicaSet it rolls out.
const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// currentTemplateHashes maps each service Deployment to the pod-template-hash
// of its newest ReplicaSet, which is the one running the current spec.
func (s *Swarm) currentTemplateHashes(ctx context.Context, namespace string) (map[string]string, error) {
	replicaSets, err := s.k8s.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmServiceSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	hashes := make(map[string]string)
	revisions := make(map[string]int64)
	for index := range replicaSets.Items {
		replicaSet := &replicaSets.Items[index]
		owner := metav1.GetControllerOf(replicaSet)
		if owner == nil || owner.Kind != "Deployment" {
			continue
		}
		revision, _ := strconv.ParseInt(replicaSet.Annotations[deploymentRevisionAnnotation], 10, 64)
		if _, seen := hashes[owner.Name]; seen && revision <= revisions[owner.Name] {
			continue
		}
		hashes[owner.Name] = replicaSet.Labels[appsv1.DefaultDeploymentUniqueLabelKey]
		revisions[owner.Name] = revision
	}
	return hashes, nil
}

func taskNetworkAttachments(pod *corev1.Pod, networks map[string]networktypes.Network) []swarmtypes.NetworkAttachment {
	var addresses []netip.Prefix
	if address, err := netip.ParseAddr(pod.Status.PodIP); err == nil {
		addresses = []netip.Prefix{netip.PrefixFrom(address, address.BitLen())}
	}
	var attachments []swarmtypes.NetworkAttachment
	for label := range pod.Labels {
		network, attached := networks[label]
		if !attached {
			continue
		}
		attachments = append(attachments, swarmtypes.NetworkAttachment{
			Network: swarmtypes.Network{
				ID:          network.ID,
				Spec:        swarmtypes.NetworkSpec{Annotations: swarmtypes.Annotations{Name: network.Name, Labels: network.Labels}},
				DriverState: swarmtypes.Driver{Name: network.Driver},
			},
			Addresses: addresses,
		})
	}
	slices.SortFunc(attachments, func(a, b swarmtypes.NetworkAttachment) int {
		return strings.Compare(a.Network.Spec.Name, b.Network.Spec.Name)
	})
	return attachments
}

// nodeIDsByName best-effort maps node names to Swarm node IDs; task placement
// is still reported without it when node access is not granted.
func (s *Swarm) nodeIDsByName(ctx context.Context) map[string]string {
	nodes, err := s.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	ids := make(map[string]string, len(nodes.Items))
	for index := range nodes.Items {
		ids[nodes.Items[index].Name] = identity.DockerIDFromUID(nodes.Items[index].UID)
	}
	return ids
}

func swarmTask(pod *corev1.Pod, service swarmtypes.Service, slot int, nodeID string) swarmtypes.Task {
	status := podTaskStatus(pod)
	desired := swarmtypes.TaskStateRunning
	if pod.DeletionTimestamp != nil {
		desired = swarmtypes.TaskStateShutdown
	}
	return swarmtypes.Task{
		ID:   identity.DockerIDFromUID(pod.UID),
		Meta: swarmMeta(pod),
		Name: pod.Name, Labels: map[string]string{
			"com.docker.swarm.service.id":   service.ID,
			"com.docker.swarm.service.name": service.Spec.Name,
			"com.docker.swarm.node.id":      nodeID,
		},
		Spec:         service.Spec.TaskTemplate,
		ServiceID:    service.ID,
		Slot:         slot,
		NodeID:       nodeID,
		Status:       status,
		DesiredState: desired,
	}
}

func podTaskStatus(pod *corev1.Pod) swarmtypes.TaskStatus {
	status := swarmtypes.TaskStatus{Timestamp: pod.CreationTimestamp.Time}
	switch pod.Status.Phase {
	case corev1.PodPending:
		status.State = swarmtypes.TaskStatePending
	case corev1.PodRunning:
		status.State = swarmtypes.TaskStateRunning
	case corev1.PodSucceeded:
		status.State = swarmtypes.TaskStateComplete
	case corev1.PodFailed:
		status.State = swarmtypes.TaskStateFailed
	default:
		status.State = swarmtypes.TaskStateNew
	}
	status.Message = pod.Status.Message
	status.Err = pod.Status.Reason
	for index := range pod.Status.ContainerStatuses {
		container := &pod.Status.ContainerStatuses[index]
		if container.Name != pod.Spec.Containers[0].Name {
			continue
		}
		containerStatus := &swarmtypes.ContainerStatus{ContainerID: strings.TrimPrefix(container.ContainerID, "containerd://")}
		if terminated := container.State.Terminated; terminated != nil {
			containerStatus.ExitCode = int(terminated.ExitCode)
			status.Timestamp = terminated.FinishedAt.Time
			if terminated.ExitCode != 0 {
				status.State = swarmtypes.TaskStateFailed
				status.Err = terminated.Reason
			}
		}
		if running := container.State.Running; running != nil {
			status.Timestamp = running.StartedAt.Time
		}
		if waiting := container.State.Waiting; waiting != nil {
			status.State = swarmtypes.TaskStateStarting
			status.Message = waiting.Message
			status.Err = waiting.Reason
		}
		status.ContainerStatus = containerStatus
	}
	return status
}

func validateTaskFilters(filters swarmFilters) error {
	return filters.Validate(map[string]bool{"id": true, "label": true, "name": true, "node": true, "service": true, "desired-state": true, "_up-to-date": true})
}

func matchesTaskFilters(filters swarmFilters, task swarmtypes.Task) bool {
	if filters.Len() == 0 {
		return true
	}
	if !filters.FuzzyMatch("id", task.ID) {
		return false
	}
	if !filters.Match("name", task.Name) {
		return false
	}
	if !filters.ExactMatch("desired-state", string(task.DesiredState)) {
		return false
	}
	if !filters.FuzzyMatch("node", task.NodeID) {
		return false
	}
	if values := filters.Get("service"); len(values) > 0 {
		name := task.Labels["com.docker.swarm.service.name"]
		matched := false
		for _, value := range values {
			if value == name || strings.HasPrefix(task.ServiceID, value) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return filters.MatchKVList("label", task.Labels)
}
