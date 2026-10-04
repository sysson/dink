package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerCommandReportsDeadline(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nexec sleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	setup := buildKitSetup{context: "test", env: os.Environ()}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := setup.docker(ctx, "buildx", "build")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "process error:") {
		t.Fatalf("deadline hidden behind process failure: %v", err)
	}
}

func TestDockerCommandPreservesProcessFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nprintf 'pull failed\\n'\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	setup := buildKitSetup{context: "test", env: os.Environ()}
	output, err := setup.docker(t.Context(), "pull", "alpine")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 42 || string(output) != "pull failed\n" {
		t.Fatalf("command error/output changed: %v, %q", err, output)
	}
}

func TestWithoutRegistryCredentialsExportsActualContextArchive(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
test "$1" = --context
test "$2" = test
test "$3" = context
case "$4" in
  export)
    test "$#" = 6
    printf 'test-archive' > "$6"
    printf 'Exported context to file\n'
    ;;
  import)
    test "$(cat "$6")" = test-archive
    test "$DOCKER_CONFIG" != "$ORIGINAL_DOCKER_CONFIG"
    test ! -e "$DOCKER_CONFIG/config.json"
    ;;
  *) exit 42 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", dir)
	t.Setenv("ORIGINAL_DOCKER_CONFIG", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"registry.test":{"auth":"test"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	setup := buildKitSetup{context: "test", env: os.Environ()}
	isolated := setup.withoutRegistryCredentials(t)
	if isolated.context != setup.context {
		t.Fatal("isolated configuration changed context identity")
	}
}
