package memkv_test

import (
	"testing"

	"github.com/sysson/dink/core/registry/backend/kv/kvtest"
	"github.com/sysson/dink/core/registry/backend/kv/memkv"
)

func TestConformance(t *testing.T) {
	kvtest.Run(t, memkv.New())
}
