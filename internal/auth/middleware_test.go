package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sysson/dink/internal/identity"
	authv1 "github.com/sysson/dink/sdk/auth/v1"
)

type testPluginClient struct {
	response *authv1.AuthZResResponse
	request  *authv1.AuthZResRequest
}

func (c *testPluginClient) AuthZReq(context.Context, *authv1.AuthZReqRequest) (*authv1.AuthZReqResponse, error) {
	return &authv1.AuthZReqResponse{Allow: true}, nil
}

func (c *testPluginClient) AuthZRes(_ context.Context, req *authv1.AuthZResRequest) (*authv1.AuthZResResponse, error) {
	c.request = req
	return c.response, nil
}

func TestMiddlewareDeniesResponse(t *testing.T) {
	client := &testPluginClient{response: &authv1.AuthZResResponse{Msg: "policy violation"}}
	handler := Middleware(&Chain{plugins: []plugin{{name: "test", client: client}}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Original", "must-not-leak")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("original response"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/containers/json", nil)
	req = req.WithContext(identity.NewContext(req.Context(), identity.Identity{Namespace: "tenant", CommonName: "alice"}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
	if got := res.Body.String(); got != "response denied by authorization plugin: policy violation\n" {
		t.Errorf("body = %q", got)
	}
	if got := res.Header().Get("X-Original"); got != "" {
		t.Errorf("original response header leaked: %q", got)
	}
	if got := string(client.request.GetResponseBody()); got != "original response" {
		t.Errorf("response body sent to plugin = %q", got)
	}
}

func TestMiddlewareCommitsAllowedResponse(t *testing.T) {
	client := &testPluginClient{response: &authv1.AuthZResResponse{Allow: true}}
	handler := Middleware(&Chain{plugins: []plugin{{name: "test", client: client}}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Result", "created")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created response"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/containers/json", nil)
	req = req.WithContext(identity.NewContext(req.Context(), identity.Identity{Namespace: "tenant", CommonName: "alice"}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", res.Code, http.StatusCreated)
	}
	if got := res.Body.String(); got != "created response" {
		t.Errorf("body = %q", got)
	}
	if got := res.Header().Get("X-Result"); got != "created" {
		t.Errorf("response header = %q", got)
	}
}
