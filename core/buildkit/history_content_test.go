package buildkit

import (
	"context"
	"testing"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	control "github.com/moby/buildkit/api/services/control"
	"github.com/opencontainers/go-digest"
	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type historyContentBackend struct {
	contentapi.UnimplementedContentServer
	requests chan string
}

func (b *historyContentBackend) Info(_ context.Context, req *contentapi.InfoRequest) (*contentapi.InfoResponse, error) {
	b.requests <- req.Digest
	return &contentapi.InfoResponse{Info: &contentapi.Info{Digest: req.Digest, Size: 7, Labels: map[string]string{"backend": "foreign-record"}}}, nil
}

func (b *historyContentBackend) Read(req *contentapi.ReadContentRequest, stream contentapi.Content_ReadServer) error {
	b.requests <- req.Digest
	if err := stream.SendHeader(metadata.Pairs("content-test", "header")); err != nil {
		return err
	}
	stream.SetTrailer(metadata.Pairs("content-test", "trailer"))
	return stream.Send(&contentapi.ReadContentResponse{Offset: req.Offset, Data: []byte("payload")})
}

type historyReadCapture struct {
	grpc.ServerStream
	ctx      context.Context
	response *contentapi.ReadContentResponse
	headers  metadata.MD
	trailer  metadata.MD
}

func (s *historyReadCapture) Context() context.Context { return s.ctx }
func (s *historyReadCapture) SendHeader(md metadata.MD) error {
	s.headers = md
	return nil
}
func (s *historyReadCapture) SetTrailer(md metadata.MD) { s.trailer = md }
func (s *historyReadCapture) Send(response *contentapi.ReadContentResponse) error {
	s.response = response
	return nil
}
func (s *historyReadCapture) SendMsg(message any) error {
	data, err := proto.Marshal(message.(proto.Message))
	if err != nil {
		return err
	}
	s.response = new(contentapi.ReadContentResponse)
	return proto.Unmarshal(data, s.response)
}

func TestHistoryContentReadsRequireOwnedDescriptors(t *testing.T) {
	owner := identity.Identity{Namespace: "tenant", CommonName: "client"}
	foreign := identity.Identity{Namespace: "other", CommonName: "client"}
	ownedDigest := digest.FromString("owned").String()
	foreignDigest := digest.FromString("foreign").String()
	content := &historyContentBackend{requests: make(chan string, 8)}
	ownEvent := historyEvent(owner, "owned-build")
	ownEvent.Record.Result = &control.BuildResultInfo{Attestations: []*control.Descriptor{{Digest: ownedDigest}}}
	foreignEvent := historyEvent(foreign, "foreign-build")
	foreignEvent.Record.Logs = &control.Descriptor{Digest: foreignDigest}
	backend := &historyBackend{events: []*control.BuildHistoryEvent{foreignEvent, ownEvent}, content: content}
	g := newHistoryGateway(t, backend)
	proxy := &historyContent{gateway: g}
	ctx := historyContext(t, owner)
	response, err := proxy.Info(ctx, &contentapi.InfoRequest{Digest: ownedDigest})
	if err != nil {
		t.Fatal(err)
	}
	if response.Info.Digest != ownedDigest || response.Info.Size != 7 || len(response.Info.Labels) != 0 {
		t.Fatalf("unsafe or incomplete content info: %v", response)
	}
	if <-content.requests != ownedDigest {
		t.Fatal("incorrect content request")
	}
	capture := &historyReadCapture{ctx: ctx}
	if err := proxy.Read(&contentapi.ReadContentRequest{Digest: ownedDigest, Offset: 2, Size: 7}, capture); err != nil {
		t.Fatal(err)
	}
	if <-content.requests != ownedDigest || capture.response.Offset != 2 || string(capture.response.Data) != "payload" {
		t.Fatalf("content stream was not preserved: %v", capture.response)
	}
	if capture.headers.Get("content-test")[0] != "header" || capture.trailer.Get("content-test")[0] != "trailer" {
		t.Fatal("content stream metadata was not forwarded")
	}
	for _, value := range []string{foreignDigest, digest.FromString("unreferenced").String()} {
		if _, err := proxy.Info(ctx, &contentapi.InfoRequest{Digest: value}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthorized info %q = %v", value, err)
		}
		if err := proxy.Read(&contentapi.ReadContentRequest{Digest: value}, &historyReadCapture{ctx: ctx}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthorized read %q = %v", value, err)
		}
	}
	if _, err := proxy.Info(ctx, &contentapi.InfoRequest{Digest: "invalid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid digest: %v", err)
	}
	if _, err := proxy.Info(context.Background(), &contentapi.InfoRequest{Digest: ownedDigest}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated content: %v", err)
	}
	if _, err := proxy.Delete(ctx, &contentapi.DeleteContentRequest{Digest: ownedDigest}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("content mutation was allowed: %v", err)
	}
	select {
	case request := <-content.requests:
		t.Fatalf("unauthorized request reached backend: %s", request)
	default:
	}
}

func TestHistoryDescriptorCoverage(t *testing.T) {
	value := digest.FromString("descriptor").String()
	descriptor := &control.Descriptor{Digest: value}
	for _, record := range []*control.BuildHistoryRecord{
		{Logs: descriptor}, {Trace: descriptor}, {ExternalError: descriptor},
		{Result: &control.BuildResultInfo{ResultDeprecated: descriptor}},
		{Result: &control.BuildResultInfo{Attestations: []*control.Descriptor{descriptor}}},
		{Result: &control.BuildResultInfo{Results: map[int64]*control.Descriptor{0: descriptor}}},
		{Results: map[string]*control.BuildResultInfo{"linux/amd64": {Attestations: []*control.Descriptor{descriptor}}}},
	} {
		if !historyReferencesDigest(record, value) || historyReferencesDigest(record, digest.FromString("other").String()) {
			t.Fatalf("incorrect descriptor authorization: %v", record)
		}
	}
	if historyReferencesDigest(&control.BuildHistoryRecord{}, value) {
		t.Fatal("empty record authorized a digest")
	}
}
