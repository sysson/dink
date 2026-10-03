package buildkit

import (
	"context"
	"slices"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSolveRevokesCredentialsOnFailureAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		push   bool
	}{
		{name: "failure"}, {name: "cancellation", cancel: true},
		{name: "push failure", push: true}, {name: "push cancellation", push: true, cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &solveClient{}
			g, publisher, b, ctx := newSolveGateway(t, backend)
			backend.solve = func(ctx context.Context, _ *control.SolveRequest) (*control.SolveResponse, error) {
				if b.credential == nil {
					t.Fatal("missing credential during solve")
				}
				if test.cancel {
					b.cancel()
					select {
					case <-ctx.Done():
						return nil, status.FromContextError(ctx.Err()).Err()
					case <-time.After(time.Second):
						t.Fatal("session cancellation did not cancel solve")
					}
				}
				return nil, status.Error(codes.Internal, "export failed")
			}
			request := solveRequest()
			if test.push {
				request.Exporters[0].Attrs["push"] = "true"
			}
			_, err := g.Solve(ctx, request)
			want := codes.Internal
			if test.cancel {
				want = codes.Canceled
			}
			if status.Code(err) != want {
				t.Fatalf("error = %v, want %v", err, want)
			}
			if !slices.Equal(publisher.revoked, []string{"build-user"}) || publisher.revokeContextErr != nil || b.credential != nil || b.solving.Load() {
				t.Fatal("credential cleanup failed or used a cancelled context")
			}
		})
	}
}

func TestSolveRejectsInvalidRequestsBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*control.SolveRequest, *bridge)
		code   codes.Code
	}{
		{"missing ref", func(r *control.SolveRequest, _ *bridge) { r.Ref = "" }, codes.InvalidArgument},
		{"missing session", func(r *control.SolveRequest, _ *bridge) { r.Session = "" }, codes.InvalidArgument},
		{"foreign session", func(_ *control.SolveRequest, b *bridge) { b.id.Namespace = "other" }, codes.PermissionDenied},
		{"concurrent solve", func(_ *control.SolveRequest, b *bridge) { b.solving.Store(true) }, codes.FailedPrecondition},
		{"proxy network", func(r *control.SolveRequest, _ *bridge) { r.ProxyNetwork = true }, codes.Unimplemented},
		{"untagged moby", func(r *control.SolveRequest, _ *bridge) { r.Exporters[0].Attrs["name"] = "" }, codes.Unimplemented},
		{"push by digest", func(r *control.SolveRequest, _ *bridge) { r.Exporters[0].Attrs["push-by-digest"] = "true" }, codes.Unimplemented},
		{"invalid push", func(r *control.SolveRequest, _ *bridge) { r.Exporters[0].Attrs["push"] = "invalid" }, codes.InvalidArgument},
		{"multiple moby", func(r *control.SolveRequest, _ *bridge) {
			r.Exporters = append(r.Exporters, &control.Exporter{Type: "moby", Attrs: map[string]string{"name": "second:test"}})
		}, codes.Unimplemented},
		{"foreign context session", func(r *control.SolveRequest, _ *bridge) {
			r.FrontendAttrs = map[string]string{"local-sessionid:context": "foreign"}
		}, codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &solveClient{solve: func(context.Context, *control.SolveRequest) (*control.SolveResponse, error) {
				t.Fatal("invalid request reached backend")
				return nil, nil
			}}
			g, _, b, ctx := newSolveGateway(t, backend)
			request := solveRequest()
			test.change(request, b)
			if _, err := g.Solve(ctx, request); status.Code(err) != test.code {
				t.Fatalf("error = %v, want %v", err, test.code)
			}
		})
	}
}

func TestGatewayRequiresConfigurationAndIdentity(t *testing.T) {
	g, err := New(config.BuildKit{}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	if _, err := g.Info(context.Background(), &control.InfoRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity = %v", err)
	}
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"})
	if _, err := g.Info(ctx, &control.InfoRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing backend = %v", err)
	}
}
