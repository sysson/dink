package buildkit_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func TestGatewayRevokesCredentialsOnFailureAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		cancelBuild bool
		push        bool
	}{
		{name: "export failure"},
		{name: "client cancellation", cancelBuild: true},
		{name: "push export failure", push: true},
		{name: "push client cancellation", cancelBuild: true, push: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend, address := newBackend(t)
			backend.fail, backend.block, backend.push = !test.cancelBuild, test.cancelBuild, test.push
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
			done := make(chan error, 1)
			go func() {
				attrs := map[string]string{"name": "app:test"}
				if test.push {
					attrs["push"] = "true"
				}
				_, err := bk.Solve(ctx, nil, client.SolveOpt{
					Frontend: "dockerfile.v0",
					Exports:  []client.ExportEntry{{Type: "moby", Attrs: attrs}},
					Session:  []session.Attachable{&testSessionAuth{}, &echoSession{}},
				}, nil)
				done <- err
			}()
			select {
			case <-f.publisher.issued:
			case <-ctx.Done():
				t.Fatal("build did not issue a credential")
			}
			if test.cancelBuild {
				cancel()
			}
			if err := <-done; err == nil {
				t.Fatal("failed build reported success")
			}
			select {
			case username := <-f.publisher.revoked:
				if namespace, _, err := f.credentials.VerifyBuild(context.Background(), username, f.publisher.lastCredential().Password); err != nil || namespace != "" {
					t.Fatalf("failed build retained its credential: %q %v", namespace, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("build credential was not revoked")
			}
		})
	}
}

func TestGatewayRejectsForeignSessionsAndUnsupportedExports(t *testing.T) {
	_, address := newBackend(t)
	f := newFixture(t, address)
	httpURL, _ := url.Parse(gatewayHTTP(t, f))
	connection, err := grpc.NewClient("passthrough:///"+httpURL.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := session.NewSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Run(ctx, grpchijack.Dialer(control.NewControlClient(connection))) }()
	defer func() { _ = s.Close() }()
	other := identity.NewContext(ctx, identity.Identity{Namespace: "other", CommonName: "client"})
	request := &control.SolveRequest{Ref: "test", Session: s.ID()}
	if _, err := f.gateway.Solve(other, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign session error = %v", err)
	}
	owner := identity.NewContext(ctx, identity.Identity{Namespace: "tenant", CommonName: "client"})
	for _, tc := range []struct{ name, pushByDigest string }{
		{"", ""},
		{"app:test", "true"},
	} {
		request.Exporters = []*control.Exporter{{Type: "moby", Attrs: map[string]string{"name": tc.name, "push-by-digest": tc.pushByDigest}}}
		if _, err := f.gateway.Solve(owner, request); status.Code(err) != codes.Unimplemented {
			t.Fatalf("unsupported exporter error = %v", err)
		}
	}
	request.Exporters = []*control.Exporter{{Type: "moby", Attrs: map[string]string{"name": "app:test", "push": "invalid"}}}
	if _, err := f.gateway.Solve(owner, request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid push flag error = %v", err)
	}
	request.FrontendAttrs = map[string]string{"local-sessionid:context": "foreign-session"}
	if _, err := f.gateway.Solve(owner, request); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign frontend session error = %v", err)
	}
}

func TestGatewayRequiresConfigurationAndIdentity(t *testing.T) {
	g, err := buildkit.New(config.BuildKit{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if _, err := g.Info(context.Background(), &control.InfoRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity = %v", err)
	}
	ctx := identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"})
	if _, err := g.Info(ctx, &control.InfoRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing backend = %v", err)
	}
}
