package container

import (
	"context"
	"net/http"

	"github.com/sysson/dink/internal/server/httputils"
)

func (c *containerRouter) postContainersCreate() httputils.HTTPFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		// Implement the handler logic here
		return nil
	}
}
