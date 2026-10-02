package buildkit_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	control "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/buildid"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/session/grpchijack"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/v2/daemon/server/imagebackend"
	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry"
	"github.com/sysson/dink/core/registry/api"
	apiserver "github.com/sysson/dink/core/registry/api/server"
	"github.com/sysson/dink/core/registry/pullauth"
	registryserver "github.com/sysson/dink/core/registry/server"
	distributionrouter "github.com/sysson/dink/core/server/router/distribution"
	grpcrouter "github.com/sysson/dink/core/server/router/grpc"
	imagerouter "github.com/sysson/dink/core/server/router/image"
	"github.com/sysson/dink/core/translator"
	"github.com/sysson/dink/core/types"
	"github.com/sysson/ocistore"
	"github.com/sysson/ocistore/blobstore/memblob"
	"github.com/sysson/ocistore/kv/memkv"
	"github.com/sysson/ocistore/kvmeta"
	"github.com/sysson/ocistore/query"
	"github.com/sysson/syskit/httpx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type fixture struct {
	gateway     *buildkit.Gateway
	grpcServer  *grpc.Server
	api         *api.Client
	credentials *pullauth.Store
	publisher   *recordingPublisher
	registryURL string
}

type recordingPublisher struct {
	*api.Client
	mu         sync.Mutex
	credential api.BuildCredential
	issued     chan struct{}
	revoked    chan string
}

func (p *recordingPublisher) IssueBuildCredential(ctx context.Context, names []string) (api.BuildCredential, error) {
	credential, err := p.Client.IssueBuildCredential(ctx, names)
	p.mu.Lock()
	p.credential = credential
	p.mu.Unlock()
	p.issued <- struct{}{}
	return credential, err
}

func (p *recordingPublisher) RevokeBuildCredential(ctx context.Context, username string) error {
	err := p.Client.RevokeBuildCredential(ctx, username)
	p.revoked <- username
	return err
}

func (p *recordingPublisher) lastCredential() api.BuildCredential {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.credential
}

func newFixture(t *testing.T, backend string) *fixture {
	return newFixtureTLS(t, backend, nil)
}

func newFixtureTLS(t *testing.T, backend string, registryTLS *tls.Config) *fixture {
	t.Helper()
	ctx := context.Background()
	blobs, err := (memblob.Config{}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kv := memkv.New()
	meta, err := kvmeta.New(ctx, kv)
	if err != nil {
		t.Fatal(err)
	}
	store, err := ocistore.New(blobs, meta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = meta.Close(); _ = blobs.Close() })
	queries, err := query.New(store.Index())
	if err != nil {
		t.Fatal(err)
	}
	credentials := pullauth.New(kv)
	apiServer, err := apiserver.New(registry.New(store, store.Index()), queries, credentials)
	if err != nil {
		t.Fatal(err)
	}
	path, handler := apiServer.Handler()
	ociHandler, err := registryserver.New(pullauth.Scope(store))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	mux.Handle("/", pullauth.Middleware(credentials)(ociHandler))
	registryHTTP := httptest.NewUnstartedServer(mux)
	if os.Getenv("DINK_BUILDKIT_TEST_REGISTRY_HOST") != "" {
		listener, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		_ = registryHTTP.Listener.Close()
		registryHTTP.Listener = listener
	}
	if registryTLS != nil {
		registryHTTP.TLS = registryTLS
		registryHTTP.StartTLS()
	} else {
		registryHTTP.Start()
	}
	t.Cleanup(registryHTTP.Close)
	registryURL, _ := url.Parse(registryHTTP.URL)
	registryURL.Host = net.JoinHostPort("127.0.0.1", registryURL.Port())
	publisher := &recordingPublisher{Client: api.NewClient(registryHTTP.Client(), registryURL.String()), issued: make(chan struct{}, 8), revoked: make(chan string, 8)}
	publishURL := registryURL.String()
	if host := os.Getenv("DINK_BUILDKIT_TEST_REGISTRY_HOST"); host != "" {
		registryURL.Host = net.JoinHostPort(host, registryURL.Port())
		publishURL = registryURL.String()
	}
	gateway, err := buildkit.New(config.BuildKit{URL: backend}, publishURL, publisher)
	if err != nil {
		t.Fatal(err)
	}
	gs := gateway.GRPCServer()
	t.Cleanup(func() { gs.Stop(); _ = gateway.Close() })
	return &fixture{gateway: gateway, grpcServer: gs, api: publisher.Client, publisher: publisher, credentials: credentials, registryURL: registryHTTP.URL}
}

