package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func e2eSetup(t *testing.T) buildKitSetup {
	t.Helper()
	if os.Getenv("DINK_E2E") != "1" {
		t.Skip("run make test-e2e for disposable-cluster client tests")
	}
	if os.Getenv("DINK_INTEGRATION_CONTEXT") == "" || os.Getenv("DINK_INTEGRATION_OTHER_CONTEXT") == "" {
		t.Fatal("E2E requires both tenant Docker contexts")
	}
	return buildKitIntegration(t)
}

func waitDocker(t *testing.T, s buildKitSetup, want string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var output []byte
	var err error
	for {
		output, err = s.docker(ctx, args...)
		if err == nil && strings.TrimSpace(string(output)) == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for docker %v to return %q: %v\n%s", args, want, err, output)
		case <-time.After(time.Second):
		}
	}
}

func cleanupDocker(t *testing.T, s buildKitSetup, args ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if output, err := s.docker(ctx, args...); err != nil {
			t.Errorf("cleanup docker %v: %v\n%s", args, err, output)
		}
	})
}

type workloadIdentity struct {
	Kind string
	UID  string
	Spec string
}

func e2eWorkloadIdentity(t *testing.T, s buildKitSetup, selector string) []workloadIdentity {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "kubectl", "-n", "e2e-a", "get", "deployments,pods", "-l", selector, "-o", "json")
	command.Env = s.env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reading E2E workload identity: %v\n%s", err, output)
	}
	var result struct {
		Items []struct {
			Kind     string
			Metadata struct {
				UID string
			}
			Spec json.RawMessage
		}
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	identities := make([]workloadIdentity, 0, len(result.Items))
	for _, item := range result.Items {
		spec := ""
		if item.Kind == "Deployment" {
			spec = string(item.Spec)
		}
		identities = append(identities, workloadIdentity{item.Kind, item.Metadata.UID, spec})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].UID < identities[j].UID })
	if len(identities) != 2 {
		t.Fatalf("expected one Deployment and one Pod, got %+v", identities)
	}
	return identities
}

func TestE2EContainerBuildAndIsolation(t *testing.T) {
	s := e2eSetup(t)
	image := s.id + ":runtime"
	dir := t.TempDir()
	for name, content := range map[string]string{
		"Dockerfile": "FROM alpine:3.21\nCOPY payload /payload\n",
		"payload":    "built-and-ran-through-dink\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s.mustDocker(t, "buildx", "build", "--builder", s.context, "--load", "--progress=plain", "-t", image, dir)
	s.cleanImages(t, image)
	t.Run("short-lived attached output", func(t *testing.T) {
		output := s.mustDocker(t, "run", "--rm", image, "cat", "/payload")
		if strings.TrimSpace(string(output)) != "built-and-ran-through-dink" {
			t.Fatalf("built image output: %s", output)
		}
	})

	name := s.id + "-container"
	s.mustDocker(t, "run", "-d", "--name", name, image, "sh", "-c", `trap 'kill "$child"; wait "$child"; exit 0' TERM INT; echo e2e-ready; sleep 600 & child=$!; wait "$child"`)
	id := strings.TrimSpace(string(s.mustDocker(t, "inspect", "--format", "{{.Id}}", name)))
	cleanupDocker(t, s, "rm", "-f", id)
	waitDocker(t, s, "true", "inspect", "--format", "{{.State.Running}}", name)
	waitDocker(t, s, "e2e-ready", "logs", name)
	if output := s.mustDocker(t, "exec", name, "cat", "/payload"); strings.TrimSpace(string(output)) != "built-and-ran-through-dink" {
		t.Fatalf("exec output: %s", output)
	}
	other := s
	other.context = os.Getenv("DINK_INTEGRATION_OTHER_CONTEXT")
	for _, args := range [][]string{{"inspect", name}, {"image", "inspect", image}} {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		output, err := other.docker(ctx, args...)
		cancel()
		if err == nil || !strings.Contains(strings.ToLower(string(output)), "not found") &&
			!strings.Contains(strings.ToLower(string(output)), "no such") {
			t.Fatalf("other tenant must receive not-found for %v: %v\n%s", args, err, output)
		}
	}
	// Docker treats force-removing a missing container as success.
	other.mustDocker(t, "rm", "-f", name)
	waitDocker(t, s, "true", "inspect", "--format", "{{.State.Running}}", name)
	before := e2eWorkloadIdentity(t, s, "app="+name)
	newName := s.id + "_Renamed"
	s.mustDocker(t, "rename", name, newName)
	waitDocker(t, s, "/"+newName, "inspect", "--format", "{{.Name}}", id)
	after := e2eWorkloadIdentity(t, s, "app="+name)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rename replaced a workload/Pod or changed the Deployment spec: before %+v, after %+v", before, after)
	}
	if output := s.mustDocker(t, "exec", newName, "cat", "/payload"); strings.TrimSpace(string(output)) != "built-and-ran-through-dink" {
		t.Fatalf("exec after rename: %s", output)
	}
	s.mustDocker(t, "stop", newName)
	waitDocker(t, s, "false", "inspect", "--format", "{{.State.Running}}", id)
}

