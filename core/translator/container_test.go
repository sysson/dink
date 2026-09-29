package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	kubeexec "k8s.io/client-go/util/exec"
	metricsv1 "k8s.io/metrics/pkg/apis/metrics/v1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

type fakeRegistry struct {
	digests map[string]string
	configs map[string]*dockerspec.DockerOCIImageConfig
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
	result := &imagebackend.InspectData{ID: digest, RepoDigests: []string{name + "@" + digest}}
	result.Config = f.configs[name]
	return result, nil
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
	if !IsKind(err, KindConflict) {
		t.Fatalf("duplicate ContainerCreate error = %v, want conflict", err)
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
	if got := deployment.Spec.Template.Labels[networkLabelPrefix+networkObjectName("bridge")]; got != "true" {
		t.Fatalf("default network membership label = %q, want true", got)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Fatalf("DNS service = %+v, err = %v; want headless service", dnsService, err)
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

func TestContainerCreateGeneratesNameWhenMissing(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}
	deployments, err := client.AppsV1().Deployments("tenant").List(ctx, metav1.ListOptions{})
	if err != nil || len(deployments.Items) != 1 {
		t.Fatalf("created Deployments = %d, err = %v; want one", len(deployments.Items), err)
	}
	deployment := deployments.Items[0]
	name := deployment.Name
	if problems := validation.IsDNS1123Label(name); len(problems) > 0 {
		t.Fatalf("generated name %q is not a DNS label: %v", name, problems)
	}
	parts := strings.Split(name, "-")
	if len(parts) != 3 || len(parts[2]) != 8 {
		t.Fatalf("generated name = %q, want friendly adjective-noun-number form", name)
	}
	if deployment.Labels["app"] != name || deployment.Spec.Selector.MatchLabels["app"] != name || deployment.Spec.Template.Spec.Containers[0].Name != name {
		t.Fatalf("Deployment name %q does not match its labels, selector, and container", name)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, name, metav1.GetOptions{})
	if err != nil || service.Spec.Selector["app"] != name {
		t.Fatalf("generated-name Service = %+v, err = %v", service, err)
	}
}

func TestContainerCreateResources(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: &fakeRegistry{
		digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}, pullHost: "localhost:5000"}
	for _, test := range []struct {
		name        string
		resources   container.Resources
		wantCPU     string
		wantMemory  string
		wantRequest string
		wantError   bool
	}{
		{name: "nano", resources: container.Resources{NanoCPUs: 1_500_000_000, Memory: 512 << 20, MemoryReservation: 256 << 20}, wantCPU: "1500m", wantMemory: "512Mi", wantRequest: "256Mi"},
		{name: "quota", resources: container.Resources{CPUQuota: 50000, Memory: 128 << 20}, wantCPU: "500m", wantMemory: "128Mi"},
		{name: "period", resources: container.Resources{CPUQuota: 50000, CPUPeriod: 200000}, wantCPU: "250m"},
		{name: "none"},
		{name: "empty device weights", resources: container.Resources{BlkioWeightDevice: make([]*blkiodev.WeightDevice, 0)}},
		{name: "CLI defaults", resources: container.Resources{
			BlkioWeightDevice: make([]*blkiodev.WeightDevice, 0), MemorySwappiness: new(int64(-1)),
			OomKillDisable: new(false), PidsLimit: new(int64(0)),
		}},
		{name: "device weights", resources: container.Resources{BlkioWeightDevice: []*blkiodev.WeightDevice{{}}}, wantError: true},
		{name: "explicit swappiness", resources: container.Resources{MemorySwappiness: new(int64(0))}, wantError: true},
		{name: "explicit OOM setting", resources: container.Resources{OomKillDisable: new(true)}, wantError: true},
		{name: "explicit PID limit", resources: container.Resources{PidsLimit: new(int64(10))}, wantError: true},
		{name: "shares", resources: container.Resources{CPUShares: 1024}, wantError: true},
		{name: "swap", resources: container.Resources{MemorySwap: 1024}, wantError: true},
		{name: "conflict", resources: container.Resources{NanoCPUs: 1_000_000_000, CPUQuota: 50000}, wantError: true},
		{name: "reservation", resources: container.Resources{Memory: 128 << 20, MemoryReservation: 256 << 20}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
				Name: test.name, Config: &container.Config{Image: "nginx"}, HostConfig: &container.HostConfig{Resources: test.resources},
			})
			if test.wantError {
				if err == nil {
					t.Fatal("unsupported or invalid resources accepted")
				}
				if _, err := client.AppsV1().Deployments("tenant").Get(ctx, test.name, metav1.GetOptions{}); err == nil {
					t.Fatal("created a deployment with unsupported resources")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, test.name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			resources := deployment.Spec.Template.Spec.Containers[0].Resources
			var cpu, memory, request string
			if value, ok := resources.Limits[corev1.ResourceCPU]; ok {
				cpu = value.String()
			}
			if value, ok := resources.Limits[corev1.ResourceMemory]; ok {
				memory = value.String()
			}
			if value, ok := resources.Requests[corev1.ResourceMemory]; ok {
				request = value.String()
			}
			if cpu != test.wantCPU || memory != test.wantMemory || request != test.wantRequest {
				t.Fatalf("pod resources = %+v, want cpu=%s memory=%s request=%s", resources, test.wantCPU, test.wantMemory, test.wantRequest)
			}
		})
	}
}

