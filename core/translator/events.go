package translator

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
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
	pods, err := d.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	pvcs, err := d.k8s.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: eventVolumeSelector})
	if err != nil {
		return nil, nil, kubeError(err)
	}

	historical, err := d.historicalDockerEvents(ctx, namespace, since, until, eventFilters, deployments.Items, pods.Items, pvcs.Items)
	if err != nil {
		return nil, nil, err
	}

	watchCtx, cancel := context.WithCancel(ctx)
	deploymentWatch, err := d.k8s.AppsV1().Deployments(namespace).Watch(watchCtx, metav1.ListOptions{ResourceVersion: deployments.ResourceVersion})
	if err != nil {
		cancel()
		return nil, nil, kubeError(err)
	}
	podWatch, err := d.k8s.CoreV1().Pods(namespace).Watch(watchCtx, metav1.ListOptions{ResourceVersion: pods.ResourceVersion})
	if err != nil {
		deploymentWatch.Stop()
		cancel()
		return nil, nil, kubeError(err)
	}
	pvcWatch, err := d.k8s.CoreV1().PersistentVolumeClaims(namespace).Watch(watchCtx, metav1.ListOptions{
		LabelSelector:   eventVolumeSelector,
		ResourceVersion: pvcs.ResourceVersion,
	})
	if err != nil {
		deploymentWatch.Stop()
		podWatch.Stop()
		cancel()
		return nil, nil, kubeError(err)
	}

	stream := make(chan any, 64)
	d.eventsMu.Lock()
	if d.eventCancels == nil {
		d.eventCancels = make(map[chan any]context.CancelFunc)
	}
	d.eventCancels[stream] = cancel
	d.eventsMu.Unlock()
	go d.forwardKubernetesEvents(watchCtx, stream, eventFilters, since, until, deployments.Items, pods.Items, deploymentWatch, podWatch, pvcWatch)
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

func (d *Docker) forwardKubernetesEvents(ctx context.Context, output chan any, eventFilters filters.Args, since, until time.Time, initialDeployments []appsv1.Deployment, initialPods []corev1.Pod, deploymentWatch, podWatch, pvcWatch watch.Interface) {
	defer close(output)
	defer deploymentWatch.Stop()
	defer podWatch.Stop()
	defer pvcWatch.Stop()
	defer func() {
		d.eventsMu.Lock()
		delete(d.eventCancels, output)
		d.eventsMu.Unlock()
	}()

	deployments := make(map[string]*appsv1.Deployment, len(initialDeployments))
	for index := range initialDeployments {
		deployment := initialDeployments[index].DeepCopy()
		deployments[deployment.Name] = deployment
	}
	podPhases := make(map[string]corev1.PodPhase, len(initialPods))
	for index := range initialPods {
		pod := &initialPods[index]
		podPhases[pod.Namespace+"/"+pod.Name] = pod.Status.Phase
	}
	deploymentEvents := deploymentWatch.ResultChan()
	podEvents := podWatch.ResultChan()
	pvcEvents := pvcWatch.ResultChan()
	for deploymentEvents != nil || podEvents != nil || pvcEvents != nil {
		select {
		case <-ctx.Done():
			return
		case event, open := <-deploymentEvents:
			if !open {
				deploymentEvents = nil
				continue
			}
			deployment, ok := event.Object.(*appsv1.Deployment)
			if !ok {
				continue
			}
			previous := deployments[deployment.Name]
			var action string
			switch event.Type {
			case watch.Added:
				action = "create"
				deployments[deployment.Name] = deployment.DeepCopy()
			case watch.Modified:
				if previous != nil && !apiequality.Semantic.DeepEqual(previous.Spec.Template, deployment.Spec.Template) {
					action = "update"
				}
				deployments[deployment.Name] = deployment.DeepCopy()
			case watch.Deleted:
				action = "destroy"
				delete(deployments, deployment.Name)
			default:
				continue
			}
			if action != "" {
				message := containerDockerEvent(deployment, action, time.Now())
				d.sendDockerEvent(ctx, output, eventFilters, since, until, message)
			}
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
			previous := podPhases[key]
			podPhases[key] = pod.Status.Phase
			deployment := deployments[pod.Labels["app"]]
			if deployment == nil {
				continue
			}
			action := ""
			if event.Type == watch.Deleted && previous == corev1.PodRunning {
				action = "stop"
				delete(podPhases, key)
			} else if event.Type == watch.Modified {
				switch {
				case pod.Status.Phase == corev1.PodRunning && previous != corev1.PodRunning:
					action = "start"
				case (pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded) && previous != pod.Status.Phase:
					action = "die"
				}
			}
			if action != "" {
				message := containerDockerEvent(deployment, action, time.Now())
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

func (d *Docker) sendDockerEvent(ctx context.Context, output chan any, eventFilters filters.Args, since, until time.Time, message events.Message) {
	if !eventTimeMatches(message, since, until) || !matchesDockerEvent(eventFilters, message) {
		return
	}
	select {
	case output <- message:
	case <-ctx.Done():
	}
}

func (d *Docker) historicalDockerEvents(ctx context.Context, namespace string, since, until time.Time, eventFilters filters.Args, deployments []appsv1.Deployment, pods []corev1.Pod, pvcs []corev1.PersistentVolumeClaim) ([]events.Message, error) {
	if since.IsZero() {
		return nil, nil
	}
	kubernetesEvents, err := d.k8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	deploymentByName := make(map[string]*appsv1.Deployment, len(deployments))
	for index := range deployments {
		deploymentByName[deployments[index].Name] = &deployments[index]
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

func containerDockerEvent(deployment *appsv1.Deployment, action string, timestamp time.Time) events.Message {
	attributes := map[string]string{"name": deployment.Name}
	config, _, err := containerMetadata(deployment)
	if err == nil && config != nil {
		maps.Copy(attributes, config.Labels)
		if config.Image != "" {
			attributes["image"] = config.Image
		}
	}
	if attributes["image"] == "" && len(deployment.Spec.Template.Spec.Containers) > 0 {
		attributes["image"] = deployment.Spec.Template.Spec.Containers[0].Image
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