func TestE2ECompose(t *testing.T) {
	s := e2eSetup(t)
	file := filepath.Join(t.TempDir(), "compose.yaml")
	content := `services:
  web:
    image: busybox:1.37
    command: ["sh", "-c", "trap 'kill \"$$child\"; wait \"$$child\"; exit 0' TERM INT; test -f /data/index.html || echo compose-ready > /data/index.html; httpd -f -p 8080 -h /data & child=$$!; wait \"$$child\""]
    volumes: ["data:/data"]
  client:
    image: alpine:3.21
    command: ["sh", "-c", "trap 'kill \"$$child\"; wait \"$$child\"; exit 0' TERM INT; sleep 600 & child=$$!; wait \"$$child\""]
volumes:
  data: {}
`
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"compose", "-p", s.id, "-f", file}
	cleanupDocker(t, s, append(append([]string{}, base...), "down", "--volumes", "--remove-orphans")...)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, args := range [][]string{{"ps", "-a"}, {"logs", "--no-color"}} {
			output, err := s.docker(ctx, append(append([]string{}, base...), args...)...)
			t.Logf("Compose diagnostics %v: %v\n%s", args, err, output)
		}
		command := exec.CommandContext(ctx, "kubectl", "-n", "e2e-a", "get", "pods,services,endpointslices", "-o", "yaml")
		command.Env = s.env
		output, err := command.CombinedOutput()
		t.Logf("Compose Kubernetes diagnostics: %v\n%s", err, output)
	})
	s.mustDocker(t, append(append([]string{}, base...), "up", "-d")...)
	fetch := append(append([]string{}, base...), "exec", "-T", "--interactive=false", "client", "wget", "-T", "5", "-qO-", "http://web:8080")
	waitDocker(t, s, "compose-ready", fetch...)
	s.mustDocker(t, append(append([]string{}, base...), "exec", "-T", "--interactive=false", "web", "sh", "-c", "echo persisted > /data/index.html")...)
	s.mustDocker(t, append(append([]string{}, base...), "up", "-d", "--force-recreate", "web")...)
	waitDocker(t, s, "persisted", fetch...)
}

func TestE2EAttachableOverlayNetwork(t *testing.T) {
	s := e2eSetup(t)
	name := s.id + "-network"
	s.mustDocker(t, "network", "create", "--driver", "overlay", "--attachable", name)
	cleanupDocker(t, s, "network", "rm", name)
	output := s.mustDocker(t, "network", "inspect", "--format", "{{.Driver}} {{.Scope}} {{.Attachable}}", name)
	if strings.TrimSpace(string(output)) != "overlay swarm true" {
		t.Fatalf("attachable overlay network properties: %s", output)
	}
}

func TestE2ESwarmService(t *testing.T) {
	s := e2eSetup(t)
	name := s.id + "-service"
	s.mustDocker(t, "service", "create", "--name", name, "--replicas", "1", "alpine:3.21", "sh", "-c", `trap 'kill "$child"; wait "$child"; exit 0' TERM INT; sleep 600 & child=$!; wait "$child"`)
	cleanupDocker(t, s, "service", "rm", name)
	waitDocker(t, s, "1", "service", "inspect", "--format", "{{.ServiceStatus.RunningTasks}}", name)
	s.mustDocker(t, "service", "scale", fmt.Sprintf("%s=2", name))
	waitDocker(t, s, "2", "service", "inspect", "--format", "{{.ServiceStatus.RunningTasks}}", name)
}
