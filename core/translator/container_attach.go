package translator

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/v2/daemon/server/backend"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
)

type containerAttachSession struct {
	sizes chan remotecommand.TerminalSize
}

func (d *Docker) ContainerResize(ctx context.Context, name string, height, width uint32) error {
	if height > 65535 || width > 65535 {
		return InvalidArgument(fmt.Errorf("container terminal dimensions exceed Kubernetes limits"))
	}
	deployment, err := d.findContainer(ctx, name)
	if err != nil {
		return err
	}
	key := deployment.Namespace + "/" + string(deployment.UID)
	d.attachMu.Lock()
	defer d.attachMu.Unlock()
	if len(d.attaches[key]) == 0 {
		return Conflict(fmt.Errorf("container %s has no active TTY attach", name))
	}
	size := remotecommand.TerminalSize{Height: uint16(height), Width: uint16(width)}
	for session := range d.attaches[key] {
		select {
		case session.sizes <- size:
		default:
			select {
			case <-session.sizes:
			default:
			}
			session.sizes <- size
		}
	}
	return nil
}

func (d *Docker) ContainerAttach(ctx context.Context, name string, config *backend.ContainerAttachConfig) (attachErr error) {
	if config == nil || config.GetStreams == nil {
		return InvalidArgument(fmt.Errorf("attach streams are required"))
	}
	if !config.Stream || config.Logs || config.DetachKeys != "" {
		return Unsupported(fmt.Errorf("attach requires a live stream without historical logs or detach keys"))
	}
	deployment, err := d.findContainer(ctx, name)
	if err != nil {
		return err
	}
	containerSpec, err := namedContainer(&deployment.Template.Spec, deployment.Name)
	if err != nil {
		return err
	}
	if config.UseStdin && !containerSpec.Stdin {
		return InvalidArgument(fmt.Errorf("container stdin was not enabled at creation"))
	}
	var session *containerAttachSession
	if containerSpec.TTY {
		session = &containerAttachSession{sizes: make(chan remotecommand.TerminalSize, 1)}
		key := deployment.Namespace + "/" + string(deployment.UID)
		d.attachMu.Lock()
		if d.attaches == nil {
			d.attaches = make(map[string]map[*containerAttachSession]struct{})
		}
		if d.attaches[key] == nil {
			d.attaches[key] = make(map[*containerAttachSession]struct{})
		}
		d.attaches[key][session] = struct{}{}
		d.attachMu.Unlock()
		defer func() {
			d.attachMu.Lock()
			delete(d.attaches[key], session)
			if len(d.attaches[key]) == 0 {
				delete(d.attaches, key)
			}
			d.attachMu.Unlock()
		}()
	}
	stdin, stdout, stderr, err := config.GetStreams(!containerSpec.TTY, func() {})
	if err != nil {
		return err
	}
	errorOutput := stderr
	defer func() {
		if attachErr != nil && ctx.Err() == nil {
			_, _ = fmt.Fprintln(errorOutput, attachErr)
		}
		_ = stdin.Close()
	}()
	pod, err := d.waitAttachPod(ctx, deployment, !containerSpec.TTY && !config.UseStdin)
	if err != nil {
		return err
	}
	if pod == nil {
		return nil
	}
	if !containerSpec.TTY && !config.UseStdin {
		logs, err := d.k8s.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: containerSpec.Name, Follow: true,
		}).Stream(ctx)
		if err != nil {
			return kubeError(err)
		}
		defer func() {
			_ = logs.Close()
		}()
		output := stdout
		if !config.UseStdout {
			output = stderr
		}
		_, err = io.Copy(output, logs)
		return err
	}
	var input io.Reader
	if config.UseStdin {
		input = stdin
	}
	if !config.UseStdout && (!containerSpec.TTY || !config.UseStderr) {
		stdout = nil
	}
	if !config.UseStderr || containerSpec.TTY {
		stderr = nil
	}
	var sizes remotecommand.TerminalSizeQueue
	if session != nil {
		sizes = terminalResizeQueue{ctx: ctx, sizes: session.sizes}
	}
	err = d.streamPod(ctx, pod.Namespace, pod.Name, corev1.PodExecOptions{
		Container: containerSpec.Name, Stdin: input != nil, Stdout: stdout != nil, Stderr: stderr != nil, TTY: containerSpec.TTY,
	}, remotecommand.StreamOptions{Stdin: input, Stdout: stdout, Stderr: stderr, Tty: containerSpec.TTY, TerminalSizeQueue: sizes}, true)
	if err != nil && d.attachTargetExited(ctx, deployment, pod.Name) {
		return nil
	}
	return err
}

func (d *Docker) attachTargetExited(ctx context.Context, deployment *containerWorkload, podName string) bool {
	_, hostConfig, err := containerMetadata(deployment)
	if err != nil || ctx.Err() != nil {
		return false
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		pod, err := d.k8s.CoreV1().Pods(deployment.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && hostConfig.AutoRemove {
			return true
		}
		if err == nil {
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == deployment.Name &&
					(status.State.Terminated != nil || hostConfig.AutoRemove && status.LastTerminationState.Terminated != nil) {
					return true
				}
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func (d *Docker) waitAttachPod(ctx context.Context, deployment *containerWorkload, replayExited bool) (*corev1.Pod, error) {
	_, hostConfig, err := containerMetadata(deployment)
	if err != nil {
		return nil, err
	}
	containerName := deployment.Name
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := d.refreshWorkload(ctx, deployment)
		if apierrors.IsNotFound(err) {
			if hostConfig.AutoRemove {
				return nil, nil
			}
			return nil, NotFound(fmt.Errorf("container %s no longer exists", deployment.Name))
		}
		if err != nil {
			return nil, kubeError(err)
		}
		pods, err := d.containerPods(ctx, deployment)
		if err != nil {
			return nil, err
		}
		for index := range pods {
			pod := &pods[index]
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name != containerName {
					continue
				}
				if status.State.Terminated != nil || hostConfig.AutoRemove && status.LastTerminationState.Terminated != nil {
					if replayExited && status.State.Terminated != nil {
						return pod, nil
					}
					return nil, nil
				}
				if pod.Status.Phase == corev1.PodRunning && status.State.Running != nil {
					return pod, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
