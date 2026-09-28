package translator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) ContainerExecCreate() {

}

func (d *Docker) ContainerExecInspect() {

}

func (d *Docker) ContainerExecResize() {

}

func (d *Docker) ContainerExecStart() {

}

func (d *Docker) ExecExists() {

}

func (d *Docker) ContainerArchivePath() {

}

func (d *Docker) ContainerExport() {

}

func (d *Docker) ContainerExtractToDir() {

}

func (d *Docker) ContainerStatPath() {

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

func (d *Docker) ContainerKill() {

}

func (d *Docker) ContainerPause() {

}

func (d *Docker) ContainerRename() {

}

func (d *Docker) ContainerResize() {

}

func (d *Docker) ContainerRestart() {

}

func (d *Docker) ContainerRm() {

}

func (d *Docker) ContainerStart() {

}

func (d *Docker) ContainerStop() {

}

func (d *Docker) ContainerUnpause() {

}

func (d *Docker) ContainerUpdate() {

}

func (d *Docker) ContainerWait() {

}

func (d *Docker) ContainerAttach() {

}

func (d *Docker) ContainerChanges() {

}

func (d *Docker) ContainerInspect() {

}

func (d *Docker) ContainerLogs() {

}

func (d *Docker) ContainerStats() {

}

func (d *Docker) ContainerTop() {

}

func (d *Docker) Containers() {

}

func (d *Docker) ContainerPrune() {

}

func (d *Docker) CreateImageFromContainer() {

}

func (d *Docker) RawSysInfo() {

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
