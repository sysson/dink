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

	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/version"
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

// legacyDiskUsage is the /system/df response shape used before API 1.52.
type legacyDiskUsage struct {
	LayersSize int64               `json:"LayersSize,omitempty"`
	Images     []image.Summary     `json:"Images,omitzero"`
	Containers []container.Summary `json:"Containers,omitzero"`
	Volumes    []volume.Volume     `json:"Volumes,omitzero"`
	BuildCache []build.CacheRecord `json:"BuildCache,omitzero"`
}

type diskUsageResponse struct {
	*legacyDiskUsage
	*system.DiskUsage
}

func (s *systemRouter) getDiskUsage(w http.ResponseWriter, r *http.Request) error {
	apiVersion := version.VersionFromRequest(r)
	var getContainers, getImages, getVolumes, getBuildCache bool
	typeValues, ok := r.URL.Query()["type"]
	if versions.LessThan(apiVersion, "1.42") || !ok {
		getContainers, getImages, getVolumes, getBuildCache = true, true, true, s.builder != nil
	} else {
		for _, value := range typeValues {
			switch system.DiskUsageObject(value) {
			case system.ContainerObject:
				getContainers = true
			case system.ImageObject:
				getImages = true
			case system.VolumeObject:
				getVolumes = true
			case system.BuildCacheObject:
				getBuildCache = s.builder != nil
			default:
				return httpx.BadRequest(fmt.Errorf("unknown object type: %s", value))
			}
		}
	}

	// API 1.52 clients that ask for verbose output understand the new shape; others also get the legacy fields.
	legacyFields := true
	verbose := false
	if !versions.LessThan(apiVersion, "1.52") {
		verbose, _ = strconv.ParseBool(r.URL.Query().Get("verbose"))
		legacyFields = !verbose
	}

	usage := &backend.DiskUsage{}
	if getContainers || getImages || getVolumes {
		result, err := s.translator.SystemDiskUsage(r.Context(), backend.DiskUsageOptions{
			Containers: getContainers,
			Images:     getImages,
			Volumes:    getVolumes,
			Verbose:    verbose || legacyFields,
		})
		if err != nil {
			return err
		}
		usage = result
	}
	if getBuildCache {
		buildCache, err := s.builder.DiskUsage(r.Context(), buildbackend.DiskUsageOptions{Verbose: verbose || legacyFields})
		if err != nil {
			return fmt.Errorf("getting build cache usage: %w", err)
		}
		usage.BuildCache = buildCache
	}

	var legacy legacyDiskUsage
	if legacyFields {
		if usage.Images != nil {
			legacy.LayersSize = usage.Images.TotalSize
			legacy.Images = nonNilSlice(usage.Images.Items)
		}
		if usage.Containers != nil {
			legacy.Containers = nonNilSlice(usage.Containers.Items)
		}
		if usage.Volumes != nil {
			legacy.Volumes = nonNilSlice(usage.Volumes.Items)
		}
		if usage.BuildCache != nil {
			legacy.BuildCache = nonNilSlice(usage.BuildCache.Items)
		}
	}
	if versions.LessThan(apiVersion, "1.52") {
		return httpx.WriteJSON(w, http.StatusOK, &legacy)
	}
	return httpx.WriteJSON(w, http.StatusOK, &diskUsageResponse{
		legacyDiskUsage: &legacy,
		DiskUsage: &system.DiskUsage{
			ImageUsage:      usage.Images,
			ContainerUsage:  usage.Containers,
			VolumeUsage:     usage.Volumes,
			BuildCacheUsage: usage.BuildCache,
		},
	})
}

// nonNilSlice keeps legacy fields as [] rather than null once they are requested.
func nonNilSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
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
