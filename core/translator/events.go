package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

const eventVolumeSelector = volumeManagedLabel + "=" + volumeManagedValue

func (d *Docker) SubscribeToEvents(ctx context.Context, since, until time.Time, eventFilters filters.Args) ([]events.Message, chan any, error) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.Namespace == "" {
		return nil, nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if err := eventFilters.Validate(map[string]bool{
		"container": true, "event": true, "image": true, "label": true,
		"scope": true, "type": true, "volume": true,
	}); err != nil {
		return nil, nil, InvalidArgument(err)
	}

	namespace := id.Namespace
	deployments, err := d.k8s.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	jobs, err := d.k8s.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	workloads := make([]*containerWorkload, 0, len(deployments.Items)+len(jobs.Items))
	for index := range deployments.Items {
		workloads = append(workloads, deploymentWorkload(&deployments.Items[index]))
	}
	for index := range jobs.Items {
		if isContainerJob(&jobs.Items[index]) {
			workloads = append(workloads, jobWorkload(&jobs.Items[index]))
		}
	}
	pods, err := d.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	pvcs, err := d.k8s.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: eventVolumeSelector})
	if err != nil {
		return nil, nil, kubeError(err)
	}

	historical, err := d.historicalDockerEvents(ctx, namespace, since, until, eventFilters, workloads, pods.Items, pvcs.Items)
	if err != nil {
		return nil, nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	var watches []watch.Interface
	stopWatches := func() {
		for _, watcher := range watches {
			watcher.Stop()
		}
		cancel()
	}
	for _, start := range []func() (watch.Interface, error){
		func() (watch.Interface, error) {
			return d.k8s.AppsV1().Deployments(namespace).Watch(watchCtx, metav1.ListOptions{ResourceVersion: deployments.ResourceVersion})
		},
		func() (watch.Interface, error) {
			return d.k8s.BatchV1().Jobs(namespace).Watch(watchCtx, metav1.ListOptions{ResourceVersion: jobs.ResourceVersion})
		},
		func() (watch.Interface, error) {
			return d.k8s.CoreV1().Pods(namespace).Watch(watchCtx, metav1.ListOptions{ResourceVersion: pods.ResourceVersion})
		},
		func() (watch.Interface, error) {
			return d.k8s.CoreV1().PersistentVolumeClaims(namespace).Watch(watchCtx, metav1.ListOptions{
				LabelSelector:   eventVolumeSelector,
				ResourceVersion: pvcs.ResourceVersion,
			})
		},
	} {
		watcher, err := start()
		if err != nil {
			stopWatches()
			return nil, nil, kubeError(err)
		}
		watches = append(watches, watcher)
	}

	stream := make(chan any, 64)
	d.eventsMu.Lock()
	if d.eventCancels == nil {
		d.eventCancels = make(map[chan any]context.CancelFunc)
	}
	d.eventCancels[stream] = cancel
	d.eventsMu.Unlock()
	go d.forwardKubernetesEvents(watchCtx, stream, eventFilters, since, until, workloads, pods.Items, watches[0], watches[1], watches[2], watches[3])
	return historical, stream, nil
}

