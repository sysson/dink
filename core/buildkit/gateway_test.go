package buildkit

import (
	"context"
	"errors"
	"io"
	"net/url"
	"slices"
	"testing"

	control "github.com/moby/buildkit/api/services/control"
	bktypes "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/session/auth"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type solveClient struct {
	control.ControlClient
	solve   func(context.Context, *control.SolveRequest) (*control.SolveResponse, error)
	usage   func(context.Context, *control.DiskUsageRequest) (*control.DiskUsageResponse, error)
	prune   func(context.Context, *control.PruneRequest) (grpc.ServerStreamingClient[control.UsageRecord], error)
	workers *control.ListWorkersResponse
}

func (c *solveClient) Solve(ctx context.Context, request *control.SolveRequest, _ ...grpc.CallOption) (*control.SolveResponse, error) {
	return c.solve(ctx, request)
}

func (c *solveClient) DiskUsage(ctx context.Context, request *control.DiskUsageRequest, _ ...grpc.CallOption) (*control.DiskUsageResponse, error) {
	return c.usage(ctx, request)
}

func (c *solveClient) Prune(ctx context.Context, request *control.PruneRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[control.UsageRecord], error) {
	return c.prune(ctx, request)
}

func (c *solveClient) ListWorkers(context.Context, *control.ListWorkersRequest, ...grpc.CallOption) (*control.ListWorkersResponse, error) {
	return c.workers, nil
}

type usageStream struct {
	grpc.ClientStream
	records []*control.UsageRecord
	header  metadata.MD
	trailer metadata.MD
	err     error
}

func (s *usageStream) Recv() (*control.UsageRecord, error) {
	if len(s.records) > 0 {
		record := s.records[0]
		s.records = s.records[1:]
		return record, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

func (s *usageStream) Header() (metadata.MD, error) { return s.header, nil }
func (s *usageStream) Trailer() metadata.MD         { return s.trailer }

type usageServer struct {
	grpc.ServerStream
	ctx     context.Context
	headers metadata.MD
	trailer metadata.MD
	records []*control.UsageRecord
}

func (s *usageServer) Context() context.Context { return s.ctx }
func (s *usageServer) SendHeader(headers metadata.MD) error {
	s.headers = headers
	return nil
}
func (s *usageServer) SetTrailer(trailer metadata.MD) { s.trailer = trailer }
func (s *usageServer) Send(record *control.UsageRecord) error {
	s.records = append(s.records, record)
	return nil
}

type testPublisher struct {
	credential       api.BuildCredential
	names            []string
	revoked          []string
	issueErr         error
	revokeErr        error
	revokeContextErr error
}

func (p *testPublisher) IssueBuildCredential(_ context.Context, names []string) (api.BuildCredential, error) {
	p.names = slices.Clone(names)
	return p.credential, p.issueErr
}

func (p *testPublisher) RevokeBuildCredential(ctx context.Context, username string) error {
	p.revoked = append(p.revoked, username)
	p.revokeContextErr = ctx.Err()
	return p.revokeErr
}

func newSolveGateway(t *testing.T, backend *solveClient) (*Gateway, *testPublisher, *bridge, context.Context) {
	t.Helper()
	id := identity.Identity{Namespace: "tenant", CommonName: "client"}
	ctx, cancel := context.WithCancel(identity.NewContext(t.Context(), id))
	t.Cleanup(cancel)
	b := &bridge{id: id, ctx: ctx, cancel: cancel, ready: make(chan struct{})}
	close(b.ready)
	publisher := &testPublisher{credential: api.BuildCredential{
		Username: "build-user", Password: "build-secret", References: []string{"tenant/app:test", "tenant/app:latest"},
	}}
	registry, err := url.Parse("http://dinki.test:5000")
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{
		control: backend, publisher: publisher, registry: registry,
		sessions: map[string]*bridge{"session": b},
	}, publisher, b, ctx
}

func solveRequest() *control.SolveRequest {
	return &control.SolveRequest{Ref: "build-ref", Session: "session", Frontend: "dockerfile.v0",
		Exporters: []*control.Exporter{{Type: "moby", Attrs: map[string]string{"name": "app:test,app:latest"}}},
	}
}

func TestSolveAdaptsMobyExportWithoutMutatingCaller(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			backend := &solveClient{}
			g, publisher, b, ctx := newSolveGateway(t, backend)
			g.registry.Scheme = scheme
			input := solveRequest()
			input.FrontendAttrs = map[string]string{historyNamesAttr: "forged", historyExporterAttr: "9", "target": "release"}
			before := proto.Clone(input)
			backendResponse := &control.SolveResponse{ExporterResponse: map[string]string{
				"image.name": "internal", "containerimage.digest": "manifest", "containerimage.config.digest": "config",
			}}
			backend.solve = func(ctx context.Context, request *control.SolveRequest) (*control.SolveResponse, error) {
				if request.Ref != historyRef(b.id, input.Ref) || request.Session != input.Session {
					t.Fatalf("unsafe build reference or changed session: %v", request)
				}
				exporter := request.Exporters[0]
				if exporter.Type != "image" || exporter.Attrs["name"] != "dinki.test:5000/tenant/app:test,dinki.test:5000/tenant/app:latest" ||
					exporter.Attrs["push"] != "true" || exporter.Attrs["store"] != "false" ||
					exporter.Attrs["registry.insecure"] != map[string]string{"http": "true", "https": "false"}[scheme] {
					t.Fatalf("incorrect internal export: %v", exporter)
				}
				if request.FrontendAttrs[historyNamesAttr] != "app:test,app:latest" || request.FrontendAttrs[historyExporterAttr] != "0" ||
					request.FrontendAttrs["target"] != "release" {
					t.Fatalf("incorrect history attributes: %v", request.FrontendAttrs)
				}
				authP := &authProxy{bridge: b, host: g.registry.Host}
				credential, err := authP.Credentials(ctx, &auth.CredentialsRequest{Host: g.registry.Host})
				if err != nil || credential.Username != publisher.credential.Username || credential.Secret != publisher.credential.Password {
					t.Fatalf("internal credential not available during solve: %v %v", credential, err)
				}
				return backendResponse, nil
			}
			response, err := g.Solve(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if response.ExporterResponse["image.name"] != "app:test,app:latest" || response.ExporterResponse["containerimage.digest"] != "manifest" ||
				response.ExporterResponse["containerimage.config.digest"] != "" {
				t.Fatalf("incorrect Docker response: %v", response)
			}
			if !proto.Equal(input, before) || backendResponse.ExporterResponse["image.name"] != "internal" {
				t.Fatal("mutated caller request or backend response")
			}
			if !slices.Equal(publisher.names, []string{"app:test", "app:latest"}) || !slices.Equal(publisher.revoked, []string{"build-user"}) ||
				b.credential != nil || b.solving.Load() {
				t.Fatal("incorrect credential scope or incomplete solve cleanup")
			}
		})
	}
}

func TestSolveExplicitOutputDoesNotIssueCredentials(t *testing.T) {
	backend := &solveClient{}
	g, publisher, b, ctx := newSolveGateway(t, backend)
	input := solveRequest()
	input.Exporters = []*control.Exporter{{Type: "local", Attrs: map[string]string{"dest": "/output"}}}
	backend.solve = func(_ context.Context, got *control.SolveRequest) (*control.SolveResponse, error) {
		if !proto.Equal(got.Exporters[0], input.Exporters[0]) || got.Ref != historyRef(b.id, input.Ref) {
			t.Fatalf("explicit output was changed: %v", got)
		}
		return &control.SolveResponse{}, nil
	}
	if _, err := g.Solve(ctx, input); err != nil {
		t.Fatal(err)
	}
	if len(publisher.names) != 0 || len(publisher.revoked) != 0 {
		t.Fatal("explicit output issued an internal registry credential")
	}
}

func TestSolveDeprecatedExporterIsAdapted(t *testing.T) {
	backend := &solveClient{}
	g, _, _, ctx := newSolveGateway(t, backend)
	input := solveRequest()
	input.Exporters = nil
	input.ExporterDeprecated, input.ExporterAttrsDeprecated = "moby", map[string]string{"name": "app:test"}
	backend.solve = func(_ context.Context, got *control.SolveRequest) (*control.SolveResponse, error) {
		if got.ExporterDeprecated != "" || got.ExporterAttrsDeprecated != nil || len(got.Exporters) != 1 || got.Exporters[0].Type != "image" {
			t.Fatalf("deprecated exporter not normalized: %v", got)
		}
		return &control.SolveResponse{}, nil
	}
	if _, err := g.Solve(ctx, input); err != nil {
		t.Fatal(err)
	}
}

func TestSolveCredentialErrorsAreSurfaced(t *testing.T) {
	for _, issuing := range []bool{true, false} {
		t.Run(map[bool]string{true: "issue", false: "revoke"}[issuing], func(t *testing.T) {
			backend := &solveClient{}
			g, publisher, b, ctx := newSolveGateway(t, backend)
			failure := errors.New("credential store unavailable")
			if issuing {
				publisher.issueErr = failure
			} else {
				publisher.revokeErr = failure
			}
			called := false
			backend.solve = func(context.Context, *control.SolveRequest) (*control.SolveResponse, error) {
				called = true
				return &control.SolveResponse{}, nil
			}
			if _, err := g.Solve(ctx, solveRequest()); err == nil {
				t.Fatal("credential error reported success")
			}
			if called == issuing || b.credential != nil || b.solving.Load() {
				t.Fatal("incorrect backend invocation or missing cleanup")
			}
		})
	}
}

func TestListWorkersDoesNotAdvertiseContainerdCapabilities(t *testing.T) {
	backend := &solveClient{workers: &control.ListWorkersResponse{Record: []*bktypes.WorkerRecord{{
		Labels: map[string]string{"org.mobyproject.buildkit.worker.snapshotter": "overlayfs", "executor": "oci"},
	}}}}
	g, _, _, ctx := newSolveGateway(t, backend)
	got, err := g.ListWorkers(ctx, &control.ListWorkersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Record[0].Labels["org.mobyproject.buildkit.worker.snapshotter"]; ok || got.Record[0].Labels["executor"] != "oci" {
		t.Fatalf("incorrect advertised capabilities: %v", got)
	}
	if backend.workers.Record[0].Labels["org.mobyproject.buildkit.worker.snapshotter"] != "overlayfs" {
		t.Fatal("mutated backend worker labels")
	}
}

func TestDiskUsageIsAvailableToAnyTenant(t *testing.T) {
	request := &control.DiskUsageRequest{Filter: []string{"type==regular"}, AgeLimit: 3600}
	response := &control.DiskUsageResponse{Record: []*control.UsageRecord{{ID: "cache", Size: 42}}}
	backend := &solveClient{usage: func(_ context.Context, got *control.DiskUsageRequest) (*control.DiskUsageResponse, error) {
		if !proto.Equal(got, request) {
			t.Fatalf("request = %v, want %v", got, request)
		}
		return response, nil
	}}
	g := &Gateway{control: backend}
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "another-tenant"})

	got, err := g.DiskUsage(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if got != response {
		t.Fatalf("response = %v, want backend response %v", got, response)
	}
}

func TestPruneForwardsRecordsAndMetadata(t *testing.T) {
	request := &control.PruneRequest{
		Filter: []string{"type==regular"}, All: true, KeepDuration: 60,
		ReservedSpace: 10, MaxUsedSpace: 100, MinFreeSpace: 20,
	}
	stream := &usageStream{
		records: []*control.UsageRecord{{ID: "cache-1"}, {ID: "cache-2"}},
		header:  metadata.Pairs("header", "value"),
		trailer: metadata.Pairs("trailer", "value"),
	}
	backend := &solveClient{prune: func(_ context.Context, got *control.PruneRequest) (grpc.ServerStreamingClient[control.UsageRecord], error) {
		if !proto.Equal(got, request) {
			t.Fatalf("request = %v, want %v", got, request)
		}
		return stream, nil
	}}
	g := &Gateway{control: backend}
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "another-tenant"})
	server := &usageServer{ctx: ctx}

	if err := g.Prune(request, server); err != nil {
		t.Fatal(err)
	}
	if len(server.records) != 2 || server.records[0].ID != "cache-1" || server.records[1].ID != "cache-2" {
		t.Fatalf("prune records = %v", server.records)
	}
	if !slices.Equal(server.headers.Get("header"), []string{"value"}) || !slices.Equal(server.trailer.Get("trailer"), []string{"value"}) {
		t.Fatalf("prune metadata = headers %v, trailer %v", server.headers, server.trailer)
	}
}

func TestPruneCacheCollectsRecords(t *testing.T) {
	request := &control.PruneRequest{All: true}
	stream := &usageStream{records: []*control.UsageRecord{{ID: "cache-1", Size: 10}, {ID: "cache-2", Size: 20}}}
	backend := &solveClient{prune: func(_ context.Context, got *control.PruneRequest) (grpc.ServerStreamingClient[control.UsageRecord], error) {
		if !proto.Equal(got, request) {
			t.Fatalf("request = %v, want %v", got, request)
		}
		return stream, nil
	}}
	g := &Gateway{control: backend}
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "another-tenant"})

	records, err := g.PruneCache(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != "cache-1" || records[1].ID != "cache-2" {
		t.Fatalf("prune records = %v", records)
	}
}
