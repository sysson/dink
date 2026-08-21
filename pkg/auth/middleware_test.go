package auth

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sysson/dink/pkg/identity"
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
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Original", "must-not-leak")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"result":"original"}`))
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
	if got := string(client.request.GetResponseBody()); got != `{"result":"original"}` {
		t.Errorf("response body sent to plugin = %q", got)
	}
}

func TestMiddlewareCommitsAllowedResponse(t *testing.T) {
	client := &testPluginClient{response: &authv1.AuthZResResponse{Allow: true}}
	handler := Middleware(&Chain{plugins: []plugin{{name: "test", client: client}}})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Result", "created")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"result":"created"}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/containers/json", nil)
	req = req.WithContext(identity.NewContext(req.Context(), identity.Identity{Namespace: "tenant", CommonName: "alice"}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", res.Code, http.StatusCreated)
	}
	if got := res.Body.String(); got != `{"result":"created"}` {
		t.Errorf("body = %q", got)
	}
	if got := res.Header().Get("X-Result"); got != "created" {
		t.Errorf("response header = %q", got)
	}
}

func TestRequestBodyPeeksAndPreservesJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/containers/create", strings.NewReader(`{"image":"alpine"}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	body, err := requestBody(req)
	if err != nil {
		t.Fatalf("requestBody returned error: %v", err)
	}
	if got := string(body); got != `{"image":"alpine"}` {
		t.Errorf("authorized body = %q", got)
	}
	remaining, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("reading preserved body: %v", err)
	}
	if got := string(remaining); got != `{"image":"alpine"}` {
		t.Errorf("downstream body = %q", got)
	}
}

func TestFlattenHeaderMatchesDocker(t *testing.T) {
	headers := http.Header{
		"Authorization":        {"secret"},
		"X-Registry-Auth":      {"secret"},
		"X-Registry-Config":    {"secret"},
		"X-Authorization-Info": {"safe"},
		"X-Multi":              {"first", "last"},
	}

	flattened := flattenHeader(headers)
	if _, ok := flattened["Authorization"]; ok {
		t.Error("Authorization header was forwarded")
	}
	if _, ok := flattened["X-Registry-Auth"]; ok {
		t.Error("X-Registry-Auth header was forwarded")
	}
	if _, ok := flattened["X-Registry-Config"]; ok {
		t.Error("X-Registry-Config header was forwarded")
	}
	if got := flattened["X-Authorization-Info"]; got != "safe" {
		t.Errorf("non-sensitive auth-named header = %q, want %q", got, "safe")
	}
	if got := flattened["X-Multi"]; got != "last" {
		t.Errorf("multi-value header = %q, want %q", got, "last")
	}
}

func TestRequestBodySkipsNonJSON(t *testing.T) {
	bodyReader := strings.NewReader(strings.Repeat("x", maxBodySize+1))
	req := httptest.NewRequest(http.MethodPost, "/exec/id/start", bodyReader)
	req.Header.Set("Content-Type", "application/octet-stream")

	body, err := requestBody(req)
	if err != nil {
		t.Fatalf("requestBody returned error: %v", err)
	}
	if body != nil {
		t.Fatalf("authorized body = %d bytes, want nil", len(body))
	}
	// The body must remain untouched; reading it verifies that no peek occurred.
	remaining, readErr := io.ReadAll(req.Body)
	if readErr != nil {
		t.Fatalf("reading non-JSON body: %v", readErr)
	}
	if len(remaining) != maxBodySize+1 {
		t.Errorf("non-JSON body length = %d, want %d", len(remaining), maxBodySize+1)
	}
}

func TestRequestBodyRejectsOversizedJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/containers/create", strings.NewReader(strings.Repeat("x", maxBodySize+1)))
	req.Header.Set("Content-Type", "application/json")

	if _, err := requestBody(req); err == nil {
		t.Fatal("requestBody returned nil error for oversized JSON")
	}
}

func TestResponseModifierFlushesAfterLimit(t *testing.T) {
	res := httptest.NewRecorder()
	modifier := newResponseModifier(res)
	body := bytes.Repeat([]byte("x"), maxBufferSize+1)

	if _, err := modifier.Write(body); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if modifier.rawBody() != nil {
		t.Fatal("raw response body retained after automatic flush")
	}
	if got := res.Body.Len(); got != len(body) {
		t.Errorf("flushed response length = %d, want %d", got, len(body))
	}
}

func TestResponseBodySkipsNonJSON(t *testing.T) {
	body := []byte("raw stream")
	if got := responseBody("/containers/id/logs", http.Header{"Content-Type": {"application/octet-stream"}}, body); got != nil {
		t.Fatalf("non-JSON response body = %q, want nil", got)
	}
	if got := responseBody("/containers/id/json", http.Header{"Content-Type": {"application/json"}}, body); string(got) != string(body) {
		t.Fatalf("JSON response body = %q, want %q", got, body)
	}
}

type testHijackWriter struct {
	header http.Header
	conn   net.Conn
}

func (w *testHijackWriter) Header() http.Header { return w.header }

func (w *testHijackWriter) Write(body []byte) (int, error) { return len(body), nil }

func (w *testHijackWriter) WriteHeader(int) {}

func (w *testHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestResponseModifierDelegatesHijack(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()
	defer func() { _ = clientConn.Close() }()

	writer := &testHijackWriter{header: make(http.Header), conn: serverConn}
	modifier := newResponseModifier(writer)
	conn, _, err := modifier.Hijack()
	if err != nil {
		t.Fatalf("Hijack returned error: %v", err)
	}
	if conn != serverConn {
		t.Fatal("Hijack did not return the underlying connection")
	}
	if !modifier.hijacked {
		t.Fatal("modifier was not marked hijacked")
	}
}
