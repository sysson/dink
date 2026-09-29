package translator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/v2/daemon/server/backend"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) ContainerLogs(ctx context.Context, name string, options *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, bool, error) {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if options == nil {
		return nil, false, InvalidArgument(fmt.Errorf("log options are required"))
	}
	if options.Details || !options.ShowStdout || !options.ShowStderr {
		return nil, false, Unsupported(fmt.Errorf("kubernetes pod logs cannot separate stdout and stderr or provide Docker log attributes"))
	}
	pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
	if err != nil {
		return nil, false, kubeError(err)
	}
	var selected *corev1.Pod
	for index := range pods.Items {
		pod := &pods.Items[index]
		if selected == nil || (pod.Status.Phase == corev1.PodRunning && selected.Status.Phase != corev1.PodRunning) {
			selected = pod
		}
	}
	if selected == nil {
		return nil, false, NotFound(fmt.Errorf("no pod found for container %s", name))
	}
	if len(selected.Spec.Containers) == 0 || len(deployment.Spec.Template.Spec.Containers) == 0 {
		return nil, false, NotFound(fmt.Errorf("no container found in pod for %s", name))
	}
	logOptions := &corev1.PodLogOptions{Container: selected.Spec.Containers[0].Name, Follow: options.Follow, Timestamps: true}
	if !options.Since.IsZero() {
		logOptions.SinceTime = &metav1.Time{Time: options.Since}
	}
	if !options.Until.IsZero() && options.Follow {
		return nil, false, InvalidArgument(fmt.Errorf("following logs with an until timestamp is not supported"))
	}
	if options.Tail != "" && options.Tail != "all" {
		lines, err := strconv.ParseInt(options.Tail, 10, 64)
		if err != nil || lines < 0 {
			return nil, false, InvalidArgument(fmt.Errorf("invalid log tail %q", options.Tail))
		}
		logOptions.TailLines = &lines
	}
	stream, err := d.k8s.CoreV1().Pods(deployment.Namespace).GetLogs(selected.Name, logOptions).Stream(ctx)
	if err != nil {
		return nil, false, kubeError(err)
	}
	messages := make(chan *backend.LogMessage)
	go func() {
		defer close(messages)
		defer func() {
			_ = stream.Close()
		}()
		reader := bufio.NewReader(stream)
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				stamp, body, found := strings.Cut(string(line), " ")
				if !found {
					body = string(line)
				}
				timestamp, parseErr := time.Parse(time.RFC3339Nano, stamp)
				if parseErr != nil {
					body = string(line)
				}
				if options.Until.IsZero() || !timestamp.After(options.Until) {
					select {
					case messages <- &backend.LogMessage{Source: "stdout", Line: []byte(body), Timestamp: timestamp}:
					case <-ctx.Done():
						return
					}
				}
			}
			if readErr != nil {
				if readErr != io.EOF && ctx.Err() == nil {
					select {
					case messages <- &backend.LogMessage{Err: readErr}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return messages, deployment.Spec.Template.Spec.Containers[0].TTY, nil
}
