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
	"github.com/sysson/dink/dink/pkg/utils/httputils"
)

type Middleware func(next httputils.HTTPFunc) httputils.HTTPFunc

func RequestID() Middleware {
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
			var err error
			handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err = next(ctx, w, r)
			}))
			handler.ServeHTTP(w, r)
			return err
		}
	}
}

func Logging(out io.Writer, level string) Middleware {
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
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
			var err error
			handler := requestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				err = next(ctx, w, r)
			}))
			handler.ServeHTTP(w, r)
			return err
		}
	}
}
func Version(serverVersion, defaultAPIVersion, minAPIVersion string) Middleware {
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
			w.Header().Set("Server", fmt.Sprintf("Docker/%s (%s)", serverVersion, runtime.GOOS))
			w.Header().Set("Api-Version", defaultAPIVersion)
			w.Header().Set("Ostype", runtime.GOOS)
			apiVersion := r.PathValue("version")
			if apiVersion == "" {
				apiVersion = defaultAPIVersion
			}
			if versions.LessThan(apiVersion, minAPIVersion) {
				return httputils.BadRequest(fmt.Errorf("API version %s is not supported. Minimum supported version is %s", apiVersion, minAPIVersion))
			}
			if versions.GreaterThan(apiVersion, defaultAPIVersion) {
				return httputils.BadRequest(fmt.Errorf("API version %s is not supported. Maximum supported version is %s", apiVersion, defaultAPIVersion))
			}
			r = r.WithContext(context.WithValue(r.Context(), httputils.APIVersion{}, apiVersion))
			return next(ctx, w, r)
		}
	}
}
