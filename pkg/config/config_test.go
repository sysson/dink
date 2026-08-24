package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func TestValidate_ValidConfigPasses(t *testing.T) {
	cfg := validTLSConfig(t)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_DisableTLSAllowsMissingTLSFiles(t *testing.T) {
	cfg := validTLSConfig(t)
	cfg.DisableTLS = true
	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = ""
	cfg.ClientCAFile = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config with disableTLS=true, got error: %v", err)
	}
}

func TestValidate_InvalidCases(t *testing.T) {
	type testCase struct {
		name       string
		mutate     func(*Config)
		wantSubstr string
	}

	testCases := []testCase{
		{
			name: "empty namespace",
			mutate: func(c *Config) {
				c.Namespace = ""
			},
			wantSubstr: "namespace must not be empty",
		},
		{
			name: "invalid log level",
			mutate: func(c *Config) {
				c.LogLevel = "verbose"
			},
			wantSubstr: "logLevel=\"verbose\" is invalid",
		},
		{
			name: "invalid access log level",
			mutate: func(c *Config) {
				c.AccessLogLevel = "trace"
			},
			wantSubstr: "accessLogLevel=\"trace\" is invalid",
		},
		{
			name: "non numeric plaintext port",
			mutate: func(c *Config) {
				c.Port = "abc"
			},
			wantSubstr: "port=\"abc\" is invalid",
		},
		{
			name: "out of range plaintext port",
			mutate: func(c *Config) {
				c.Port = "70000"
			},
			wantSubstr: "port=\"70000\" is invalid",
		},
		{
			name: "missing tls port when tls enabled",
			mutate: func(c *Config) {
				c.TLSPort = ""
			},
			wantSubstr: "tlsPort must not be empty when TLS is enabled",
		},
		{
			name: "invalid tls port",
			mutate: func(c *Config) {
				c.TLSPort = "0"
			},
			wantSubstr: "tlsPort=\"0\" is invalid",
		},
		{
			name: "missing tls cert file",
			mutate: func(c *Config) {
				c.TLSCertFile = ""
			},
			wantSubstr: "tlsCertFile must not be empty when TLS is enabled",
		},
		{
			name: "tls cert file does not exist",
			mutate: func(c *Config) {
				c.TLSCertFile = filepath.Join(t.TempDir(), "missing.crt")
			},
			wantSubstr: "tlsCertFile=",
		},
		{
			name: "missing tls key file",
			mutate: func(c *Config) {
				c.TLSKeyFile = ""
			},
			wantSubstr: "tlsKeyFile must not be empty when TLS is enabled",
		},
		{
			name: "invalid min tls version",
			mutate: func(c *Config) {
				c.MinTLSVersion = "1.1"
			},
			wantSubstr: "minTLSVersion=\"1.1\" is invalid",
		},
		{
			name: "client ca set while tls disabled",
			mutate: func(c *Config) {
				c.DisableTLS = true
				c.ClientCAFile = "/tmp/ca.pem"
			},
			wantSubstr: "clientCAFile is set but disableTLS=true",
		},
		{
			name: "plaintext and tls ports collide when both listeners enabled",
			mutate: func(c *Config) {
				c.AllowPlaintextWithTLS = true
				c.Port = c.TLSPort
			},
			wantSubstr: "port and tlsPort must be different",
		},
		{
			name: "auth plugin missing name",
			mutate: func(c *Config) {
				c.AuthPlugins = []AuthPlugin{{Name: "", Path: "/plugin"}}
			},
			wantSubstr: "authPlugins[0].name must not be empty",
		},
		{
			name: "auth plugin missing path",
			mutate: func(c *Config) {
				c.AuthPlugins = []AuthPlugin{{Name: "plugin", Path: ""}}
			},
			wantSubstr: "authPlugins[0].path must not be empty",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validTLSConfig(t)
			tc.mutate(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSubstr)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("expected error to contain %q, got %q", tc.wantSubstr, err.Error())
			}
		})
	}
}

