package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/sysson/dink/dink/pkg/identity"
	"github.com/sysson/dink/dink/pkg/utils/httputils"
	"github.com/sysson/dink/dink/pkg/utils/ioutils"
)

func Middleware(chain *AuthChain) func(next httputils.HTTPFunc) httputils.HTTPFunc {
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		if chain.Len() == 0 {
			return next
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
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
			errH := next(ctx, rw, r)

			if err := authCtx.AuthZResponse(rw, r); err != nil {
				return httputils.Forbidden(fmt.Errorf("AuthZResponse returned error: %w", err))
			}
			return errH
		}
	}
}
