package session

import (
	"context"
	"net/http"
)

type Translator interface {
	HandleHTTPRequest(context.Context, http.ResponseWriter, *http.Request) error
}
