package system

import (
	"fmt"
	"net/http"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
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
	version, err := s.translator.SystemVersion(r.Context())
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, version)
}

func (s *systemRouter) getDiskUsage(w http.ResponseWriter, r *http.Request) error {
	return nil
}

func (s *systemRouter) postAuth(w http.ResponseWriter, r *http.Request) error {
	auth, err := authconfig.Decode(r.Header.Get(registry.AuthHeader))
	if err != nil {
		return httpx.BadRequest(fmt.Errorf("decoding auth config: %w", err))
	}
	identityToken, err := s.translator.AuthenticateToRegistry(r.Context(), auth)
	if err != nil {
		return httpx.Unauthorized(fmt.Errorf("authenticating to %s: %w", auth.ServerAddress, err))
	}

	return httpx.WriteJSON(w, http.StatusOK, registry.AuthResponse{
		Status:        "Login Succeeded",
		IdentityToken: identityToken,
	})
}