func TestContainerUpdateResources(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Annotations: map[string]string{
			containerHostConfigAnnotation: `{"NetworkMode":"bridge"}`,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(1)),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}},
		},
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	response, err := docker.ContainerUpdate(ctx, "web", &container.UpdateConfig{
		NanoCPUs: 500_000_000, Memory: 128 << 20, MemoryReservation: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Warnings) != 1 {
		t.Fatalf("update warnings = %v, want Pod replacement warning", response.Warnings)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resources := deployment.Spec.Template.Spec.Containers[0].Resources
	if resources.Limits.Cpu().MilliValue() != 500 || resources.Limits.Memory().Value() != 128<<20 || resources.Requests.Memory().Value() != 64<<20 {
		t.Fatalf("updated Pod resources = %+v", resources)
	}
	_, hostConfig, err := containerMetadata(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if hostConfig.NetworkMode != "bridge" || hostConfig.NanoCPUs != 500_000_000 || hostConfig.Memory != 128<<20 || hostConfig.MemoryReservation != 64<<20 {
		t.Fatalf("updated host config = %+v", hostConfig)
	}
}

func TestContainerStartRetriesOnConflict(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
	})
	updates := 0
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatalf("ContainerStart() error = %v", err)
	}
	if updates != 2 {
		t.Fatalf("Deployment update attempts = %d, want 2", updates)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v, want 1", deployment.Spec.Replicas)
	}
}

func TestContainerKillRetriesOnConflict(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset(&appsv1.Deployment{
		Name: "web", Namespace: "tenant",
	})
	updates := 0
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if err := docker.ContainerKill(ctx, "web", "SIGINT"); err != nil {
		t.Fatalf("ContainerKill() error = %v", err)
	}
	if updates != 2 {
		t.Fatalf("Deployment update attempts = %d, want 2", updates)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 0 || deployment.Annotations[containerSignalExitCodeAnnotation] != "130" {
		t.Fatalf("killed Deployment = replicas %v, annotations %+v", deployment.Spec.Replicas, deployment.Annotations)
	}
}

func TestContainerCreateResourceDefaults(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: &fakeRegistry{
		digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}, pullHost: "localhost:5000", defaultResources: config.ResourceDefaults{
		Limits: config.ResourceValues{CPU: "500m", Memory: "512Mi"}, Requests: config.ResourceValues{CPU: "100m", Memory: "128Mi"},
	}}
	assertResources := func(name, wantCPU, wantMemory, wantRequest string) {
		t.Helper()
		deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pod := deployment.Spec.Template.Spec.Containers[0]
		if got := pod.Resources.Limits.Cpu().String(); got != wantCPU {
			t.Fatalf("%s Pod CPU limit = %s, want %s", name, got, wantCPU)
		}
		if got := pod.Resources.Limits.Memory().String(); got != wantMemory {
			t.Fatalf("%s Pod memory limit = %s, want %s", name, got, wantMemory)
		}
		if got := pod.Resources.Requests.Memory().String(); got != wantRequest {
			t.Fatalf("%s Pod memory request = %s, want %s", name, got, wantRequest)
		}
		inspect, _, err := docker.ContainerInspect(ctx, name, backend.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if inspect.HostConfig.NanoCPUs != pod.Resources.Limits.Cpu().MilliValue()*1_000_000 || inspect.HostConfig.Memory != pod.Resources.Limits.Memory().Value() ||
			inspect.HostConfig.MemoryReservation != pod.Resources.Requests.Memory().Value() || inspect.HostConfig.Annotations["dink.io/requests.cpu"] != pod.Resources.Requests.Cpu().String() {
			t.Fatalf("%s inspect HostConfig = %+v, Pod resources = %+v", name, inspect.HostConfig, pod.Resources)
		}
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "global", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}
	assertResources("global", "500m", "512Mi", "128Mi")
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "oversized-request", Config: &container.Config{Image: "nginx"}, HostConfig: &container.HostConfig{
		MemoryReservation: 768 << 20,
	}}); err == nil {
		t.Fatal("explicit memory request larger than default limit accepted")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "oversized-request", metav1.GetOptions{}); err == nil {
		t.Fatal("created workload with request above its effective limit")
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "global-pod", Namespace: "tenant", Labels: map[string]string{"app": "global"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "global", Resources: corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("600m"), corev1.ResourceMemory: resource.MustParse("768Mi")},
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	inspect, _, err := docker.ContainerInspect(ctx, "global", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.HostConfig.NanoCPUs != 600_000_000 || inspect.HostConfig.Memory != 768<<20 || inspect.HostConfig.MemoryReservation != 256<<20 || inspect.HostConfig.Annotations["dink.io/requests.cpu"] != "200m" {
		t.Fatalf("inspect did not report admitted Pod resources: %+v", inspect.HostConfig)
	}
	if _, err := client.CoreV1().ConfigMaps("tenant").Create(ctx, &corev1.ConfigMap{
		Name: tenantResourceDefaultsConfigMap, Namespace: "tenant",
		Data: map[string]string{"resources.json": `{"limits":{"cpu":"750m"},"requests":{"memory":"192Mi"}}`},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "tenant-default", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}
	assertResources("tenant-default", "750m", "512Mi", "192Mi")
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "explicit", Config: &container.Config{Image: "nginx"}, HostConfig: &container.HostConfig{
		NanoCPUs: 200_000_000, Memory: 256 << 20, MemoryReservation: 64 << 20,
	}}); err != nil {
		t.Fatal(err)
	}
	assertResources("explicit", "200m", "256Mi", "64Mi")
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "small-explicit", Config: &container.Config{Image: "nginx"}, HostConfig: &container.HostConfig{
		NanoCPUs: 50_000_000, Memory: 64 << 20,
	}}); err != nil {
		t.Fatal(err)
	}
	assertResources("small-explicit", "50m", "64Mi", "64Mi")
	if inspect, _, err := docker.ContainerInspect(ctx, "small-explicit", backend.ContainerInspectOptions{}); err != nil || inspect.HostConfig.Annotations["dink.io/requests.cpu"] != "50m" {
		t.Fatalf("explicit CPU limit should cap default request, inspect = %+v, err = %v", inspect, err)
	}
	configMap, err := client.CoreV1().ConfigMaps("tenant").Get(ctx, tenantResourceDefaultsConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	configMap.Data["resources.json"] = `{"limits":{"cpu":"not-cpu"}}`
	if _, err := client.CoreV1().ConfigMaps("tenant").Update(ctx, configMap, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "bad-default", Config: &container.Config{Image: "nginx"}}); err == nil {
		t.Fatal("invalid tenant defaults accepted")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "bad-default", metav1.GetOptions{}); err == nil {
		t.Fatal("created workload despite invalid tenant defaults")
	}
}

