package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sysson/dink/dink/pkg/config"
	"github.com/urfave/cli/v3"
)

func TestLoadCLIConfigPrecedence(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "config.json")
	fileConfig := map[string]any{
		"namespace":      "from-file",
		"port":           "2375",
		"logLevel":       "warn",
		"accessLogLevel": "error",
		"disableTLS":     true,
	}
	data, err := json.Marshal(fileConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DINK_NAMESPACE", "from-environment")
	t.Setenv("DINK_PORT", "2380")
	t.Setenv("DINK_CLIENTCAFILE", "")

	opts := newOptions(config.New())

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

	if got, want := opts.cfg.Namespace, "from-environment"; got != want {
		t.Fatalf("namespace = %q, want %q", got, want)
	}
	if got, want := opts.cfg.Port, "2390"; got != want {
		t.Fatalf("port = %q, want %q", got, want)
	}
	if got, want := opts.cfg.LogLevel, "warn"; got != want {
		t.Fatalf("log level = %q, want %q", got, want)
	}
	if opts.cfg.DisableTLS == nil || !*opts.cfg.DisableTLS {
		t.Fatal("disableTLS was not preserved from the file")
	}
	if opts.cfg.TLSPort != "" {
		t.Fatalf("tls port = %q, want empty because it was omitted from the file", opts.cfg.TLSPort)
	}
}

func TestMergeConfigExplicitFalseBool(t *testing.T) {
	trueValue := true
	falseValue := false
	opts := newOptions(&config.Config{DisableTLS: &falseValue})
	flag := &cli.BoolFlag{
		Name:        "disableTLS",
		Destination: opts.cfg.DisableTLS,
	}
	if err := flag.Set("disableTLS", "false"); err != nil {
		t.Fatal(err)
	}
	opts.flags = &[]cli.Flag{flag}

	cfg := &config.Config{DisableTLS: &trueValue}
	if err := mergeConfig(cfg, opts); err != nil {
		t.Fatal(err)
	}
	if cfg.DisableTLS == nil || *cfg.DisableTLS {
		t.Fatal("disableTLS = true, want explicit false override")
	}
}

func TestRunnerCmdIncludesConfigFlags(t *testing.T) {
	cmd := runnerCmd(os.Stdout, os.Stderr)

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
