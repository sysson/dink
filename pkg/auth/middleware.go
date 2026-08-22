package auth

import (
	"log/slog"
	"net/http"

	"github.com/sysson/dink/pkg/identity"
	"github.com/sysson/dink/pkg/utils/ioutils"
)

func Middleware(chain *AuthChain, logger *slog.Logger) func(next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
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
				logger.Error("AuthZRequest returned error", slog.String("method", r.Method), slog.String("uri", r.RequestURI), slog.Any("error", err))
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}

			rw := ioutils.NewResponseModifier(w)
			next.ServeHTTP(rw, r)
			if err := authCtx.AuthZResponse(rw, r); err != nil {
				logger.Error("AuthZResponse returned error", slog.String("method", r.Method), slog.String("uri", r.RequestURI), slog.Any("error", err))
				http.Error(w, err.Error(), http.StatusForbidden)
				return
			}
		})
	}
}
