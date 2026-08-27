package debug

import (
	"net/http"
	"net/http/pprof"
)

func handlePprof(w http.ResponseWriter, r *http.Request) error {
	pprof.Handler(r.PathValue("name")).ServeHTTP(w, r)
	return nil
}
