package container

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types"
	dockertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/pkg/filters"
)

type lifecycleStub struct {
	Translator
	call           string
	name           string
	signal         string
	stopOptions    backend.ContainerStopOptions
	restartOptions backend.ContainerStopOptions
	remove         *backend.ContainerRmConfig
	listOptions    *backend.ContainerListOptions
	inspectName    string
	inspectOptions backend.ContainerInspectOptions
	pruneFilters   filters.Args
	logOptions     *backend.ContainerLogsOptions
	waitCondition  dockertypes.WaitCondition
	waitEntered    chan struct{}
	waitRelease    chan struct{}
	waitError      error
	statsOptions   *backend.ContainerStatsConfig
	updateConfig   *dockertypes.UpdateConfig
	execRequest    *dockertypes.ExecCreateRequest
	execHeight     uint32
	execWidth      uint32
	resizeHeight   uint32
	resizeWidth    uint32
}

func (s *lifecycleStub) ContainerExecResize(_ context.Context, _ string, height, width uint32) error {
	s.execHeight, s.execWidth = height, width
	return nil
}

func (s *lifecycleStub) ContainerResize(_ context.Context, _ string, height, width uint32) error {
	s.resizeHeight, s.resizeWidth = height, width
	return nil
}

func (s *lifecycleStub) ContainerExecCreate(_ context.Context, _ string, request *dockertypes.ExecCreateRequest) (string, error) {
	s.execRequest = request
	return "exec-id", nil
}

func (s *lifecycleStub) ContainerUpdate(_ context.Context, _ string, config *dockertypes.UpdateConfig) (dockertypes.UpdateResponse, error) {
	s.updateConfig = config
	return dockertypes.UpdateResponse{Warnings: []string{"updated"}}, nil
}

func (s *lifecycleStub) ContainerExecInspect(_ context.Context, _ string) (*dockertypes.ExecInspectResponse, error) {
	return &dockertypes.ExecInspectResponse{ID: "exec-id", ProcessConfig: &dockertypes.ExecProcessConfig{}, OpenStdout: true, OpenStderr: true}, nil
}

func (s *lifecycleStub) ContainerExecStart(_ context.Context, _ string, streams backend.ExecStartConfig) error {
	_, _ = streams.Stdout.Write([]byte("hello\n"))
	_, _ = streams.Stderr.Write([]byte("error\n"))
	return nil
}

func (s *lifecycleStub) ContainerAttach(_ context.Context, _ string, config *backend.ContainerAttachConfig) error {
	stdin, stdout, stderr, err := config.GetStreams(true, func() {})
	if err != nil {
		return err
	}
	defer func() { _ = stdin.Close() }()
	_, _ = stdout.Write([]byte("attached\n"))
	_, _ = stderr.Write([]byte("stderr\n"))
	return nil
}

func (s *lifecycleStub) ContainerStats(_ context.Context, _ string, options *backend.ContainerStatsConfig) error {
	s.statsOptions = options
	_, err := options.OutStream().Write([]byte(`{"read":"2026-01-01T00:00:00Z"}`))
	return err
}

func (s *lifecycleStub) ContainerWait(_ context.Context, _ string, condition dockertypes.WaitCondition) (dockertypes.WaitResponse, error) {
	s.waitCondition = condition
	if s.waitRelease != nil {
		close(s.waitEntered)
		<-s.waitRelease
	}
	if s.waitError != nil {
		return dockertypes.WaitResponse{}, s.waitError
	}
	return dockertypes.WaitResponse{StatusCode: 42}, nil
}

func (s *lifecycleStub) ContainerLogs(_ context.Context, _ string, options *backend.ContainerLogsOptions) (<-chan *backend.LogMessage, bool, error) {
	s.logOptions = options
	messages := make(chan *backend.LogMessage, 1)
	messages <- &backend.LogMessage{Source: "stdout", Line: []byte("hello\n"), Timestamp: time.Unix(1, 0)}
	close(messages)
	return messages, false, nil
}

func (s *lifecycleStub) ContainerPrune(_ context.Context, pruneFilters filters.Args) (*dockertypes.PruneReport, error) {
	s.pruneFilters = pruneFilters
	return &dockertypes.PruneReport{ContainersDeleted: []string{"container-id"}}, nil
}

func (s *lifecycleStub) Containers(_ context.Context, options *backend.ContainerListOptions) ([]dockertypes.Summary, error) {
	s.listOptions = options
	return []dockertypes.Summary{{ID: "container-id", Names: []string{"/web"}, State: dockertypes.StateRunning}}, nil
}

