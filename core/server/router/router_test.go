package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func TestWithMinAPIVersion(t *testing.T) {
	const minVersion = "1.31"

	tests := []struct {
		name        string
		apiVersion  string
		wantStatus  int
		wantMessage string
		wantCalled  bool
	}{
		{
			name:        "missing API version",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "API version not specified",
		},
		{
			name:        "API version below minimum",
			apiVersion:  "1.30",
			wantStatus:  http.StatusBadRequest,
			wantMessage: "API version 1.30 is not supported. Minimum supported version is 1.31",
		},
		{
			name:       "API version at minimum",
			apiVersion: "1.31",
			wantCalled: true,
		},
		{
			name:       "API version above minimum",
			apiVersion: "1.32",
			wantCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			route := NewGetRoute("/resource", func(http.ResponseWriter, *http.Request) error {
				called = true
				return nil
			}, WithMinAPIVersion(minVersion))

			request := httptest.NewRequest(http.MethodGet, "/resource", nil)
			if tt.apiVersion != "" {
				ctx := context.WithValue(request.Context(), version.APIVersion{}, tt.apiVersion)
				request = request.WithContext(ctx)
			}

			err := route.Handler()(httptest.NewRecorder(), request)
			if called != tt.wantCalled {
				t.Fatalf("handler called = %t, want %t", called, tt.wantCalled)
			}
			if tt.wantStatus == 0 {
				if err != nil {
					t.Fatalf("Handler() returned error: %v", err)
				}
				return
			}

			httpErr, ok := err.(*httpx.HTTPError)
			if !ok {
				t.Fatalf("Handler() error = %T, want *httpx.HTTPError", err)
			}
			if httpErr.StatusCode != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", httpErr.StatusCode, tt.wantStatus)
			}
			if httpErr.Message.Error() != tt.wantMessage {
				t.Fatalf("message = %q, want %q", httpErr.Message, tt.wantMessage)
			}
		})
	}
}
