package pullauth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociserver"
	"github.com/sysson/ocistore/kv/memkv"
)

func TestBuildCredentialScopeExpiryAndRevocation(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	store := New(memkv.New())
	store.now = func() time.Time { return now }
	username, password, err := store.IssueBuild(ctx, "team", []string{"team/app"})
	if err != nil {
		t.Fatal(err)
	}
	tenant, repos, err := store.VerifyBuild(ctx, username, password)
	if err != nil || tenant != "team" || len(repos) != 1 || repos[0] != "team/app" {
		t.Fatalf("VerifyBuild = %q %v %v", tenant, repos, err)
	}
	if tenant, _, err := store.VerifyBuild(ctx, username, "wrong"); err != nil || tenant != "" {
		t.Fatalf("wrong password = %q %v", tenant, err)
	}
	if err := store.RevokeBuild(ctx, "other", username); err == nil {
		t.Fatal("another tenant revoked a build credential")
	}
	now = now.Add(BuildCredentialLifetime)
	if tenant, _, err := store.VerifyBuild(ctx, username, password); err != nil || tenant != "" {
		t.Fatalf("expired credential = %q %v", tenant, err)
	}
	now = now.Add(-BuildCredentialLifetime)
	if err := store.RevokeBuild(ctx, "team", username); err != nil {
		t.Fatal(err)
	}
	if tenant, _, err := store.VerifyBuild(ctx, username, password); err != nil || tenant != "" {
		t.Fatalf("revoked credential = %q %v", tenant, err)
	}
	if _, _, err := store.IssueBuild(ctx, "team", []string{"other/app"}); err == nil {
		t.Fatal("issued cross-tenant credential")
	}
}

func TestBuildScopeOnlyAllowsExactRepositoriesAndNoDeletion(t *testing.T) {
	ctx := context.Background()
	mem := ocimem.New()
	scoped := Scope(mem)
	build := withBuildScope(NewContext(ctx, "team"), []string{"team/app"})
	data := []byte("layer")
	desc := oci.Descriptor{Digest: ocidigest.FromBytes(data), Size: int64(len(data)), MediaType: "application/octet-stream"}
	if _, err := scoped.PushBlob(build, "team/app", desc, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"team/other", "other/app", "team/app/sub"} {
		if _, err := scoped.PushBlob(build, repo, desc, bytes.NewReader(data)); !errors.Is(err, oci.ErrUnsupported) {
			t.Fatalf("PushBlob(%s) = %v", repo, err)
		}
		if _, err := scoped.ResolveBlob(build, repo, desc.Digest); !errors.Is(err, oci.ErrNameUnknown) {
			t.Fatalf("ResolveBlob(%s) = %v", repo, err)
		}
	}
	if err := scoped.DeleteBlob(build, "team/app", desc.Digest); !errors.Is(err, oci.ErrUnsupported) {
		t.Fatalf("DeleteBlob = %v", err)
	}
	if _, err := scoped.MountBlob(build, "other/app", "team/app", desc.Digest); !errors.Is(err, oci.ErrUnsupported) {
		t.Fatalf("cross-tenant mount = %v", err)
	}
}

func TestBuildAndPullCredentialsHaveSeparateWritePermissions(t *testing.T) {
	ctx := context.Background()
	store := New(memkv.New())
	pullPassword, err := store.Issue(ctx, "team")
	if err != nil {
		t.Fatal(err)
	}
	username, password, err := store.IssueBuild(ctx, "team", []string{"team/app"})
	if err != nil {
		t.Fatal(err)
	}
	ociHandler, err := ociserver.New(Scope(ocimem.New()), nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := Middleware(store)(ociHandler)
	for _, tc := range []struct {
		name, user, password, repo string
		wantSuccess                bool
	}{
		{"build own repo", username, password, "team/app", true},
		{"build other repo", username, password, "team/other", false},
		{"build other tenant", username, password, "other/app", false},
		{"pull stays read-only", "team", pullPassword, "team/app", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v2/"+tc.repo+"/blobs/uploads/", nil)
			request.SetBasicAuth(tc.user, tc.password)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if success := response.Code < 400; success != tc.wantSuccess {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}
