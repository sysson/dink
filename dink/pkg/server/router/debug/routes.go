package debug

import (
	"context"
	"net/http"
	"net/http/pprof"
)

func handlePprof(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	pprof.Handler(r.PathValue("name")).ServeHTTP(w, r)
	return nil
}
