// Package buildkit adapts Docker's BuildKit protocol to an external buildkitd.
package buildkit

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	control "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client/buildid"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry/api"
	"github.com/sysson/dink/core/registry/pullauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type Publisher interface {
	IssueBuildCredential(context.Context, []string) (api.BuildCredential, error)
	RevokeBuildCredential(context.Context, string) error
}

type Gateway struct {
	control.UnimplementedControlServer
	conn      *grpc.ClientConn
	control   control.ControlClient
	publisher Publisher
	registry  *url.URL
	manager   *session.Manager
	mu        sync.Mutex
	sessions  map[string]*bridge
}

func (g *Gateway) Enabled() bool { return g.control != nil }

func New(cfg config.BuildKit, registryURL string, publisher Publisher) (*Gateway, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	manager, err := session.NewManager()
	if err != nil {
		return nil, err
	}
	g := &Gateway{publisher: publisher, manager: manager, sessions: make(map[string]*bridge)}
	if cfg.URL == "" {
		return g, nil
	}
	if publisher == nil {
		return nil, errors.New("BuildKit requires a registry publisher")
	}
	if cfg.RegistryURL != "" {
		registryURL = cfg.RegistryURL
	}
	g.registry, err = url.Parse(registryURL)
	if err != nil || g.registry.Host == "" || (g.registry.Scheme != "http" && g.registry.Scheme != "https") || g.registry.Path != "" && g.registry.Path != "/" || g.registry.User != nil || g.registry.RawQuery != "" || g.registry.Fragment != "" {
		return nil, fmt.Errorf("BuildKit registry address must be an http(s) origin")
	}
	backend, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	transport := credentials.TransportCredentials(insecure.NewCredentials())
	if cfg.CAFile != "" || cfg.CertFile != "" || cfg.ServerName != "" {
		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("reading BuildKit CA: %w", err)
			}
			tlsConfig.RootCAs = x509.NewCertPool()
			if !tlsConfig.RootCAs.AppendCertsFromPEM(pem) {
				return nil, errors.New("BuildKit CA contains no certificates")
			}
		}
		if cfg.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("loading BuildKit client certificate: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		transport = credentials.NewTLS(tlsConfig)
	}
	target := "passthrough:///" + backend.Host
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20), grpc.MaxCallSendMsgSize(16<<20)),
	}
	if backend.Scheme == "unix" {
		target = "passthrough:///buildkit"
		opts = append(opts, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", backend.Path)
		}))
	}
	g.conn, err = grpc.NewClient(target, opts...)
	if err != nil {
		return nil, err
	}
	g.control = control.NewControlClient(g.conn)
	return g, nil
}

func (g *Gateway) Close() error {
	g.mu.Lock()
	for _, b := range g.sessions {
		b.cancel()
	}
	g.mu.Unlock()
	if g.conn != nil {
		return g.conn.Close()
	}
	return nil
}

func (g *Gateway) GRPCServer() *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(16<<20), grpc.MaxSendMsgSize(16<<20),
		grpc.UnknownServiceHandler(g.serveGateway),
	)
	control.RegisterControlServer(server, g)
	contentapi.RegisterContentServer(server, &historyContent{gateway: g})
	return server
}

func (g *Gateway) check(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok || !pullauth.ValidNamespace(id.Namespace) {
		return identity.Identity{}, status.Error(codes.Unauthenticated, "build requests require a tenant identity")
	}
	if g.control == nil {
		return id, status.Error(codes.Unavailable, "BuildKit backend is not configured")
	}
	return id, nil
}

func tenantRef(id identity.Identity, ref string) string {
	sum := sha256.Sum256([]byte(id.Namespace + "\x00" + id.CommonName + "\x00" + ref))
	return hex.EncodeToString(sum[:])
}

func (g *Gateway) Info(ctx context.Context, request *control.InfoRequest) (*control.InfoResponse, error) {
	if _, err := g.check(ctx); err != nil {
		return nil, err
	}
	return g.control.Info(ctx, request)
}

