package translator

import (
	"context"
	"fmt"
	"sync"

	"github.com/moby/moby/v2/daemon/server/backend"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServiceLogs merges the Pod logs of the selected services and tasks into one
// stream, which is the closest equivalent to a Swarm service log.
func (s *Swarm) ServiceLogs(ctx context.Context, selector *backend.LogSelector, options *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, error) {
	if selector == nil || (len(selector.Services) == 0 && len(selector.Tasks) == 0) {
		return nil, InvalidArgument(fmt.Errorf("a service or task must be selected"))
	}
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	pods, err := s.selectedPods(ctx, namespace, selector)
	if err != nil {
		return nil, err
	}
	if len(pods) == 0 {
		return nil, NotFound(fmt.Errorf("no running task found for the selected service"))
	}
	streams := make([]<-chan *backend.LogMessage, 0, len(pods))
	for index := range pods {
		stream, err := s.docker.podLogs(ctx, namespace, &pods[index], options)
		if err != nil {
			return nil, err
		}
		streams = append(streams, stream)
	}
	return mergeLogStreams(ctx, streams), nil
}

func (s *Swarm) selectedPods(ctx context.Context, namespace string, selector *backend.LogSelector) ([]corev1.Pod, error) {
	pods, err := s.k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmServiceSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	var selected []corev1.Pod
	for _, name := range selector.Services {
		deployment, err := s.findService(ctx, name)
		if err != nil {
			return nil, err
		}
		for index := range pods.Items {
			if pods.Items[index].Labels["app"] == deployment.Name && pods.Items[index].DeletionTimestamp == nil {
				selected = append(selected, pods.Items[index])
			}
		}
	}
	for _, id := range selector.Tasks {
		task, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		for index := range pods.Items {
			if pods.Items[index].Name == task.Name {
				selected = append(selected, pods.Items[index])
			}
		}
	}
	return selected, nil
}

func mergeLogStreams(ctx context.Context, streams []<-chan *backend.LogMessage) <-chan *backend.LogMessage {
	merged := make(chan *backend.LogMessage)
	var wait sync.WaitGroup
	wait.Add(len(streams))
	for _, stream := range streams {
		go func(stream <-chan *backend.LogMessage) {
			defer wait.Done()
			for message := range stream {
				select {
				case merged <- message:
				case <-ctx.Done():
					return
				}
			}
		}(stream)
	}
	go func() {
		wait.Wait()
		close(merged)
	}()
	return merged
}