func (d *Docker) UnsubscribeFromEvents(_ context.Context, stream chan any) error {
	d.eventsMu.Lock()
	cancel := d.eventCancels[stream]
	delete(d.eventCancels, stream)
	d.eventsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (d *Docker) forwardKubernetesEvents(ctx context.Context, output chan any, eventFilters filters.Args, since, until time.Time, initialWorkloads []*containerWorkload, initialPods []corev1.Pod, deploymentWatch, jobWatch, podWatch, pvcWatch watch.Interface) {
	defer close(output)
	defer deploymentWatch.Stop()
	defer jobWatch.Stop()
	defer podWatch.Stop()
	defer pvcWatch.Stop()
	defer func() {
		d.eventsMu.Lock()
		delete(d.eventCancels, output)
		d.eventsMu.Unlock()
	}()

	deployments := make(map[string]*containerWorkload, len(initialWorkloads))
	for _, workload := range initialWorkloads {
		deployments[workload.Name] = workload
	}
	podStates := make(map[string]podEventState, len(initialPods))
	for index := range initialPods {
		pod := &initialPods[index]
		podStates[pod.Namespace+"/"+pod.Name] = observePodEventState(deployments[pod.Labels["app"]], pod, time.Now())
	}
	// Readiness stays false while a healthcheck is starting, so the starting -> unhealthy transition needs polling.
	healthTicker := time.NewTicker(time.Second)
	defer healthTicker.Stop()
	deploymentEvents := deploymentWatch.ResultChan()
	jobEvents := jobWatch.ResultChan()
	podEvents := podWatch.ResultChan()
	pvcEvents := pvcWatch.ResultChan()
	handleWorkload := func(eventType watch.EventType, deployment *containerWorkload) {
		previous := deployments[deployment.Name]
		var action string
		switch eventType {
		case watch.Added:
			action = "create"
			deployments[deployment.Name] = deployment
		case watch.Modified:
			if previous != nil && !apiequality.Semantic.DeepEqual(previous.Template, deployment.Template) {
				action = "update"
			}
			deployments[deployment.Name] = deployment
		case watch.Deleted:
			action = "destroy"
			delete(deployments, deployment.Name)
		default:
			return
		}
		if action != "" {
			d.sendDockerEvent(ctx, output, eventFilters, since, until, containerDockerEvent(deployment, action, time.Now()))
		}
	}
	for deploymentEvents != nil || jobEvents != nil || podEvents != nil || pvcEvents != nil {
		select {
		case <-ctx.Done():
			return
		case now := <-healthTicker.C:
			for key, previous := range podStates {
				if previous.health != container.Starting {
					continue
				}
				deployment := deployments[previous.pod.Labels["app"]]
				current := observePodEventState(deployment, previous.pod, now)
				podStates[key] = current
				if deployment != nil {
					for _, message := range podTransitionEvents(deployment, previous, current, now) {
						d.sendDockerEvent(ctx, output, eventFilters, since, until, message)
					}
				}
			}
		case event, open := <-deploymentEvents:
			if !open {
				deploymentEvents = nil
				continue
			}
			deployment, ok := event.Object.(*appsv1.Deployment)
			if !ok {
				continue
			}
			handleWorkload(event.Type, deploymentWorkload(deployment))
		case event, open := <-jobEvents:
			if !open {
				jobEvents = nil
				continue
			}
			job, ok := event.Object.(*batchv1.Job)
			if !ok || !isContainerJob(job) {
				continue
			}
			handleWorkload(event.Type, jobWorkload(job))
		case event, open := <-podEvents:
			if !open {
				podEvents = nil
				continue
			}
			pod, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			key := pod.Namespace + "/" + pod.Name
			previous := podStates[key]
			deployment := deployments[pod.Labels["app"]]
			now := time.Now()
			if event.Type == watch.Deleted {
				delete(podStates, key)
				if deployment != nil && previous.startedAt != "" {
					d.sendDockerEvent(ctx, output, eventFilters, since, until, containerDockerEvent(deployment, "stop", now))
				}
				continue
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				continue
			}
			current := observePodEventState(deployment, pod, now)
			podStates[key] = current
			if deployment == nil {
				continue
			}
			for _, message := range podTransitionEvents(deployment, previous, current, now) {
				d.sendDockerEvent(ctx, output, eventFilters, since, until, message)
			}
		case event, open := <-pvcEvents:
			if !open {
				pvcEvents = nil
				continue
			}
			pvc, ok := event.Object.(*corev1.PersistentVolumeClaim)
			if !ok {
				continue
			}
			action := ""
			switch event.Type {
			case watch.Added:
				action = "create"
			case watch.Deleted:
				action = "destroy"
			}
			if action != "" {
				message := volumeDockerEvent(pvc, action, time.Now())
				d.sendDockerEvent(ctx, output, eventFilters, since, until, message)
			}
		}
	}
}

// podEventState is the container state of a pod, as far as Docker events are concerned.
type podEventState struct {
	pod *corev1.Pod
	// startedAt identifies the running container instance; empty when not running.
	startedAt string
	// termination identifies the latest container exit.
	termination string
	exitCode    int32
	health      container.HealthStatus
}

func observePodEventState(deployment *containerWorkload, pod *corev1.Pod, now time.Time) podEventState {
	state := podEventState{pod: pod}
	status := podContainerStatus(pod.Labels["app"], pod)
	if status == nil {
		switch pod.Status.Phase {
		case corev1.PodRunning:
			state.startedAt = string(pod.Status.Phase)
		case corev1.PodFailed, corev1.PodSucceeded:
			state.termination = string(pod.Status.Phase)
		}
		return state
	}
	if status.State.Running != nil {
		state.startedAt = status.ContainerID + "@" + status.State.Running.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if terminated := lastTermination(status); terminated != nil {
		state.termination = terminated.ContainerID + "@" + terminated.FinishedAt.UTC().Format(time.RFC3339Nano)
		state.exitCode = terminated.ExitCode
	}
	if deployment != nil && state.startedAt != "" {
		state.health = containerHealthStatus(deployment, status, now)
	}
	return state
}

// podTransitionEvents mirrors Docker's restart loop: each exit is a die, each restart a start.
func podTransitionEvents(deployment *containerWorkload, previous, current podEventState, now time.Time) []events.Message {
	var messages []events.Message
	if current.termination != "" && current.termination != previous.termination {
		message := containerDockerEvent(deployment, string(events.ActionDie), now)
		message.Actor.Attributes["exitCode"] = strconv.Itoa(int(current.exitCode))
		messages = append(messages, message)
	}
	if current.startedAt != "" && current.startedAt != previous.startedAt {
		messages = append(messages, containerDockerEvent(deployment, string(events.ActionStart), now))
	}
	if current.health != previous.health && (current.health == container.Healthy || current.health == container.Unhealthy) {
		messages = append(messages, containerDockerEvent(deployment, string(events.ActionHealthStatus)+": "+string(current.health), now))
	}
	return messages
}

func (d *Docker) sendDockerEvent(ctx context.Context, output chan any, eventFilters filters.Args, since, until time.Time, message events.Message) {
	if !eventTimeMatches(message, since, until) || !matchesDockerEvent(eventFilters, message) {
		return
	}
	select {
	case output <- message:
	case <-ctx.Done():
	}
}

func (d *Docker) historicalDockerEvents(ctx context.Context, namespace string, since, until time.Time, eventFilters filters.Args, workloads []*containerWorkload, pods []corev1.Pod, pvcs []corev1.PersistentVolumeClaim) ([]events.Message, error) {
	if since.IsZero() {
		return nil, nil
	}
	kubernetesEvents, err := d.k8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	deploymentByName := make(map[string]*containerWorkload, len(workloads))
	for _, workload := range workloads {
		deploymentByName[workload.Name] = workload
	}
	podByName := make(map[string]*corev1.Pod, len(pods))
	for index := range pods {
		podByName[pods[index].Name] = &pods[index]
	}
	pvcByName := make(map[string]*corev1.PersistentVolumeClaim, len(pvcs))
	for index := range pvcs {
		pvcByName[pvcs[index].Name] = &pvcs[index]
	}
	result := make([]events.Message, 0)
	for index := range kubernetesEvents.Items {
		kubeEvent := &kubernetesEvents.Items[index]
		timestamp := kubernetesEventTime(kubeEvent)
		if timestamp.IsZero() || timestamp.Before(since) || !until.IsZero() && timestamp.After(until) {
			continue
		}
		var message events.Message
		switch kubeEvent.InvolvedObject.Kind {
		case "Pod":
			pod := podByName[kubeEvent.InvolvedObject.Name]
			if pod == nil {
				continue
			}
			deployment := deploymentByName[pod.Labels["app"]]
			if deployment == nil {
				continue
			}
			action := mapPodEventAction(kubeEvent.Reason)
			if action == "" {
				continue
			}
			message = containerDockerEvent(deployment, action, timestamp)
		case "PersistentVolumeClaim":
			pvc := pvcByName[kubeEvent.InvolvedObject.Name]
			if pvc == nil || kubeEvent.Reason != "ProvisioningSucceeded" {
				continue
			}
			message = volumeDockerEvent(pvc, "create", timestamp)
		default:
			continue
		}
		if matchesDockerEvent(eventFilters, message) {
			result = append(result, message)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].TimeNano < result[j].TimeNano })
	return result, nil
}

func containerDockerEvent(deployment *containerWorkload, action string, timestamp time.Time) events.Message {
	attributes := map[string]string{"name": deployment.Name}
	config, _, err := containerMetadata(deployment)
	if err == nil && config != nil {
		maps.Copy(attributes, config.Labels)
		if config.Image != "" {
			attributes["image"] = config.Image
		}
	}
	if attributes["image"] == "" && len(deployment.Template.Spec.Containers) > 0 {
		attributes["image"] = deployment.Template.Spec.Containers[0].Image
	}
	id := identity.DockerIDFromUID(deployment.UID)
	if id == "" {
		id = deployment.Name
	}
	return dockerEvent(events.ContainerEventType, id, action, attributes, timestamp)
}

func volumeDockerEvent(pvc *corev1.PersistentVolumeClaim, action string, timestamp time.Time) events.Message {
	name := pvc.Annotations[volumeNameAnnotation]
	if name == "" {
		name = pvc.Name
	}
	attributes := map[string]string{"name": name, "driver": "local"}
	var labels map[string]string
	if err := json.Unmarshal([]byte(pvc.Annotations[volumeLabelsAnnotation]), &labels); err == nil {
		maps.Copy(attributes, labels)
	}
	return dockerEvent(events.VolumeEventType, name, action, attributes, timestamp)
}

func dockerEvent(eventType events.Type, id, action string, attributes map[string]string, timestamp time.Time) events.Message {
	return events.Message{
		Type:     eventType,
		Action:   events.Action(action),
		Scope:    "local",
		Time:     timestamp.Unix(),
		TimeNano: timestamp.UnixNano(),
		Actor:    events.Actor{ID: id, Attributes: attributes},
	}
}

func eventTimeMatches(message events.Message, since, until time.Time) bool {
	timestamp := time.Unix(0, message.TimeNano)
	if message.TimeNano == 0 {
		timestamp = time.Unix(message.Time, 0)
	}
	return (since.IsZero() || !timestamp.Before(since)) && (until.IsZero() || !timestamp.After(until))
}

func matchesDockerEvent(eventFilters filters.Args, message events.Message) bool {
	for _, fieldValue := range []struct{ key, value string }{
		{"type", string(message.Type)}, {"event", string(message.Action)}, {"scope", message.Scope},
	} {
		if len(eventFilters.Get(fieldValue.key)) > 0 && !eventFilters.Match(fieldValue.key, fieldValue.value) {
			return false
		}
	}
	if len(eventFilters.Get("label")) > 0 && !eventFilters.MatchKVList("label", message.Actor.Attributes) {
		return false
	}
	for _, field := range []string{"container", "image", "volume"} {
		values := eventFilters.Get(field)
		if len(values) == 0 {
			continue
		}
		candidates := []string{message.Actor.ID}
		if field == "container" || field == "volume" {
			candidates = append(candidates, message.Actor.Attributes["name"])
		}
		if field == "image" {
			candidates = []string{message.Actor.Attributes["image"]}
		}
		matched := false
		for _, candidate := range candidates {
			if candidate != "" && eventFilters.Match(field, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func kubernetesEventTime(event *corev1.Event) time.Time {
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time
	}
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

func mapPodEventAction(reason string) string {
	switch reason {
	case "Created":
		return "create"
	case "Started":
		return "start"
	case "Killing":
		return "kill"
	default:
		return ""
	}
}
