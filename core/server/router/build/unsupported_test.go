package build

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sysson/dink/core/translator"
)

func TestLegacyBuildEndpointsReturnUnsupported(t *testing.T) {
	api := New(pruneTranslatorStub{})
	for _, route := range api.Routes() {
		if route.Path() == "/build/prune" {
			continue
		}
		err := route.Handler()(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, route.Path(), nil))
		var unsupported *translator.Error
		if !errors.As(err, &unsupported) || unsupported.Kind() != translator.KindUnsupported {
			t.Fatalf("%s returned %v, want an unsupported error", route.Path(), err)
		}
	}
}