func TestSetEffectiveConfig_CommandScopedFlagApplied(t *testing.T) {
	var got *Config

	cmd := &cli.Command{
		Name: "dink",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config"},
			&cli.BoolFlag{Name: "allowPlaintextWithTLS"},
			&cli.BoolFlag{Name: "disableTLS"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			cfg := NewConfig()
			cfg.EnvPrefix = "ZZZ_DINK_TEST_NO_ENV"
			if err := cfg.SetEffectiveConfig(c); err != nil {
				return err
			}
			got = cfg
			return nil
		},
	}

	err := cmd.Run(context.Background(), []string{"dink", "--allowPlaintextWithTLS=true", "--disableTLS=true"})
	if err != nil {
		t.Fatalf("unexpected command error: %v", err)
	}
	if got == nil {
		t.Fatal("expected config from command action, got nil")
	}
	if !got.AllowPlaintextWithTLS {
		t.Fatal("expected allowPlaintextWithTLS=true from command flag")
	}
	if !got.DisableTLS {
		t.Fatal("expected disableTLS=true from command flag")
	}
}

func TestSetEffectiveConfig_Precedence(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "dink.yaml")
	writeFile(t, configPath, "namespace: file-namespace\n")

	t.Run("default is used with no file env or flags", func(t *testing.T) {
		cfg := loadConfigFromCommand(t, "DINKTEST_PRECEDENCE_NONE", "", nil)
		if got, want := cfg.Namespace, "dink"; got != want {
			t.Fatalf("expected namespace %q, got %q", want, got)
		}
	})

	t.Run("config file overrides default", func(t *testing.T) {
		cfg := loadConfigFromCommand(t, "DINKTEST_PRECEDENCE_DEFAULT", configPath, nil)
		if got, want := cfg.Namespace, "file-namespace"; got != want {
			t.Fatalf("expected namespace %q, got %q", want, got)
		}
	})

	t.Run("env overrides config file", func(t *testing.T) {
		env := map[string]string{
			"DINKTEST_PRECEDENCE_ENV_NAMESPACE": "env-namespace",
		}
		cfg := loadConfigFromCommand(t, "DINKTEST_PRECEDENCE_ENV", configPath, env)
		if got, want := cfg.Namespace, "env-namespace"; got != want {
			t.Fatalf("expected namespace %q, got %q", want, got)
		}
	})

	t.Run("flag overrides env and config file", func(t *testing.T) {
		env := map[string]string{
			"DINKTEST_PRECEDENCE_FLAG_NAMESPACE": "env-namespace",
		}
		cfg := loadConfigFromCommand(
			t,
			"DINKTEST_PRECEDENCE_FLAG",
			configPath,
			env,
			"--namespace=flag-namespace",
		)
		if got, want := cfg.Namespace, "flag-namespace"; got != want {
			t.Fatalf("expected namespace %q, got %q", want, got)
		}
	})
}

func loadConfigFromCommand(t *testing.T, envPrefix, configPath string, env map[string]string, args ...string) *Config {
	t.Helper()

	for k, v := range env {
		t.Setenv(k, v)
	}

	var got *Config
	cmd := &cli.Command{
		Name: "dink",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "config"},
			&cli.StringFlag{Name: "namespace"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			cfg := NewConfig()
			cfg.EnvPrefix = envPrefix
			if err := cfg.SetEffectiveConfig(c); err != nil {
				return err
			}
			got = cfg
			return nil
		},
	}

	cmdArgs := []string{"dink"}
	if configPath != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--config=%s", configPath))
	}
	cmdArgs = append(cmdArgs, args...)

	if err := cmd.Run(context.Background(), cmdArgs); err != nil {
		t.Fatalf("unexpected command error: %v", err)
	}
	if got == nil {
		t.Fatal("expected config from command action, got nil")
	}

	return got
}

func validTLSConfig(t *testing.T) *Config {
	t.Helper()
	dir := t.TempDir()

	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "ca.crt")

	writeFile(t, certFile, "dummy cert")
	writeFile(t, keyFile, "dummy key")
	writeFile(t, caFile, "dummy ca")

	cfg := NewConfig()
	cfg.DisableTLS = false
	cfg.Port = "2375"
	cfg.TLSPort = "2376"
	cfg.TLSCertFile = certFile
	cfg.TLSKeyFile = keyFile
	cfg.ClientCAFile = caFile
	cfg.MinTLSVersion = "1.2"
	return cfg
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file %s: %v", path, err)
	}
}
