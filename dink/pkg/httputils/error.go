package httputils

import (
	"errors"
	"fmt"
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

type HTTPError struct {
	StatusCode int
	Message    error
}

func (e *HTTPError) Write(w http.ResponseWriter) error {
	return WriteJSON(w, e.StatusCode, Message(e.Message.Error()))
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

func InternalServerError(err error) *HTTPError {
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
