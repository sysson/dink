package translator

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/moby/moby/api/types/blkiodev"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestEnsurePullSecretRepairsStaleCredential(t *testing.T) {
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	for _, test := range []struct {
		name     string
		password string
		fail     bool
	}{
		{name: "stale", password: "old-secret"},
		{name: "valid", password: "secret"},
		{name: "registry unavailable", password: "old-secret", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := dockerConfigJSON("dinki.io/tenant", "tenant", test.password)
			if err != nil {
				t.Fatal(err)
			}
			client := kubernetesfake.NewClientset(&corev1.Secret{
				Name: pullSecretName, Namespace: "tenant", Labels: map[string]string{"keep": "yes"},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{corev1.DockerConfigJsonKey: config},
			})
			registry := &fakeRegistry{}
			if test.fail {
				registry.credentialError = errors.New("registry unavailable")
			}
			docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry, pullHost: "dinki.io"}
			err = docker.ensurePullSecret(ctx, "tenant")
			if test.fail != (err != nil) {
				t.Fatalf("ensurePullSecret error = %v", err)
			}
			secret, err := client.CoreV1().Secrets("tenant").Get(ctx, pullSecretName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := "secret"
			if test.fail {
				want = test.password
			}
			if got := dockerConfigPassword(secret.Data[corev1.DockerConfigJsonKey], "dinki.io/tenant", "tenant"); got != want {
				t.Fatalf("pull credential not updated as expected")
			}
			if secret.Labels["keep"] != "yes" {
				t.Fatal("existing metadata was lost")
			}
			if !test.fail && registry.issued != map[bool]int{true: 0, false: 1}[test.password == "secret"] {
				t.Fatal("unexpected credential rotation")
			}
		})
	}
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

func TestContainerCreateMapsHealthcheckToReadinessProbe(t *testing.T) {
	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	client := kubernetesfake.NewClientset()
	registry := &fakeRegistry{
		digests: map[string]string{"nginx": digest},
		configs: map[string]*dockerspec.DockerOCIImageConfig{
			"nginx": {
				ImageConfig: ocispec.ImageConfig{},
				Healthcheck: &dockerspec.HealthcheckConfig{
					Test:          []string{"CMD-SHELL", "curl -fsS http://localhost/health"},
					Interval:      1500 * time.Millisecond,
					Timeout:       250 * time.Millisecond,
					StartPeriod:   2 * time.Second,
					StartInterval: 200 * time.Millisecond,
					Retries:       5,
				},
				Shell: []string{"/custom-shell"},
			},
		},
	}
	docker := &Docker{k8s: &k8s.KubeClient{Interface: client}, registry: registry, pullHost: "localhost:5000"}
	response, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name:   "healthchecked",
		Config: &container.Config{Image: "nginx"},
	})
	if err != nil {
		t.Fatal(err)
	}

	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "healthchecked", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	podContainer := deployment.Spec.Template.Spec.Containers[0]
	probe := podContainer.ReadinessProbe
	if probe == nil || probe.Exec == nil {
		t.Fatalf("readiness probe = %+v, want exec probe", probe)
	}
	wantCommand := []string{"/custom-shell", "-c", "curl -fsS http://localhost/health"}
	if strings.Join(probe.Exec.Command, "\x00") != strings.Join(wantCommand, "\x00") {
		t.Fatalf("probe command = %q", probe.Exec.Command)
	}
	if probe.PeriodSeconds != 2 || probe.TimeoutSeconds != 1 || probe.InitialDelaySeconds != 2 || probe.FailureThreshold != 5 || probe.SuccessThreshold != 1 {
		t.Fatalf("probe timing = %+v", probe)
	}
	if podContainer.LivenessProbe != nil {
		t.Fatal("Docker healthcheck unexpectedly configured a liveness probe")
	}
	if len(response.Warnings) != 3 {
		t.Fatalf("warnings = %v, want start-period, start-interval, and sub-second timing caveats", response.Warnings)
	}
}

func TestDockerHealthProbeDisableAndInvalidTest(t *testing.T) {
	probe, warnings, err := dockerHealthProbe(&container.HealthConfig{Test: []string{"NONE"}}, nil)
	if err != nil || probe != nil || len(warnings) != 0 {
		t.Fatalf("disabled healthcheck = %+v, %v, %v; want no probe or warnings", probe, warnings, err)
	}
	if _, _, err := dockerHealthProbe(&container.HealthConfig{Test: []string{"HTTP", "/health"}}, nil); err == nil {
		t.Fatal("unsupported healthcheck test was accepted")
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
