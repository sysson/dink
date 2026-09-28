package translator

import (
	"context"
	"net/http"
)

func (d *Docker) HandleHTTPRequest(context.Context, http.ResponseWriter, *http.Request) error {
	return ErrNotImplemented
}
