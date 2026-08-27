package httputils

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIVersionFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "nil context", want: ""},
		{name: "missing value", ctx: context.Background(), want: ""},
		{name: "string value", ctx: context.WithValue(context.Background(), APIVersion{}, "v1"), want: "v1"},
		{name: "wrong value type", ctx: context.WithValue(context.Background(), APIVersion{}, 1), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := APIVersionFromContext(tt.ctx); got != tt.want {
				t.Fatalf("APIVersionFromContext() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPResponseWriteJSON(t *testing.T) {
	response := NewHTTPError(http.StatusBadRequest, errors.New("invalid <input>"))
	recorder := httptest.NewRecorder()

	if err := WriteJSON(recorder, http.StatusBadRequest, response); err != nil {
		t.Fatalf("WriteJSON() returned error: %v", err)
	}
	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("status code = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("content type = %q, want %q", got, want)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if got, want := body["message"], "invalid <input>"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestHTTPResponseErrorAndUnwrap(t *testing.T) {
	underlying := errors.New("missing resource")
	response := NotFound(underlying)

	if got, want := response.Error(), "HTTP error 404: missing resource"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(response, underlying) {
		t.Fatal("expected HTTPResponse to unwrap the underlying error")
	}
}

func TestHTTPHandler(t *testing.T) {
	tests := []struct {
		name       string
		handler    HTTPFunc
		wantStatus int
		wantBody   string
	}{
		{
			name: "success leaves response untouched",
			handler: func(w http.ResponseWriter, r *http.Request) error {
				return nil
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "generic error becomes server error",
			handler: func(w http.ResponseWriter, r *http.Request) error {
				return errors.New("database unavailable")
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   `{"message":"internal server error"}`,
		},
		{
			name: "HTTPResponse preserves status and message",
			handler: func(w http.ResponseWriter, r *http.Request) error {
				return Forbidden(ErrHTTPForbidden)
			},
			wantStatus: http.StatusForbidden,
			wantBody:   `{"message":"forbidden"}`,
		},
		{
			name: "cancelled request records client closed status",
			handler: func(w http.ResponseWriter, r *http.Request) error {
				return errors.New("handler stopped")
			},
			wantStatus: statusClientClosedRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/resource", nil)
			if tt.name == "cancelled request records client closed status" {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}

			recorder := httptest.NewRecorder()
			HTTPHandler(tt.handler).ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if got := strings.TrimSpace(recorder.Body.String()); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
			if tt.wantBody != "" && recorder.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("expected JSON content type, got %q", recorder.Header().Get("Content-Type"))
			}
		})
	}
}

func TestHTTPHandlerAcceptsWrappedHTTPResponse(t *testing.T) {
	underlying := errors.New("bad input")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/resource", nil)

	HTTPHandler(func(w http.ResponseWriter, r *http.Request) error {
		return errors.Join(errors.New("context"), BadRequest(underlying))
	}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body["message"] != "bad input" {
		t.Fatalf("message = %q, want %q", body["message"], "bad input")
	}
}

func TestHTTPErrorConstructors(t *testing.T) {
	tests := []struct {
		name        string
		constructor func(error) *HTTPError
		wantStatus  int
	}{
		{name: "server error", constructor: ServerError, wantStatus: http.StatusInternalServerError},
		{name: "not found", constructor: NotFound, wantStatus: http.StatusNotFound},
		{name: "forbidden", constructor: Forbidden, wantStatus: http.StatusForbidden},
		{name: "unauthorized", constructor: Unauthorized, wantStatus: http.StatusUnauthorized},
		{name: "request timeout", constructor: RequestTimeout, wantStatus: http.StatusRequestTimeout},
		{name: "bad request", constructor: BadRequest, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			underlying := errors.New("cause")
			response := tt.constructor(underlying)
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", response.StatusCode, tt.wantStatus)
			}
			if !errors.Is(response, underlying) {
				t.Fatal("expected constructor response to unwrap its cause")
			}
		})
	}
}
