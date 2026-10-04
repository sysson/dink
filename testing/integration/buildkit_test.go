package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type buildKitSetup struct {
	context string
	env     []string
	id      string
}

func buildKitIntegration(t *testing.T) buildKitSetup {
	t.Helper()
	if os.Getenv("DINK_E2E") != "1" {
		t.Skip("run make test-e2e for disposable-cluster BuildKit tests")
	}
	name := os.Getenv("DINK_INTEGRATION_CONTEXT")
	if name == "" {
		t.Fatal("E2E runner did not configure a Dink Docker context")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatal("Docker CLI is required when DINK_INTEGRATION_CONTEXT is set")
	}
	setup := buildKitSetup{context: name, id: fmt.Sprintf("dink-it-%d", time.Now().UnixNano())}
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if strings.HasPrefix(key, "BUILDX_") || strings.HasPrefix(key, "DOCKER_") && key != "DOCKER_CONFIG" {
			continue
		}
		setup.env = append(setup.env, variable)
	}
	setup.env = append(setup.env, "DOCKER_BUILDKIT=1")
	output := setup.mustDocker(t, "buildx", "inspect", setup.context)
	driver := ""
	for line := range strings.SplitSeq(string(output), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && key == "Driver" {
			driver = strings.TrimSpace(value)
		}
	}
	if driver != "docker" {
		t.Fatalf("integration context must use the Docker driver:\n%s", output)
	}
	return setup
}

func (s buildKitSetup) docker(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "docker", append([]string{"--context", s.context}, args...)...)
	command.Env = s.env
	output, err := command.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return output, fmt.Errorf("Docker command interrupted: %w (process error: %v)", ctx.Err(), err)
	}
	return output, err
}

func (s buildKitSetup) mustDocker(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	output, err := s.docker(ctx, args...)
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, output)
	}
	return output
}

func (s buildKitSetup) cleanImages(t *testing.T, names ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := s.docker(ctx, append([]string{"image", "rm", "--force"}, names...)...); err != nil {
			t.Errorf("cleaning integration images: %v\n%s", err, output)
		}
	})
}

func (s buildKitSetup) withoutRegistryCredentials(t *testing.T) buildKitSetup {
	t.Helper()
	dir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "context.tar")
	s.mustDocker(t, "context", "export", s.context, archive)
	isolated := s
	isolated.env = nil
	for _, variable := range s.env {
		if !strings.HasPrefix(variable, "DOCKER_CONFIG=") {
			isolated.env = append(isolated.env, variable)
		}
	}
	isolated.env = append(isolated.env, "DOCKER_CONFIG="+dir)
	isolated.mustDocker(t, "context", "import", s.context, archive)
	return isolated
}

func buildContext(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"Dockerfile": "FROM scratch\nCOPY payload /payload\nLABEL dink.integration-test=true\n",
		"payload":    payload,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type integrationImage struct {
	ID     string
	Config struct{ Labels map[string]string }
	RootFS struct{ Layers []string }
}

func (s buildKitSetup) inspectImage(t *testing.T, name string) integrationImage {
	t.Helper()
	var images []integrationImage
	if err := json.Unmarshal(s.mustDocker(t, "image", "inspect", name), &images); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].ID == "" || len(images[0].RootFS.Layers) != 1 ||
		images[0].Config.Labels["dink.integration-test"] != "true" {
		t.Fatalf("missing image or incorrect config/layers for %s: %+v", name, images)
	}
	return images[0]
}

type buildMetadata struct {
	Name       string          `json:"image.name"`
	Digest     string          `json:"containerimage.digest"`
	Ref        string          `json:"buildx.build.ref"`
	Provenance json.RawMessage `json:"buildx.build.provenance"`
}

func checkBuildMetadata(t *testing.T, iidFile, metadataFile, names, imageID string) buildMetadata {
	t.Helper()
	iid, err := os.ReadFile(iidFile)
	if err != nil || strings.TrimSpace(string(iid)) != imageID {
		t.Fatalf("iidfile = %q %v, image ID = %s", iid, err, imageID)
	}
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var result buildMetadata
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Name != names || result.Digest != imageID || result.Ref == "" || strings.Contains(result.Ref, "dink-history-") ||
		len(result.Provenance) == 0 || string(result.Provenance) == "null" {
		t.Fatalf("incorrect names, digest, reference, or provenance: %s", data)
	}
	return result
}

func (s buildKitSetup) cleanHistory(t *testing.T, result buildMetadata) {
	t.Helper()
	ref := result.Ref[strings.LastIndex(result.Ref, "/")+1:]
	if ref == "" || strings.Contains(ref, "dink-history-") {
		t.Fatalf("unsafe or missing integration build reference: %q", ref)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if output, err := s.docker(ctx, "buildx", "history", "rm", "--builder", s.context, ref); err != nil {
			t.Errorf("cleaning integration history: %v\n%s", err, output)
		}
	})
}

func readBuildMetadata(t *testing.T, file string) buildMetadata {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var result buildMetadata
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func (s buildKitSetup) checkHistory(t *testing.T, result buildMetadata) {
	t.Helper()
	ref := result.Ref[strings.LastIndex(result.Ref, "/")+1:]
	output := s.mustDocker(t, "buildx", "history", "ls", "--builder", s.context, "--format", "json")
	if strings.Contains(string(output), "dink-history-") {
		t.Fatalf("internal history references leaked: %s", output)
	}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	found := false
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid history JSON: %v\n%s", err, output)
		}
		for key, value := range record {
			if strings.EqualFold(key, "ref") && (value == ref || value == result.Ref) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("built reference %s missing from history: %s", ref, output)
	}
	s.mustDocker(t, "buildx", "history", "inspect", "--builder", s.context, ref)
	logs := s.mustDocker(t, "buildx", "history", "logs", "--builder", s.context, ref)
	if !strings.Contains(string(logs), "COPY payload /payload") {
		t.Fatalf("history did not retain build logs: %s", logs)
	}
}

