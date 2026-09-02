package translator

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/dink/identity"
	"github.com/sysson/dink/pkg/httputils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
		return container.CreateResponse{}, httputils.Unauthorized(fmt.Errorf("missing identity in context"))
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
						Containers: []corev1.Container{
							{
								Name:  cfg.Name,
								Image: cfg.Config.Image,
							},
						},
					},
				},
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return container.CreateResponse{}, err
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
