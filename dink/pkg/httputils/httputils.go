package httputils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/sysson/dink/dink/pkg/log"
)

var (
	ErrHTTPServerError    = errors.New("internal server error")
	ErrHTTPNotFound       = errors.New("not found")
	ErrHTTPForbidden      = errors.New("forbidden")
	ErrHTTPUnauthorized   = errors.New("unauthorized")
	ErrHTTPRequestTimeout = errors.New("request timeout")
	ErrHTTPBadRequest     = errors.New("bad request")
)

const statusClientClosedRequest = 499

type HTTPFunc func(w http.ResponseWriter, r *http.Request) error

func HTTPHandler(handler HTTPFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(w, r)
		if err == nil {
			return
		}
		if contextErr := r.Context().Err(); contextErr != nil {
			log.G(r.Context()).Info("request cancelled by client", "method", r.Method, "url", r.URL.String(), "error", err)
			w.WriteHeader(statusClientClosedRequest)
			return
		}
		if httpResp, ok := errors.AsType[*HTTPError](err); ok {
			_ = WriteJSON(w, httpResp.StatusCode, httpResp)
			if httpResp.StatusCode >= 500 {
				log.G(r.Context()).Error("handler returned error", "method", r.Method, "url", r.URL.String(), "error", httpResp.Error())
			}
			return
		}
		log.G(r.Context()).Error("server error", "method", r.Method, "url", r.URL.String(), "error", err)
		_ = WriteJSON(w, http.StatusInternalServerError, NewHTTPError(http.StatusInternalServerError, ErrHTTPServerError))
	}
}

func KeyFromContext[T, V any](ctx context.Context, key T) (V, bool) {
	if ctx == nil {
		var zero V
		return zero, false
	}
	if v := ctx.Value(key); v != nil {
		if value, ok := v.(V); ok {
			return value, true
		}
	}
	var zero V
	return zero, false
}

type HTTPError struct {
	StatusCode int
	Message    error
}

func (e *HTTPError) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Message string `json:"message"`
	}{
		Message: e.Message.Error(),
	})
}

func WriteJSON(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func NewHTTPError(statusCode int, err error) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Message:    err,
	}
}

func (e *HTTPError) Unwrap() error {
	return e.Message
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP error %d: %s", e.StatusCode, e.Message)
}

func ServerError(err error) *HTTPError {
	return NewHTTPError(http.StatusInternalServerError, err)
}

func NotFound(err error) *HTTPError {
	return NewHTTPError(http.StatusNotFound, err)
}

func Forbidden(err error) *HTTPError {
	return NewHTTPError(http.StatusForbidden, err)
}

func Unauthorized(err error) *HTTPError {
	return NewHTTPError(http.StatusUnauthorized, err)
}

func RequestTimeout(err error) *HTTPError {
	return NewHTTPError(http.StatusRequestTimeout, err)
}

func BadRequest(err error) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, err)
}
