package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunnerFailureCleanup(t *testing.T) {
	cases := []struct {
		name        string
		startStatus string
		dnsStatus   string
		deleteFails bool
		wantStatus  int
		interruptAt string
	}{
		{"deletes state", "42", "0", false, 42, ""},
		{"retains state on deletion failure", "42", "0", true, 1, ""},
		{"reports scenario failure with upstream disabled", "0", "0", false, 1, ""},
		{"rejects unavailable CoreDNS", "0", "51", false, 51, ""},
		{"interrupt during build", "0", "0", false, 130, "go build"},
		{"interrupt during cluster startup", "0", "0", false, 130, "minikube start"},
		{"interrupt retains failed cluster deletion", "0", "0", true, 1, "minikube start"},
		{"retains state on network deletion failure", "42", "0", false, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			trace := filepath.Join(dir, "trace")
			mock := `#!/usr/bin/env bash
set -eu
tool=$(basename "$0")
printf '%s|%s|%s|%s|%s\n' "$tool $*" "${DOCKER_CONTEXT:-}" "${DOCKER_CONFIG:-}" "${KUBECONFIG:-}" "${MINIKUBE_HOME:-}" >> "$MOCK_TRACE"
if [[ "$tool $1" == "${MOCK_INTERRUPT_AT:-}" ]]; then
  trap 'exit 130' INT TERM
  touch "$MOCK_READY"
  while :; do sleep 1; done
fi
case "$tool $1" in
  "docker --context")
    if [[ "$*" == *"network ls"* ]]; then echo mock-network; fi
    if [[ "$*" == *"network rm"* ]]; then exit "$MOCK_NETWORK_DELETE_STATUS"; fi
    ;;
  "minikube start") exit "$MOCK_START_STATUS" ;;
  "minikube delete") exit "$MOCK_DELETE_STATUS" ;;
  "minikube ssh") echo 'config_path = "/etc/containerd/certs.d"' ;;
  "minikube ip") echo '192.168.49.2' ;;
  "kubectl rollout") exit "$MOCK_DNS_STATUS" ;;
  "go build") printf '#!/usr/bin/env bash\nexit 0\n' > "$3"; chmod +x "$3" ;;
  "go run") printf 'disposable-password' > "$3/registry-password"; touch "$3/htpasswd" ;;
  "go test")
    env | grep -E '^(DINK_INTEGRATION_|SSL_CERT_FILE=)' > "$MOCK_TEST_ENV"
    exit 43 ;;
esac
`
			for _, tool := range []string{"docker", "go", "minikube", "kubectl", "git"} {
				if err := os.WriteFile(filepath.Join(bin, tool), []byte(mock), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "run.sh")
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			command.WaitDelay = time.Second
			for _, variable := range os.Environ() {
				key, _, _ := strings.Cut(variable, "=")
				if strings.HasPrefix(key, "DOCKER_") || strings.HasPrefix(key, "E2E_") ||
					key == "KUBECONFIG" || key == "MINIKUBE_HOME" || key == "TMPDIR" || key == "PATH" {
					continue
				}
				command.Env = append(command.Env, variable)
			}
			deleteStatus := "0"
			if tc.deleteFails {
				deleteStatus = "1"
			}
			networkDeleteStatus := "0"
			if tc.name == "retains state on network deletion failure" {
				networkDeleteStatus = "1"
			}
			command.Env = append(command.Env,
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TMPDIR="+dir, "MOCK_TRACE="+trace, "MOCK_DELETE_STATUS="+deleteStatus,
				"MOCK_START_STATUS="+tc.startStatus, "E2E_UPSTREAM=0",
				"MOCK_DNS_STATUS="+tc.dnsStatus,
				"MOCK_NETWORK_DELETE_STATUS="+networkDeleteStatus,
				"MOCK_INTERRUPT_AT="+tc.interruptAt, "MOCK_READY="+filepath.Join(dir, "ready"),
				"MOCK_TEST_ENV="+filepath.Join(dir, "test-env"),
				"DOCKER_HOST=tcp://wrong.invalid:2376", "DOCKER_CONTEXT=wrong",
				"DOCKER_CONFIG="+filepath.Join(dir, "original-docker"),
				"KUBECONFIG="+filepath.Join(dir, "original-kubeconfig"),
				"MINIKUBE_HOME="+filepath.Join(dir, "original-minikube"),
			)
			var buffer bytes.Buffer
			command.Stdout, command.Stderr = &buffer, &buffer
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			if tc.interruptAt != "" {
				for {
					if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
						break
					} else if !os.IsNotExist(err) {
						t.Fatal(err)
					}
					select {
					case <-ctx.Done():
						_ = command.Wait()
						t.Fatalf("runner never reached interruption point:\n%s", buffer.String())
					case <-time.After(10 * time.Millisecond):
					}
				}
				// Terminal Ctrl+C signals the entire foreground process group,
				// including the runner's logging process.
				if err := syscall.Kill(-command.Process.Pid, syscall.SIGINT); err != nil {
					t.Fatal(err)
				}
			}
			err := command.Wait()
			output := buffer.Bytes()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != tc.wantStatus {
				t.Fatalf("wanted exit %d, got %v\n%s", tc.wantStatus, err, output)
			}
			data, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			var state string
			var deleted bool
			var networkCreated, networkDeleted bool
			for _, line := range lines {
				fields := strings.Split(line, "|")
				if strings.HasPrefix(fields[0], "docker network create ") {
					networkCreated = true
					if !strings.Contains(fields[0], "com.docker.network.driver.mtu=1280") {
						t.Fatalf("test bridge missing safe MTU: %s", line)
					}
				}
				if strings.Contains(fields[0], "network rm ") {
					networkDeleted = true
				}
				if strings.HasPrefix(fields[0], "minikube start ") {
					if fields[1] != "e2e-host" || !strings.HasPrefix(fields[3], dir+"/dink-e2e-state.") {
						t.Fatalf("cluster start did not use isolated configuration: %s", line)
					}
					state = filepath.Dir(fields[3])
					if !strings.Contains(fields[0], "--network=dink-e2e-") {
						t.Fatalf("cluster did not use its dedicated bridge: %s", line)
					}
					if fields[2] != filepath.Join(state, "docker") || fields[4] != filepath.Join(state, "minikube") {
						t.Fatalf("configuration paths escape state directory: %s", line)
					}
				}
				if tc.interruptAt == "go build" && strings.HasPrefix(fields[0], "go build ") {
					state = filepath.Dir(fields[3])
				}
				if strings.HasPrefix(fields[0], "minikube delete ") {
					deleted = true
				}
			}
			if state == "" || deleted != (tc.interruptAt != "go build") {
				t.Fatalf("cluster creation/deletion not attempted:\n%s", data)
			}
			if networkCreated != (tc.interruptAt != "go build") || networkDeleted != (networkCreated && !tc.deleteFails) {
				t.Fatalf("unexpected network lifecycle: created=%t deleted=%t\n%s", networkCreated, networkDeleted, data)
			}
			_, err = os.Stat(state)
			if tc.deleteFails || networkDeleteStatus != "0" {
				if err != nil || !strings.Contains(string(output), "State retained") {
					t.Fatalf("deletion failure must retain state and report recovery: %v\n%s", err, output)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("temporary credentials/state were not removed: %v", err)
			}
			if tc.interruptAt == "" && tc.startStatus == "0" && tc.dnsStatus == "0" && !strings.Contains(string(output), "Local E2E suite failed") {
				t.Fatalf("scenario failure was not reported:\n%s", output)
			}
			if tc.interruptAt == "" && tc.startStatus == "0" && tc.dnsStatus == "0" {
				env, err := os.ReadFile(filepath.Join(dir, "test-env"))
				if err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{
					"DINK_INTEGRATION_PUSH_REPOSITORY=192.168.49.2:32500/buildkit-e2e",
					"DINK_INTEGRATION_BUILDKIT_ADDR=tcp://192.168.49.2:32374",
					"DINK_INTEGRATION_BUILDKIT_CA=" + state + "/buildkit-ca.pem",
					"DINK_INTEGRATION_BUILDKIT_CERT=" + state + "/certs/e2e-a/default/cert.pem",
					"DINK_INTEGRATION_BUILDKIT_KEY=" + state + "/certs/e2e-a/default/key.pem",
					"DINK_INTEGRATION_BUILDKIT_SERVER_NAME=buildkit.dink-system.svc.cluster.local",
					"SSL_CERT_FILE=" + state + "/buildkit-ca.pem",
				} {
					if !strings.Contains(string(env), want+"\n") {
						t.Fatalf("missing automated BuildKit setting %q:\n%s", want, env)
					}
				}
				if !strings.Contains(string(data), "go test ./testing/integration -run ^(TestE2E|TestBuildKit)") ||
					!strings.Contains(string(data), "docker --context e2e-a login 192.168.49.2:32500 --username e2e --password-stdin") {
					t.Fatalf("runner did not select all BuildKit tests or configure registry login:\n%s", data)
				}
			}
			if tc.dnsStatus != "0" && strings.Contains(string(data), "docker build ") {
				t.Fatalf("runner continued after CoreDNS readiness failed:\n%s", data)
			}
		})
	}
}
