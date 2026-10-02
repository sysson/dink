package buildkit_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
)

// This test never starts containers or changes cluster workloads. The caller
// supplies an isolated backend and a host address it can reach for publication.
func TestExternalBuildKitDockerBuild(t *testing.T) {
	address := os.Getenv("DINK_BUILDKIT_TEST_ADDR")
	if address == "" {
		t.Skip("set DINK_BUILDKIT_TEST_ADDR to test a real external BuildKit daemon")
	}
	f := newFixture(t, address)
	httpURL := gatewayHTTP(t, f)
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM scratch\nCOPY payload /payload\nLABEL dink.build-test=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "payload"), []byte("built through dink\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	u, _ := url.Parse(httpURL)
	iidFile := filepath.Join(t.TempDir(), "iid")
	metadataFile := filepath.Join(t.TempDir(), "metadata.json")
	command := exec.CommandContext(ctx, "docker", "--host", "tcp://"+u.Host, "build", "--progress=plain", "--iidfile", iidFile, "--metadata-file", metadataFile, "-t", "app:test", contextDir)
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if !strings.HasPrefix(name, "DOCKER_") && !strings.HasPrefix(name, "BUILDX_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "DOCKER_BUILDKIT=1", "DOCKER_CONFIG="+t.TempDir())
	output, err := command.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("docker build: %v", err)
	}
	tenant := identity.NewContext(ctx, identity.Identity{Namespace: "tenant"})
	image, err := f.api.ImageInspect(tenant, "app:test", imagebackend.ImageInspectOpts{})
	if err != nil || image == nil {
		t.Fatalf("built image is not inspectable: %v %v", image, err)
	}
	if len(image.RootFS.Layers) != 1 || image.Config.Labels["dink.build-test"] != "true" {
		t.Fatalf("built image lost its layer or config: %v", image.InspectResponse)
	}
	iid, err := os.ReadFile(iidFile)
	if err != nil || strings.TrimSpace(string(iid)) != image.ID {
		t.Fatalf("iidfile = %q %v, image ID = %s", iid, err, image.ID)
	}
	metadataJSON, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var outputMetadata map[string]any
	if err := json.Unmarshal(metadataJSON, &outputMetadata); err != nil {
		t.Fatal(err)
	}
	if outputMetadata["image.name"] != "app:test" {
		t.Fatalf("metadata image name = %v", outputMetadata["image.name"])
	}
	historyCommand := exec.CommandContext(ctx, "docker", "--host", "tcp://"+u.Host, "buildx", "history", "ls", "--builder", "default", "--format", "json")
	historyCommand.Env = command.Env
	historyOutput, err := historyCommand.CombinedOutput()
	t.Log(string(historyOutput))
	if err != nil {
		t.Fatalf("buildx history ls after real build: %v", err)
	}
	if strings.TrimSpace(string(historyOutput)) == "" || strings.Contains(string(historyOutput), "dink-history-") {
		t.Fatalf("missing or unadapted history after real build: %s", historyOutput)
	}
	buildRef, ok := outputMetadata["buildx.build.ref"].(string)
	if !ok || buildRef == "" {
		t.Fatalf("missing build reference in metadata: %v", outputMetadata)
	}
	// Metadata qualifies the ref with builder/node; history commands take the build ID.
	buildRef = buildRef[strings.LastIndex(buildRef, "/")+1:]
	for _, subcommand := range []string{"inspect", "logs"} {
		historyCommand := exec.CommandContext(ctx, "docker", "--host", "tcp://"+u.Host, "buildx", "history", subcommand, "--builder", "default", buildRef)
		historyCommand.Env = command.Env
		historyOutput, err := historyCommand.CombinedOutput()
		t.Logf("history %s:\n%s", subcommand, historyOutput)
		if err != nil {
			t.Fatalf("buildx history %s after real build: %v", subcommand, err)
		}
	}
	credential := f.publisher.lastCredential()
	if namespace, _, err := f.credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
		t.Fatalf("build credential was not revoked: %q %v", namespace, err)
	}
	loadCommand := exec.CommandContext(ctx, "docker", "--host", "tcp://"+u.Host, "buildx", "build", "--builder", "default", "--progress=plain", "--load", "-t", "app:loaded", contextDir)
	loadCommand.Env = command.Env
	output, err = loadCommand.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("buildx --load: %v", err)
	}
	loaded, err := f.api.ImageInspect(tenant, "app:loaded", imagebackend.ImageInspectOpts{})
	if err != nil || loaded == nil || loaded.ID != image.ID {
		t.Fatalf("buildx --load did not publish the same image: %v %v", loaded, err)
	}
	localDir := t.TempDir()
	localCommand := exec.CommandContext(ctx, "docker", "--host", "tcp://"+u.Host, "buildx", "build", "--builder", "default", "--progress=plain", "--output", "type=local,dest="+localDir, contextDir)
	localCommand.Env = command.Env
	output, err = localCommand.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatalf("explicit local output: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(localDir, "payload"))
	if err != nil || string(payload) != "built through dink\n" {
		t.Fatalf("local output payload = %q %v", payload, err)
	}
}
