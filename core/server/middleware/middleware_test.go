package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoggingSkipsKubernetesProbes(t *testing.T) {
	var output bytes.Buffer
	handler := Logging(t.Context(), &output, "info")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	request.Header.Set("User-Agent", "kube-probe/1.34")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if output.Len() != 0 {
		t.Fatalf("probe produced access log: %s", output.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v2/", nil)
	request.Header.Set("User-Agent", "docker/27.0")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if output.Len() == 0 {
		t.Fatal("non-probe request did not produce an access log")
	}
}
