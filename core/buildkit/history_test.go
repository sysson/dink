package buildkit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/v2/pkg/filters"
	control "github.com/moby/buildkit/api/services/control"
	bktypes "github.com/moby/buildkit/api/types"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type historyBackend struct {
	control.UnimplementedControlServer
	events    []*control.BuildHistoryEvent
	request   chan *control.BuildHistoryRequest
	updates   chan *control.UpdateBuildHistoryRequest
	cancelled chan struct{}
	filter    bool
	wait      bool
	content   *historyContentBackend
}

func (b *historyBackend) ListenBuildHistory(req *control.BuildHistoryRequest, stream control.Control_ListenBuildHistoryServer) error {
	b.request <- proto.Clone(req).(*control.BuildHistoryRequest)
	if err := stream.SendHeader(metadata.Pairs("history-test", "header")); err != nil {
		return err
	}
	stream.SetTrailer(metadata.Pairs("history-test", "trailer"))
	matcher, err := filters.ParseAll(req.Filter...)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	var sent int32
	for _, event := range b.events {
		if b.filter {
			if event.Record == nil || req.Ref != "" && event.Record.Ref != req.Ref {
				continue
			}
			if !matcher.Match(filters.AdapterFunc(func(path []string) (string, bool) {
				if len(path) == 1 && path[0] == "ref" {
					return event.Record.Ref, true
				}
				return "", false
			})) {
				continue
			}
			if req.Limit > 0 && sent >= req.Limit {
				break
			}
		}
		if err := stream.Send(event); err != nil {
			return err
		}
		sent++
	}
	if b.wait {
		<-stream.Context().Done()
		close(b.cancelled)
		return status.FromContextError(stream.Context().Err()).Err()
	}
	return nil
}

func (b *historyBackend) UpdateBuildHistory(_ context.Context, req *control.UpdateBuildHistoryRequest) (*control.UpdateBuildHistoryResponse, error) {
	b.updates <- proto.Clone(req).(*control.UpdateBuildHistoryRequest)
	return &control.UpdateBuildHistoryResponse{}, nil
}

func (b *historyBackend) Info(context.Context, *control.InfoRequest) (*control.InfoResponse, error) {
	return &control.InfoResponse{BuildkitVersion: &bktypes.BuildkitVersion{Version: "v0.33.1"}}, nil
}

func (b *historyBackend) ListWorkers(context.Context, *control.ListWorkersRequest) (*control.ListWorkersResponse, error) {
	return &control.ListWorkersResponse{Record: []*bktypes.WorkerRecord{{ID: "history-worker", BuildkitVersion: &bktypes.BuildkitVersion{Version: "v0.33.1"}}}}, nil
}

func newHistoryGateway(t *testing.T, backend *historyBackend) *Gateway {
	t.Helper()
	backend.request = make(chan *control.BuildHistoryRequest, 8)
	backend.updates = make(chan *control.UpdateBuildHistoryRequest, 8)
	backend.cancelled = make(chan struct{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	control.RegisterControlServer(server, backend)
	if backend.content != nil {
		contentapi.RegisterContentServer(server, backend.content)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Gateway{control: control.NewControlClient(conn), conn: conn}
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

func TestHistoryScopesBeforeBackendLimitAndAcrossRestarts(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	backend := &historyBackend{filter: true, events: []*control.BuildHistoryEvent{
		historyEvent(identity.Identity{Namespace: "foreign", CommonName: "client"}, "foreign"),
		historyEvent(owner, "first"),
		historyEvent(owner, "second"),
	}}
	for range 2 {
		g := newHistoryGateway(t, backend)
		capture := &historyCapture{ctx: historyContext(t, owner)}
		if err := g.ListenBuildHistory(&control.BuildHistoryRequest{EarlyExit: true, Limit: 1}, capture); err != nil {
			t.Fatal(err)
		}
		if len(capture.events) != 1 || capture.events[0].Record.Ref != "first" {
			t.Fatalf("limit was applied before ownership: %v", capture.events)
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
	for _, filter := range []string{"status==completed", "repository==https://example.com/refs/main", ""} {
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

func TestBuildxHistoryListThroughDockerUpgrade(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker CLI is not installed")
	}
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	event := historyEvent(owner, "history-cli-build")
	event.Type = control.BuildHistoryEventType_COMPLETE
	event.Record.Frontend = "dockerfile.v0"
	event.Record.FrontendAttrs = map[string]string{"filename": "Dockerfile"}
	event.Record.CreatedAt = timestamppb.New(time.Now().Add(-time.Minute))
	event.Record.CompletedAt = timestamppb.Now()
	backend := &historyBackend{filter: true, events: []*control.BuildHistoryEvent{
		historyEvent(identity.Identity{Namespace: "foreign", CommonName: "client"}, "foreign-build"),
		event,
	}}
	g := newHistoryGateway(t, backend)
	gs := g.GRPCServer()
	t.Cleanup(gs.Stop)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := identity.NewContext(r.Context(), owner)
		switch {
		case r.URL.Path == "/grpc":
			conn, err := Hijack(w, r)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			if err := ServeHTTP2(ctx, conn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gs.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), owner)))
			})); err != nil {
				t.Errorf("serving upgraded history: %v", err)
			}
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.56")
			w.Header().Set("Builder-Version", "2")
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte("OK"))
			}
		case strings.HasSuffix(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "29.8.0", "ApiVersion": "1.56", "MinAPIVersion": "1.44", "Os": "linux", "Arch": "amd64"})
		case strings.HasSuffix(r.URL.Path, "/info"):
			_ = json.NewEncoder(w).Encode(map[string]string{"ID": "history-test", "ServerVersion": "29.8.0", "OSType": "linux", "Architecture": "x86_64"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "--host", "tcp://"+strings.TrimPrefix(server.URL, "http://"), "buildx", "history", "ls", "--builder", "default", "--format", "json")
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if !strings.HasPrefix(name, "DOCKER_") && !strings.HasPrefix(name, "BUILDX_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "DOCKER_CONFIG="+t.TempDir())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("buildx history ls: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "history-cli-build") || strings.Contains(string(output), "foreign-build") || strings.Contains(string(output), historyPrefix(owner)) {
		t.Fatalf("unexpected CLI history output: %s", output)
	}
	t.Logf("buildx history ls: %s", output)

}
