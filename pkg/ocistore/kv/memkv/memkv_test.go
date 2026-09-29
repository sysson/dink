package memkv_test

import (
	"testing"

	"github.com/sysson/dink/pkg/ocistore/kv/kvtest"
	"github.com/sysson/dink/pkg/ocistore/kv/memkv"
)

func TestConformance(t *testing.T) {
	kvtest.Run(t, memkv.New())
}
