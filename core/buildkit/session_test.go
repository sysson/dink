package buildkit

import (
	"context"
	"testing"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/sysson/dink/core/registry/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type authCaller struct {
	session.Caller
	conn *grpc.ClientConn
}

func (c *authCaller) Conn() *grpc.ClientConn { return c.conn }

type upstreamAuth struct {
	auth.UnimplementedAuthServer
}

func (*upstreamAuth) Credentials(_ context.Context, request *auth.CredentialsRequest) (*auth.CredentialsResponse, error) {
	if request.Host != "upstream.test" {
		return nil, status.Error(codes.PermissionDenied, "unknown upstream registry")
	}
	return &auth.CredentialsResponse{Username: "upstream-user", Secret: "upstream-secret"}, nil
}

func (*upstreamAuth) FetchToken(_ context.Context, request *auth.FetchTokenRequest) (*auth.FetchTokenResponse, error) {
	return &auth.FetchTokenResponse{Token: request.Host, ExpiresIn: 60}, nil
}

func (*upstreamAuth) GetTokenAuthority(_ context.Context, request *auth.GetTokenAuthorityRequest) (*auth.GetTokenAuthorityResponse, error) {
	return &auth.GetTokenAuthorityResponse{PublicKey: []byte(request.Host)}, nil
}

func (*upstreamAuth) VerifyTokenAuthority(_ context.Context, request *auth.VerifyTokenAuthorityRequest) (*auth.VerifyTokenAuthorityResponse, error) {
	return &auth.VerifyTokenAuthorityResponse{Signed: []byte(request.Host)}, nil
}

func TestAuthProxyKeepsInternalCredentialsSeparateFromClientAuth(t *testing.T) {
	conn := newUnitConnection(t, func(server *grpc.Server) { auth.RegisterAuthServer(server, &upstreamAuth{}) })
	b := &bridge{caller: &authCaller{conn: conn}}
	proxy := &authProxy{bridge: b, host: "dinki.test"}
	ctx := t.Context()
	if _, err := proxy.Credentials(ctx, &auth.CredentialsRequest{Host: "dinki.test"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("internal credentials outside solve = %v", err)
	}
	b.setCredential(&api.BuildCredential{Username: "internal-user", Password: "internal-secret"})
	for _, test := range []struct{ host, username, secret string }{
		{"dinki.test", "internal-user", "internal-secret"},
		{"upstream.test", "upstream-user", "upstream-secret"},
	} {
		response, err := proxy.Credentials(ctx, &auth.CredentialsRequest{Host: test.host})
		if err != nil || response.Username != test.username || response.Secret != test.secret {
			t.Fatalf("credential routing for %s: %v %v", test.host, response, err)
		}
	}
	if _, err := proxy.Credentials(ctx, &auth.CredentialsRequest{Host: "denied.test"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("upstream credential error lost: %v", err)
	}
	b.setCredential(nil)
	if _, err := proxy.Credentials(ctx, &auth.CredentialsRequest{Host: "dinki.test"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("credentials remained available after solve = %v", err)
	}
	for _, host := range []string{"dinki.test", "upstream.test"} {
		token, tokenErr := proxy.FetchToken(ctx, &auth.FetchTokenRequest{Host: host})
		authority, authorityErr := proxy.GetTokenAuthority(ctx, &auth.GetTokenAuthorityRequest{Host: host})
		signature, signatureErr := proxy.VerifyTokenAuthority(ctx, &auth.VerifyTokenAuthorityRequest{Host: host})
		if host == "dinki.test" {
			for _, err := range []error{tokenErr, authorityErr, signatureErr} {
				if status.Code(err) != codes.Unimplemented {
					t.Fatalf("internal token auth was not rejected: %v", err)
				}
			}
		} else if tokenErr != nil || authorityErr != nil || signatureErr != nil || token.Token != host || token.ExpiresIn != 60 ||
			string(authority.PublicKey) != host || string(signature.Signed) != host {
			t.Fatalf("upstream token services not forwarded: %v %v %v", tokenErr, authorityErr, signatureErr)
		}
	}
}

func TestSessionProxyRegistersOnlyValidUnreservedMethods(t *testing.T) {
	server := grpc.NewServer()
	defer server.Stop()
	proxy := &sessionProxy{methods: []string{
		"/test.Session/Echo", "/test.Session/Echo", "/test.Session/Copy",
		"/moby.filesync.v1.Auth/Credentials", "/grpc.health.v1.Health/Check",
		"invalid", "/test.Session/", "/test.Session/invalid/path",
	}}
	proxy.Register(server)
	services := server.GetServiceInfo()
	service, ok := services["test.Session"]
	if !ok || len(services) != 1 || len(service.Methods) != 2 {
		t.Fatalf("unexpected exposed services: %v", services)
	}
	for _, method := range service.Methods {
		if method.Name != "Echo" && method.Name != "Copy" || !method.IsClientStream || !method.IsServerStream {
			t.Fatalf("incorrect forwarded method: %v", method)
		}
	}
}
