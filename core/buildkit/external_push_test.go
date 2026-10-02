package buildkit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
)

func upstreamTestTLS(t *testing.T) *tls.Config {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated push-test registry"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	if host := os.Getenv("DINK_BUILDKIT_TEST_REGISTRY_HOST"); host != "" {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	caDir := os.Getenv("DINK_BUILDKIT_TEST_CA_DIR")
	if caDir == "" {
		caDir = t.TempDir()
	}
	caFile := filepath.Join(caDir, "upstream-ca.pem")
	caHandle, err := os.OpenFile(caFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(caFile); err != nil {
			t.Errorf("removing upstream test CA: %v", err)
		}
	})
	_, writeErr := caHandle.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	closeErr := caHandle.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write upstream CA: %v; close: %v", writeErr, closeErr)
	}
	// Dinki's HTTP fallback verifies TLS using the process's system-root override.
	t.Setenv("SSL_CERT_FILE", caFile)
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func TestExternalBuildKitDockerPush(t *testing.T) {
	address := os.Getenv("DINK_BUILDKIT_TEST_ADDR")
	if address == "" {
		t.Skip("set DINK_BUILDKIT_TEST_ADDR to test real builds and upstream pushes")
	}
	f := newFixture(t, address)
	// A separate registry requires credentials unrelated to Dinki's build credential.
	upstream := newFixtureTLS(t, address, upstreamTestTLS(t))
	upstreamURL, err := url.Parse(upstream.registryURL)
	if err != nil {
		t.Fatal(err)
	}
	if host := os.Getenv("DINK_BUILDKIT_TEST_REGISTRY_HOST"); host != "" {
		upstreamURL.Host = net.JoinHostPort(host, upstreamURL.Port())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	username, password, err := upstream.credentials.IssueBuild(ctx, "upstream", []string{"upstream/app", "upstream/second"})
	if err != nil {
		t.Fatal(err)
	}
	dockerConfig := t.TempDir()
	configPath := filepath.Join(dockerConfig, "config.json")
	writeAuth := func(password string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{"auths": map[string]any{
			upstreamURL.Host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + password))},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeAuth(password)
	contextDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM scratch\nCOPY payload /payload\nLABEL dink.push-test=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "payload"), []byte("built and pushed through dink\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gatewayURL, err := url.Parse(gatewayHTTP(t, f))
	if err != nil {
		t.Fatal(err)
	}
	tags := []string{upstreamURL.Host + "/upstream/app:test", upstreamURL.Host + "/upstream/app:latest", upstreamURL.Host + "/upstream/second:v1"}
	iidFile, metadataFile := filepath.Join(t.TempDir(), "iid"), filepath.Join(t.TempDir(), "metadata.json")
	runPush := func(tags []string, iidFile, metadataFile string) ([]byte, error) {
		args := []string{"--host", "tcp://" + gatewayURL.Host, "buildx", "build", "--builder", "default",
			"--push", "--progress=plain", "--iidfile", iidFile, "--metadata-file", metadataFile}
		if os.Getenv("DINK_BUILDKIT_TEST_CA_DIR") == "" {
			args = append(args, "--output", "type=image,registry.insecure=true")
		}
		for _, tag := range tags {
			args = append(args, "-t", tag)
		}
		args = append(args, contextDir)
		command := exec.CommandContext(ctx, "docker", args...)
		for _, variable := range os.Environ() {
			name, _, _ := strings.Cut(variable, "=")
			if !strings.HasPrefix(name, "DOCKER_") && !strings.HasPrefix(name, "BUILDX_") {
				command.Env = append(command.Env, variable)
			}
		}
		command.Env = append(command.Env, "DOCKER_CONFIG="+dockerConfig)
		return command.CombinedOutput()
	}
	output, err := runPush(tags, iidFile, metadataFile)
	t.Log(string(output))
	if err != nil {
		t.Fatalf("authenticated buildx --push: %v", err)
	}
	tenant := identity.NewContext(ctx, identity.Identity{Namespace: "tenant"})
	upstreamTenant := identity.NewContext(ctx, identity.Identity{Namespace: "upstream"})
	var imageID string
	for _, tag := range tags {
		internal, err := f.api.ImageInspect(tenant, tag, imagebackend.ImageInspectOpts{})
		if err != nil || internal == nil {
			t.Fatalf("Dinki copy missing for %s: %v", tag, err)
		}
		upstreamName := strings.TrimPrefix(tag, upstreamURL.Host+"/upstream/")
		published, err := upstream.api.ImageInspect(upstreamTenant, upstreamName, imagebackend.ImageInspectOpts{})
		if err != nil || published == nil {
			t.Fatalf("upstream copy missing for %s: %v", tag, err)
		}
		if internal.ID != published.ID || len(internal.RootFS.Layers) != 1 || internal.Config.Labels["dink.push-test"] != "true" {
			t.Fatalf("upstream and Dinki outputs differ: %v %v", internal, published)
		}
		imageID = internal.ID
	}
	iid, err := os.ReadFile(iidFile)
	if err != nil || strings.TrimSpace(string(iid)) != imageID {
		t.Fatalf("push iidfile = %q %v; Dinki image ID = %s", iid, err, imageID)
	}
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result["image.name"] != strings.Join(tags, ",") || result["containerimage.digest"] != imageID {
		t.Fatalf("unexpected push metadata: %v", result)
	}
	buildRef, ok := result["buildx.build.ref"].(string)
	if !ok || buildRef == "" {
		t.Fatalf("missing pushed build reference: %v", result)
	}
	buildRef = buildRef[strings.LastIndex(buildRef, "/")+1:]
	bk, err := client.New(ctx, "tcp://"+gatewayURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bk.Close() }()
	history, err := bk.ControlClient().ListenBuildHistory(ctx, &control.BuildHistoryRequest{Ref: buildRef, EarlyExit: true})
	if err != nil {
		t.Fatal(err)
	}
	event, err := history.Recv()
	if err != nil || event.Record == nil || len(event.Record.Exporters) != 2 {
		t.Fatalf("missing dual-push history: %v %v", event, err)
	}
	for _, exporter := range event.Record.Exporters {
		if exporter.Attrs["name"] != strings.Join(tags, ",") {
			t.Fatalf("internal names leaked into push history: %v", event.Record)
		}
	}
	if event.Record.ExporterResponse["image.name"] != strings.Join(tags, ",") || event.Record.Result == nil ||
		event.Record.Result.Results[0] == nil || event.Record.Result.Results[0].Digest != imageID ||
		event.Record.Result.Results[1] == nil || event.Record.Result.Results[1].Digest != imageID {
		t.Fatalf("incorrect push history metadata: %v", event.Record)
	}
	if err := history.CloseSend(); err != nil {
		t.Fatal(err)
	}
	credential := f.publisher.lastCredential()
	if namespace, _, err := f.credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
		t.Fatalf("push retained internal credential: %q %v", namespace, err)
	}
	writeAuth("incorrect-password")
	failedIID, failedMetadata := filepath.Join(t.TempDir(), "iid"), filepath.Join(t.TempDir(), "metadata.json")
	failedTag := upstreamURL.Host + "/upstream/app:denied"
	output, err = runPush([]string{failedTag}, failedIID, failedMetadata)
	t.Log(string(output))
	if err == nil {
		t.Fatal("upstream authentication failure reported build success")
	}
	if _, err := upstream.api.ImageInspect(upstreamTenant, "app:denied", imagebackend.ImageInspectOpts{}); err == nil {
		t.Fatal("denied upstream push published its tag")
	}
	if _, err := os.Stat(failedIID); !os.IsNotExist(err) {
		t.Fatalf("failed push created a success-shaped iidfile: %v", err)
	}
	credential = f.publisher.lastCredential()
	if namespace, _, err := f.credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
		t.Fatalf("failed push retained internal credential: %q %v", namespace, err)
	}
}
