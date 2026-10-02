package pullauth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sysson/syskit/logx"
)

// Verifier checks a namespace's pull credential.
type Verifier interface {
	Verify(ctx context.Context, namespace, password string) (bool, error)
}

type BuildVerifier interface {
	VerifyBuild(context.Context, string, string) (string, []string, error)
}

type contextKey struct{}

// NewContext returns a copy of ctx scoped to namespace.
func NewContext(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, contextKey{}, namespace)
}

// FromContext returns the namespace ctx is scoped to.
func FromContext(ctx context.Context) (string, bool) {
	namespace, ok := ctx.Value(contextKey{}).(string)
	return namespace, ok && namespace != ""
}

// Middleware admits only requests with a valid Basic pull credential and
// scopes them to its namespace.
func Middleware(verifier Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			namespace, password, ok := r.BasicAuth()
			if !ok {
				unauthorized(w, "authentication required")
				return
			}
			if strings.HasPrefix(namespace, "build_") {
				buildVerifier, ok := verifier.(BuildVerifier)
				if !ok {
					unauthorized(w, "build credentials are unavailable")
					return
				}
				tenant, repositories, err := buildVerifier.VerifyBuild(r.Context(), namespace, password)
				if err != nil {
					logx.G(r.Context()).WithError(err).Error("verifying build credential")
					http.Error(w, "unable to verify build credential", http.StatusInternalServerError)
					return
				}
				if tenant == "" {
					unauthorized(w, "invalid or expired build credential")
					return
				}
				ctx := withBuildScope(NewContext(r.Context(), tenant), repositories)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			valid, err := verifier.Verify(r.Context(), namespace, password)
			if err != nil {
				logx.G(r.Context()).WithError(err).Error("verifying pull credential", "namespace", namespace)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(ociErrors("UNKNOWN", "unable to verify credentials"))
				return
			}
			if !valid {
				unauthorized(w, "invalid credentials")
				return
			}
			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), namespace)))
		})
	}
}

func unauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("WWW-Authenticate", `Basic realm="dinki"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(ociErrors("UNAUTHORIZED", message))
}

type ociError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ociErrors(code, message string) map[string][]ociError {
	return map[string][]ociError{"errors": {{Code: code, Message: message}}}
}
