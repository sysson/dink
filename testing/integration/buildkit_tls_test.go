package integration

import (
	"context"
	"os"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry/api"
)

func TestBuildKitBackendMutualTLS(t *testing.T) {
	if os.Getenv("DINK_E2E") != "1" {
		t.Skip("run make test-e2e for disposable-cluster BuildKit TLS tests")
	}
	address := os.Getenv("DINK_INTEGRATION_BUILDKIT_ADDR")
	if address == "" {
		t.Fatal("E2E runner did not configure the BuildKit TLS endpoint")
	}
	cfg := config.BuildKit{
		URL:        address,
		CAFile:     os.Getenv("DINK_INTEGRATION_BUILDKIT_CA"),
		CertFile:   os.Getenv("DINK_INTEGRATION_BUILDKIT_CERT"),
		KeyFile:    os.Getenv("DINK_INTEGRATION_BUILDKIT_KEY"),
		ServerName: os.Getenv("DINK_INTEGRATION_BUILDKIT_SERVER_NAME"),
	}
	if cfg.CAFile == "" || cfg.CertFile == "" || cfg.KeyFile == "" || cfg.ServerName == "" {
		t.Fatal("all DINK_INTEGRATION_BUILDKIT_{CA,CERT,KEY,SERVER_NAME} settings are required")
	}
	call := func(t *testing.T, cfg config.BuildKit) error {
		t.Helper()
		g, err := buildkit.New(cfg, "https://unused.test", api.Unavailable(context.Canceled))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = g.Close() }()
		ctx, cancel := context.WithTimeout(identity.NewContext(t.Context(), identity.Identity{Namespace: "integration"}), 5*time.Second)
		defer cancel()
		_, err = g.Info(ctx, &control.InfoRequest{})
		return err
	}
	if err := call(t, cfg); err != nil {
		t.Fatalf("real backend mTLS connection: %v", err)
	}
	t.Run("wrong server identity", func(t *testing.T) {
		wrong := cfg
		wrong.ServerName = "wrong.integration.invalid"
		if err := call(t, wrong); err == nil {
			t.Fatal("backend accepted the wrong server identity")
		}
	})
	t.Run("missing client certificate", func(t *testing.T) {
		anonymous := cfg
		anonymous.CertFile, anonymous.KeyFile = "", ""
		if err := call(t, anonymous); err == nil {
			t.Fatal("backend accepted a client without a certificate")
		}
	})
}
