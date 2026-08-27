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

type HTTPFunc func(ctx context.Context, w http.ResponseWriter, r *http.Request) error
type APIVersion struct{}

func HTTPHandler(ctx context.Context, handler HTTPFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := handler(ctx, w, r)
		if err == nil {
			return
		}
		if r.Context().Err() != nil {
			log.G(ctx).Info("request cancelled by client", "method", r.Method, "url", r.URL.String(), "error", err)
			_ = NewHTTPError(statusClientClosedRequest, r.Context().Err()).WriteJSON(w)
			return
		}
		if httpResp, ok := errors.AsType[*HTTPResponse](err); ok {
			_ = httpResp.WriteJSON(w)
			if httpResp.StatusCode >= 500 {
				log.G(ctx).Error("handler returned error", "method", r.Method, "url", r.URL.String(), "error", httpResp.Unwrap().Error())
			}
			return
		}
		log.G(ctx).Error("server error", "method", r.Method, "url", r.URL.String(), "error", err)
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

type HTTPResponse struct {
	StatusCode int
	Msg        any
}

func (e *HTTPResponse) WriteJSON(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.StatusCode)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err, ok := e.Msg.(error); ok {
		return enc.Encode(struct {
			Message string `json:"message"`
		}{Message: err.Error()})
	}
	return enc.Encode(e.Msg)
}

func NewHTTPError(statusCode int, err error) *HTTPResponse {
	return &HTTPResponse{
		StatusCode: statusCode,
		Msg:        err,
	}
}

func (e *HTTPResponse) Unwrap() error {
	return e.Msg.(error)
}

func (e *HTTPResponse) Error() string {
	return fmt.Sprintf("HTTP error %d: %s", e.StatusCode, e.Msg.(error).Error())
}

func ServerError(err error) *HTTPResponse {
	return NewHTTPError(http.StatusInternalServerError, err)
}

func NotFound(err error) *HTTPResponse {
	return NewHTTPError(http.StatusNotFound, err)
}

func Forbidden(err error) *HTTPResponse {
	return NewHTTPError(http.StatusForbidden, err)
}

func Unauthorized(err error) *HTTPResponse {
	return NewHTTPError(http.StatusUnauthorized, err)
}

func RequestTimeout(err error) *HTTPResponse {
	return NewHTTPError(http.StatusRequestTimeout, err)
}

func BadRequest(err error) *HTTPResponse {
	return NewHTTPError(http.StatusBadRequest, err)
}
