package translator

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	networktypes "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/pkg/filters"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	ktesting "k8s.io/client-go/testing"
)

func TestDockerContainerNames(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	for _, name := range []string{"Web_One", "web-one", "web.one", "1web", strings.Repeat("a", 100)} {
		t.Run(name, func(t *testing.T) {
			if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
				Name: name, Config: &container.Config{Image: "nginx"},
			}); err != nil {
				t.Fatal(err)
			}
			workload, err := docker.findContainer(ctx, "/"+name)
			if err != nil {
				t.Fatal(err)
			}
			if len(validation.IsDNS1035Label(workload.Name)) > 0 || workload.Annotations[containerNameAnnotation] != name {
				t.Fatalf("internal identity/name = %q / %+v", workload.Name, workload.Annotations)
			}
			if workload.Template.Spec.Containers[0].Name != workload.Name ||
				workload.Template.Labels["app"] != workload.Name {
				t.Fatal("Pod spec and selector must use the stable internal identity")
			}
			inspect, _, err := docker.ContainerInspect(ctx, name, backend.ContainerInspectOptions{})
			if err != nil || inspect.Name != "/"+name {
				t.Fatalf("inspect = %+v, err = %v", inspect, err)
			}
			service, err := client.CoreV1().Services("tenant").Get(ctx, containerServiceName(name), metav1.GetOptions{})
			if err != nil || !workloadOwnsService(workload, service) {
				t.Fatalf("DNS Service = %+v, err = %v", service, err)
			}
			if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
				Name: name, Config: &container.Config{Image: "nginx"},
			}); !IsKind(err, KindConflict) {
				t.Fatalf("duplicate name error = %v", err)
			}
			if workload.Name != name {
				if _, err := docker.findContainer(ctx, workload.Name); !IsKind(err, KindNotFound) {
					t.Fatalf("internal identity exposed as Docker name: %v", err)
				}
			}
		})
	}
	for _, name := range []string{"bad name", "_bad", "-bad", "bad/name", "//bad", "."} {
		_, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: name, Config: &container.Config{Image: "nginx"}})
		if !IsKind(err, KindInvalidArgument) {
			t.Fatalf("invalid Docker name %q: %v", name, err)
		}
	}
}

