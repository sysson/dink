package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/registry"
)

type authTranslator struct {
	Translator
	auth  *registry.AuthConfig
	token string
}

func (s *authTranslator) AuthenticateToRegistry(_ context.Context, auth *registry.AuthConfig) (string, error) {
	s.auth = auth
	return s.token, nil
}

func TestPostAuthUsesJSONBody(t *testing.T) {
	translator := &authTranslator{token: "identity-token"}
	router := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodPost, "/auth", strings.NewReader(`{"username":"alice","password":"secret","serveraddress":"registry.example"}`))
	request.Header.Set("X-Registry-Auth", "not-the-login-request-body")
	response := httptest.NewRecorder()

	if err := router.postAuth(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if translator.auth == nil || translator.auth.ServerAddress != "registry.example" || translator.auth.Username != "alice" || translator.auth.Password != "secret" {
		t.Fatalf("auth passed to translator = %+v", translator.auth)
	}
	var result registry.AuthResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.IdentityToken != "identity-token" {
		t.Fatalf("identity token = %q, want %q", result.IdentityToken, "identity-token")
	}
}
