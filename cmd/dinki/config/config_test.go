package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMergesFileWithDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
		"server": {"port": "5002"},
		"tls": {"disabled": true},
		"api": {"disabled": true},
		"storage": {"mem": {}},
		"metadata": {"etcd": {"endpoints": ["etcd-0:2379"], "prefix": "/dinki"}}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Port != "5002" {
		t.Fatalf("server port = %q, want 5002", cfg.Server.Port)
	}
	if cfg.Server.HealthPort != "8080" {
		t.Fatalf("server health port = %q, want inherited default 8080", cfg.Server.HealthPort)
	}
	if cfg.Log.Level != "info" {
		t.Fatalf("log level = %q, want inherited default info", cfg.Log.Level)
	}
	if !cfg.AccessLog.Enabled || cfg.AccessLog.Level != "error" {
		t.Fatalf("access log = %+v, want inherited default enabled at error level", cfg.AccessLog)
	}
	if cfg.Storage.File != nil || cfg.Storage.Mem == nil {
		t.Fatalf("storage = %+v, want only mem replacing the default file driver", cfg.Storage)
	}
	if cfg.Metadata.BBolt != nil || cfg.Metadata.Etcd == nil || cfg.Metadata.Etcd.Endpoints[0] != "etcd-0:2379" {
		t.Fatalf("metadata = %+v, want only etcd replacing the default bbolt driver", cfg.Metadata)
	}
}

func TestLoadRejectsMissingExplicitFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("Load() error = nil, want missing-file error")
	}
}

func TestDefaultUsesPersistentBackends(t *testing.T) {
	cfg := Default()
	if cfg.Storage.File == nil || cfg.Storage.File.Path != "/var/lib/dinki/blobs" {
		t.Fatalf("storage = %+v, want file-backed default", cfg.Storage)
	}
	if cfg.Metadata.BBolt == nil || cfg.Metadata.BBolt.Path != "/var/lib/dinki/metadata.db" {
		t.Fatalf("metadata = %+v, want bbolt-backed default", cfg.Metadata)
	}
	if !cfg.AccessLog.Enabled {
		t.Fatal("access logging is disabled by default")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestValidateAPI(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*Config)
		wantErr bool
	}{
		"default":          {mutate: func(*Config) {}},
		"disabled ignores": {mutate: func(c *Config) { c.API.Disabled = true; c.API.ClientCAFile = "" }},
		"missing CA":       {mutate: func(c *Config) { c.API.ClientCAFile = "" }, wantErr: true},
		"missing subject":  {mutate: func(c *Config) { c.API.ClientOrganization = ""; c.API.ClientCommonName = "" }, wantErr: true},
		"plaintext api": {mutate: func(c *Config) {
			c.TLS.Disabled = true
			c.API.ClientCAFile = ""
			c.API.ClientOrganization = ""
			c.API.ClientCommonName = ""
		}, wantErr: true},
		"duplicate server ports": {mutate: func(c *Config) {
			c.Server.HealthPort = c.Server.Port
		}, wantErr: true},
		"invalid health port": {mutate: func(c *Config) {
			c.Server.HealthPort = "invalid"
		}, wantErr: true},
		"graphql without api": {mutate: func(c *Config) {
			c.API.Disabled = true
			c.GraphQL.Enabled = true
		}, wantErr: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			test.mutate(&cfg)
			if err := cfg.Validate(); (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestLoadRejectsInvalidDrivers(t *testing.T) {
	for name, body := range map[string]string{
		"unknown driver":   `{"storage": {"s4": {}}}`,
		"unknown field":    `{"metadata": {"bbolt": {"path": "/db", "mode": 1}}}`,
		"two drivers":      `{"metadata": {"mem": {}, "bbolt": {"path": "/db"}}}`,
		"no driver":        `{"storage": {}}`,
		"invalid settings": `{"metadata": {"nats": {"servers": []}}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"tls": {"disabled": true}, `+body[1:]), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: Load() succeeded, want error", name)
		}
	}
}

func TestLoadDeployConfig(t *testing.T) {
	cfg, err := Load("../../../deploy/dinki-config.json")
	if err != nil {
		t.Fatalf("Load(deploy config) error = %v", err)
	}
	if cfg.Storage.File == nil || cfg.Metadata.BBolt == nil {
		t.Fatalf("deploy config drivers = %+v / %+v, want file and bbolt", cfg.Storage, cfg.Metadata)
	}
}
