package api_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sysson/dink/core/identity"
)

func TestBuildCredentialsMapTenantImageNames(t *testing.T) {
	client, credentials := newAPIWithCredentials(t)
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	credential, err := client.IssueBuildCredential(ctx, []string{"app:test", "ghcr.io/team/app:v1", "localhost:5000/app:v2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tenant/app:test", "tenant/ghcr.io/team/app:v1", "tenant/localhost-5000/app:v2"}
	if !slices.Equal(credential.References, want) {
		t.Fatalf("references = %v, want %v", credential.References, want)
	}
	namespace, repositories, err := credentials.VerifyBuild(ctx, credential.Username, credential.Password)
	if err != nil || namespace != "tenant" || len(repositories) != 3 {
		t.Fatalf("VerifyBuild = %q %v %v", namespace, repositories, err)
	}
	other := identity.NewContext(context.Background(), identity.Identity{Namespace: "other"})
	if err := client.RevokeBuildCredential(other, credential.Username); err == nil {
		t.Fatal("another tenant revoked the credential")
	}
	if err := client.RevokeBuildCredential(ctx, credential.Username); err != nil {
		t.Fatal(err)
	}
	if namespace, _, err := credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
		t.Fatalf("revoked credential = %q %v", namespace, err)
	}
	for _, names := range [][]string{nil, {"INVALID:test"}, {"app@sha256:" + strings.Repeat("a", 64)}} {
		if _, err := client.IssueBuildCredential(ctx, names); err == nil {
			t.Fatalf("accepted invalid build names %v", names)
		}
	}
}
