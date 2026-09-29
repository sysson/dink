package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/registry"
	systemtypes "github.com/moby/moby/api/types/system"
	"github.com/sysson/dink/pkg/filters"
)

type authTranslator struct {
	Translator
	auth  *registry.AuthConfig
	token string
}

func (s *authTranslator) AuthenticateToRegistry(_ context.Context, auth *registry.AuthConfig) (string, error) {
	s.auth = auth
	return s.token, nil
}

func TestPostAuthUsesJSONBody(t *testing.T) {
	translator := &authTranslator{token: "identity-token"}
	router := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(`{"username":"alice","password":"secret","serveraddress":"registry.example"}`))
	request.Header.Set("X-Registry-Auth", "not-the-login-request-body")
	response := httptest.NewRecorder()

	if err := router.postAuth(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if translator.auth == nil || translator.auth.ServerAddress != "registry.example" || translator.auth.Username != "alice" || translator.auth.Password != "secret" {
		t.Fatalf("auth passed to translator = %+v", translator.auth)
	}
	var result registry.AuthResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.IdentityToken != "identity-token" {
		t.Fatalf("identity token = %q, want %q", result.IdentityToken, "identity-token")
	}
}

type infoTranslatorStub struct {
	Translator
	info *systemtypes.Info
}

func (s *infoTranslatorStub) SystemInfo(context.Context) (*systemtypes.Info, error) {
	return s.info, nil
}

func TestGetInfo(t *testing.T) {
	translator := &infoTranslatorStub{info: &systemtypes.Info{
		Name:            "dink",
		OperatingSystem: "Kubernetes-backed Dink",
		Containers:      2,
		Warnings:        []string{"Counts are scoped to the authenticated namespace."},
	}}
	router := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodGet, "/info", nil)
	response := httptest.NewRecorder()

	if err := router.getInfo(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var info systemtypes.Info
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "dink" || info.OperatingSystem != "Kubernetes-backed Dink" || info.Containers != 2 || len(info.Warnings) != 1 {
		t.Fatalf("system info = %+v", info)
	}
}

type eventTranslatorStub struct {
	Translator
	history []events.Message
	stream  chan any
	since   time.Time
	filters filters.Args
	closed  chan any
}

func (s *eventTranslatorStub) SubscribeToEvents(_ context.Context, since, _ time.Time, eventFilters filters.Args) ([]events.Message, chan any, error) {
	s.since, s.filters = since, eventFilters
	return s.history, s.stream, nil
}

func (s *eventTranslatorStub) UnsubscribeFromEvents(_ context.Context, stream chan any) error {
	s.closed = stream
	return nil
}

func TestGetEventsStreamsJSONAndUsesQueryFilters(t *testing.T) {
	since := time.Unix(100, 0)
	historical := events.Message{Type: events.ContainerEventType, Action: "create", Actor: events.Actor{ID: "old"}}
	live := events.Message{Type: events.ContainerEventType, Action: "start", Actor: events.Actor{ID: "live"}}
	stream := make(chan any, 1)
	stream <- live
	close(stream)
	translator := &eventTranslatorStub{history: []events.Message{historical}, stream: stream}
	api := &systemRouter{translator: translator}
	query := url.Values{
		"since":   []string{"100"},
		"filters": []string{`{"type":{"container":true}}`},
	}
	request := httptest.NewRequest(http.MethodGet, "/events?"+query.Encode(), nil)
	response := httptest.NewRecorder()

	if err := api.getEvents(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || !response.Flushed {
		t.Fatalf("response = status %d, content type %q, flushed %t", response.Code, response.Header().Get("Content-Type"), response.Flushed)
	}
	decoder := json.NewDecoder(response.Body)
	for _, want := range []events.Message{historical, live} {
		var got events.Message
		if err := decoder.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || got.Action != want.Action || got.Actor.ID != want.Actor.ID {
			t.Fatalf("event = %+v, want %+v", got, want)
		}
	}
	if translator.since != since || !translator.filters.ExactMatch("type", "container") || translator.closed != stream {
		t.Fatalf("subscription args: since=%v filters=%+v unsubscribed=%t", translator.since, translator.filters, translator.closed == stream)
	}
}

func TestGetEventsRejectsInvalidTimestamp(t *testing.T) {
	translator := &eventTranslatorStub{stream: make(chan any)}
	api := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodGet, "/events?since=not-a-timestamp", nil)
	response := httptest.NewRecorder()

	if err := api.getEvents(response, request); err == nil {
		t.Fatal("expected invalid timestamp error")
	}
	if !translator.since.IsZero() {
		t.Fatalf("translator called for invalid timestamp: %v", translator.since)
	}
}

type eventStreamResponseWriter struct {
	header  http.Header
	status  int
	flushed chan struct{}
}

func (w *eventStreamResponseWriter) Header() http.Header {
	return w.header
}

func (w *eventStreamResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *eventStreamResponseWriter) Write(value []byte) (int, error) {
	return len(value), nil
}

func (w *eventStreamResponseWriter) Flush() {
	w.flushed <- struct{}{}
}

func TestGetEventsFlushesHeadersBeforeFirstEvent(t *testing.T) {
	stream := make(chan any)
	translator := &eventTranslatorStub{stream: stream}
	api := &systemRouter{translator: translator}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	response := &eventStreamResponseWriter{header: make(http.Header), flushed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		done <- api.getEvents(response, request)
	}()

	select {
	case <-response.flushed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("event response headers were not flushed before an event arrived")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if response.status != http.StatusOK || response.header.Get("Content-Type") != "application/json" {
		t.Fatalf("event response = %d, headers %v", response.status, response.header)
	}
	if translator.closed != stream {
		t.Fatal("event subscription was not closed after client cancellation")
	}
}
