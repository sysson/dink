package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
	"github.com/sysson/syskit/logx"
)

type Middleware func(next http.Handler) http.Handler

func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return middleware.RequestID(next)
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
			return strings.HasPrefix(req.UserAgent(), "kube-probe/") || respStatus == 404 || respStatus == 405
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
		return requestLogger(ContextLogger()(next))
	}
}

func ContextLogger() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger := logx.G(r.Context()).With("request.id", middleware.GetReqID(r.Context()))
			r = r.WithContext(logx.WithLogger(r.Context(), logger))
			next.ServeHTTP(w, r)
		})
	}
}