func TestContainerRenamePreservesWorkload(t *testing.T) {
	for _, autoRemove := range []bool{false, true} {
		t.Run(map[bool]string{false: "Deployment", true: "Job"}[autoRemove], func(t *testing.T) {
			ctx, docker, client := newPolicyFixture(t)
			if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
				Name: "Original_Name", Config: &container.Config{Image: "nginx"},
				HostConfig: &container.HostConfig{AutoRemove: autoRemove},
			}); err != nil {
				t.Fatal(err)
			}
			before, err := docker.findContainer(ctx, "Original_Name")
			if err != nil {
				t.Fatal(err)
			}
			// The fake Kubernetes client doesn't generate UIDs.
			before.UID = "12345678-1234-1234-1234-123456789abc"
			if autoRemove {
				job, err := client.BatchV1().Jobs("tenant").Get(ctx, before.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				job.UID = before.UID
				if _, err := client.BatchV1().Jobs("tenant").Update(ctx, job, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else {
				deployment, err := client.AppsV1().Deployments("tenant").Get(ctx, before.Name, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				deployment.UID = before.UID
				if _, err := client.AppsV1().Deployments("tenant").Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			service, err := client.CoreV1().Services("tenant").Get(ctx, containerServiceName("Original_Name"), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			service.OwnerReferences = []metav1.OwnerReference{before.ownerReference()}
			if _, err := client.CoreV1().Services("tenant").Update(ctx, service, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := docker.ContainerStart(ctx, "Original_Name", "", ""); err != nil {
				t.Fatal(err)
			}
			before, err = docker.findContainer(ctx, "Original_Name")
			if err != nil {
				t.Fatal(err)
			}
			id := identity.DockerIDFromUID(before.UID)
			if err := docker.ContainerRename(ctx, id[:12], "/New_Name"); err != nil {
				t.Fatal(err)
			}
			after, err := docker.findContainer(ctx, "New_Name")
			if err != nil {
				t.Fatal(err)
			}
			if before.Name != after.Name || before.UID != after.UID || before.Started != after.Started ||
				!reflect.DeepEqual(before.Template, after.Template) {
				t.Fatal("rename changed the workload identity, running state or Pod template")
			}
			if _, err := docker.findContainer(ctx, "Original_Name"); !IsKind(err, KindNotFound) {
				t.Fatalf("old Docker name still resolves: %v", err)
			}
			inspect, _, err := docker.ContainerInspect(ctx, id, backend.ContainerInspectOptions{})
			if err != nil || inspect.Name != "/New_Name" || inspect.ID != id {
				t.Fatalf("inspect after rename: %+v, %v", inspect, err)
			}
			options := &backend.ContainerListOptions{All: true}
			options.Filters.Add("name", "New_Name")
			list, err := docker.Containers(ctx, options)
			if err != nil || len(list) != 1 || list[0].Names[0] != "/New_Name" {
				t.Fatalf("filtered list after rename: %+v, %v", list, err)
			}
			if _, err := client.CoreV1().Services("tenant").Get(ctx, service.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatalf("old DNS claim remains: %v", err)
			}
			if err := docker.ContainerRm(ctx, id, &backend.ContainerRmConfig{ForceRemove: true}); err != nil {
				t.Fatal(err)
			}
			services, err := client.CoreV1().Services("tenant").List(ctx, metav1.ListOptions{})
			if err != nil || len(services.Items) != 0 {
				t.Fatalf("services remain after removing renamed container: %+v, %v", services, err)
			}
		})
	}
}

func TestContainerRenameLegacyAndNameReuse(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	legacy := &appsv1.Deployment{
		Name: "old", Namespace: "tenant", UID: "12345678-1234-1234-1234-123456789abc",
		Spec: appsv1.DeploymentSpec{Replicas: new(int32(1)), Template: corev1.PodTemplateSpec{
			Labels: map[string]string{"app": "old"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "old"}}},
		}},
	}
	if _, err := client.AppsV1().Deployments("tenant").Create(ctx, legacy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	service := containerDNSService(deploymentWorkload(legacy), nil)
	service.Annotations = nil
	if _, err := client.CoreV1().Services("tenant").Create(ctx, service, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerRename(ctx, "old", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{Name: "old", Config: &container.Config{Image: "nginx"}}); err != nil {
		t.Fatal(err)
	}
	reused, err := docker.findContainer(ctx, "old")
	if err != nil || reused.Name == legacy.Name {
		t.Fatalf("reused name resolves to old internal workload: %+v, %v", reused, err)
	}
	renamed, err := docker.findContainer(ctx, "new")
	if err != nil || renamed.Name != legacy.Name || !reflect.DeepEqual(renamed.Template, legacy.Spec.Template) {
		t.Fatalf("legacy workload replaced: %+v, %v", renamed, err)
	}
	if err := docker.ContainerRename(ctx, "new", "old"); !IsKind(err, KindConflict) {
		t.Fatalf("rename over another container: %v", err)
	}
}

func TestContainerRenameAliasAndPublishedServices(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	if _, err := docker.ContainerCreate(ctx, backend.ContainerCreateConfig{
		Name: "web", Config: &container.Config{Image: "nginx"},
		HostConfig: &container.HostConfig{PortBindings: networktypes.PortMap{
			networktypes.MustParsePort("80/tcp"): {{HostPort: "8080"}},
		}},
		NetworkingConfig: &networktypes.NetworkingConfig{EndpointsConfig: map[string]*networktypes.EndpointSettings{
			"bridge": {Aliases: []string{"web", "frontend"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	published, err := client.CoreV1().Services("tenant").Get(ctx, publishedPortsServiceName("web"), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerRename(ctx, "web", "frontend"); err != nil {
		t.Fatal(err)
	}
	after, err := client.CoreV1().Services("tenant").Get(ctx, published.Name, metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(published.Spec, after.Spec) {
		t.Fatalf("published ports changed during rename: %+v, %v", after, err)
	}
	old, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{})
	if err != nil || old.Labels[aliasOfLabel] != "web" || old.Annotations[containerNameAnnotation] != "" {
		t.Fatalf("explicit old-name alias not retained: %+v, %v", old, err)
	}
	if err := docker.DisconnectContainerFromNetwork(ctx, "bridge", "frontend", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("old alias retained after disconnect: %v", err)
	}
	primary, err := client.CoreV1().Services("tenant").Get(ctx, "frontend", metav1.GetOptions{})
	if err != nil || primary.Labels[aliasOfLabel] != "" {
		t.Fatalf("new primary service lost after disconnect: %+v, %v", primary, err)
	}
}

func TestContainerRenameFailuresAndIsolation(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	createAliasedContainer(t, ctx, docker, "web", "frontend")
	createAliasedContainer(t, ctx, docker, "other", "occupied")
	for _, name := range []string{"other", "occupied"} {
		if err := docker.ContainerRename(ctx, "web", name); !IsKind(err, KindConflict) {
			t.Fatalf("rename to occupied name %s: %v", name, err)
		}
	}
	for _, name := range []string{"", "bad name", "//bad"} {
		if err := docker.ContainerRename(ctx, "web", name); !IsKind(err, KindInvalidArgument) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
	other := identity.NewContext(context.Background(), identity.Identity{Namespace: "other-tenant"})
	if err := docker.ContainerRename(other, "web", "stolen"); !IsKind(err, KindNotFound) {
		t.Fatalf("cross-tenant rename: %v", err)
	}
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("injected update failure")
	})
	if err := docker.ContainerRename(ctx, "web", "new"); err == nil || !strings.Contains(err.Error(), "injected update failure") {
		t.Fatalf("update failure hidden: %v", err)
	}
	if _, err := docker.findContainer(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "new", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("failed rename leaked its DNS claim: %v", err)
	}
}

func TestContainerRenameRetriesAndReconciles(t *testing.T) {
	ctx, docker, client := newPolicyFixture(t)
	createAliasedContainer(t, ctx, docker, "web")
	updates := 0
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("stale resource version"))
		}
		return false, nil, nil
	})
	failedDelete := false
	client.PrependReactor("delete", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.(ktesting.DeleteAction).GetName() == "web" && !failedDelete {
			failedDelete = true
			return true, nil, errors.New("injected cleanup failure")
		}
		return false, nil, nil
	})
	if err := docker.ContainerRename(ctx, "web", "new"); err == nil || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("post-commit Service error hidden: %v", err)
	}
	if updates != 2 {
		t.Fatalf("resourceVersion conflict did not retry: %d updates", updates)
	}
	workload, err := docker.findContainer(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.setContainerName(ctx, workload, "web", "another"); !IsKind(err, KindConflict) {
		t.Fatalf("stale logical-name comparison accepted: %v", err)
	}
	if err := docker.setContainerName(ctx, workload, "web", "new"); err != nil {
		t.Fatalf("concurrent rename to the same name must be idempotent: %v", err)
	}
	if err := docker.ContainerRename(ctx, "new", "new"); err != nil {
		t.Fatalf("rename retry failed: %v", err)
	}
	if _, err := client.CoreV1().Services("tenant").Get(ctx, "web", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retry did not remove stale primary Service: %v", err)
	}
}

func TestContainerRenameEvents(t *testing.T) {
	ctx, docker, _ := newPolicyFixture(t)
	createAliasedContainer(t, ctx, docker, "web")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, stream, err := docker.SubscribeToEvents(ctx, time.Time{}, time.Time{}, filters.NewArgs(filters.Arg("event", "rename")))
	if err != nil {
		t.Fatal(err)
	}
	if err := docker.ContainerRename(ctx, "web", "New_Name"); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-stream:
		message, ok := value.(events.Message)
		if !ok || message.Action != "rename" || message.Actor.Attributes["name"] != "New_Name" {
			t.Fatalf("rename event = %#v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("no rename event emitted")
	}
}
