package container

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/moby/moby/v2/daemon/server/backend"
)

type lifecycleStub struct {
	Translator
	call           string
	name           string
	signal         string
	stopOptions    backend.ContainerStopOptions
	restartOptions backend.ContainerStopOptions
	remove         *backend.ContainerRmConfig
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
