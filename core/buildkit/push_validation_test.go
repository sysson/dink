package buildkit

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPushNamesValidation(t *testing.T) {
	for _, names := range []string{"app:test", "docker.io/team/app:latest", "ghcr.io/team/app:v1,registry.example:5443/app:v2"} {
		if err := validatePushNames(names, "dinki.internal:5000"); err != nil {
			t.Fatalf("valid names %q: %v", names, err)
		}
	}
	for _, names := range []string{"", "app", "bad image:test", "app:test,", "dinki.internal:5000/app:test", "app:test@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if err := validatePushNames(names, "dinki.internal:5000"); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid names %q: %v", names, err)
		}
	}
}