func TestContainerCreateMergesImageDefaults(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	registry := &fakeRegistry{
		digests: map[string]string{"nginx": digest},
		configs: map[string]*dockerspec.DockerOCIImageConfig{
			"nginx": {
				ImageConfig: ocispec.ImageConfig{
					User:       "1000:1000",
					Env:        []string{"MODE=image", "IMAGE_ONLY=present"},
					Entrypoint: []string{"/image-entrypoint"},
					Cmd:        []string{"--image-default"},
					WorkingDir: "/image-workdir",
					ExposedPorts: map[string]struct{}{
						"8080/tcp": {},
					},
					Labels: map[string]string{"from-image": "yes", "owner": "image"},
				},
			},
		},
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry, pullHost: "localhost:5000"}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image:  "nginx",
			Env:    []string{"MODE=request", "REQUEST_ONLY=present"},
			Labels: map[string]string{"owner": "request"},
		},
		HostConfig: &container.HostConfig{PublishAllPorts: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	podContainer := deployment.Spec.Template.Spec.Containers[0]
	if podContainer.Command == nil || len(podContainer.Command) != 1 || podContainer.Command[0] != "/image-entrypoint" || len(podContainer.Args) != 1 || podContainer.Args[0] != "--image-default" || podContainer.WorkingDir != "/image-workdir" {
		t.Fatalf("pod command defaults = %+v", podContainer)
	}
	env := make(map[string]string, len(podContainer.Env))
	for _, value := range podContainer.Env {
		env[value.Name] = value.Value
	}
	if env["MODE"] != "request" || env["IMAGE_ONLY"] != "present" || env["REQUEST_ONLY"] != "present" {
		t.Fatalf("pod env defaults/overrides = %v", env)
	}
	if len(podContainer.Ports) != 1 || podContainer.Ports[0].ContainerPort != 8080 {
		t.Fatalf("image exposed ports not published: %+v", podContainer.Ports)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil || service.Spec.Type != corev1.ServiceTypeNodePort || len(service.Spec.Ports) != 1 {
		t.Fatalf("image exposed port Service = %+v, err = %v", service, err)
	}

	inspect, _, err := docker.ContainerInspect(ctx, "web", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Config.Labels["from-image"] != "yes" || inspect.Config.Labels["owner"] != "request" || inspect.Config.Env[0] != "MODE=request" {
		t.Fatalf("merged inspect config = %+v", inspect.Config)
	}
}

func TestContainerCreateUsesRequestedNetworkMembership(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	const networkName = "isolated"
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "dink.io/v1alpha1",
		"kind":       "DockerNetwork",
		"metadata": map[string]any{
			"name":      networkObjectName(networkName),
			"namespace": "tenant",
			"uid":       "12345678-1234-1234-1234-123456789abc",
		},
		"spec": map[string]any{"name": networkName, "driver": "bridge"},
	}}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		networkResource: "DockerNetworkList",
	}, obj)
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client, Dynamic: dynamicClient},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:   "web",
		Config: &container.Config{Image: "nginx"},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	labels := deployment.Spec.Template.Labels
	if labels[networkLabelPrefix+networkObjectName(networkName)] != "true" {
		t.Fatalf("requested network label missing: %v", labels)
	}
	if _, hasBridge := labels[networkLabelPrefix+networkObjectName("bridge")]; hasBridge {
		t.Fatalf("default bridge label also present for explicit network: %v", labels)
	}
}

