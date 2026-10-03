package session

import (
	"net/http"
)

func (sr *sessionRouter) startSession(w http.ResponseWriter, r *http.Request) error {
	return sr.translator.HandleHTTPRequest(r.Context(), w, r)
}
