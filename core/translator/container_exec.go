package translator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/term"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	kubeexec "k8s.io/client-go/util/exec"
)

// terminalSize converts a Docker [height, width] console size, dropping
// dimensions Kubernetes cannot represent.
func terminalSize(size *[2]uint) (remotecommand.TerminalSize, bool) {
	if size == nil || size[0] == 0 || size[1] == 0 || size[0] > 65535 || size[1] > 65535 {
		return remotecommand.TerminalSize{}, false
	}
	return remotecommand.TerminalSize{Height: uint16(size[0]), Width: uint16(size[1])}, true
}

type containerExec struct {
	namespace   string
	containerID string
	podName     string
	podUID      string
	container   string
	config      container.ExecCreateRequest
	running     bool
	exitCode    *int
	resize      chan remotecommand.TerminalSize
}

type terminalResizeQueue struct {
	ctx   context.Context
	sizes <-chan remotecommand.TerminalSize
}

func (queue terminalResizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case size := <-queue.sizes:
		return &size
	case <-queue.ctx.Done():
		return nil
	}
}

// detachReader ends the stream once the client types its detach sequence.
type detachReader struct {
	reader   io.Reader
	cancel   context.CancelFunc
	detached *atomic.Bool
}

func (reader *detachReader) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	if _, ok := errors.AsType[term.EscapeError](err); ok {
		reader.detached.Store(true)
		reader.cancel()
		return read, io.EOF
	}
	return read, err
}