func TestContainerInspectAndList(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx:latest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	created := metav1.NewTime(time.Now().Add(-time.Minute))
	response, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image:      "nginx:latest",
			Entrypoint: []string{"/entrypoint"},
			Cmd:        []string{"--serve"},
			Env:        []string{"MODE=production"},
			WorkingDir: "/srv",
			Labels:     map[string]string{"team": "blue"},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{
			network.MustParsePort("8080/tcp"): {{HostPort: "18080"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deployment.CreationTimestamp = created
	if _, err := client.AppsV1().Deployments("tenant").Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: deployment.Spec.Template.Labels,
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			PodIP:     "10.1.2.3",
			StartTime: &created,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:    "web",
				Image:   "localhost:5000/tenant/nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				ImageID: "containerd://sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				State:   corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: created}},
			}},
		},
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	inspect, _, err := docker.ContainerInspect(ctx, "web", backend.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.ID != response.ID || inspect.Name != "/web" || inspect.Config.Image != "nginx:latest" || inspect.Path != "/entrypoint" || len(inspect.Args) != 1 || inspect.Args[0] != "--serve" {
		t.Fatalf("inspect response = %+v", inspect)
	}
	if inspect.State.Status != container.StateRunning || inspect.State.StartedAt == "" || inspect.NetworkSettings.Networks["bridge"].IPAddress.String() != "10.1.2.3" {
		t.Fatalf("inspect state/network = %+v, %+v", inspect.State, inspect.NetworkSettings)
	}
	port := network.MustParsePort("8080/tcp")
	if got := inspect.NetworkSettings.Ports[port]; len(got) != 1 || got[0].HostPort != "18080" {
		t.Fatalf("inspect port bindings = %+v", inspect.NetworkSettings.Ports)
	}

	listed, err := docker.Containers(ctx, &backend.ContainerListOptions{})
	if err != nil || len(listed) != 1 || listed[0].ID != response.ID || listed[0].Image != "nginx:latest" || listed[0].State != container.StateRunning || listed[0].Labels["team"] != "blue" {
		t.Fatalf("containers = %+v, err = %v", listed, err)
	}
	if listed[0].Ports == nil || len(listed[0].Ports) != 1 || listed[0].Ports[0].PublicPort != 18080 {
		t.Fatalf("listed ports = %+v", listed[0].Ports)
	}

	if err := docker.ContainerStop(ctx, "web", backend.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	listed, err = docker.Containers(ctx, &backend.ContainerListOptions{})
	if err != nil || len(listed) != 0 {
		t.Fatalf("default container list after stop = %+v, err = %v; want empty", listed, err)
	}
	listed, err = docker.Containers(ctx, &backend.ContainerListOptions{All: true})
	if err != nil || len(listed) != 1 || listed[0].State != container.StateExited {
		t.Fatalf("all container list after stop = %+v, err = %v", listed, err)
	}
}

func TestContainerNetworkModeMembership(t *testing.T) {
	docker := &Docker{}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	defaultLabels, _, err := docker.containerNetworkLabels(ctx, backend.ContainerCreateConfig{
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			network.NetworkDefault: {},
		}},
	})
	if err != nil || defaultLabels[networkLabelPrefix+networkObjectName(network.NetworkBridge)] != "true" {
		t.Fatalf("default endpoint mapping = %v, err = %v", defaultLabels, err)
	}
	for _, test := range []struct {
		mode        container.NetworkMode
		wantHost    bool
		wantNetwork string
	}{
		{mode: "host", wantHost: true},
		{mode: "none", wantNetwork: "none"},
	} {
		labels, hostNetwork, err := docker.containerNetworkLabels(ctx, backend.ContainerCreateConfig{HostConfig: &container.HostConfig{NetworkMode: test.mode}})
		if err != nil {
			t.Fatalf("network mode %q: %v", test.mode, err)
		}
		if hostNetwork != test.wantHost {
			t.Fatalf("network mode %q hostNetwork = %v, want %v", test.mode, hostNetwork, test.wantHost)
		}
		if test.wantNetwork == "" {
			if len(labels) != 0 {
				t.Fatalf("network mode %q labels = %v, want none", test.mode, labels)
			}
			continue
		}
		if labels[networkLabelPrefix+networkObjectName(test.wantNetwork)] != "true" {
			t.Fatalf("network mode %q labels = %v", test.mode, labels)
		}
	}
}

