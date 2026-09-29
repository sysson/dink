package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/registry"
	"github.com/sysson/dink/pkg/filters"
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
	since, err := parseEventTimestamp(r.URL.Query().Get("since"))
	if err != nil {
		return httpx.BadRequest(fmt.Errorf("invalid since timestamp: %w", err))
	}
	until, err := parseEventTimestamp(r.URL.Query().Get("until"))
	if err != nil {
		return httpx.BadRequest(fmt.Errorf("invalid until timestamp: %w", err))
	}
	eventFilters, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	historical, stream, err := s.translator.SubscribeToEvents(r.Context(), since, until, eventFilters)
	if err != nil {
		return err
	}
	defer func() {
		_ = s.translator.UnsubscribeFromEvents(context.Background(), stream)
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return httpx.InternalServerError(errors.New("event streaming is not supported by the response writer"))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	encoder := json.NewEncoder(w)
	writeEvent := func(event events.Message) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	for _, event := range historical {
		if err := writeEvent(event); err != nil {
			return err
		}
	}

	var untilChannel <-chan time.Time
	if !until.IsZero() {
		delay := time.Until(until)
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		untilChannel = timer.C
	}
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-untilChannel:
			return nil
		case value, open := <-stream:
			if !open {
				return nil
			}
			event, ok := value.(events.Message)
			if !ok {
				return fmt.Errorf("unexpected event type %T", value)
			}
			if err := writeEvent(event); err != nil {
				return err
			}
		}
	}
}

func parseEventTimestamp(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		whole, fraction := math.Modf(seconds)
		return time.Unix(int64(whole), int64(fraction*float64(time.Second))), nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func (s *systemRouter) getInfo(w http.ResponseWriter, r *http.Request) error {
	info, err := s.translator.SystemInfo(r.Context())
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, info)
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
	var auth registry.AuthConfig
	if err := json.NewDecoder(r.Body).Decode(&auth); err != nil {
		return httpx.BadRequest(fmt.Errorf("decoding auth config: %w", err))
	}
	if auth.ServerAddress == "" {
		return httpx.BadRequest(errors.New("serveraddress is required"))
	}
	identityToken, err := s.translator.AuthenticateToRegistry(r.Context(), &auth)
	if err != nil {
		return httpx.Unauthorized(fmt.Errorf("authenticating to %s: %w", auth.ServerAddress, err))
	}

	return httpx.WriteJSON(w, http.StatusOK, registry.AuthResponse{
		Status:        "Login Succeeded",
		IdentityToken: identityToken,
	})
}