func (s *lifecycleStub) ContainerInspect(_ context.Context, name string, options backend.ContainerInspectOptions) (*dockertypes.InspectResponse, network.HardwareAddr, error) {
	s.inspectName, s.inspectOptions = name, options
	return &dockertypes.InspectResponse{ID: "container-id", Name: "/" + name}, nil, nil
}

func (s *lifecycleStub) ContainerKill(_ context.Context, name, signal string) error {
	s.call, s.name, s.signal = "kill", name, signal
	return nil
}

func (s *lifecycleStub) ContainerPause(_ context.Context, name string) error {
	s.call, s.name = "pause", name
	return nil
}

func (s *lifecycleStub) ContainerUnpause(_ context.Context, name string) error {
	s.call, s.name = "unpause", name
	return nil
}

func (s *lifecycleStub) ContainerRestart(_ context.Context, name string, options backend.ContainerStopOptions) error {
	s.call, s.name, s.restartOptions = "restart", name, options
	return nil
}

func (s *lifecycleStub) ContainerStart(_ context.Context, name, _, _ string) error {
	s.call, s.name = "start", name
	return nil
}

func (s *lifecycleStub) ContainerStop(_ context.Context, name string, options backend.ContainerStopOptions) error {
	s.call, s.name, s.stopOptions = "stop", name, options
	return nil
}

func (s *lifecycleStub) ContainerRm(_ context.Context, name string, config *backend.ContainerRmConfig) error {
	s.call, s.name, s.remove = "remove", name, config
	return nil
}

func TestContainerLifecycleRoutes(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request) error
		method  string
		path    string
		call    string
	}{
		{"kill", api.postContainersKill, http.MethodPost, "/containers/web/kill?signal=SIGKILL", "kill"},
		{"pause", api.postContainersPause, http.MethodPost, "/containers/web/pause", "pause"},
		{"unpause", api.postContainersUnpause, http.MethodPost, "/containers/web/unpause", "unpause"},
		{"restart", api.postContainersRestart, http.MethodPost, "/containers/web/restart?t=3", "restart"},
		{"start", api.postContainersStart, http.MethodPost, "/containers/web/start", "start"},
		{"stop", api.postContainersStop, http.MethodPost, "/containers/web/stop?signal=SIGTERM", "stop"},
		{"remove", api.deleteContainers, http.MethodDelete, "/containers/web?force=true&v=false&link=false", "remove"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub.call = ""
			request := httptest.NewRequest(test.method, test.path, nil)
			request.SetPathValue("name", "web")
			response := httptest.NewRecorder()
			if err := test.handler(response, request); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("response = %d %q, want 204 with no body", response.Code, response.Body.String())
			}
			if stub.call != test.call || stub.name != "web" {
				t.Fatalf("translator call = %q(%q), want %q(web)", stub.call, stub.name, test.call)
			}
		})
	}
	if stub.signal != "SIGKILL" {
		t.Fatalf("kill signal = %q, want SIGKILL", stub.signal)
	}
	if stub.restartOptions.Timeout == nil || *stub.restartOptions.Timeout != 3 {
		t.Fatalf("restart timeout = %v, want 3", stub.restartOptions.Timeout)
	}
	if stub.stopOptions.Signal != "SIGTERM" {
		t.Fatalf("stop signal = %q, want SIGTERM", stub.stopOptions.Signal)
	}
	if !stub.remove.ForceRemove || stub.remove.RemoveVolume || stub.remove.RemoveLink {
		t.Fatalf("remove options = %+v", stub.remove)
	}
}

func TestContainerUpdateRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/containers/web/update", strings.NewReader(`{"Memory":128,"MemoryReservation":64}`))
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.postContainerUpdate(response, request); err != nil {
		t.Fatal(err)
	}
	var updateResponse dockertypes.UpdateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &updateResponse); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(updateResponse.Warnings) != 1 || stub.updateConfig == nil || stub.updateConfig.Memory != 128 || stub.updateConfig.MemoryReservation != 64 {
		t.Fatalf("update response = %d %q, config = %+v", response.Code, response.Body.String(), stub.updateConfig)
	}
}

