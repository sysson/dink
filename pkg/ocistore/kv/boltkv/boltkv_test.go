package boltkv_test

import (
	"path/filepath"
	"testing"

	"github.com/sysson/dink/pkg/ocistore/kv/boltkv"
	"github.com/sysson/dink/pkg/ocistore/kv/kvtest"
)

func TestConformance(t *testing.T) {
	store, err := boltkv.Open(filepath.Join(t.TempDir(), "meta.db"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	kvtest.Run(t, store)
}