func TestBuildKitBuildLoadLocalOutputAndHistory(t *testing.T) {
	setup := buildKitIntegration(t)
	dir := buildContext(t, "built through real Dink\n")
	tag, loadedTag := setup.id+":build", setup.id+":loaded"
	iid, metadata := filepath.Join(t.TempDir(), "iid"), filepath.Join(t.TempDir(), "metadata.json")
	setup.mustDocker(t, "build", "--builder", setup.context, "--progress=plain", "--iidfile", iid, "--metadata-file", metadata, "-t", tag, dir)
	setup.cleanImages(t, tag)
	setup.cleanHistory(t, readBuildMetadata(t, metadata))
	image := setup.inspectImage(t, tag)
	result := checkBuildMetadata(t, iid, metadata, tag, image.ID)
	setup.checkHistory(t, result)
	loadMetadata := filepath.Join(t.TempDir(), "metadata.json")
	setup.mustDocker(t, "buildx", "build", "--builder", setup.context, "--load", "--metadata-file", loadMetadata, "-t", loadedTag, dir)
	setup.cleanImages(t, loadedTag)
	setup.cleanHistory(t, readBuildMetadata(t, loadMetadata))
	if loaded := setup.inspectImage(t, loadedTag); loaded.ID != image.ID {
		t.Fatalf("--load image ID = %s, normal build = %s", loaded.ID, image.ID)
	}
	localDir := t.TempDir()
	localMetadata := filepath.Join(t.TempDir(), "metadata.json")
	setup.mustDocker(t, "buildx", "build", "--builder", setup.context, "--metadata-file", localMetadata, "--output", "type=local,dest="+localDir, dir)
	setup.cleanHistory(t, readBuildMetadata(t, localMetadata))
	payload, err := os.ReadFile(filepath.Join(localDir, "payload"))
	if err != nil || string(payload) != "built through real Dink\n" {
		t.Fatalf("local output = %q %v", payload, err)
	}
	if other := os.Getenv("DINK_INTEGRATION_OTHER_CONTEXT"); other != "" {
		otherSetup := setup
		otherSetup.context = other
		otherSetup.mustDocker(t, "version")
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		if output, err := otherSetup.docker(ctx, "image", "inspect", tag); err == nil {
			t.Fatalf("other tenant could inspect built image: %s", output)
		}
		history := otherSetup.mustDocker(t, "buildx", "history", "ls", "--builder", otherSetup.context, "--format", "json")
		if strings.Contains(string(history), result.Ref[strings.LastIndex(result.Ref, "/")+1:]) {
			t.Fatalf("other tenant could list build history: %s", history)
		}
	}
}

func TestBuildKitPushRetainsDinkiCopy(t *testing.T) {
	setup := buildKitIntegration(t)
	repository := os.Getenv("DINK_INTEGRATION_PUSH_REPOSITORY")
	if repository == "" {
		t.Fatal("E2E runner did not configure the disposable push repository")
	}
	dir := buildContext(t, "pushed through real Dink\n")
	tags := []string{repository + ":" + setup.id, repository + ":" + setup.id + "-second"}
	iid, metadata := filepath.Join(t.TempDir(), "iid"), filepath.Join(t.TempDir(), "metadata.json")
	args := []string{"buildx", "build", "--builder", setup.context, "--push", "--progress=plain", "--iidfile", iid, "--metadata-file", metadata}
	for _, tag := range tags {
		args = append(args, "-t", tag)
	}
	setup.mustDocker(t, append(args, dir)...)
	setup.cleanImages(t, tags...)
	setup.cleanHistory(t, readBuildMetadata(t, metadata))
	var imageID string
	for _, tag := range tags {
		image := setup.inspectImage(t, tag)
		var upstream struct {
			Descriptor struct {
				Digest string `json:"digest"`
			}
		}
		output := setup.mustDocker(t, "manifest", "inspect", "--verbose", tag)
		if err := json.Unmarshal(output, &upstream); err != nil {
			t.Fatal(err)
		}
		if upstream.Descriptor.Digest != image.ID {
			t.Fatalf("Dinki ID %s differs from upstream digest %s", image.ID, upstream.Descriptor.Digest)
		}
		if imageID != "" && imageID != image.ID {
			t.Fatal("push tags have different image IDs")
		}
		imageID = image.ID
	}
	result := checkBuildMetadata(t, iid, metadata, strings.Join(tags, ","), imageID)
	setup.checkHistory(t, result)
	t.Run("missing upstream credentials fails", func(t *testing.T) {
		anonymous := setup.withoutRegistryCredentials(t)
		failedIID := filepath.Join(t.TempDir(), "iid")
		failedTag := repository + ":" + setup.id + "-denied"
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		output, err := anonymous.docker(ctx, "buildx", "build", "--builder", anonymous.context, "--push",
			"--iidfile", failedIID, "-t", failedTag, dir)
		if err == nil {
			setup.cleanImages(t, failedTag)
			t.Fatal("unauthenticated upstream push succeeded; use a repository requiring authentication")
		}
		message := strings.ToLower(string(output))
		if !strings.Contains(message, "unauthorized") && !strings.Contains(message, "denied") &&
			!strings.Contains(message, "authentication") && !strings.Contains(message, "insufficient_scope") {
			t.Fatalf("push failed for a reason other than upstream authentication: %v\n%s", err, output)
		}
		if _, err := os.Stat(failedIID); !os.IsNotExist(err) {
			t.Fatalf("failed push created a success-shaped iidfile: %v", err)
		}
	})
}
