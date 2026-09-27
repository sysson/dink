package registry

import (
	"strings"
	"testing"
)

func TestImageIDPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
		isID   bool
	}{
		{name: "abc12345679", prefix: "abc12345679", isID: true},
		{name: "sha256:abc12345679", prefix: "abc12345679", isID: true},
		{name: "0123", prefix: "0123", isID: true},
		{name: "012", isID: false},
		{name: strings.Repeat("a", 65), isID: false},
		{name: "nginx", isID: false},
		{name: "nginx:latest", isID: false},
		{name: "library/abcdef", isID: false},
		{name: "nginx@sha256:abcdef", isID: false},
		{name: "ABCDEF", isID: false},
		{name: "md4:abcdef", isID: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix, isID := imageIDPrefix(tc.name)
			if isID != tc.isID || prefix != tc.prefix {
				t.Fatalf("imageIDPrefix(%q) = %q, %v; want %q, %v", tc.name, prefix, isID, tc.prefix, tc.isID)
			}
		})
	}
}

func TestResolvedImageReference(t *testing.T) {
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	image := resolvedImage{namespace: "dev", repository: "dev/nginx", digest: digest}

	tagged := image
	tagged.tag = "latest"
	if got, want := tagged.reference(), "nginx:latest"; got != want {
		t.Errorf("tagged reference = %q, want %q", got, want)
	}
	if got, want := image.reference(), "nginx@"+digest; got != want {
		t.Errorf("untagged reference = %q, want %q", got, want)
	}
	byDigest := image
	byDigest.tag = digestTag(digest)
	if got, want := byDigest.reference(), "nginx@"+digest; got != want {
		t.Errorf("digest-tag reference = %q, want %q", got, want)
	}
}
