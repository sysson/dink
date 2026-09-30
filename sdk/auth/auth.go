// Package auth lets a plugin authorize Docker API requests and responses.
package auth

import (
	"context"

	authv1 "github.com/sysson/dink/sdk/auth/v1"
	"github.com/sysson/dink/sdk/auth/v1/authconnect"
	"github.com/sysson/dink/sdk/plugin"
)

type Request struct {
	Namespace       string
	User            string
	UserAuthNMethod string
	RequestMethod   string
	RequestURI      string
	// RequestBody is only set for JSON bodies.
	RequestBody    []byte
	RequestHeaders map[string]string
	// RequestPeerCertificates holds the client's certificate chain, each JSON-encoded.
	RequestPeerCertificates [][]byte
}

type Response struct {
	Request
	ResponseBody       []byte
	ResponseHeaders    map[string]string
	ResponseStatusCode int
}

type Decision struct {
	Allow bool
	// Msg is returned to the Docker client when the request is denied.
	Msg string
}

// Authorizer is implemented by auth plugins. Returning an error denies the request.
type Authorizer interface {
	AuthorizeRequest(ctx context.Context, req *Request) (Decision, error)
	AuthorizeResponse(ctx context.Context, res *Response) (Decision, error)
}

// Plugin serves a as the plugin's auth implementation.
func Plugin(a Authorizer) plugin.Option {
	path, h := authconnect.NewAuthPluginServiceHandler(handler{a: a})
	return plugin.WithService(plugin.TypeAuth, path, h)
}

type handler struct {
	a Authorizer
}

func (h handler) AuthZReq(ctx context.Context, in *authv1.AuthZReqRequest) (*authv1.AuthZReqResponse, error) {
	d, err := h.a.AuthorizeRequest(ctx, &Request{
		Namespace:               in.GetNamespace(),
		User:                    in.GetUser(),
		UserAuthNMethod:         in.GetUserAuthNMethod(),
		RequestMethod:           in.GetRequestMethod(),
		RequestURI:              in.GetRequestURI(),
		RequestBody:             in.GetRequestBody(),
		RequestHeaders:          in.GetRequestHeaders(),
		RequestPeerCertificates: in.GetRequestPeerCertificates(),
	})
	if err != nil {
		return nil, err
	}
	return &authv1.AuthZReqResponse{Allow: d.Allow, Msg: d.Msg}, nil
}

func (h handler) AuthZRes(ctx context.Context, in *authv1.AuthZResRequest) (*authv1.AuthZResResponse, error) {
	d, err := h.a.AuthorizeResponse(ctx, &Response{
		Namespace:               in.GetNamespace(),
		User:                    in.GetUser(),
		UserAuthNMethod:         in.GetUserAuthNMethod(),
		RequestMethod:           in.GetRequestMethod(),
		RequestURI:              in.GetRequestURI(),
		RequestBody:             in.GetRequestBody(),
		RequestHeaders:          in.GetRequestHeaders(),
		RequestPeerCertificates: in.GetRequestPeerCertificates(),
		ResponseBody:            in.GetResponseBody(),
		ResponseHeaders:         in.GetResponseHeaders(),
		ResponseStatusCode:      int(in.GetResponseStatusCode()),
	})
	if err != nil {
		return nil, err
	}
	return &authv1.AuthZResResponse{Allow: d.Allow, Msg: d.Msg}, nil
}
