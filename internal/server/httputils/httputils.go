package httputils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var (
	ErrHTTPInvalidJSON    = errors.New("invalid JSON")
	ErrHTTPServerError    = errors.New("internal server error")
	ErrHTTPNotFound       = errors.New("not found")
	ErrHTTPForbidden      = errors.New("forbidden")
	ErrHTTPUnauthorized   = errors.New("unauthorized")
	ErrHTTPRequestTimeout = errors.New("request timeout")
)

type HTTPFunc func(ctx context.Context, w http.ResponseWriter, r *http.Request) error

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

func InvalidJSON() *HTTPError {
	return NewHTTPError(http.StatusBadRequest, ErrHTTPInvalidJSON)
}

func ServerError() *HTTPError {
	return NewHTTPError(http.StatusInternalServerError, ErrHTTPServerError)
}

func NotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, ErrHTTPNotFound)
}

func Forbidden() *HTTPError {
	return NewHTTPError(http.StatusForbidden, ErrHTTPForbidden)
}

func Unauthorized() *HTTPError {
	return NewHTTPError(http.StatusUnauthorized, ErrHTTPUnauthorized)
}

func RequestTimeout() *HTTPError {
	return NewHTTPError(http.StatusRequestTimeout, ErrHTTPRequestTimeout)
}