func (g *Gateway) ListWorkers(ctx context.Context, request *control.ListWorkersRequest) (*control.ListWorkersResponse, error) {
	if _, err := g.check(ctx); err != nil {
		return nil, err
	}
	response, err := g.control.ListWorkers(ctx, request)
	if err != nil {
		return nil, err
	}
	response = proto.Clone(response).(*control.ListWorkersResponse)
	for _, worker := range response.Record {
		// Buildx interprets this label as Docker's containerd image-store
		// capability, which this gateway does not implement.
		delete(worker.Labels, "org.mobyproject.buildkit.worker.snapshotter")
	}
	return response, nil
}

func (g *Gateway) Solve(ctx context.Context, request *control.SolveRequest) (response *control.SolveResponse, retErr error) {
	id, err := g.check(ctx)
	if err != nil {
		return nil, err
	}
	if request.Ref == "" || request.Session == "" {
		return nil, status.Error(codes.InvalidArgument, "build ref and session are required")
	}
	if request.Definition != nil || len(request.FrontendInputs) != 0 || request.SourcePolicy != nil || request.SourcePolicySession != "" || len(request.Entitlements) != 0 || request.ProxyNetwork {
		return nil, status.Error(codes.Unimplemented, "raw LLB, source policies and privileged build entitlements are not supported")
	}
	if err := g.validateFrontendSessions(id, request.FrontendAttrs, request.Session); err != nil {
		return nil, err
	}
	b, err := g.getBridge(ctx, request.Session, id)
	if err != nil {
		return nil, err
	}
	if !b.solving.CompareAndSwap(false, true) {
		return nil, status.Error(codes.FailedPrecondition, "only one build per session is supported")
	}
	defer b.solving.Store(false)
	ctx, cancel := context.WithTimeout(ctx, pullauth.BuildCredentialLifetime-time.Minute)
	defer cancel()
	stopSessionCancel := context.AfterFunc(b.ctx, cancel)
	defer stopSessionCancel()
	request = proto.Clone(request).(*control.SolveRequest)
	request.Ref = historyRef(id, request.Ref)
	request.FrontendAttrs = maps.Clone(request.FrontendAttrs)
	if request.FrontendAttrs == nil {
		request.FrontendAttrs = make(map[string]string)
	}
	delete(request.FrontendAttrs, historyNamesAttr)
	delete(request.FrontendAttrs, historyExporterAttr)
	if request.ExporterDeprecated != "" && len(request.Exporters) == 0 {
		request.Exporters = []*control.Exporter{{Type: request.ExporterDeprecated, Attrs: request.ExporterAttrsDeprecated}}
		request.ExporterDeprecated = ""
		request.ExporterAttrsDeprecated = nil
	}
	mobyIndex := -1
	originalNames := ""
	pushUpstream := false
	for i, exporter := range request.Exporters {
		if exporter.Type != "moby" {
			continue
		}
		if mobyIndex != -1 {
			return nil, status.Error(codes.Unimplemented, "multiple moby exporters are not supported")
		}
		mobyIndex = i
		if value := exporter.Attrs["push"]; value != "" {
			pushUpstream, err = strconv.ParseBool(value)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, "invalid image exporter push flag")
			}
		}
		if exporter.Attrs["push-by-digest"] != "" && exporter.Attrs["push-by-digest"] != "false" {
			return nil, status.Error(codes.Unimplemented, "push-by-digest is not supported")
		}
	}
	if mobyIndex >= 0 {
		exporter := request.Exporters[mobyIndex]
		originalNames = exporter.Attrs["name"]
		if exporter.Attrs["name"] == "" {
			return nil, status.Error(codes.Unimplemented, "this build gateway requires an image tag (-t)")
		}
		if pushUpstream {
			if err := validatePushNames(originalNames, g.registry.Host); err != nil {
				return nil, err
			}
			upstream := proto.Clone(exporter).(*control.Exporter)
			upstream.Type = "image"
			upstream.Attrs["push"] = "true"
			upstream.Attrs["store"] = "false"
			// Keep upstream TLS/export options separate from the internal registry.
			request.Exporters = append(request.Exporters, upstream)
		}
		credential, err := g.publisher.IssueBuildCredential(ctx, strings.Split(exporter.Attrs["name"], ","))
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "issuing build credential: %v", err)
		}
		b.setCredential(&credential)
		defer func() {
			b.setCredential(nil)
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cleanupCancel()
			if err := g.publisher.RevokeBuildCredential(cleanupCtx, credential.Username); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("revoking build credential: %w", err))
			}
		}()
		names := make([]string, len(credential.References))
		for i, ref := range credential.References {
			names[i] = g.registry.Host + "/" + ref
		}
		exporter.Type = "image"
		exporter.Attrs = maps.Clone(exporter.Attrs)
		exporter.Attrs["name"] = strings.Join(names, ",")
		exporter.Attrs["push"] = "true"
		exporter.Attrs["store"] = "false"
		exporter.Attrs["registry.insecure"] = fmt.Sprint(g.registry.Scheme == "http")
		request.FrontendAttrs[historyNamesAttr] = originalNames
		request.FrontendAttrs[historyExporterAttr] = fmt.Sprint(mobyIndex)
	}
	response, retErr = g.control.Solve(ctx, request)
	if retErr == nil && mobyIndex >= 0 && response != nil {
		response = proto.Clone(response).(*control.SolveResponse)
		if response.ExporterResponse == nil {
			response.ExporterResponse = make(map[string]string)
		}
		response.ExporterResponse["image.name"] = originalNames
		// Dinki identifies images by manifest/index digest. Buildx otherwise
		// prefers the config digest for Docker-driver --iidfile output.
		delete(response.ExporterResponse, "containerimage.config.digest")
	}
	return response, retErr
}

