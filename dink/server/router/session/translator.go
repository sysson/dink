package session

import (
	"net/http"
)

type Translator interface {
	HandleHTTPRequest(w http.ResponseWriter, r *http.Request) error
}
