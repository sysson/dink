package command

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sysson/dink/cmd/dinkle/namespaces"
	"github.com/sysson/dink/cmd/dinkle/store"
	"github.com/sysson/syskit/pki"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestInstallationVersion(t *testing.T) {
	for _, tc := range []struct {
		requested, built, want string
	}{
		{"", "v1.2.3", "v1.2.3"},
		{"v0.2.1-beta.1", "Dev", "v0.2.1-beta.1"},
		{"v1.0.0", "v0.1.0", "v1.0.0"},
		{"", "Dev", ""},
		{"main", "v1.0.0", ""},
		{"v1.2.3/../../main", "Dev", ""},
		{"v01.2.3", "Dev", ""},
	} {
		got, err := installationVersion(tc.requested, tc.built)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("installationVersion(%q, %q) = %q, %v", tc.requested, tc.built, got, err)
		}
	}
}

func TestFetchInstallation(t *testing.T) {
	manifest := []byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: dink-system\n")
	digest := sha256.Sum256(manifest)
	for _, tc := range []struct {
		name, checksum string
		status         int
		wantError      bool
	}{
		{"valid", fmt.Sprintf("%x  install.yaml\n", digest), 200, false},
		{"mismatch", strings.Repeat("0", 64) + "  install.yaml\n", 200, true},
		{"wrong filename", fmt.Sprintf("%x  other.yaml\n", digest), 200, true},
		{"invalid digest", "abc  install.yaml\n", 200, true},
		{"missing asset", "", 404, true},
		{"oversized checksum", strings.Repeat("a", 1025), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				if strings.HasSuffix(r.URL.Path, ".sha256") {
					_, _ = io.WriteString(w, tc.checksum)
				} else {
					_, _ = w.Write(manifest)
				}
			}))
			defer server.Close()
			got, err := fetchInstallation(context.Background(), server.Client(), server.URL)
			if (err != nil) != tc.wantError {
				t.Fatalf("fetchInstallation error = %v", err)
			}
			if err == nil && !bytes.Equal(got, manifest) {
				t.Fatalf("manifest = %q", got)
			}
		})
	}
}

func TestApplyInstallation(t *testing.T) {
	for failAt := -1; failAt < 5; failAt++ {
		var calls [][]string
		out := &bytes.Buffer{}
		expected := errors.New("kubectl failed")
		run := func(input io.Reader, args ...string) error {
			calls = append(calls, args)
			if len(calls) == 1 {
				got, err := io.ReadAll(input)
				if err != nil || string(got) != "manifest" {
					t.Fatalf("apply input = %q, %v", got, err)
				}
			}
			if len(calls)-1 == failAt {
				return expected
			}
			return nil
		}
		err := applyInstallation(run, []byte("manifest"), "v1.0.0", out)
		if failAt >= 0 {
			if !errors.Is(err, expected) || len(calls) != failAt+1 || out.Len() != 0 {
				t.Fatalf("failure %d: err %v, calls %v, output %q", failAt, err, calls, out)
			}
		} else if err != nil || len(calls) != 5 || !strings.Contains(out.String(), "v1.0.0 is ready") {
			t.Fatalf("successful apply: err %v, calls %v, output %q", err, calls, out)
		}
		if !slices.Equal(calls[0], []string{"apply", "-f", "-"}) {
			t.Fatalf("first command = %v", calls[0])
		}
	}
}

func installationTestOptions(t *testing.T, client kubernetes.Interface) *Options {
	t.Helper()
	return &Options{
		certsDir:      t.TempDir(),
		newKubeClient: func(context.Context, string) (kubernetes.Interface, error) { return client, nil },
	}
}

func TestPrepareInstallationPreservesCertificates(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	o := installationTestOptions(t, client)
	if err := prepareInstallation(ctx, o, false, []string{"docker.example.com"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before, err := client.CoreV1().Secrets(defaultSystemNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(before.Items) != 4 {
		t.Fatalf("secrets = %v, %v", before, err)
	}
	for _, upgrade := range []bool{false, true} {
		secondHost := installationTestOptions(t, client)
		client.ClearActions()
		if err := prepareInstallation(ctx, secondHost, upgrade, nil, io.Discard); err != nil {
			t.Fatal(err)
		}
		if upgrade {
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("upgrade made a mutating action: %v", action)
				}
			}
		}
		after, err := client.CoreV1().Secrets(defaultSystemNamespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range before.Items {
			for _, actual := range after.Items {
				if actual.Name == secret.Name {
					for key, value := range secret.Data {
						if !bytes.Equal(value, actual.Data[key]) {
							t.Fatalf("%s %s changed on upgrade=%t", secret.Name, key, upgrade)
						}
					}
				}
			}
		}
	}
	if err := prepareInstallation(ctx, o, false, []string{"new.example.com"}, io.Discard); err == nil {
		t.Fatal("new DNS SAN must not be silently ignored")
	}
}

func TestPrepareInstallationRejectsConflictingCA(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	o := installationTestOptions(t, client)
	if err := prepareInstallation(ctx, o, false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	other := installationTestOptions(t, client)
	ca, err := pki.NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewFileStore(other.certsDir).SaveCA(ctx, ca.KeyPair()); err != nil {
		t.Fatal(err)
	}
	if err := prepareInstallation(ctx, other, false, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "CAs differ") {
		t.Fatalf("expected conflict, got %v", err)
	}
	clusterCA, err := namespaces.NewSecretStore(client, defaultSystemNamespace, defaultCASecretName).LoadCA(ctx)
	if err != nil || bytes.Equal(clusterCA.Cert, ca.KeyPair().Cert) {
		t.Fatalf("cluster CA overwritten: %v", err)
	}
}

func TestUpgradeDoesNotBootstrapMissingSecrets(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	o := installationTestOptions(t, client)
	if err := prepareInstallation(ctx, o, true, nil, io.Discard); err == nil {
		t.Fatal("upgrade must reject missing CA")
	}
	if err := prepareInstallation(ctx, o, false, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := client.CoreV1().Secrets(defaultSystemNamespace).Delete(ctx, "dinki-tls", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := prepareInstallation(ctx, o, true, nil, io.Discard); err == nil {
		t.Fatal("upgrade must reject missing certificate")
	}
	secrets, err := client.CoreV1().Secrets(defaultSystemNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(secrets.Items) != 3 {
		t.Fatalf("upgrade recreated secret: %v, %v", secrets, err)
	}
}

func TestInstallationCLIValidation(t *testing.T) {
	for _, args := range [][]string{
		{"dinkle", "install"},
		{"dinkle", "install", "--yes", "--version", "main"},
		{"dinkle", "install", "--yes", "--version", "v1.0.0", "--timeout", "0s"},
		{"dinkle", "upgrade", "--yes", "--version", "v1.0.0", "--dnsName", "example.com"},
	} {
		cmd, err := New(io.Discard, io.Discard)
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Run(context.Background(), args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestCLIVersionReportsBuildMetadata(t *testing.T) {
	out := &bytes.Buffer{}
	cmd, err := New(out, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(context.Background(), []string{"dinkle", "--version"}); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"dinkle version", "commit ", "built ", "dirty "} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("version output %q does not contain %q", out, part)
		}
	}

}