func TestContainerCreatePublishesExplicitPorts(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	response, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web",
		Config: &container.Config{
			Image: "nginx",
			ExposedPorts: network.PortSet{
				network.MustParsePort("80/tcp"): {},
				network.MustParsePort("53/udp"): {},
			},
		},
		HostConfig: &container.HostConfig{PortBindings: network.PortMap{
			network.MustParsePort("80/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "8080"}},
			network.MustParsePort("53/udp"): {{HostPort: "5353"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Warnings) != 1 {
		t.Fatalf("warnings = %v, want host IP warning", response.Warnings)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := deployment.Spec.Template.Spec.Containers[0].Ports; len(got) != 2 {
		t.Fatalf("pod ports = %+v, want TCP and UDP container ports", got)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.Type != corev1.ServiceTypeClusterIP || len(dnsService.Spec.Ports) != 2 {
		t.Fatalf("DNS service = %+v, err = %v; want ClusterIP with two target ports", dnsService, err)
	}

	service, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer || len(service.Spec.Ports) != 2 {
		t.Fatalf("published service = %+v, want LoadBalancer with two ports", service.Spec)
	}
	portsByProtocol := make(map[corev1.Protocol]corev1.ServicePort, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		portsByProtocol[port.Protocol] = port
	}
	if tcp := portsByProtocol[corev1.ProtocolTCP]; tcp.Port != 8080 || tcp.TargetPort.IntVal != 80 {
		t.Fatalf("published TCP service port = %+v", tcp)
	}
	if udp := portsByProtocol[corev1.ProtocolUDP]; udp.Port != 5353 || udp.TargetPort.IntVal != 53 {
		t.Fatalf("published UDP service port = %+v", udp)
	}
}

func TestContainerCreatePublishesAllPortsWithNodePort(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{
		k8s:      &k8s.KubeClient{Interface: client},
		registry: &fakeRegistry{digests: map[string]string{"nginx": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		pullHost: "localhost:5000",
	}
	_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:       "web",
		Config:     &container.Config{Image: "nginx", ExposedPorts: network.PortSet{network.MustParsePort("80/tcp"): {}}},
		HostConfig: &container.HostConfig{PublishAllPorts: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	dnsService, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || dnsService.Spec.Type != corev1.ServiceTypeClusterIP || len(dnsService.Spec.Ports) != 1 {
		t.Fatalf("DNS service = %+v, err = %v; want ClusterIP with one target port", dnsService, err)
	}
	service, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if service.Spec.Type != corev1.ServiceTypeNodePort || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port != 80 || service.Spec.Ports[0].TargetPort.IntVal != 80 {
		t.Fatalf("published service = %+v, want NodePort service for 80/tcp", service.Spec)
	}
}

func TestContainerLifecycle(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(1)
	deployment, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
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
	if err := docker.ContainerKill(ctx, "web", "INT"); err != nil {
		t.Fatalf("ContainerKill INT: %v", err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || *deployment.Spec.Replicas != 0 {
		t.Fatalf("replicas after INT = %v, err = %v; want 0", deployment.Spec.Replicas, err)
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || deployment.Annotations[containerSignalExitCodeAnnotation] != "" {
		t.Fatalf("signal exit status survived start: %+v, err = %v", deployment.Annotations, err)
	}
	if err := docker.ContainerKill(ctx, "web", "HUP"); err == nil {
		t.Fatal("unsupported signal accepted")
	}

	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerStop(otherTenant, "web", backend.ContainerStopOptions{}); err == nil {
		t.Fatal("ContainerStop crossed tenant namespace")
	}
	for _, serviceName := range []string{"web", publishedPortsServiceName("web")} {
		if _, err := client.CoreV1().Services("tenant").Create(ctx, &corev1.Service{Name: serviceName, Namespace: "tenant"}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{}); err == nil {
		t.Fatal("running container removed without force")
	}
	if err := docker.ContainerRm(ctx, dockerID, &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
		t.Fatalf("ContainerRm by ID: %v", err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{}); err == nil {
		t.Fatal("published service remains after ContainerRm")
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("DNS service remains after ContainerRm")
	}
}

func TestContainerPrune(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	for _, item := range []struct {
		name     string
		replicas int32
		label    string
	}{
		{"old", 0, "web"}, {"running", 1, "web"}, {"other", 0, "db"},
	} {
		annotations := map[string]string{containerConfigAnnotation: `{"Labels":{"app":"` + item.label + `"}}`}
		if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
			Name: item.name, Namespace: "tenant", Annotations: annotations,
			Spec: appsv1.DeploymentSpec{Replicas: &item.replicas},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "unmanaged", Namespace: "tenant", Spec: appsv1.DeploymentSpec{Replicas: &zero},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	report, err := docker.ContainerPrune(ctx, filters.NewArgs(filters.Arg("label", "app=web")))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ContainersDeleted) != 1 {
		t.Fatalf("deleted = %v, want one container", report.ContainersDeleted)
	}
	for _, name := range []string{"running", "other", "unmanaged"} {
		if _, err := client.AppsV1().Deployments("tenant").Get(ctx, name, metav1.GetOptions{}); err != nil {
			t.Fatalf("%s removed: %v", name, err)
		}
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "old", metav1.GetOptions{}); err == nil {
		t.Fatal("pruned deployment still exists")
	}
	if _, err := docker.ContainerPrune(ctx, filters.NewArgs(filters.Arg("unknown", "value"))); err == nil {
		t.Fatal("unsupported filter accepted")
	}
}

func TestContainerLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/apps/v1/namespaces/tenant/deployments/web":
			_ = json.NewEncoder(w).Encode(&appsv1.Deployment{
				APIVersion: "apps/v1", Kind: "Deployment",
				Name: "web", Namespace: "tenant",
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
			})
		case "/api/v1/namespaces/tenant/pods":
			if r.URL.Query().Get("labelSelector") != "app=web" {
				t.Errorf("label selector = %q", r.URL.Query().Get("labelSelector"))
			}
			_ = json.NewEncoder(w).Encode(&corev1.PodList{
				APIVersion: "v1", Kind: "PodList",
				Items: []corev1.Pod{{Name: "web-pod", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}},
			})
		case "/api/v1/namespaces/tenant/pods/web-pod/log":
			if r.URL.Query().Get("tailLines") != "2" || r.URL.Query().Get("timestamps") != "true" {
				t.Errorf("log query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("2026-01-01T00:00:00Z hello\n"))
		default:
			t.Errorf("unexpected Kubernetes request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	messages, tty, err := docker.ContainerLogs(ctx, "web", &backend.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "2"})
	if err != nil {
		t.Fatal(err)
	}
	message, ok := <-messages
	if !ok || tty || message.Source != "stdout" || string(message.Line) != "hello\n" {
		t.Fatalf("message = %+v, ok = %v, tty = %v", message, ok, tty)
	}
	if _, ok := <-messages; ok {
		t.Fatal("unexpected second log message")
	}
}

func TestContainerWait(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	replicas := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	response, err := docker.ContainerWait(ctx, "web", container.WaitConditionNotRunning)
	if err != nil || response.StatusCode != 42 {
		t.Fatalf("wait result = %+v, err = %v", response, err)
	}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := docker.ContainerWait(deadline, "web", container.WaitConditionRemoved); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancellation = %v", err)
	}
	timer := time.AfterFunc(50*time.Millisecond, func() {
		_ = client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{})
	})
	defer timer.Stop()
	response, err = docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err != nil || response.StatusCode != 0 {
		t.Fatalf("wait for removal = %+v, err = %v", response, err)
	}
	response, err = docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err == nil {
		t.Fatalf("wait on unknown container = %+v, want not found", response)
	}
}

func TestContainerWaitAutoRemove(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 2*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	zero := int32(0)
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &zero},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	timer := time.AfterFunc(40*time.Millisecond, func() {
		_ = docker.ContainerStart(ctx, "web", "", "")
		_, _ = client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
			Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 42}},
			}}},
		}, metav1.CreateOptions{})
	})
	defer timer.Stop()
	response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
	if err != nil || response.StatusCode != 42 {
		t.Fatalf("auto-remove wait = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{}); err == nil {
		t.Fatal("auto-removed container still exists")
	}
}

func TestContainerWaitAutoRemoveAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved); err != nil || response.StatusCode != 137 {
		t.Fatalf("restarted container auto-remove wait = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err == nil {
		t.Fatal("restarted container remains after its first exit")
	}
}

func TestContainerWaitAutoRemoveWaitsForDeletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deleteRequested := make(chan struct{})
	client.PrependReactor("delete", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		close(deleteRequested)
		return true, nil, nil
	})
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	result := make(chan error, 1)
	go func() {
		_, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
		result <- err
	}()
	select {
	case <-deleteRequested:
	case <-ctx.Done():
		t.Fatal("auto-remove did not request deployment deletion")
	}
	select {
	case err := <-result:
		t.Fatalf("wait returned before deployment deletion completed: %v", err)
	default:
	}
	if err := client.Tracker().Delete(appsv1.SchemeGroupVersion.WithResource("deployments"), "tenant", "web"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("wait did not finish when deployment was deleted")
	}
}

func TestContainerWaitIgnoresPreviousDeploymentPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 400*time.Millisecond)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "current-deployment", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().ReplicaSets("tenant").Create(ctx, &appsv1.ReplicaSet{
		Name: "old-rs", Namespace: "tenant", UID: "old-rs-uid",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: "previous-deployment"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "old-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "old-rs", UID: "old-rs-uid"}},
		Status:          corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 137}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait on new deployment = %+v, err = %v", response, err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err != nil {
		t.Fatalf("new deployment was removed by old pod: %v", err)
	}
}

func TestContainerWaitAutoRemoveAfterSignal(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(1)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	result := make(chan struct {
		response container.WaitResponse
		err      error
	}, 1)
	go func() {
		response, err := docker.ContainerWait(ctx, "web", container.WaitConditionRemoved)
		result <- struct {
			response container.WaitResponse
			err      error
		}{response, err}
	}()
	if err := docker.ContainerKill(ctx, "web", "INT"); err != nil {
		t.Fatal(err)
	}
	select {
	case item := <-result:
		if item.err != nil || item.response.StatusCode != 130 {
			t.Fatalf("wait after INT = %+v, err = %v", item.response, item.err)
		}
	case <-ctx.Done():
		t.Fatal("wait did not finish after INT removed its pod")
	}
	if _, err := client.AppsV1().Deployments("tenant").Get(context.Background(), "web", metav1.GetOptions{}); err == nil {
		t.Fatal("auto-remove left a stopped Deployment behind")
	}
}

func TestContainerStats(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	metricsClient := metricsfake.NewSimpleClientset()
	metric := &metricsv1.PodMetrics{
		APIVersion: "metrics.k8s.io/v1", Kind: "PodMetrics",
		Name: "web-pod", Namespace: "tenant",
		Timestamp: metav1.NewTime(time.Unix(100, 0)), Window: metav1.Duration{Duration: 2 * time.Second},
		Containers: []metricsv1.ContainerMetrics{{Name: "web", Usage: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi"),
		}}},
	}
	if err := metricsClient.Tracker().Create(metricsv1.SchemeGroupVersion.WithResource("pods"), metric, "tenant"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client, MetricsV1Interface: metricsClient.MetricsV1()}}
	var output bytes.Buffer
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatal(err)
	}
	var stats container.StatsResponse
	if err := json.Unmarshal(output.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.CPUStats.CPUUsage.TotalUsage != 500_000_000 || stats.MemoryStats.Usage != 64<<20 || stats.MemoryStats.Limit != 128<<20 {
		t.Fatalf("unexpected CPU/memory stats: %+v", stats)
	}
	output.Reset()
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{Stream: true, OneShot: true, OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatalf("one-shot stats: %v", err)
	}
	if bytes.Count(output.Bytes(), []byte(`"read"`)) != 1 {
		t.Fatalf("one-shot stats output = %q", output.String())
	}
	output.Reset()
	deadline, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := docker.ContainerStats(deadline, "web", &backend.ContainerStatsConfig{Stream: true, OutStream: func() io.Writer { return &output }}); err != nil {
		t.Fatalf("cancelled stats stream: %v", err)
	}
	if bytes.Count(output.Bytes(), []byte(`"read"`)) != 1 {
		t.Fatalf("stats stream emitted duplicate samples: %q", output.String())
	}
	docker.k8s.MetricsV1Interface = nil
	if err := docker.ContainerStats(ctx, "web", &backend.ContainerStatsConfig{OutStream: func() io.Writer { return &output }}); err == nil {
		t.Fatal("missing metrics API reported successful stats")
	}
}

