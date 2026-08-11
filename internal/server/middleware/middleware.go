package middleware

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
)

type Middleware func(next http.Handler) http.Handler

func Logging(logger *slog.Logger) Middleware {
	isDebugHeaderSet := func(r *http.Request) bool {
		return r.Header.Get("Debug") == "reveal-body-logs"
	}
	return httplog.RequestLogger(logger, &httplog.Options{
		Level:         slog.LevelInfo,
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
}
