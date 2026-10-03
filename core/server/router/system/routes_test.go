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

	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	systemtypes "github.com/moby/moby/api/types/system"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
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

func TestGetInfoEncodesSwarmDurations(t *testing.T) {
	heartbeat := time.Second
	translator := &infoTranslatorStub{info: &systemtypes.Info{
		Name: "dink",
		Swarm: swarmtypes.Info{Cluster: &swarmtypes.ClusterInfo{
			Spec: swarmtypes.Spec{Dispatcher: swarmtypes.DispatcherConfig{HeartbeatPeriod: heartbeat}},
		}},
	}}
	router := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodGet, "/info", nil)
	response := httptest.NewRecorder()

	if err := router.getInfo(response, request); err != nil {
		t.Fatal(err)
	}
	var info systemtypes.Info
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatalf("invalid /info JSON: %v; body = %s", err, response.Body.String())
	}
	if got := info.Swarm.Cluster.Spec.Dispatcher.HeartbeatPeriod; got != heartbeat {
		t.Fatalf("heartbeat period = %s, want %s", got, heartbeat)
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

type diskUsageTranslatorStub struct {
	Translator
	options backend.DiskUsageOptions
}

func (s *diskUsageTranslatorStub) SystemDiskUsage(_ context.Context, options backend.DiskUsageOptions) (*backend.DiskUsage, error) {
	s.options = options
	usage := &backend.DiskUsage{}
	if options.Images {
		usage.Images = &backend.ImageDiskUsage{TotalCount: 1, TotalSize: 10, Items: []image.Summary{{ID: "sha256:a", Size: 10}}}
	}
	if options.Volumes {
		usage.Volumes = &backend.VolumeDiskUsage{TotalCount: 1}
	}
	return usage, nil
}

type buildDiskUsageStub struct {
	options buildbackend.DiskUsageOptions
}

func (s *buildDiskUsageStub) DiskUsage(_ context.Context, options buildbackend.DiskUsageOptions) (*buildbackend.DiskUsage, error) {
	s.options = options
	return &buildbackend.DiskUsage{
		TotalCount: 1, TotalSize: 42, ActiveCount: 1, Reclaimable: 10,
		Items: []buildtypes.CacheRecord{{ID: "cache-id", Size: 42}},
	}, nil
}

func TestGetDiskUsage(t *testing.T) {
	for _, test := range []struct {
		name, apiVersion, query string
		check                   func(t *testing.T, body map[string]json.RawMessage, options backend.DiskUsageOptions)
	}{
		{
			name: "legacy", apiVersion: "1.51",
			check: func(t *testing.T, body map[string]json.RawMessage, options backend.DiskUsageOptions) {
				if !options.Containers || !options.Images || !options.Volumes || !options.Verbose {
					t.Fatalf("options = %+v, want everything verbose", options)
				}
				if string(body["LayersSize"]) != "10" || body["Images"] == nil || body["ImageUsage"] != nil {
					t.Fatalf("legacy body = %v", body)
				}
			},
		},
		{
			name: "typed verbose", apiVersion: "1.52", query: "type=image&verbose=1",
			check: func(t *testing.T, body map[string]json.RawMessage, options backend.DiskUsageOptions) {
				if options.Containers || !options.Images || options.Volumes || !options.Verbose {
					t.Fatalf("options = %+v, want verbose images only", options)
				}
				if body["ImageUsage"] == nil || body["Images"] != nil || body["VolumeUsage"] != nil {
					t.Fatalf("body = %v", body)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			translator := &diskUsageTranslatorStub{}
			api := &systemRouter{translator: translator}
			request := httptest.NewRequest(http.MethodGet, "/system/df?"+test.query, nil)
			request.SetPathValue("version", test.apiVersion)
			response := httptest.NewRecorder()
			if err := api.getDiskUsage(response, request); err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			test.check(t, body, translator.options)
		})
	}

	api := &systemRouter{translator: &diskUsageTranslatorStub{}}
	request := httptest.NewRequest(http.MethodGet, "/system/df?type=bogus", nil)
	if err := api.getDiskUsage(httptest.NewRecorder(), request); err == nil {
		t.Fatal("unknown object type was accepted")
	}
}

func TestGetDiskUsageIncludesBuildCache(t *testing.T) {
	for _, test := range []struct {
		name, apiVersion, query string
		legacy                  bool
	}{
		{name: "legacy", apiVersion: "1.51", query: "type=build-cache", legacy: true},
		{name: "typed verbose", apiVersion: "1.52", query: "type=build-cache&verbose=1"},
		{name: "typed summary", apiVersion: "1.52", query: "type=build-cache"},
	} {
		t.Run(test.name, func(t *testing.T) {
			builder := &buildDiskUsageStub{}
			api := &systemRouter{translator: &diskUsageTranslatorStub{}, builder: builder}
			request := httptest.NewRequest(http.MethodGet, "/system/df?"+test.query, nil)
			request.SetPathValue("version", test.apiVersion)
			response := httptest.NewRecorder()

			if err := api.getDiskUsage(response, request); err != nil {
				t.Fatal(err)
			}
			if !builder.options.Verbose {
				t.Fatalf("BuildKit options = %+v", builder.options)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid disk usage JSON: %v; body = %s", err, response.Body.String())
			}
			if test.legacy {
				var report struct {
					BuildCache []buildtypes.CacheRecord `json:"BuildCache"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				if len(report.BuildCache) != 1 || report.BuildCache[0].ID != "cache-id" {
					t.Fatalf("legacy BuildCache = %+v", report.BuildCache)
				}
				return
			}
			if body["BuildCacheUsage"] == nil {
				t.Fatalf("summary disk usage body = %s", response.Body.String())
			}
			var report struct {
				BuildCacheUsage buildtypes.DiskUsage `json:"BuildCacheUsage"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.BuildCacheUsage.TotalCount != 1 || report.BuildCacheUsage.TotalSize != 42 || len(report.BuildCacheUsage.Items) != 1 {
				t.Fatalf("BuildCacheUsage = %+v", report.BuildCacheUsage)
			}
		})
	}
}