func TestContainerExecCreateInspect(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"echo", "hello"}, AttachStdout: true})
	if err != nil || execID == "" {
		t.Fatalf("exec create ID = %q, err = %v", execID, err)
	}
	inspect, err := docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.ID != execID || inspect.ProcessConfig.Entrypoint != "echo" || inspect.Running || inspect.ExitCode != nil {
		t.Fatalf("exec inspect = %+v, err = %v", inspect, err)
	}
	otherTenant := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if _, err := docker.ContainerExecInspect(otherTenant, execID); err == nil {
		t.Fatal("exec ID leaked across tenants")
	}
	if _, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"id"}, User: "root"}); err == nil {
		t.Fatal("unsupported exec user accepted")
	}
	var stdout bytes.Buffer
	docker.podStream = func(_ context.Context, namespace, podName string, options corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if namespace != "tenant" || podName != "web-pod" || attach || len(options.Command) != 2 || options.Command[0] != "echo" || !options.Stdout {
			t.Errorf("unexpected exec options: namespace = %q, pod = %q, options = %+v, attach = %v", namespace, podName, options, attach)
		}
		_, _ = streams.Stdout.Write([]byte("hello\n"))
		return kubeexec.CodeExitError{Err: errors.New("exit status 7"), Code: 7}
	}
	if err := docker.ContainerExecStart(otherTenant, execID, backend.ExecStartConfig{Stdout: &stdout}); err == nil {
		t.Fatal("cross-tenant exec start succeeded")
	}
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: &stdout}); err != nil {
		t.Fatal(err)
	}
	inspect, err = docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.Running || inspect.ExitCode == nil || *inspect.ExitCode != 7 || stdout.String() != "hello\n" {
		t.Fatalf("completed exec = %+v, output = %q, err = %v", inspect, stdout.String(), err)
	}
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: &stdout}); err == nil {
		t.Fatal("completed exec restarted")
	}
	if err := docker.ContainerRm(ctx, "web", &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerExecInspect(ctx, execID); err == nil {
		t.Fatal("removed container left an inspectable exec ID")
	}
}

