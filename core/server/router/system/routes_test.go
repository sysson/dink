package system

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/registry"
	systemtypes "github.com/moby/moby/api/types/system"
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

type infoTranslatorStub struct {
	Translator
	info *systemtypes.Info
}

func (s *infoTranslatorStub) SystemInfo(context.Context) (*systemtypes.Info, error) {
	return s.info, nil
}

func TestGetInfo(t *testing.T) {
	translator := &infoTranslatorStub{info: &systemtypes.Info{
		Name:            "dink",
		OperatingSystem: "Kubernetes-backed Dink",
		Containers:      2,
		Warnings:        []string{"Counts are scoped to the authenticated namespace."},
	}}
	router := &systemRouter{translator: translator}
	request := httptest.NewRequest(http.MethodGet, "/info", nil)
	response := httptest.NewRecorder()

	if err := router.getInfo(response, request); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var info systemtypes.Info
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "dink" || info.OperatingSystem != "Kubernetes-backed Dink" || info.Containers != 2 || len(info.Warnings) != 1 {
		t.Fatalf("system info = %+v", info)
	}
}