type grpcBackend struct {
	control.UnimplementedControlServer
	manager  *session.Manager
	fail     bool
	block    bool
	push     bool
	pushFail bool
	request  chan *control.SolveRequest
	host     string
}

func newBackend(t *testing.T) (*grpcBackend, string) {
	t.Helper()
	manager, err := session.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	backend := &grpcBackend{manager: manager, request: make(chan *control.SolveRequest, 8)}
	gs := grpc.NewServer()
	control.RegisterControlServer(gs, backend)
	gatewaypb.RegisterLLBBridgeServer(gs, &gatewayBackend{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(listener) }()
	t.Cleanup(gs.Stop)
	return backend, "tcp://" + listener.Addr().String()
}

func (b *grpcBackend) Session(stream control.Control_SessionServer) error {
	conn, _, headers := grpchijack.Hijack(stream)
	return b.manager.HandleConn(stream.Context(), conn, headers)
}

func (b *grpcBackend) Status(_ *control.StatusRequest, _ control.Control_StatusServer) error {
	return nil
}

func (b *grpcBackend) Solve(ctx context.Context, request *control.SolveRequest) (*control.SolveResponse, error) {
	b.request <- proto.Clone(request).(*control.SolveRequest)
	caller, err := b.manager.Get(ctx, request.Session, false)
	if err != nil {
		return nil, err
	}
	authClient := auth.NewAuthClient(caller.Conn())
	upstream, err := authClient.Credentials(ctx, &auth.CredentialsRequest{Host: "upstream.example"})
	if err != nil || upstream.Username != "upstream-user" || upstream.Secret != "upstream-secret" {
		return nil, fmt.Errorf("upstream auth was not preserved: %v %v", upstream, err)
	}
	stream, err := caller.Conn().NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, "/test.Session/Echo")
	if err != nil {
		return nil, err
	}
	if err := stream.SendMsg(wrapperspb.String("context payload")); err != nil {
		return nil, err
	}
	if err := stream.CloseSend(); err != nil {
		return nil, err
	}
	reply := new(wrapperspb.StringValue)
	if err := stream.RecvMsg(reply); err != nil || reply.Value != "context payload" {
		return nil, fmt.Errorf("session payload was not preserved: %v %v", reply, err)
	}
	credential, err := authClient.Credentials(ctx, &auth.CredentialsRequest{Host: b.host})
	if err != nil {
		return nil, err
	}
	if b.fail {
		return nil, status.Error(codes.Internal, "test export failure")
	}
	if b.block {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	expectedExporters := 1
	if b.push {
		expectedExporters = 2
	}
	if len(request.Exporters) != expectedExporters || request.Exporters[0].Type != "image" || request.Exporters[0].Attrs["push"] != "true" {
		return nil, errors.New("incorrect exporter adaptation")
	}
	if b.push {
		upstreamExporter := request.Exporters[1]
		if upstreamExporter.Type != "image" || upstreamExporter.Attrs["push"] != "true" || upstreamExporter.Attrs["store"] != "false" {
			return nil, errors.New("incorrect upstream push adaptation")
		}
	}
	exporter := request.Exporters[0]
	registryClient, err := registry.NewClient("http://"+b.host, registry.ClientOptions{
		Auth: &registrytypes.AuthConfig{Username: credential.Username, Password: credential.Secret},
	})
	if err != nil {
		return nil, err
	}
	var digest string
	for name := range strings.SplitSeq(exporter.Attrs["name"], ",") {
		reference, err := ociref.Parse(name)
		if err != nil {
			return nil, err
		}
		config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"config":{},"history":[]}`)
		desc := oci.Descriptor{Digest: ocidigest.FromBytes(config), Size: int64(len(config)), MediaType: "application/vnd.oci.image.config.v1+json"}
		if _, err := registryClient.PushBlob(ctx, reference.Repository, desc, bytes.NewReader(config)); err != nil {
			return nil, err
		}
		manifest, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": desc, "layers": []oci.Descriptor{},
		})
		if err != nil {
			return nil, err
		}
		result, err := registryClient.PushManifest(ctx, reference.Repository, manifest, "application/vnd.oci.image.manifest.v1+json", &oci.PushManifestParameters{Tags: []string{reference.Tag}})
		if err != nil {
			return nil, err
		}
		digest = string(result.Digest)
	}
	if b.pushFail {
		return nil, status.Error(codes.PermissionDenied, "upstream registry denied the push")
	}
	return &control.SolveResponse{ExporterResponse: map[string]string{"containerimage.digest": digest, "containerimage.config.digest": "config-digest"}}, nil
}

type gatewayBackend struct {
	gatewaypb.UnimplementedLLBBridgeServer
}

func (b *gatewayBackend) Ping(ctx context.Context, _ *gatewaypb.PingRequest) (*gatewaypb.PongResponse, error) {
	if ref := buildid.FromIncomingContext(ctx); !strings.HasPrefix(ref, "dink-history-") || !strings.HasSuffix(ref, "-YnVpbGQtcmVm") {
		return nil, errors.New("gateway build ref was not tenant-scoped")
	}
	return &gatewaypb.PongResponse{}, nil
}

type testSessionAuth struct {
	auth.UnimplementedAuthServer
}

func (p *testSessionAuth) Register(gs *grpc.Server) { auth.RegisterAuthServer(gs, p) }
func (p *testSessionAuth) Credentials(context.Context, *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	return &auth.CredentialsResponse{Username: "upstream-user", Secret: "upstream-secret"}, nil
}

type echoSession struct{}

func (*echoSession) Register(gs *grpc.Server) {
	gs.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Session", HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName: "Echo", ClientStreams: true, ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				for {
					message := new(wrapperspb.StringValue)
					if err := stream.RecvMsg(message); err != nil {
						if errors.Is(err, io.EOF) {
							return nil
						}
						return err
					}
					if err := stream.SendMsg(message); err != nil {
						return err
					}
				}
			},
		}},
	}, &echoSession{})
}

type grpcProvider struct{ server *grpc.Server }

func (p grpcProvider) GRPCServer() *grpc.Server { return p.server }

type fixtureImagePush struct {
	imagerouter.Translator
	client *api.Client
}

func (p *fixtureImagePush) PushImage(ctx context.Context, ref ociref.Reference, options imagebackend.PushOptions) error {
	return p.client.PushImage(ctx, ref, options)
}

func (p *fixtureImagePush) ImageInspect(ctx context.Context, name string, options imagebackend.ImageInspectOpts) (*imagebackend.InspectData, error) {
	return p.client.ImageInspect(ctx, name, options)
}

func gatewayHTTP(t *testing.T, f *fixture) string {
	t.Helper()
	legacy := grpcrouter.New(grpcProvider{f.grpcServer}).Routes()[0].Handler()
	var push, inspect httpx.HTTPErrorFunc
	for _, route := range imagerouter.New(&fixtureImagePush{client: f.api}, f.api).Routes() {
		if route.Path() == "/images/{name:.*}/push" {
			push = route.Handler()
		}
		if route.Path() == "/images/{name:.*}/json" {
			inspect = route.Handler()
		}
	}
	distribution := distributionrouter.New(&translator.Docker{}).Routes()[0].Handler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		namespace := r.Header.Get("X-Test-Tenant")
		if namespace == "" {
			namespace = "tenant"
		}
		r = r.WithContext(identity.NewContext(r.Context(), identity.Identity{Namespace: namespace, CommonName: "client"}))
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			f.grpcServer.ServeHTTP(w, r)
			return
		}
		var err error
		path := r.URL.Path
		if strings.HasPrefix(path, "/v") {
			_, path, _ = strings.Cut(strings.TrimPrefix(path, "/"), "/")
			path = "/" + path
		}
		switch path {
		case "/grpc":
			err = legacy(w, r)
		case "/session":
			err = f.gateway.HandleHTTPRequest(r.Context(), w, r)
		case "/_ping":
			w.Header().Set("API-Version", "1.56")
			w.Header().Set("Builder-Version", "2")
			w.Header().Set("Docker-Experimental", "false")
			if r.Method != http.MethodHead {
				_, err = w.Write([]byte("OK"))
			}
		case "/version":
			err = json.NewEncoder(w).Encode(map[string]any{
				"Version": "29.8.0", "ApiVersion": "1.56", "MinAPIVersion": "1.44", "Os": "linux", "Arch": "amd64",
			})
		case "/info":
			err = json.NewEncoder(w).Encode(map[string]any{"ID": "dink-build-test", "ServerVersion": "29.8.0", "OSType": "linux", "Architecture": "x86_64"})
		default:
			switch {
			case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/push"):
				r.SetPathValue("name", strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/push"))
				httpx.ErrorLogger(push)(w, r)
			case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
				r.SetPathValue("name", strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json"))
				httpx.ErrorLogger(inspect)(w, r)
			case strings.HasPrefix(path, "/distribution/") && strings.HasSuffix(path, "/json"):
				r.SetPathValue("name", strings.TrimSuffix(strings.TrimPrefix(path, "/distribution/"), "/json"))
				httpx.ErrorLogger(distribution)(w, r)
			default:
				http.NotFound(w, r)
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("gateway HTTP: %v", err)
		}
	})
	server := httptest.NewUnstartedServer(handler)
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server.Config.Protocols = protocols
	server.Start()
	t.Cleanup(server.Close)
	return server.URL
}

func upgradeDialer(baseURL, path string) session.Dialer {
	return func(ctx context.Context, protocol string, headers map[string][]string) (net.Conn, error) {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, nil)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		request.Header = make(http.Header)
		for name, values := range headers {
			request.Header[http.CanonicalHeaderKey(name)] = values
		}
		request.Header.Set("Upgrade", protocol)
		request.Header.Set("Connection", "Upgrade")
		if err := request.Write(conn); err != nil {
			_ = conn.Close()
			return nil, err
		}
		buffered := bufio.NewReader(conn)
		response, err := http.ReadResponse(buffered, request)
		if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
			_ = conn.Close()
			return nil, fmt.Errorf("upgrade failed: %v %v", response, err)
		}
		return &readBufferedConn{Conn: conn, reader: buffered}, nil
	}
}

type readBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *readBufferedConn) Read(data []byte) (int, error) { return c.reader.Read(data) }

func TestGatewayPublishesAndPreservesSessions(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			backend, address := newBackend(t)
			f := newFixture(t, address)
			registryURL, _ := url.Parse(f.registryURL)
			backend.host = registryURL.Host
			httpURL := gatewayHTTP(t, f)
			httpAddress, _ := url.Parse(httpURL)
			var opts []client.ClientOpt
			if legacy {
				opts = []client.ClientOpt{
					client.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
						return upgradeDialer(httpURL, "/grpc")(ctx, "h2c", nil)
					}),
					client.WithSessionDialer(upgradeDialer(httpURL, "/session")),
				}

			}
			bk, err := client.New(context.Background(), "tcp://"+httpAddress.Host, opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = bk.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			response, err := bk.Solve(ctx, nil, client.SolveOpt{
				Frontend: "dockerfile.v0",
				Exports:  []client.ExportEntry{{Type: "moby", Attrs: map[string]string{"name": "docker.io/library/app:test,ghcr.io/team/app:v1"}}},
				Session:  []session.Attachable{&testSessionAuth{}, &echoSession{}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.ExporterResponse["containerimage.digest"] == "" {
				t.Fatal("missing exported image digest")
			}
			if response.ExporterResponse["containerimage.config.digest"] != "" {
				t.Fatal("Docker-driver image ID would differ from Dinki's manifest ID")
			}
			request := <-backend.request
			if request.Exporters[0].Attrs["store"] != "false" || request.Exporters[0].Attrs["registry.insecure"] != "true" || !strings.Contains(request.Exporters[0].Attrs["name"], "/tenant/app:test") {
				t.Fatalf("backend exporter = %v", request.Exporters[0])
			}
			if !strings.HasPrefix(request.Ref, "dink-history-") || request.FrontendAttrs["dink.history.original-image-names"] != "docker.io/library/app:test,ghcr.io/team/app:v1" || request.FrontendAttrs["dink.history.exporter-index"] != "0" {
				t.Fatalf("missing durable history ownership or image metadata: %v", request)
			}
			tenant := identity.NewContext(ctx, identity.Identity{Namespace: "tenant"})
			images, err := f.api.Images(tenant, types.ImageListOptions{})
			if err != nil || len(images) == 0 {
				t.Fatalf("published images = %v %v", images, err)
			}
			other := identity.NewContext(ctx, identity.Identity{Namespace: "other"})
			images, err = f.api.Images(other, types.ImageListOptions{})
			if err != nil || len(images) != 0 {
				t.Fatalf("other tenant images = %v %v", images, err)
			}
			credential := f.publisher.lastCredential()
			if namespace, _, err := f.credentials.VerifyBuild(ctx, credential.Username, credential.Password); err != nil || namespace != "" {
				t.Fatalf("build credential was not revoked: %q %v", namespace, err)
			}
			connection, err := grpc.NewClient("passthrough:///"+httpAddress.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = connection.Close() }()
			if _, err := gatewaypb.NewLLBBridgeClient(connection).Ping(buildid.AppendToOutgoingContext(ctx, "build-ref"), &gatewaypb.PingRequest{}); err != nil {
				t.Fatalf("gateway protocol: %v", err)
			}
		})
	}
}
