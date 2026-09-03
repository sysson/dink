package middleware

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/sysson/syskit/logx"
)

type Middleware func(next http.Handler) http.Handler
type APIVersion struct{}

func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r)
			})).ServeHTTP(w, r)
		})
	}
}

func Logging(ctx context.Context, out io.Writer, level string) Middleware {
	isDebugHeaderSet := func(r *http.Request) bool {
		return r.Header.Get("Debug") == "reveal-body-logs"
	}
	leveler := new(slog.LevelVar)
	err := leveler.UnmarshalText([]byte(level))
	if err != nil {
		leveler.Set(slog.LevelInfo)
	}
	requestLogger := httplog.RequestLogger(slog.New(slog.NewJSONHandler(
		out, &slog.HandlerOptions{Level: leveler},
	)), &httplog.Options{
		Level:         leveler.Level(),
		Schema:        httplog.SchemaOTEL,
		RecoverPanics: true,
		Skip: func(req *http.Request, respStatus int) bool {
			return respStatus == 404 || respStatus == 405
		},
		LogRequestHeaders:  []string{"Origin"},
		LogResponseHeaders: []string{},

		LogRequestBody:  isDebugHeaderSet,
		LogResponseBody: isDebugHeaderSet,

		LogExtraAttrs: func(req *http.Request, reqBody string, respStatus int) []slog.Attr {
			return []slog.Attr{
				slog.String("http.request.id", middleware.GetReqID(req.Context())),
			}
		},
	})
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(logx.WithLogger(r.Context(), logx.G(ctx).With("request.id", middleware.GetReqID(r.Context())))))
			})).ServeHTTP(w, r)
		})
	}
}
func Version(serverVersion, defaultAPIVersion, minAPIVersion string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Server", fmt.Sprintf("Docker/%s (%s)", serverVersion, runtime.GOOS))
			w.Header().Set("Api-Version", defaultAPIVersion)
			w.Header().Set("Ostype", runtime.GOOS)
			apiVersion := r.PathValue("version")
			if apiVersion == "" {
				apiVersion = defaultAPIVersion
			}
			if versions.LessThan(apiVersion, minAPIVersion) {
				http.Error(w, fmt.Sprintf("API version %s is not supported. Minimum supported version is %s", apiVersion, minAPIVersion), http.StatusBadRequest)
				return
			}
			if versions.GreaterThan(apiVersion, defaultAPIVersion) {
				http.Error(w, fmt.Sprintf("API version %s is not supported. Maximum supported version is %s", apiVersion, defaultAPIVersion), http.StatusBadRequest)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), APIVersion{}, apiVersion))
			next.ServeHTTP(w, r)
		})
	}
}
