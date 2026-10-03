package translator

import (
	"context"
	"net/http/httptest"
	"testing"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/core/secrets"
	"github.com/sysson/dink/sdk/plugin"
	sdksecrets "github.com/sysson/dink/sdk/secrets"
)

type mapResolver map[string]string

func (m mapResolver) Resolve(_ context.Context, req *sdksecrets.Request) ([]sdksecrets.Secret, error) {
	out := make([]sdksecrets.Secret, 0, len(req.Refs))
	for _, r := range req.Refs {
		out = append(out, sdksecrets.Secret{Name: r.Name, Value: m[req.Namespace+"/"+r.Ref]})
	}
	return out, nil
}

func withSecretsPlugin(t *testing.T, swarm *Swarm, values mapResolver) {
	t.Helper()
	h, err := plugin.Handler(plugin.Info{Name: "vault"}, sdksecrets.Plugin(values))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	reg, err := plugins.New("dink-system", []plugins.Static{{Name: "vault", Types: []plugins.Type{plugins.TypeSecrets}, Endpoint: srv.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	swarm.docker.secrets = secrets.NewResolver(swarm.k8s, reg)
}

func TestSwarmServiceResolvesSecretEnv(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)
	values := mapResolver{"tenant/db/password": "first"}
	withSecretsPlugin(t, swarm, values)
	if _, err := client.CoreV1().Secrets("tenant").Create(ctx, &corev1.Secret{
		Name: secrets.StoreName, Labels: map[string]string{secrets.StoreLabel: "true"},
		Data: map[string][]byte{"token": []byte("t")},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	spec := swarmtypes.ServiceSpec{
		Name: "web",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{
			Image:  "nginx",
			Env:    []string{"PLAIN=1", "DB_PASSWORD=se://vault/db/password", "TOKEN=se://k8s/token"},
			Labels: map[string]string{podAnnotationPrefix + "fluentbit.io/parser": "json"},
		}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := deployment.Spec.Template.Spec.Containers[0].Env
	if env[0].Value != "1" ||
		env[1].ValueFrom.SecretKeyRef.Name != "dink-env-web" || env[1].ValueFrom.SecretKeyRef.Key != "DB_PASSWORD" ||
		env[2].ValueFrom.SecretKeyRef.Name != secrets.StoreName || env[2].ValueFrom.SecretKeyRef.Key != "token" {
		t.Fatalf("unexpected env: %+v", env)
	}
	for _, v := range env {
		if secrets.IsRef(v.Value) {
			t.Fatalf("reference leaked into the Pod spec: %+v", v)
		}
	}
	stored, err := client.CoreV1().Secrets("tenant").Get(ctx, "dink-env-web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Data["DB_PASSWORD"]) != "first" || len(stored.OwnerReferences) != 1 || stored.OwnerReferences[0].Name != "web" {
		t.Fatalf("unexpected env secret: %+v", stored)
	}
	firstHash := deployment.Spec.Template.Annotations[envHashAnnotation]
	if firstHash == "" {
		t.Fatal("missing env hash annotation")
	}
	if deployment.Spec.Template.Annotations["fluentbit.io/parser"] != "json" {
		t.Fatal("env hash overwrote user annotations")
	}

	values["tenant/db/password"] = "second"
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	stored, _ = client.CoreV1().Secrets("tenant").Get(ctx, "dink-env-web", metav1.GetOptions{})
	deployment, _ = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if string(stored.Data["DB_PASSWORD"]) != "second" || deployment.Spec.Template.Annotations[envHashAnnotation] == firstHash {
		t.Fatal("changed value must update the Secret and roll the Pods")
	}
	if deployment.Spec.Template.Annotations["fluentbit.io/parser"] != "json" {
		t.Fatal("secret rotation removed user annotations")
	}

	spec.TaskTemplate.ContainerSpec.Env = []string{"PLAIN=1"}
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Secrets("tenant").Get(ctx, "dink-env-web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("env secret should be removed once unused, got %v", err)
	}
}

func TestSwarmServiceRejectsInvalidSecretRef(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)
	withSecretsPlugin(t, swarm, mapResolver{})
	spec := swarmtypes.ServiceSpec{
		Name:         "web",
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx", Env: []string{"X=se://unknown/key"}}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("expected invalid argument, got %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("no Deployment should be created for an invalid reference")
	}
}

func TestApplyEnvSecretRefusesForeignSecret(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)
	if _, err := client.CoreV1().Secrets("tenant").Create(ctx, &corev1.Secret{
		Name: "dink-env-web",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	env := secrets.Env{SecretName: "dink-env-web", Values: map[string][]byte{"A": []byte("b")}}
	if err := swarm.docker.applyEnvSecret(ctx, "tenant", "web", env, deploymentOwnerReference("web", "")); !IsKind(err, KindConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}
