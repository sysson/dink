package command

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sysson/dink/dink/config"
	"github.com/urfave/cli/v3"
)

func TestLoadCLIConfigPrecedence(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.json")
	fileConfig := map[string]any{
		"kubernetes": map[string]any{"namespace": "from-file"},
		"server":     map[string]any{"port": "2375", "disableTLS": true},
		"log":        map[string]any{"level": "warn"},
		"accessLog":  map[string]any{"level": "error"},
	}
	data, err := json.Marshal(fileConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DINK_K8S_NAMESPACE", "from-environment")
	t.Setenv("DINK_SERVER_PORT", "2380")
	t.Setenv("DINK_TLS_CLIENTCAFILE", "")

	opts := newOptions(config.Default(), new(config.Config))

	var flags []cli.Flag
	opts.addFlags(&flags)
	opts.flags = &flags
	for _, flag := range flags {
		if err := flag.PreParse(); err != nil {
			t.Fatal(err)
		}
	}
	for _, flag := range flags {
		if err := flag.PostParse(); err != nil {
			t.Fatal(err)
		}
	}
	opts.configFile = configFile
	for _, flag := range flags {
		if flag.Names()[0] == "port" {
			if err := flag.Set(flag.Names()[0], map[string]string{
				"port": "2390",
			}[flag.Names()[0]]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := loadCLIConfig(opts); err != nil {
		t.Fatal(err)
	}

	if got, want := opts.cfg.Kubernetes.Namespace, "from-environment"; got != want {
		t.Fatalf("namespace = %q, want %q", got, want)
	}
	if got, want := opts.cfg.Server.Port, "2390"; got != want {
		t.Fatalf("port = %q, want %q", got, want)
	}
	if got, want := opts.cfg.Log.Level, "warn"; got != want {
		t.Fatalf("log level = %q, want %q", got, want)
	}
	if opts.cfg.Server.DisableTLS == nil || !*opts.cfg.Server.DisableTLS {
		t.Fatal("disableTLS was not preserved from the file")
	}
	if opts.cfg.Server.TLSPort != "2376" {
		t.Fatalf("tls port = %q, want the default because it was omitted from the file", opts.cfg.Server.TLSPort)
	}
}

func TestMergeConfigExplicitFalseBool(t *testing.T) {
	trueValue := true
	falseValue := false
	opts := newOptions(&config.Config{Server: config.Server{DisableTLS: &falseValue}}, new(config.Config))
	flag := &cli.BoolFlag{
		Name:   "disableTLS",
		Action: setPtr(&opts.cfg.Server.DisableTLS),
	}
	if err := flag.Set("disableTLS", "false"); err != nil {
		t.Fatal(err)
	}
	if err := flag.RunAction(t.Context(), &cli.Command{}); err != nil {
		t.Fatal(err)
	}
	opts.flags = &[]cli.Flag{flag}

	cfg := &config.Config{Server: config.Server{DisableTLS: &trueValue}}
	if err := mergeConfig(opts.cfg, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.DisableTLS == nil || *cfg.Server.DisableTLS {
		t.Fatal("disableTLS = true, want explicit false override")
	}
}

func TestRunnerCmdIncludesConfigFlags(t *testing.T) {
	cmd := runnerCmd(os.Stdout, os.Stderr, new(slog.LevelVar))

	for _, name := range []string{"tlsCertFile", "tlsKeyFile", "tlsPort"} {
		found := false
		for _, flag := range cmd.Flags {
			for _, flagName := range flag.Names() {
				if flagName == name {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("command does not contain %q flag", name)
		}
	}
}
