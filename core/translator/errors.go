package translator

import (
	"errors"

	"github.com/sysson/syskit/httpx"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// kubeError maps a Kubernetes API error to the HTTP status the Docker API
// reports for it. Other errors are returned unchanged and become a 500.
func kubeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*httpx.HTTPError](err); ok {
		return err
	}
	switch {
	case apierrors.IsNotFound(err):
		return httpx.NotFound(err)
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return httpx.Conflict(err)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return httpx.BadRequest(err)
	case apierrors.IsForbidden(err):
		return httpx.Forbidden(err)
	case apierrors.IsUnauthorized(err):
		return httpx.Unauthorized(err)
	}
	return err
}
