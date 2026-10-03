package translator

import (
	"maps"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPodMetadata(t *testing.T) {
	labels := map[string]string{
		"ordinary":                                   "not a Kubernetes label value",
		podLabelPrefix + "example.com/team":          "payments",
		podLabelPrefix + "empty":                     "",
		podAnnotationPrefix + "fluentbit.io/parser":  "json",
		podAnnotationPrefix + "example.com/settings": "{\"message\":\"spaces and\nnewlines\"}",
	}
	original := maps.Clone(labels)
	meta, err := podMetadata(labels)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(meta.Labels, map[string]string{"example.com/team": "payments", "empty": ""}) ||
		!maps.Equal(meta.Annotations, map[string]string{
			"fluentbit.io/parser": "json", "example.com/settings": labels[podAnnotationPrefix+"example.com/settings"],
		}) {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if !maps.Equal(labels, original) {
		t.Fatal("input Docker labels were modified")
	}
}

func TestPodMetadataRejectsInvalidEntries(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
	}{
		{"empty label key", podLabelPrefix, "x"},
		{"empty annotation key", podAnnotationPrefix, "x"},
		{"invalid label key", podLabelPrefix + "example.com/bad/key", "x"},
		{"invalid annotation key", podAnnotationPrefix + "bad key", "x"},
		{"invalid label value", podLabelPrefix + "team", "contains spaces"},
		{"long label value", podLabelPrefix + "team", strings.Repeat("a", 64)},
		{"long label name", podLabelPrefix + strings.Repeat("a", 64), "x"},
		{"oversized annotation", podAnnotationPrefix + "note", strings.Repeat("a", 256*1024)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := podMetadata(map[string]string{test.key: test.value})
			if !IsKind(err, KindInvalidArgument) {
				t.Fatalf("error = %v, want invalid argument", err)
			}
		})
	}
	for _, prefix := range []string{podLabelPrefix, podAnnotationPrefix} {
		for _, key := range []string{"app", managedByLabel, "dink.io/network-bridge", envHashAnnotation, "internal.dink.io/key"} {
			t.Run(prefix+key, func(t *testing.T) {
				_, err := podMetadata(map[string]string{prefix + key: "spoofed"})
				if !IsKind(err, KindInvalidArgument) {
					t.Fatalf("error = %v, want reserved-key rejection", err)
				}
			})
		}
	}
}

func TestPodMetadataAnnotationSizeBoundary(t *testing.T) {
	labels := map[string]string{podAnnotationPrefix + "note": strings.Repeat("a", 256*1024-len("note"))}
	if _, err := podMetadata(labels); err != nil {
		t.Fatalf("annotation at limit rejected: %v", err)
	}
	labels[podAnnotationPrefix+"extra"] = ""
	if _, err := podMetadata(labels); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("combined annotation size above limit: %v", err)
	}
}

func TestContainerPodMetadata(t *testing.T) {
	for _, autoRemove := range []bool{false, true} {
		t.Run(map[bool]string{false: "deployment", true: "job"}[autoRemove], func(t *testing.T) {
			ctx, swarm, client := newSwarmFixture(t)
			labels := map[string]string{
				"ordinary":                                  "docker-only",
				podLabelPrefix + "example.com/team":         "payments",
				podAnnotationPrefix + "fluentbit.io/parser": "json",
			}
			_, err := swarm.docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
				Name: "web", Config: &container.Config{Image: "nginx", Labels: labels},
				HostConfig: &container.HostConfig{AutoRemove: autoRemove},
			})
			if err != nil {
				t.Fatal(err)
			}
			workload, err := swarm.docker.findContainer(ctx, "web")
			if err != nil {
				t.Fatal(err)
			}
			meta := workload.Template.ObjectMeta
			if meta.Labels["example.com/team"] != "payments" || meta.Annotations["fluentbit.io/parser"] != "json" ||
				meta.Labels["app"] != "web" || meta.Labels[managedByLabel] != managedByDink ||
				meta.Labels[networkLabelPrefix+networkObjectName("bridge")] != "true" {
				t.Fatalf("unexpected Pod metadata: %+v", meta)
			}
			if _, exists := meta.Labels["ordinary"]; exists {
				t.Fatal("ordinary Docker label leaked into Pod labels")
			}
			if _, exists := workload.Labels["example.com/team"]; exists {
				t.Fatal("Pod label leaked onto workload")
			}
			inspect, _, err := swarm.docker.ContainerInspect(ctx, "web", backend.ContainerInspectOptions{})
			if err != nil || !maps.Equal(inspect.Config.Labels, labels) {
				t.Fatalf("Docker labels not preserved by inspect: %v", err)
			}
			if !autoRemove {
				if err := swarm.docker.setContainerReplicas(ctx, workload, 1, "2026-10-03T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
				deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if deployment.Spec.Template.Annotations["fluentbit.io/parser"] != "json" {
					t.Fatal("restart removed user annotations")
				}
			}
		})
	}
}