func TestContainerExecConsoleSizeAndDetachKeys(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{
		Cmd: []string{"sh"}, Tty: true, AttachStdin: true, AttachStdout: true,
		ConsoleSize: &[2]uint{24, 80}, DetachKeys: "ctrl-p,ctrl-q",
	})
	if err != nil {
		t.Fatal(err)
	}
	var size *remotecommand.TerminalSize
	docker.podStream = func(streamCtx context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, _ bool) error {
		size = streams.TerminalSizeQueue.Next()
		if _, err := io.Copy(io.Discard, streams.Stdin); err != nil {
			return err
		}
		<-streamCtx.Done()
		return streamCtx.Err()
	}
	// ctrl-p ctrl-q is the detach sequence, so the stream must end cleanly.
	stdin := bytes.NewReader([]byte{0x10, 0x11})
	if err := docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdin: io.NopCloser(stdin), Stdout: io.Discard}); err != nil {
		t.Fatalf("detached exec returned %v", err)
	}
	if size == nil || size.Height != 24 || size.Width != 80 {
		t.Fatalf("initial terminal size = %+v", size)
	}
	inspect, err := docker.ContainerExecInspect(ctx, execID)
	if err != nil || inspect.Running || inspect.ExitCode != nil {
		t.Fatalf("detached exec inspect = %+v, err = %v", inspect, err)
	}
}

func TestContainerAttach(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	var output bytes.Buffer
	docker.podStream = func(_ context.Context, namespace, podName string, options corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach || options.Container != "web" || !options.Stdout || options.Stdin || namespace != "tenant" || podName != "web-pod" {
			t.Errorf("unexpected attach options: %+v, attach = %v", options, attach)
		}
		_, _ = streams.Stdout.Write([]byte("attached\n"))
		return nil
	}
	config := &backend.ContainerAttachConfig{Stream: true, UseStdout: true, GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
		return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
	}}
	if err := docker.ContainerAttach(ctx, "web", config); err != nil || output.String() != "attached\n" {
		t.Fatalf("attach output = %q, err = %v", output.String(), err)
	}
	config.Logs = true
	if err := docker.ContainerAttach(ctx, "web", config); err == nil {
		t.Fatal("historical attach logs silently accepted")
	}
	config.Logs = false
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		return errors.New("attach transport failed")
	}
	if err := docker.ContainerAttach(ctx, "web", config); err == nil || !strings.Contains(err.Error(), "attach transport failed") {
		t.Fatalf("running container attach error = %v", err)
	}
}

func TestContainerAttachReplaysNonInteractiveOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apis/apps/v1/namespaces/tenant/deployments/web":
			_ = json.NewEncoder(w).Encode(&appsv1.Deployment{
				APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "tenant",
				Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
			})
		case "/api/v1/namespaces/tenant/pods":
			_ = json.NewEncoder(w).Encode(&corev1.PodList{
				APIVersion: "v1", Kind: "PodList", Items: []corev1.Pod{{
					Name: "web-pod", Namespace: "tenant", Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
				}},
			})
		case "/api/v1/namespaces/tenant/pods/web-pod/log":
			if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("container") != "web" || r.URL.Query().Has("tailLines") {
				t.Errorf("log query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.WriteString(w, "startup output\n")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "later output\n")
		default:
			t.Errorf("unexpected Kubernetes request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	var output bytes.Buffer
	if err := docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
		Stream: true, UseStdout: true, UseStderr: true,
		GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
			return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
		},
	}); err != nil || output.String() != "startup output\nlater output\n" {
		t.Fatalf("non-interactive attach output = %q, err = %v", output.String(), err)
	}
}

