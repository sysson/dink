package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/sysson/dink/pkg/types"
	"github.com/sysson/syskit/httpx"
)

func optionsHandler(w http.ResponseWriter, r *http.Request) error {
	w.WriteHeader(http.StatusOK)
	return nil
}

func (s *systemRouter) pingHandler(w http.ResponseWriter, r *http.Request) error {
	w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Add("Pragma", "no-cache")

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", "0")
		return nil
	}
	_, err := w.Write([]byte{'O', 'K'})
	return err
}

func (s *systemRouter) getEvents(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) getInfo(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) getVersion(w http.ResponseWriter, r *http.Request) error {
	version, err := s.translator.SystemVersion()
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, version)
}

func (s *systemRouter) getDiskUsage(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) postAuth(w http.ResponseWriter, r *http.Request) error {
	var cfg registrytypes.AuthConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		return httpx.BadRequest(fmt.Errorf("decoding auth config: %w", err))
	}
	if cfg.ServerAddress == "" {
		return httpx.BadRequest(errors.New("serveraddress is required"))
	}

	auth := types.RegistryAuth{
		Username:      cfg.Username,
		Password:      cfg.Password,
		RefreshToken:  cfg.IdentityToken,
		AccessToken:   cfg.RegistryToken,
		ServerAddress: cfg.ServerAddress,
	}
	identityToken, err := s.translator.AuthenticateToRegistry(r.Context(), auth)
	if err != nil {
		return httpx.Unauthorized(fmt.Errorf("authenticating to %s: %w", cfg.ServerAddress, err))
	}

	return httpx.WriteJSON(w, http.StatusOK, registrytypes.AuthResponse{
		Status:        "Login Succeeded",
		IdentityToken: identityToken,
	})
}
