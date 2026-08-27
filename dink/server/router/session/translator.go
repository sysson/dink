package session

import (
	"context"
	"net/http"
)

type Translator interface {
	HandleHTTPRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) error
}