func TestSwarmPodMetadata(t *testing.T) {
	ctx, swarm, client := newSwarmFixture(t)
	serviceLabels := map[string]string{
		"ordinary":                                   "service-only",
		podLabelPrefix + "example.com/team":          "service",
		podLabelPrefix + "example.com/service":       "yes",
		podAnnotationPrefix + "example.com/settings": "service",
	}
	containerLabels := map[string]string{
		"ordinary":                                   "container-only",
		podLabelPrefix + "example.com/team":          "container",
		podAnnotationPrefix + "example.com/settings": "container",
	}
	spec := swarmtypes.ServiceSpec{
		Name: "web", Labels: serviceLabels,
		TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx", Labels: containerLabels}},
	}
	if _, err := swarm.CreateService(ctx, spec, "", false); err != nil {
		t.Fatal(err)
	}
	deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	meta := deployment.Spec.Template.ObjectMeta
	if meta.Labels["example.com/team"] != "container" || meta.Labels["example.com/service"] != "yes" ||
		meta.Annotations["example.com/settings"] != "container" || meta.Labels[swarmKindLabel] != swarmServiceKind ||
		meta.Labels[managedByLabel] != managedByDink || meta.Labels["app"] != "web" {
		t.Fatalf("unexpected service Pod metadata: %+v", meta)
	}
	if _, exists := meta.Labels["ordinary"]; exists {
		t.Fatal("ordinary Docker label leaked into Pod labels")
	}
	service, err := swarm.GetService(ctx, "web", false)
	if err != nil || !maps.Equal(service.Spec.Labels, serviceLabels) ||
		!maps.Equal(service.Spec.TaskTemplate.ContainerSpec.Labels, containerLabels) {
		t.Fatalf("service labels not preserved by inspect: %v", err)
	}
	spec.Labels = nil
	spec.TaskTemplate.ContainerSpec.Labels = map[string]string{podLabelPrefix + "example.com/team": "updated"}
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	deployment, err = client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	meta = deployment.Spec.Template.ObjectMeta
	if meta.Labels["example.com/team"] != "updated" || len(meta.Annotations) != 0 {
		t.Fatalf("update did not replace user metadata: %+v", meta)
	}
	if _, exists := meta.Labels["example.com/service"]; exists {
		t.Fatal("removed Pod label survived update")
	}
	spec.TaskTemplate.ContainerSpec.Labels[podLabelPrefix+"app"] = "spoofed"
	if _, err := swarm.UpdateService(ctx, "web", 0, spec, swarmbackend.ServiceUpdateOptions{}, false); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("invalid service update error = %v", err)
	}
	unchanged, err := client.AppsV1().Deployments("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(unchanged.Spec.Template.Labels, meta.Labels) ||
		!maps.Equal(unchanged.Spec.Template.Annotations, meta.Annotations) {
		t.Fatal("invalid service update modified existing Pod metadata")
	}
}

func TestPodMetadataInvalidRequestsCreateNoResources(t *testing.T) {
	for _, kind := range []string{"container", "service", "task"} {
		t.Run(kind, func(t *testing.T) {
			ctx, swarm, client := newSwarmFixture(t)
			labels := map[string]string{podLabelPrefix + "app": "spoofed"}
			var err error
			if kind == "container" {
				_, err = swarm.docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
					Name: "web", Config: &container.Config{Image: "nginx", Labels: labels},
				})
			} else {
				spec := swarmtypes.ServiceSpec{
					Name: "web", TaskTemplate: swarmtypes.TaskSpec{ContainerSpec: &swarmtypes.ContainerSpec{Image: "nginx"}},
				}
				if kind == "service" {
					spec.Labels = labels
				} else {
					spec.TaskTemplate.ContainerSpec.Labels = labels
				}
				_, err = swarm.CreateService(ctx, spec, "", false)
			}
			if !IsKind(err, KindInvalidArgument) {
				t.Fatalf("error = %v, want invalid argument", err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "create" || action.GetVerb() == "update" {
					t.Fatalf("invalid metadata caused a write: %v", action)
				}
			}
		})
	}
}
