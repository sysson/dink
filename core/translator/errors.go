package translator

import (
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type ErrorKind uint8

const (
	KindInvalidArgument ErrorKind = iota + 1
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindUnsupported
	KindUnavailable
)

// Error describes a translator failure without coupling it to a transport.
type Error struct {
	kind  ErrorKind
	cause error
}

func (e *Error) Error() string { return e.cause.Error() }

func (e *Error) Unwrap() error { return e.cause }

func (e *Error) Kind() ErrorKind { return e.kind }

func IsKind(err error, kind ErrorKind) bool {
	var translatorError *Error
	return errors.As(err, &translatorError) && translatorError.Kind() == kind
}

func NewError(kind ErrorKind, cause error) error {
	if cause == nil {
		cause = errors.New("translator operation failed")
	}
	return &Error{kind: kind, cause: cause}
}

func InvalidArgument(cause error) error { return NewError(KindInvalidArgument, cause) }

func Unauthenticated(cause error) error { return NewError(KindUnauthenticated, cause) }

func Forbidden(cause error) error { return NewError(KindForbidden, cause) }

func NotFound(cause error) error { return NewError(KindNotFound, cause) }

func Conflict(cause error) error { return NewError(KindConflict, cause) }

func Unsupported(cause error) error { return NewError(KindUnsupported, cause) }

func Unavailable(cause error) error { return NewError(KindUnavailable, cause) }

var ErrNotImplemented = Unsupported(errors.New("not implemented"))

// kubeError classifies Kubernetes API failures as transport-independent errors.
func kubeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*Error](err); ok {
		return err
	}
	switch {
	case apierrors.IsNotFound(err):
		return NotFound(err)
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return Conflict(err)
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		return InvalidArgument(err)
	case apierrors.IsForbidden(err):
		return Forbidden(err)
	case apierrors.IsUnauthorized(err):
		return Unauthenticated(err)
	case apierrors.IsServiceUnavailable(err), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return Unavailable(err)
	}
	return fmt.Errorf("kubernetes operation: %w", err)
}
