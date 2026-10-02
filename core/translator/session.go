package translator

import (
	"context"
	"net/http"
)

func (d *Docker) HandleHTTPRequest(context.Context, http.ResponseWriter, *http.Request) error {
	return ErrNotImplemented
}

func (b *Builder) HandleHTTPRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	return b.gateway.HandleHTTPRequest(ctx, w, r)
}
