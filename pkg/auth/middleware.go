package auth

import (
	"fmt"
	"net/http"

	"github.com/sysson/dink/pkg/identity"
	"github.com/sysson/dink/pkg/utils/httputils"
	"github.com/sysson/dink/pkg/utils/ioutils"
)

func Middleware(chain *AuthChain) func(next httputils.HTTPFunc) httputils.HTTPFunc {
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		if chain.Len() == 0 {
			return next
		}
		return func(w http.ResponseWriter, r *http.Request) error {
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
				return httputils.Forbidden(fmt.Errorf("AuthZRequest returned error: %w", err))
			}

			rw := ioutils.NewResponseModifier(w)
			if err := next(rw, r); err != nil {
				return err
			}
			if err := authCtx.AuthZResponse(rw, r); err != nil {
				return httputils.Forbidden(fmt.Errorf("AuthZResponse returned error: %w", err))
			}
			return nil
		}
	}
}
