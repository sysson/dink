package command

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestVersionDoesNotRequireRegistryConfiguration(t *testing.T) {
	out := &bytes.Buffer{}
	if err := New(out, io.Discard).Run(context.Background(), []string{"dinki", "--version"}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"dinki version", "commit ", "built ", "dirty "} {
		if !strings.Contains(out.String(), field) {
			t.Fatalf("version output %q is missing %q", out, field)
		}
	}
}