func (d *Docker) ContainerExecCreate(ctx context.Context, name string, config *container.ExecCreateRequest) (string, error) {
	if config == nil || len(config.Cmd) == 0 {
		return "", InvalidArgument(fmt.Errorf("no exec command specified"))
	}
	if config.User != "" || config.Privileged || config.WorkingDir != "" || len(config.Env) != 0 {
		return "", InvalidArgument(fmt.Errorf("exec user, privilege, working directory and environment are not supported"))
	}
	if config.DetachKeys != "" {
		if _, err := term.ToBytes(config.DetachKeys); err != nil {
			return "", InvalidArgument(fmt.Errorf("invalid detach keys: %w", err))
		}
	}
	deployment, pod, err := d.runningContainerPod(ctx, name)
	if err != nil {
		return "", err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	d.execMu.Lock()
	if d.execs == nil {
		d.execs = make(map[string]*containerExec)
	}
	d.execs[id] = &containerExec{
		namespace: deployment.Namespace, containerID: identity.DockerIDFromUID(deployment.UID),
		podName: pod.Name, podUID: string(pod.UID), container: pod.Spec.Containers[0].Name, config: *config,
	}
	d.execMu.Unlock()
	return id, nil
}

func (d *Docker) ContainerExecInspect(ctx context.Context, execID string) (*container.ExecInspectResponse, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	d.execMu.Lock()
	defer d.execMu.Unlock()
	entry := d.execs[execID]
	if entry == nil || entry.namespace != id.Namespace {
		return nil, NotFound(fmt.Errorf("exec %s not found", execID))
	}
	return &container.ExecInspectResponse{
		ID: execID, Running: entry.running, ExitCode: entry.exitCode, ContainerID: entry.containerID,
		OpenStdin: entry.config.AttachStdin, OpenStdout: entry.config.AttachStdout, OpenStderr: entry.config.AttachStderr,
		ProcessConfig: &container.ExecProcessConfig{Tty: entry.config.Tty, Entrypoint: entry.config.Cmd[0], Arguments: entry.config.Cmd[1:]},
	}, nil
}

func (d *Docker) runningContainerPod(ctx context.Context, name string) (*appsv1.Deployment, *corev1.Pod, error) {
	deployment, err := d.findDeployment(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	pods, err := d.k8s.CoreV1().Pods(deployment.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + deployment.Name})
	if err != nil {
		return nil, nil, kubeError(err)
	}
	for index := range pods.Items {
		pod := &pods.Items[index]
		if pod.Status.Phase == corev1.PodRunning && len(pod.Spec.Containers) > 0 {
			return deployment, pod, nil
		}
	}
	return nil, nil, NotFound(fmt.Errorf("no running pod found for container %s", name))
}

func (d *Docker) ContainerExecResize(ctx context.Context, execID string, height, width uint32) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if height > 65535 || width > 65535 {
		return InvalidArgument(fmt.Errorf("exec terminal dimensions exceed Kubernetes limits"))
	}
	d.execMu.Lock()
	entry := d.execs[execID]
	if entry == nil || entry.namespace != id.Namespace {
		d.execMu.Unlock()
		return NotFound(fmt.Errorf("exec %s not found", execID))
	}
	if !entry.running || !entry.config.Tty || entry.resize == nil {
		d.execMu.Unlock()
		return Conflict(fmt.Errorf("exec %s has no active TTY", execID))
	}
	queue := entry.resize
	d.execMu.Unlock()
	select {
	case queue <- remotecommand.TerminalSize{Height: uint16(height), Width: uint16(width)}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Docker) ContainerExecStart(ctx context.Context, execID string, streams backend.ExecStartConfig) error {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	d.execMu.Lock()
	entry := d.execs[execID]
	if entry == nil || entry.namespace != id.Namespace {
		d.execMu.Unlock()
		return NotFound(fmt.Errorf("exec %s not found", execID))
	}
	if entry.running || entry.exitCode != nil {
		d.execMu.Unlock()
		return Conflict(fmt.Errorf("exec %s has already started", execID))
	}
	entry.running = true
	d.execMu.Unlock()
	defer func() {
		d.execMu.Lock()
		entry.running = false
		entry.resize = nil
		d.execMu.Unlock()
	}()
	pod, err := d.k8s.CoreV1().Pods(entry.namespace).Get(ctx, entry.podName, metav1.GetOptions{})
	if err != nil {
		return kubeError(err)
	}
	if string(pod.UID) != entry.podUID || pod.Status.Phase != corev1.PodRunning {
		return Conflict(fmt.Errorf("exec target pod has changed or stopped"))
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var detached atomic.Bool
	var stdin io.Reader
	if entry.config.AttachStdin {
		stdin = streams.Stdin
		if stdin != nil && entry.config.DetachKeys != "" {
			keys, err := term.ToBytes(entry.config.DetachKeys)
			if err != nil {
				return InvalidArgument(fmt.Errorf("invalid detach keys: %w", err))
			}
			stdin = &detachReader{reader: term.NewEscapeProxy(stdin, keys), cancel: cancel, detached: &detached}
		}
	}
	var stdout, stderr io.Writer
	if entry.config.AttachStdout || (entry.config.Tty && entry.config.AttachStderr) {
		stdout = streams.Stdout
	}
	if entry.config.AttachStderr && !entry.config.Tty {
		stderr = streams.Stderr
	}
	var sizes remotecommand.TerminalSizeQueue
	if entry.config.Tty {
		queue := make(chan remotecommand.TerminalSize, 1)
		initial, ok := terminalSize(streams.ConsoleSize)
		if !ok {
			initial, ok = terminalSize(entry.config.ConsoleSize)
		}
		if ok {
			queue <- initial
		}
		sizes = terminalResizeQueue{ctx: streamCtx, sizes: queue}
		d.execMu.Lock()
		entry.resize = queue
		d.execMu.Unlock()
	}
	err = d.streamPod(streamCtx, entry.namespace, entry.podName, corev1.PodExecOptions{
		Container: entry.container, Command: entry.config.Cmd, Stdin: stdin != nil,
		Stdout: stdout != nil, Stderr: stderr != nil, TTY: entry.config.Tty,
	}, remotecommand.StreamOptions{Stdin: stdin, Stdout: stdout, Stderr: stderr, Tty: entry.config.Tty, TerminalSizeQueue: sizes}, false)
	if detached.Load() {
		return nil
	}
	var exitErr kubeexec.ExitError
	if err == nil || errors.As(err, &exitErr) {
		code := 0
		if exitErr != nil {
			code = exitErr.ExitStatus()
		}
		d.execMu.Lock()
		entry.exitCode = &code
		d.execMu.Unlock()
		return nil
	}
	return err
}

func (d *Docker) ExecExists(ctx context.Context, execID string) (bool, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return false, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	d.execMu.Lock()
	defer d.execMu.Unlock()
	entry := d.execs[execID]
	return entry != nil && entry.namespace == id.Namespace, nil
}