func TestContainerAttachBeforeStart(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach {
			t.Error("expected pod attach")
		}
		_, err := streams.Stdout.Write([]byte("started\n"))
		return err
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	var output bytes.Buffer
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), &output, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream before start")
	}
	select {
	case err := <-result:
		t.Fatalf("attach returned before start: %v", err)
	default:
	}
	if err := docker.ContainerStart(ctx, "web", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil || output.String() != "started\n" {
			t.Fatalf("attach output = %q, err = %v", output.String(), err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not connect after start")
	}
}

func TestContainerAttachTTYResize(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	zero := int32(0)
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
		Spec: appsv1.DeploymentSpec{Replicas: &zero, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	sizes := make(chan remotecommand.TerminalSize, 2)
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, attach bool) error {
		if !attach || !streams.Tty || streams.TerminalSizeQueue == nil {
			t.Error("TTY attach has no terminal size queue")
			return nil
		}
		sizes <- *streams.TerminalSizeQueue.Next()
		sizes <- *streams.TerminalSizeQueue.Next()
		return nil
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("TTY attach did not connect")
	}
	if err := docker.ContainerResize(ctx, identity.DockerIDFromUID("12345678-1234-1234-1234-123456789abc"), 17, 232); err != nil {
		t.Fatalf("resize before pod starts: %v", err)
	}
	if err := docker.ContainerResize(ctx, "web", 19, 200); err != nil {
		t.Fatalf("updated resize before pod starts: %v", err)
	}
	otherTenant := identity.NewContext(ctx, identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerResize(otherTenant, "web", 17, 232); err == nil {
		t.Fatal("resize crossed tenant namespace")
	}
	if err := docker.ContainerResize(ctx, "web", 65536, 232); err == nil {
		t.Fatal("resize accepted dimensions exceeding Kubernetes limits")
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-sizes:
		if size.Height != 19 || size.Width != 200 {
			t.Fatalf("TTY size = %+v", size)
		}
	case <-ctx.Done():
		t.Fatal("TTY resize did not reach the pod stream")
	}
	if err := docker.ContainerResize(ctx, "web", 18, 80); err != nil {
		t.Fatalf("resize running attach: %v", err)
	}
	select {
	case size := <-sizes:
		if size.Height != 18 || size.Width != 80 {
			t.Fatalf("updated TTY size = %+v", size)
		}
	case <-ctx.Done():
		t.Fatal("running resize did not reach the pod stream")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerResize(ctx, "web", 18, 80); err == nil {
		t.Fatal("resized an inactive attach")
	}
}

func TestContainerAttachIgnoresPreviousDeploymentPod(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	currentUID := types.UID("12345678-1234-1234-1234-123456789abc")
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", UID: currentUID,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, replicaSet := range []struct {
		name string
		uid  types.UID
		root types.UID
	}{
		{"old-rs", "old-rs-uid", "previous-deployment-uid"},
		{"new-rs", "new-rs-uid", currentUID},
	} {
		if _, err := client.AppsV1().ReplicaSets("tenant").Create(ctx, &appsv1.ReplicaSet{
			Name: replicaSet.name, Namespace: "tenant", UID: replicaSet.uid,
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", UID: replicaSet.root}},
		}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	newPod := func(name string, replicaSetUID types.UID) *corev1.Pod {
		return &corev1.Pod{Name: name, Namespace: "tenant", Labels: map[string]string{"app": "web"},
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: strings.TrimSuffix(name, "-pod") + "-rs", UID: replicaSetUID}},
			Spec:            corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
			Status:          corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, newPod("old-pod", "old-rs-uid"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(_ context.Context, _, podName string, _ corev1.PodExecOptions, _ remotecommand.StreamOptions, _ bool) error {
		if podName != "new-pod" {
			return fmt.Errorf("attached to previous deployment pod %s", podName)
		}
		return nil
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not connect")
	}
	select {
	case err := <-result:
		t.Fatalf("attach selected previous pod: %v", err)
	case <-time.After(350 * time.Millisecond):
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, newPod("new-pod", "new-rs-uid"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not reach current pod")
	}
}

func TestContainerAttachExitedBeforeStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 2*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "web", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		t.Error("tried attaching to an exited container")
		return errors.New("container web not found in pod")
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream")
	}
	if err := client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("attach after auto-remove = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not finish after auto-remove")
	}
}

func TestContainerAttachExitsDuringStream(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	annotations, err := containerAnnotations(backend.ContainerCreateConfig{
		Config: &container.Config{}, HostConfig: &container.HostConfig{AutoRemove: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant", Annotations: annotations,
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", Labels: map[string]string{"app": "web"},
		Spec:   corev1.PodSpec{Containers: []corev1.Container{{Name: "web", TTY: true}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	docker.podStream = func(context.Context, string, string, corev1.PodExecOptions, remotecommand.StreamOptions, bool) error {
		pod, err := client.CoreV1().Pods("tenant").Get(ctx, "web-pod", metav1.GetOptions{})
		if err != nil {
			return err
		}
		pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
		if _, err := client.CoreV1().Pods("tenant").Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
			return err
		}
		return errors.New("unable to upgrade connection: container web not found in pod")
	}
	var output bytes.Buffer
	if err := docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
		Stream: true, UseStdout: true,
		GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
			return io.NopCloser(strings.NewReader("")), &output, &output, nil
		},
	}); err != nil || output.Len() != 0 {
		t.Fatalf("attach after exit output = %q, err = %v", output.String(), err)
	}
}

func TestContainerAttachWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}))
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, io.Discard, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("attach did not establish its stream")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("attach cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attach did not stop after cancellation")
	}
}

func TestContainerAttachRemovedWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 3*time.Second)
	defer cancel()
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{
		Name: "web", Namespace: "tenant",
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	result := make(chan error, 1)
	var output bytes.Buffer
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	go func() {
		result <- docker.ContainerAttach(ctx, "web", &backend.ContainerAttachConfig{
			Stream: true, UseStdout: true,
			GetStreams: func(bool, func()) (io.ReadCloser, io.Writer, io.Writer, error) {
				close(connected)
				return io.NopCloser(strings.NewReader("")), io.Discard, &output, nil
			},
		})
	}()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("attach did not establish its stream")
	}
	if err := client.AppsV1().Deployments("tenant").Delete(ctx, "web", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(output.String(), "no longer exists") {
			t.Fatalf("attach output = %q, err = %v", output.String(), err)
		}
	case <-ctx.Done():
		t.Fatal("attach did not exit after container removal")
	}
}

func TestContainerExecResize(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, &appsv1.Deployment{Name: "web", Namespace: "tenant"}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Pods("tenant").Create(ctx, &corev1.Pod{
		Name: "web-pod", Namespace: "tenant", UID: "pod-uid", Labels: map[string]string{"app": "web"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}}
	execID, err := docker.ContainerExecCreate(ctx, "web", &container.ExecCreateRequest{Cmd: []string{"sh"}, Tty: true, AttachStdout: true})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	docker.podStream = func(_ context.Context, _, _ string, _ corev1.PodExecOptions, streams remotecommand.StreamOptions, _ bool) error {
		close(started)
		size := streams.TerminalSizeQueue.Next()
		if size == nil || size.Height != 25 || size.Width != 80 {
			return fmt.Errorf("unexpected terminal size: %+v", size)
		}
		return nil
	}
	go func() {
		finished <- docker.ContainerExecStart(ctx, execID, backend.ExecStartConfig{Stdout: io.Discard})
	}()
	<-started
	if err := docker.ContainerExecResize(ctx, execID, 25, 80); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerExecResize(ctx, execID, 40, 100); err == nil {
		t.Fatal("resized a completed exec")
	}
}
