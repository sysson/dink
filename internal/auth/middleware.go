package auth

import (
	"bytes"
	"io"
	"net/http"

	"github.com/sysson/dink/internal/identity"
	authv1 "github.com/sysson/dink/sdk/auth/v1"
)

func Middleware(chain *Chain) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if chain.Len() == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authMethod := "TLS"
			id, ok := identity.FromContext(r.Context())
			if !ok {
				authMethod = ""
			}

			var body []byte
			if r.Body != nil {
				body, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			headers := flattenHeader(r.Header)

			reqResp, err := chain.AuthZReq(r.Context(), &authv1.AuthZReqRequest{
				Namespace:       id.Namespace,
				User:            id.CommonName,
				UserAuthnMethod: authMethod,
				RequestMethod:   r.Method,
				RequestUri:      r.RequestURI,
				RequestBody:     body,
				Headers:         headers,
			})
			if err != nil {
				http.Error(w, "authorization plugin error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !reqResp.GetAllow() {
				http.Error(w, "request denied by authorization plugin: "+reqResp.GetMsg(), http.StatusForbidden)
				return
			}

			rec := newStatusRecorder()
			next.ServeHTTP(rec, r)

			resResp, err := chain.AuthZRes(r.Context(), &authv1.AuthZResRequest{
				Namespace:          id.Namespace,
				User:               id.CommonName,
				UserAuthnMethod:    authMethod,
				RequestMethod:      r.Method,
				RequestUri:         r.RequestURI,
				RequestBody:        body,
				RequestHeader:      headers,
				ResponseBody:       rec.body.Bytes(),
				ResponseHeader:     flattenHeader(rec.header),
				ResponseStatusCode: int32(rec.status),
			})
			if err != nil {
				http.Error(w, "authorization plugin error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if !resResp.GetAllow() {
				http.Error(w, "response denied by authorization plugin: "+resResp.GetMsg(), http.StatusForbidden)
				return
			}

			rec.commit(w)
		})
	}
}

type statusRecorder struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func newStatusRecorder() *statusRecorder {
	return &statusRecorder{header: make(http.Header), status: http.StatusOK}
}

func (r *statusRecorder) Header() http.Header {
	return r.header
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(body)
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *statusRecorder) commit(w http.ResponseWriter) {
	for key, values := range r.header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body.Bytes())
}

func flattenHeader(h http.Header) map[string]string {
	flat := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			flat[k] = v[0]
		}
	}
	return flat
}
