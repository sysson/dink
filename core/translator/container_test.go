package translator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/syskit/httpx"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

type fakeRegistry struct {
	digests map[string]string
	issued  int
}

func (f *fakeRegistry) Authenticate(context.Context, *registry.AuthConfig) (string, error) {
	return "", nil
}

func (f *fakeRegistry) ImageInspect(_ context.Context, name string, _ imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	digest, ok := f.digests[name]
	if !ok {
		return nil, errors.New("no such image: " + name)
	}
	return &imagebackend.InspectData{ID: digest, RepoDigests: []string{name + "@" + digest}}, nil
}

func (f *fakeRegistry) IssuePullCredential(ctx context.Context) (string, string, error) {
	f.issued++
	id, _ := identity.FromContext(ctx)
	return id.Namespace, "secret", nil
}

func TestContainerCreatePullsFromDinki(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	registry := &fakeRegistry{digests: map[string]string{"nginx": digest}}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry, pullHost: "localhost:5000"}

	for _, name := range []string{"web", "api"} {
		if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: name, Config: &container.Config{Image: "nginx"}}); err != nil {
			t.Fatalf("ContainerCreate(%s): %v", name, err)
		}
	}
	if registry.issued != 1 {
		t.Fatalf("credential issued %d times, want once per namespace", registry.issued)
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "web", Config: &container.Config{Image: "nginx"}})
	if httpErr, ok := errors.AsType[*httpx.HTTPError](err); !ok || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate ContainerCreate error = %v, want 409", err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := deployment.Spec.Template.Spec
	if got := spec.Containers[0].Image; got != "localhost:5000/tenant/nginx@"+digest {
		t.Fatalf("pod image = %q", got)
	}
	if spec.Containers[0].ImagePullPolicy != corev1.PullAlways || len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != pullSecretName {
		t.Fatalf("pod pull settings = %+v, %+v", spec.Containers[0].ImagePullPolicy, spec.ImagePullSecrets)
	}

	secret, err := client.CoreV1().Secrets("tenant").Get(ctx, pullSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || json.Unmarshal(secret.Data[corev1.DockerConfigJsonKey], &config) != nil {
		t.Fatalf("pull secret = %+v", secret)
	}
	entry, ok := config.Auths["localhost:5000/tenant"]
	if !ok || len(config.Auths) != 1 || entry.Username != "tenant" || entry.Password != "secret" || entry.Auth != "dGVuYW50OnNlY3JldA==" {
		t.Fatalf("pull secret auths = %+v", config.Auths)
	}

	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "missing", Config: &container.Config{Image: "redis"}}); err == nil {
		t.Fatal("ContainerCreate with a missing image succeeded")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "missing", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment created for a missing image")
	}
}

func TestContainerLifecycle(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(1)
	deployment, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dockerID := identity.DockerIDFromUID(deployment.UID)

	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerStop: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas after stop = %v, err = %v; want 0", deployment.Spec.Replicas, err)
	}

	if err := docker.ContainerStart(ctx, dockerID[:12], "", ""); err != nil {
		t.Fatalf("ContainerStart by ID prefix: %v", err)
	}
	if err := docker.ContainerRestart(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatalf("ContainerRestart: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Template.Annotations["dink.io/restarted-at"] == "" {
		t.Fatalf("deployment after restart = %+v, err = %v", deployment.Spec, err)
	}

	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerStop(otherTenant, "web", backend.ContainerStopOptions{}); err == nil {
		t.Fatal("ContainerStop crossed tenant namespace")
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{}); err != nil {
		t.Fatalf("ContainerRm by ID: %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment remains after ContainerRm")
	}
}
