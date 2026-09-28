package pullauth_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/sysson/dink/core/registry/pullauth"
	"github.com/sysson/dink/pkg/ocistore/kv/memkv"
)

func TestScopeLimitsReadsToNamespace(t *testing.T) {
	ctx := context.Background()
	mem := ocimem.New()
	content := "layer"
	desc := oci.Descriptor{MediaType: "application/octet-stream", Digest: ocidigest.FromBytes([]byte(content)), Size: int64(len(content))}
	for _, repo := range []string{"team/app", "team/web", "team-a/app", "teamz/app"} {
		if _, err := mem.PushBlob(ctx, repo, desc, strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	scoped := pullauth.Scope(mem)
	team := pullauth.NewContext(ctx, "team")

	if _, err := scoped.ResolveBlob(team, "team/app", desc.Digest); err != nil {
		t.Fatalf("own blob: %v", err)
	}
	for _, repo := range []string{"team-a/app", "teamz/app"} {
		if _, err := scoped.ResolveBlob(team, repo, desc.Digest); !errors.Is(err, oci.ErrNameUnknown) {
			t.Fatalf("ResolveBlob(%s) error = %v, want ErrNameUnknown", repo, err)
		}
	}
	if _, err := scoped.ResolveBlob(ctx, "team/app", desc.Digest); !errors.Is(err, oci.ErrNameUnknown) {
		t.Fatalf("unscoped ResolveBlob error = %v, want ErrNameUnknown", err)
	}
	if _, err := scoped.PushBlob(team, "team/app", desc, strings.NewReader(content)); err == nil {
		t.Fatal("PushBlob succeeded through the scoped view")
	}

	var repos []string
	for repo, err := range scoped.Repositories(team, "") {
		if err != nil {
			t.Fatal(err)
		}
		repos = append(repos, repo)
	}
	if !slices.Equal(repos, []string{"team/app", "team/web"}) {
		t.Fatalf("Repositories = %v, want only team's", repos)
	}
}

func TestStoreIssueVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	store := pullauth.New(memkv.New())
	if ok, err := store.Verify(ctx, "team", "anything"); err != nil || ok {
		t.Fatalf("Verify before issue = %v, %v", ok, err)
	}
	password, err := store.Issue(ctx, "team")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Verify(ctx, "team", password); err != nil || !ok {
		t.Fatalf("Verify(issued) = %v, %v", ok, err)
	}
	if ok, _ := store.Verify(ctx, "other", password); ok {
		t.Fatal("password verified for another namespace")
	}
	if _, err := store.Issue(ctx, "Bad_Namespace"); err == nil {
		t.Fatal("Issue accepted an invalid namespace")
	}
	if err := store.Revoke(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Verify(ctx, "team", password); ok {
		t.Fatal("revoked password still verifies")
	}
}
