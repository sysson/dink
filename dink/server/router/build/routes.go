package build

import (
	"context"
	"net/http"
)

func (br *buildRouter) postBuild(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	// Implement the postBuild handler
	return nil
}

func (br *buildRouter) postPrune(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	// Implement the postPrune handler
	return nil
}

func (br *buildRouter) postCancel(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	// Implement the postCancel handler
	return nil
}
