package buildkit_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGatewayPushKeepsTenantImageAndHonorsUpstreamFailures(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "upstream failure"
		}
		t.Run(name, func(t *testing.T) {
			backend, address := newBackend(t)
			backend.push, backend.pushFail = true, failure
			f := newFixture(t, address)
			registryURL, _ := url.Parse(f.registryURL)
			backend.host = registryURL.Host
			httpURL, _ := url.Parse(gatewayHTTP(t, f))
			bk, err := client.New(context.Background(), "tcp://"+httpURL.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bk.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const names = "ghcr.io/team/app:test,ghcr.io/team/app:latest"
			response, err := bk.Solve(ctx, nil, client.SolveOpt{
				Frontend: "dockerfile.v0",
				Exports: []client.ExportEntry{{Type: "moby", Attrs: map[string]string{
					"name": names, "push": "true", "registry.insecure": "false",
					"compression": "gzip", "oci-mediatypes": "true",
				}}},
				Session: []session.Attachable{&testSessionAuth{}, &echoSession{}},
			}, nil)
			if failure {
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("upstream failure was not surfaced: %v", err)
				}
			} else if err != nil || response.ExporterResponse["image.name"] != names || response.ExporterResponse["containerimage.config.digest"] != "" {
				t.Fatalf("push result: %v %v", response, err)
			}
			request := <-backend.request
			internal, upstream := request.Exporters[0], request.Exporters[1]
			if !strings.Contains(internal.Attrs["name"], "/tenant/ghcr.io/team/app:test") || internal.Attrs["registry.insecure"] != "true" || internal.Attrs["store"] != "false" {
				t.Fatalf("incorrect internal push: %v", internal)
			}
			if upstream.Attrs["name"] != names || upstream.Attrs["registry.insecure"] != "false" || upstream.Attrs["compression"] != "gzip" || upstream.Attrs["oci-mediatypes"] != "true" {
				t.Fatalf("upstream destination or options changed: %v", upstream)
			}
			tenant := identity.NewContext(ctx, identity.Identity{Namespace: "tenant"})
			for tag := range strings.SplitSeq(names, ",") {
				if image, err := f.api.ImageInspect(tenant, tag, imagebackend.ImageInspectOpts{}); err != nil || image == nil {
					t.Fatalf("Dinki image %s missing: %v", tag, err)
				}
			}
			credential := f.publisher.lastCredential()
			select {
			case <-f.publisher.revoked:
			case <-ctx.Done():
				t.Fatal("build credential was not revoked")
			}
			if namespace, _, err := f.credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
				t.Fatalf("push retained build credential: %q %v", namespace, err)
			}
		})
	}
}
