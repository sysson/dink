package buildkit_test

import (
	"os"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"sigs.k8s.io/yaml"
)

func TestDeploymentGarbageCollection(t *testing.T) {
	data, err := os.ReadFile("../../deploy/buildkit.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Data map[string]string `json:"data"`
	}
	if err := yaml.Unmarshal([]byte(strings.SplitN(string(data), "\n---", 2)[0]), &manifest); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Worker struct {
			OCI struct {
				GC            bool   `toml:"gc"`
				ReservedSpace string `toml:"reservedSpace"`
				MaxUsedSpace  string `toml:"maxUsedSpace"`
				MinFreeSpace  string `toml:"minFreeSpace"`
			} `toml:"oci"`
		} `toml:"worker"`
	}
	if err := toml.NewDecoder(strings.NewReader(manifest.Data["buildkitd.toml"])).Decode(&config); err != nil {
		t.Fatal(err)
	}
	gc := config.Worker.OCI
	if !gc.GC {
		t.Fatal("worker GC must be explicitly enabled")
	}
	if gc.ReservedSpace != "2GiB" || gc.MaxUsedSpace != "10GiB" || gc.MinFreeSpace != "10GiB" {
		t.Fatalf("unexpected worker GC limits: %+v", gc)
	}
}
