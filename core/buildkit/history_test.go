package buildkit

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type historyBackend struct {
	control.ControlClient
	events    []*control.BuildHistoryEvent
	request   chan *control.BuildHistoryRequest
	updates   chan *control.UpdateBuildHistoryRequest
	cancelled chan struct{}
	wait      bool
	content   *historyContentBackend
}

func (b *historyBackend) ListenBuildHistory(ctx context.Context, req *control.BuildHistoryRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[control.BuildHistoryEvent], error) {
	b.request <- proto.Clone(req).(*control.BuildHistoryRequest)
	return &historyStream{ctx: ctx, backend: b}, nil
}

func (b *historyBackend) UpdateBuildHistory(_ context.Context, req *control.UpdateBuildHistoryRequest, _ ...grpc.CallOption) (*control.UpdateBuildHistoryResponse, error) {
	b.updates <- proto.Clone(req).(*control.UpdateBuildHistoryRequest)
	return &control.UpdateBuildHistoryResponse{}, nil
}

type historyStream struct {
	grpc.ClientStream
	ctx     context.Context
	backend *historyBackend
	index   int
}

func (s *historyStream) Header() (metadata.MD, error) {
	return metadata.Pairs("history-test", "header"), nil
}
func (s *historyStream) Trailer() metadata.MD { return metadata.Pairs("history-test", "trailer") }
func (s *historyStream) Recv() (*control.BuildHistoryEvent, error) {
	if s.index < len(s.backend.events) {
		event := s.backend.events[s.index]
		s.index++
		return event, nil
	}
	if s.backend.wait {
		<-s.ctx.Done()
		close(s.backend.cancelled)
		return nil, status.FromContextError(s.ctx.Err()).Err()
	}
	return nil, io.EOF
}

func newHistoryGateway(t *testing.T, backend *historyBackend) *Gateway {
	t.Helper()
	backend.request = make(chan *control.BuildHistoryRequest, 8)
	backend.updates = make(chan *control.UpdateBuildHistoryRequest, 8)
	backend.cancelled = make(chan struct{})
	g := &Gateway{control: backend}
	if backend.content != nil {
		g.conn = newUnitConnection(t, func(server *grpc.Server) {
			contentapi.RegisterContentServer(server, backend.content)
		})
	}
	return g
}

type historyCapture struct {
	grpc.ServerStream
	ctx     context.Context
	events  []*control.BuildHistoryEvent
	headers metadata.MD
	trailer metadata.MD
	onSend  func()
}

func (s *historyCapture) Context() context.Context { return s.ctx }
func (s *historyCapture) SendHeader(md metadata.MD) error {
	s.headers = md
	return nil
}
func (s *historyCapture) SetTrailer(md metadata.MD) { s.trailer = md }
func (s *historyCapture) Send(event *control.BuildHistoryEvent) error {
	s.events = append(s.events, event)
	if s.onSend != nil {
		s.onSend()
	}
	return nil
}

func historyEvent(id identity.Identity, ref string) *control.BuildHistoryEvent {
	return &control.BuildHistoryEvent{Record: &control.BuildHistoryRecord{Ref: historyRef(id, ref)}}
}

func historyContext(t *testing.T, id identity.Identity) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return identity.NewContext(ctx, id)
}

func TestHistoryScopesAndRestoresRecords(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	own := historyEvent(owner, "build-ref")
	own.Record.FrontendAttrs = map[string]string{historyNamesAttr: "app:test,app:latest", historyExporterAttr: "0", "target": "release"}
	own.Record.Exporters = []*control.Exporter{{Type: "image", Attrs: map[string]string{"name": "registry/tenant/app:test"}}}
	own.Record.ExporterResponse = map[string]string{"image.name": "registry/tenant/app:test", "containerimage.config.digest": "config", "containerimage.digest": "manifest"}
	backend := &historyBackend{events: []*control.BuildHistoryEvent{
		historyEvent(identity.Identity{Namespace: "foreign", CommonName: "client"}, "build-ref"),
		historyEvent(identity.Identity{Namespace: "tenant", CommonName: "other"}, "build-ref"),
		{Record: &control.BuildHistoryRecord{Ref: tenantRef(owner, "old-build")}},
		{Record: &control.BuildHistoryRecord{Ref: historyPrefix(owner) + "%%%"}},
		{},
		own,
	}}
	g := newHistoryGateway(t, backend)
	capture := &historyCapture{ctx: historyContext(t, owner)}
	req := &control.BuildHistoryRequest{EarlyExit: true}
	if err := g.ListenBuildHistory(req, capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("returned %d events, want only the owner's event", len(capture.events))
	}
	record := capture.events[0].Record
	if record.Ref != "build-ref" || record.Exporters[0].Attrs["name"] != "app:test,app:latest" || record.ExporterResponse["image.name"] != "app:test,app:latest" {
		t.Fatalf("history was not restored: %v", record)
	}
	if record.FrontendAttrs[historyNamesAttr] != "" || record.FrontendAttrs[historyExporterAttr] != "" || record.FrontendAttrs["target"] != "release" || record.ExporterResponse["containerimage.config.digest"] != "" {
		t.Fatalf("internal metadata leaked or frontend metadata lost: %v", record)
	}
	if own.Record.Ref == record.Ref || len(req.Filter) != 0 {
		t.Fatal("history adaptation mutated input")
	}
	if capture.headers.Get("history-test")[0] != "header" || capture.trailer.Get("history-test")[0] != "trailer" {
		t.Fatal("stream headers/trailers were not forwarded")
	}
}

