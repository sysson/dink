package httputils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
type APIVersion struct{}

func HTTPHandler(handler HTTPFunc, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(w, r)
		if err == nil {
			return
		}
		if r.Context().Err() != nil {
			logger.InfoContext(r.Context(), "request cancelled by client", "method", r.Method, "url", r.URL.String(), "error", err)
			_ = NewHTTPError(statusClientClosedRequest, r.Context().Err()).WriteJSON(w)
			return
		}
		if httpErr, ok := errors.AsType[*HTTPError](err); ok {
			_ = httpErr.WriteJSON(w)
			if httpErr.StatusCode >= 500 {
				logger.ErrorContext(r.Context(), "Handler returned error", "method", r.Method, "url", r.URL.String(), "error", err)
			}
			return
		}
		logger.ErrorContext(r.Context(), "server error", "method", r.Method, "url", r.URL.String(), "error", err)
		_ = ServerError(ErrHTTPServerError).WriteJSON(w)
	}
}

func APIVersionFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(APIVersion{}); v != nil {
		if version, ok := v.(string); ok {
			return version
		}
	}
	return ""
}

type HTTPError struct {
	StatusCode int
	Msg        string
}

func (e *HTTPError) WriteJSON(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.StatusCode)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	type errorResponse struct {
		Message string `json:"message"`
	}

	return enc.Encode(errorResponse{Message: e.Msg})
}

func NewHTTPError(statusCode int, err error) *HTTPError {
	return &HTTPError{
		StatusCode: statusCode,
		Msg:        err.Error(),
	}
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP error %d: %s", e.StatusCode, e.Msg)
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
