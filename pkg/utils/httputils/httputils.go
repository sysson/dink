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
	ErrHTTPInvalidJSON    = errors.New("invalid JSON")
	ErrHTTPServerError    = errors.New("internal server error")
	ErrHTTPNotFound       = errors.New("not found")
	ErrHTTPForbidden      = errors.New("forbidden")
	ErrHTTPUnauthorized   = errors.New("unauthorized")
	ErrHTTPRequestTimeout = errors.New("request timeout")
	ErrHTTPBadRequest     = errors.New("bad request")
)

type HTTPFunc func(w http.ResponseWriter, r *http.Request) error
type APIVersion struct{}

func HTTPHandler(handler HTTPFunc, logger *slog.Logger) http.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(w, r)
		if err == nil {
			return
		}
		if httpErr, ok := errors.AsType[*HTTPError](err); ok {
			logHTTPRequest(r, logger, httpErr)
			_ = httpErr.WriteJSON(w)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_ = RequestTimeout(ErrHTTPRequestTimeout).WriteJSON(w)
			return
		}
		logHTTPRequest(r, logger, err)
		_ = ServerError(ErrHTTPServerError).WriteJSON(w)
	}
}

func logHTTPRequest(r *http.Request, logger *slog.Logger, err error) {
	if err == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.ErrorContext(r.Context(), "HTTP request error", "method", r.Method, "url", r.URL.String(), "error", err)
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

func InvalidJSON(err error) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, err)
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