func TestHistoryScopesRequestBeforeBackendLimit(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	backend := &historyBackend{}
	for range 2 {
		g := newHistoryGateway(t, backend)
		capture := &historyCapture{ctx: historyContext(t, owner)}
		if err := g.ListenBuildHistory(&control.BuildHistoryRequest{EarlyExit: true, Limit: 1}, capture); err != nil {
			t.Fatal(err)
		}
		request := <-backend.request
		if request.Limit != 1 || len(request.Filter) != 1 || request.Filter[0] != "ref~=^"+historyPrefix(owner) {
			t.Fatalf("backend limit not scoped to stable identity: %v", request)
		}
	}
}

func TestHistorySpecificRefAndUpdateCannotTargetForeignBuild(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	foreign := identity.Identity{Namespace: "other", CommonName: "client"}
	backend := &historyBackend{events: []*control.BuildHistoryEvent{historyEvent(owner, "wanted"), historyEvent(owner, "other"), historyEvent(foreign, "wanted")}}
	g := newHistoryGateway(t, backend)
	ctx := historyContext(t, owner)
	capture := &historyCapture{ctx: ctx}
	if err := g.ListenBuildHistory(&control.BuildHistoryRequest{Ref: "wanted", EarlyExit: true}, capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 || capture.events[0].Record.Ref != "wanted" {
		t.Fatalf("specific history leaked records: %v", capture.events)
	}
	if req := <-backend.request; req.Ref != historyRef(owner, "wanted") {
		t.Fatalf("incorrect backend reference: %s", req.Ref)
	}
	for _, request := range []*control.UpdateBuildHistoryRequest{
		{Ref: "wanted", Delete: true}, {Ref: "wanted", Pinned: true}, {Ref: "wanted", Finalize: true},
		{Ref: historyRef(foreign, "wanted"), Delete: true},
	} {
		if _, err := g.UpdateBuildHistory(ctx, request); err != nil {
			t.Fatal(err)
		}
		got := <-backend.updates
		if got.Ref != historyRef(owner, request.Ref) || got.Delete != request.Delete || got.Pinned != request.Pinned || got.Finalize != request.Finalize {
			t.Fatalf("unsafe or incomplete history update: %v", got)
		}
	}
}

func TestHistoryFiltersAndValidation(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	backend := &historyBackend{}
	g := newHistoryGateway(t, backend)
	ctx := historyContext(t, owner)
	for _, filter := range []string{"status==completed", "status==completed,repository==https://example.com/refs/main", "repository==https://example.com/refs/main", ""} {
		capture := &historyCapture{ctx: ctx}
		if err := g.ListenBuildHistory(&control.BuildHistoryRequest{Filter: []string{filter}, EarlyExit: true}, capture); err != nil {
			t.Fatalf("filter %q: %v", filter, err)
		}
		req := <-backend.request
		if len(req.Filter) != 1 || !strings.Contains(req.Filter[0], historyPrefix(owner)) {
			t.Fatalf("unscoped filter: %v", req.Filter)
		}
	}
	if err := g.ListenBuildHistory(&control.BuildHistoryRequest{Filter: []string{"status==completed", "status==running"}, EarlyExit: true}, &historyCapture{ctx: ctx}); err != nil {
		t.Fatal(err)
	}
	for _, filter := range (<-backend.request).Filter {
		if !strings.Contains(filter, historyPrefix(owner)) {
			t.Fatalf("OR branch is not scoped: %s", filter)
		}
	}
	for _, test := range []struct {
		request *control.BuildHistoryRequest
		ctx     context.Context
		code    codes.Code
	}{
		{&control.BuildHistoryRequest{}, context.Background(), codes.Unauthenticated},
		{&control.BuildHistoryRequest{Limit: -1}, ctx, codes.InvalidArgument},
		{&control.BuildHistoryRequest{Filter: []string{"ref==build-ref"}}, ctx, codes.Unimplemented},
		{&control.BuildHistoryRequest{Filter: []string{`"ref==build,ref"`}}, ctx, codes.Unimplemented},
		{&control.BuildHistoryRequest{Filter: []string{`status==completed,"ref==build,ref"`}}, ctx, codes.Unimplemented},
		{&control.BuildHistoryRequest{Filter: []string{`"status==completed`}}, ctx, codes.InvalidArgument},
		{&control.BuildHistoryRequest{Filter: []string{`status=="completed"`}}, ctx, codes.InvalidArgument},
		{&control.BuildHistoryRequest{Filter: []string{"status==completed\nref==build-ref"}}, ctx, codes.InvalidArgument},
	} {
		err := g.ListenBuildHistory(test.request, &historyCapture{ctx: test.ctx})
		if status.Code(err) != test.code {
			t.Fatalf("validation error = %v, want %v", err, test.code)
		}
	}
	if _, err := g.UpdateBuildHistory(ctx, &control.UpdateBuildHistoryRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty update: %v", err)
	}
	if _, err := g.UpdateBuildHistory(context.Background(), &control.UpdateBuildHistoryRequest{Ref: "build"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated update: %v", err)
	}
}

func TestHistoryCancellationClosesBackendStream(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	backend := &historyBackend{wait: true, events: []*control.BuildHistoryEvent{historyEvent(owner, "live")}}
	g := newHistoryGateway(t, backend)
	ctx, cancel := context.WithCancel(historyContext(t, owner))
	defer cancel()
	err := g.ListenBuildHistory(&control.BuildHistoryRequest{ActiveOnly: true}, &historyCapture{ctx: ctx, onSend: cancel})
	if status.Code(err) != codes.Canceled && !errors.Is(err, context.Canceled) {
		t.Fatalf("stream cancellation = %v", err)
	}

	select {
	case <-backend.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("backend history stream was not cancelled")
	}
}
