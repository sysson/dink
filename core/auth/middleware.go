package auth

import (
	"fmt"
	"net/http"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	"github.com/sysson/syskit/iox"
)

func Middleware(chain *AuthChain) func(next http.Handler) http.Handler {
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
			authCtx := NewCtx(chain, &Request{
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
