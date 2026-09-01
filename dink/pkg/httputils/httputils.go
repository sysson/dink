package httputils

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"

	"github.com/sysson/dink/dink/pkg/log"
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
			_ = httpResp.Write(w)
			if httpResp.StatusCode >= 500 {
				log.G(r.Context()).Error("handler returned error", "method", r.Method, "url", r.URL.String(), "error", httpResp.Error())
			}
			return
		}
		log.G(r.Context()).Error("server error", "method", r.Method, "url", r.URL.String(), "error", err)
		_ = WriteJSON(w, http.StatusInternalServerError, Message(ErrHTTPServerError.Error()))
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

func WriteJSON(w http.ResponseWriter, code int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	return json.MarshalWrite(w, v,
		jsontext.EscapeForHTML(false),
		json.Deterministic(true),
	)
}

func ParseJSON(r *http.Request, v any) error {
	defer func() {
		_ = r.Body.Close()
	}()
	return json.UnmarshalRead(r.Body, v)
}

func Message(s string) map[string]string {
	return map[string]string{"message": s}
}
