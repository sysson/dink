package translator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

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
	// A missing image is reported as not found, which makes the docker CLI pull and retry.
	image, err := d.podImage(ctx, id.Namespace, cfg.Config.Image)
	if err != nil {
		return container.CreateResponse{}, err
	}
	if err := d.ensurePullSecret(ctx, id.Namespace); err != nil {
		return container.CreateResponse{}, err
	}
	deployment, err := d.k8s.AppsV1().Deployments(id.Namespace).Create(ctx,
		&appsv1.Deployment{
			Name: cfg.Name,
			Labels: map[string]string{
				"app": cfg.Name,
			},
			Namespace: id.Namespace,
			Spec: appsv1.DeploymentSpec{
				Replicas: new(int32(1)),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": cfg.Name,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": cfg.Name,
						},
					},
					Spec: corev1.PodSpec{
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: pullSecretName}},
						Containers: []corev1.Container{
							{
								Name:  cfg.Name,
								Image: image,
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
	return container.CreateResponse{
		ID: identity.DockerIDFromUID(deployment.UID),
	}, nil
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
	return kubeError(d.k8s.AppsV1().Deployments(deployment.Namespace).Delete(ctx, deployment.Name, options))
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

func (d *Docker) ContainerInspect(context.Context, string, backend.ContainerInspectOptions) (*container.InspectResponse, network.HardwareAddr, error) {
	return nil, nil, ErrNotImplemented
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

func (d *Docker) Containers(context.Context, *backend.ContainerListOptions) ([]container.Summary, error) {
	return nil, ErrNotImplemented
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

// pullSecretName is the dockerconfigjson Secret, one per tenant namespace,
// holding the namespace's dinki pull credential.
const pullSecretName = "dinki-pull"

// podImage returns the reference nodes pull name from: the namespace's
// repository on pullHost, pinned to the digest name resolves to now.
func (d *Docker) podImage(ctx context.Context, namespace, name string) (string, error) {
	image, err := d.registry.ImageInspect(ctx, name, imagebackend.ImageInspectOpts{})
	if err != nil {
		return "", err
	}
	if len(image.RepoDigests) == 0 {
		return "", fmt.Errorf("image %s has no repository digest", name)
	}
	return d.pullHost + "/" + namespace + "/" + image.RepoDigests[0], nil
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