func (g *Gateway) Status(request *control.StatusRequest, server control.Control_StatusServer) error {
	id, err := g.check(server.Context())
	if err != nil {
		return err
	}
	request = proto.Clone(request).(*control.StatusRequest)
	request.Ref = historyRef(id, request.Ref)
	stream, err := g.control.Status(server.Context(), request)
	if err != nil {
		return err
	}
	return relayResponses(stream, server)
}

func (g *Gateway) serveGateway(_ any, server grpc.ServerStream) error {
	id, err := g.check(server.Context())
	if err != nil {
		return err
	}
	method, _ := grpc.MethodFromServerStream(server)
	ref := buildid.FromIncomingContext(server.Context())
	if !strings.HasPrefix(method, "/moby.buildkit.v1.frontend.LLBBridge/") || ref == "" {
		return status.Error(codes.Unimplemented, "only build-scoped frontend gateway calls are supported")
	}
	ctx, cancel := context.WithCancel(server.Context())
	defer cancel()
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	md.Set("buildkit-controlapi-buildid", historyRef(id, ref))
	ctx = metadata.NewOutgoingContext(ctx, md)
	frame := new(emptypb.Empty)
	if err := server.RecvMsg(frame); err != nil {
		return err
	}
	if strings.HasSuffix(method, "/Solve") {
		request := new(gatewaypb.SolveRequest)
		data, err := proto.Marshal(frame)
		if err != nil {
			return err
		}
		if err := proto.Unmarshal(data, request); err != nil {
			return err
		}
		if request.Definition != nil || len(request.FrontendInputs) != 0 || len(request.SourcePolicies) != 0 {
			return status.Error(codes.Unimplemented, "raw LLB and frontend source policies are not supported")
		}
		if err := g.validateFrontendSessions(id, request.FrontendOpt, ""); err != nil {
			return err
		}
	}
	return forwardRPC(ctx, g.conn, method, frame, server)
}