func TestContainerInspectAndListRoutes(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	filters := url.QueryEscape(`{"name":{"web":true}}`)
	request := httptest.NewRequest(http.MethodGet, "/containers/json?all=true&size=true&limit=5&filters="+filters, nil)
	response := httptest.NewRecorder()
	if err := api.getContainersJSON(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", response.Code)
	}
	var summaries []dockertypes.Summary
	if err := json.Unmarshal(response.Body.Bytes(), &summaries); err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != "container-id" {
		t.Fatalf("list response = %+v", summaries)
	}
	if !stub.listOptions.All || !stub.listOptions.Size || stub.listOptions.Limit != 5 || !stub.listOptions.Filters.Match("name", "web") {
		t.Fatalf("list options = %+v", stub.listOptions)
	}

	inspectRequest := httptest.NewRequest(http.MethodGet, "/containers/web/json?size=true", nil)
	inspectRequest.SetPathValue("name", "web")
	inspectResponse := httptest.NewRecorder()
	if err := api.getContainersByName(inspectResponse, inspectRequest); err != nil {
		t.Fatal(err)
	}
	if inspectResponse.Code != http.StatusOK || !strings.Contains(inspectResponse.Body.String(), `"Name":"/web"`) {
		t.Fatalf("inspect response = %d %q", inspectResponse.Code, inspectResponse.Body.String())
	}
	if stub.inspectName != "web" || !stub.inspectOptions.Size {
		t.Fatalf("inspect request = %q, %+v", stub.inspectName, stub.inspectOptions)
	}
}

func TestContainerPruneRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/containers/prune?filters="+url.QueryEscape(`{"label":["app=web"]}`), nil)
	response := httptest.NewRecorder()
	if err := api.postContainersPrune(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ContainersDeleted":["container-id"]`) || !stub.pruneFilters.MatchKVList("label", map[string]string{"app": "web"}) {
		t.Fatalf("prune response = %d %q, filters = %+v", response.Code, response.Body.String(), stub.pruneFilters)
	}
}

func TestContainerLogsRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodGet, "/containers/web/logs?stdout=1&stderr=1&tail=10&timestamps=1", nil)
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.getContainersLogs(response, request); err != nil {
		t.Fatal(err)
	}
	data := response.Body.Bytes()
	if response.Code != http.StatusOK || len(data) < 8 || data[0] != 1 || int(binary.BigEndian.Uint32(data[4:8])) != len(data)-8 ||
		!strings.HasSuffix(string(data[8:]), "hello\n") || stub.logOptions.Tail != "10" || !stub.logOptions.Timestamps {
		t.Fatalf("logs response = %d %q, options = %+v", response.Code, data, stub.logOptions)
	}
	bad := httptest.NewRequest(http.MethodGet, "/containers/web/logs?stderr=1", nil)
	if err := api.getContainersLogs(httptest.NewRecorder(), bad); err == nil {
		t.Fatal("accepted stderr-only logs")
	}
}

func TestContainerWaitRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/containers/web/wait?condition=removed", nil)
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.postContainersWait(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"StatusCode":42`) || stub.waitCondition != dockertypes.WaitConditionRemoved {
		t.Fatalf("wait response = %d %q, condition = %s", response.Code, response.Body.String(), stub.waitCondition)
	}
}

func TestContainerWaitAcknowledgesBeforeExit(t *testing.T) {
	stub := &lifecycleStub{waitEntered: make(chan struct{}), waitRelease: make(chan struct{})}
	api := &containerRouter{translator: stub}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("name", "web")
		if err := api.postContainersWait(w, r); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/containers/web/wait?condition=next-exit", nil)
	if err != nil {
		t.Fatal(err)
	}
	type waitResult struct {
		response *http.Response
		err      error
	}
	acknowledged := make(chan waitResult, 1)
	go func() {
		response, err := server.Client().Do(request)
		acknowledged <- waitResult{response: response, err: err}
	}()
	select {
	case <-stub.waitEntered:
	case <-time.After(3 * time.Second):
		close(stub.waitRelease)
		t.Fatal("wait request did not reach translator")
	}
	var response waitResult
	select {
	case response = <-acknowledged:
	case <-time.After(time.Second):
		close(stub.waitRelease)
		t.Fatal("wait did not acknowledge before container exit")
	}
	if response.err != nil {
		close(stub.waitRelease)
		t.Fatal(response.err)
	}
	defer func() {
		_ = response.response.Body.Close()
	}()
	close(stub.waitRelease)
	data, err := io.ReadAll(response.response.Body)
	if err != nil || response.response.StatusCode != http.StatusOK || !strings.Contains(string(data), `"StatusCode":42`) || stub.waitCondition != dockertypes.WaitConditionNextExit {
		t.Fatalf("wait response = %d %q, err = %v", response.response.StatusCode, data, err)
	}
}

func TestContainerWaitErrorAfterAcknowledgement(t *testing.T) {
	api := &containerRouter{translator: &lifecycleStub{waitError: errors.New("pod unavailable")}}
	request := httptest.NewRequest(http.MethodPost, "/containers/web/wait?condition=next-exit", nil)
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.postContainersWait(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !response.Flushed || !strings.Contains(response.Body.String(), `"Message":"pod unavailable"`) {
		t.Fatalf("wait response = %d %q, flushed = %t", response.Code, response.Body.String(), response.Flushed)
	}
}

func TestUnimplementedContainerRoutes(t *testing.T) {
	api := &containerRouter{}
	for _, handler := range []func(http.ResponseWriter, *http.Request) error{
		api.getContainersArchive, api.postContainerUpdate,
	} {
		if err := handler(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/containers/web/stats", nil)); err == nil {
			t.Fatal("unimplemented endpoint returned success")
		}
	}
}

func TestContainerExecCreateInspectRoutes(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/containers/web/exec", strings.NewReader(`{"Cmd":["echo","hello"],"AttachStdout":true}`))
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.postContainerExecCreate(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"Id":"exec-id"`) || stub.execRequest.Cmd[0] != "echo" {
		t.Fatalf("exec create response = %d %q, request = %+v", response.Code, response.Body.String(), stub.execRequest)
	}
	inspectRequest := httptest.NewRequest(http.MethodGet, "/exec/exec-id/json", nil)
	inspectRequest.SetPathValue("id", "exec-id")
	inspect := httptest.NewRecorder()
	if err := api.getExecByID(inspect, inspectRequest); err != nil || !strings.Contains(inspect.Body.String(), `"ID":"exec-id"`) {
		t.Fatalf("exec inspect response = %q, err = %v", inspect.Body.String(), err)
	}
}

