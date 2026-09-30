package auth

import (
	"fmt"
	"net/http"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
)

func Middleware(registry *plugins.Registry) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authMethod := "TLS"
			id, ok := identity.FromContext(r.Context())
			if !ok {
				authMethod = ""
			}
			authPlugins, err := registry.Auth(r.Context(), id.Namespace)
			if err != nil {
				_ = httpx.Forbidden(fmt.Errorf("authorization plugins unavailable: %w", err)).WriteJSON(w)
				return
			}
			if len(authPlugins) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			authCtx := NewCtx(authPlugins, &Request{
				Namespace:       id.Namespace,
				User:            id.CommonName,
				UserAuthNMethod: authMethod,
				RequestMethod:   r.Method,
				RequestURI:      r.RequestURI,
			})

			if err := authCtx.AuthZRequest(w, r); err != nil {
				_ = httpx.Forbidden(fmt.Errorf("AuthZRequest returned error: %w", err)).WriteJSON(w)
				return
			}

			rw := iox.NewResponseModifier(w)
			next.ServeHTTP(rw, r)

			if err := authCtx.AuthZResponse(rw, r); err != nil {
				_ = httpx.Forbidden(fmt.Errorf("AuthZResponse returned error: %w", err)).WriteJSON(w)
				return
			}
		})
	}
}
