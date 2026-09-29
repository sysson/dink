package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sysson/dink/core/translator"
	"github.com/sysson/syskit/httpx"
)

func TestDockerAPIErrorMapsTranslatorKinds(t *testing.T) {
	tests := []struct {
		kind       translator.ErrorKind
		statusCode int
	}{
		{translator.KindInvalidArgument, http.StatusBadRequest},
		{translator.KindUnauthenticated, http.StatusUnauthorized},
		{translator.KindForbidden, http.StatusForbidden},
		{translator.KindNotFound, http.StatusNotFound},
		{translator.KindConflict, http.StatusConflict},
		{translator.KindUnsupported, http.StatusNotImplemented},
		{translator.KindUnavailable, http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.statusCode), func(t *testing.T) {
			err := translator.NewError(test.kind, errors.New("operation failed"))
			mapped := dockerAPIError(err)
			httpErr, ok := errors.AsType[*httpx.HTTPError](mapped)
			if !ok || httpErr.StatusCode != test.statusCode {
				t.Fatalf("mapped error = %v, want HTTP %d", mapped, test.statusCode)
			}
		})
	}
}

func TestDockerAPIErrorLeavesOtherErrorsUntouched(t *testing.T) {
	want := errors.New("internal failure")
	if got := dockerAPIError(want); got != want {
		t.Fatalf("mapped error = %v, want original error", got)
	}
}