func TestContainerExecStartAndAttachUpgrade(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("name", "exec-id")
		var err error
		if r.URL.Path == "/exec/exec-id/start" {
			err = api.postContainerExecStart(w, r)
		} else {
			err = api.postContainersAttach(w, r)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	for _, test := range []struct {
		path string
		body string
		want string
	}{
		{"/exec/exec-id/start", `{"Tty":false}`, "hello\n"},
		{"/containers/web/attach?stdout=1&stderr=1&stream=1", "", "attached\n"},
	} {
		request, err := http.NewRequest(http.MethodPost, server.URL+test.path, strings.NewReader(test.body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "tcp")
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Content-Type") != types.MediaTypeMultiplexedStream ||
			len(data) < 8 || data[0] != 1 || int(binary.BigEndian.Uint32(data[4:8])) != len(test.want) ||
			string(data[8:8+len(test.want)]) != test.want {
			t.Fatalf("%s response = %d, headers = %v, data = %q", test.path, response.StatusCode, response.Header, data)
		}
	}
}

func TestContainerExecResizeRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/exec/exec-id/resize?h=25&w=80", nil)
	request.SetPathValue("name", "exec-id")
	response := httptest.NewRecorder()
	if err := api.postContainerExecResize(response, request); err != nil || stub.execHeight != 25 || stub.execWidth != 80 || response.Code != http.StatusOK {
		t.Fatalf("exec resize = %d, %dx%d, err = %v", response.Code, stub.execHeight, stub.execWidth, err)
	}
	if err := api.postContainerExecResize(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/exec/exec-id/resize?h=bad&w=80", nil)); err == nil {
		t.Fatal("invalid resize dimensions accepted")
	}
}

func TestContainerResizeRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodPost, "/containers/web/resize?h=17&w=232", nil)
	request.SetPathValue("name", "web")
	response := httptest.NewRecorder()
	if err := api.postContainersResize(response, request); err != nil || response.Code != http.StatusOK || stub.resizeHeight != 17 || stub.resizeWidth != 232 {
		t.Fatalf("resize = %d, %dx%d, err = %v", response.Code, stub.resizeHeight, stub.resizeWidth, err)
	}
	if err := api.postContainersResize(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/containers/web/resize?h=bad&w=232", nil)); err == nil {
		t.Fatal("invalid resize height accepted")
	}
}

func TestContainerStatsRoute(t *testing.T) {
	stub := &lifecycleStub{}
	api := &containerRouter{translator: stub}
	request := httptest.NewRequest(http.MethodGet, "/containers/web/stats?stream=false&one-shot=true", nil)
	request.SetPathValue("name", "web")
	request.SetPathValue("version", "1.50")
	response := httptest.NewRecorder()
	if err := api.getContainersStats(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" ||
		stub.statsOptions.Stream || !stub.statsOptions.OneShot || !strings.Contains(response.Body.String(), `"read"`) {
		t.Fatalf("stats response = %d %q, options = %+v", response.Code, response.Body.String(), stub.statsOptions)
	}
	defaultRequest := httptest.NewRequest(http.MethodGet, "/containers/web/stats", nil)
	if err := api.getContainersStats(httptest.NewRecorder(), defaultRequest); err != nil || !stub.statsOptions.Stream {
		t.Fatalf("default stats stream = %+v, err = %v", stub.statsOptions, err)
	}
}
