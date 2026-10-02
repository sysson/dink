package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociref"
	registrytypes "github.com/moby/moby/api/types/registry"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (r *RegistryService) Search(ctx context.Context, searchFilters filters.Args, term string, limit int, auth *registrytypes.AuthConfig, headers map[string][]string) ([]registrytypes.SearchResult, error) {
	if err := searchFilters.Validate(map[string]bool{"stars": true, "is-official": true, "is-automated": true}); err != nil {
		return nil, httpx.BadRequest(err)
	}
	automated, err := searchFilters.GetBoolOrDefault("is-automated", false)
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	official, err := searchFilters.GetBoolOrDefault("is-official", false)
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	stars := 0
	for _, value := range searchFilters.Get("stars") {
		count, err := strconv.Atoi(value)
		if err != nil {
			return nil, httpx.BadRequest(fmt.Errorf("invalid stars filter %q: %w", value, err))
		}
		if count > stars {
			stars = count
		}
	}
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 {
		return nil, httpx.BadRequest(errors.New("search limit must be between 1 and 100"))
	}
	endpoint, err := searchEndpoint(term)
	if err != nil {
		return nil, httpx.BadRequest(err)
	}
	results := []registrytypes.SearchResult{}
	if automated {
		return results, nil
	}
	query := endpoint.Query()
	query.Set("n", strconv.Itoa(limit))
	endpoint.RawQuery = query.Encode()
	transport := ociauth.NewStdTransport(ociauth.StdTransportParams{
		Config:    searchCredentials{host: endpoint.Host, auth: auth},
		Transport: RegistryTransport(nil, headers),
	})
	client := &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many registry search redirects")
			}
			if req.URL.Host != endpoint.Host || req.URL.Scheme != endpoint.Scheme {
				return errors.New("registry search redirected to a different origin")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Docker-Token", "true")
	if auth != nil && auth.IdentityToken != "" {
		pingURL := *endpoint
		pingURL.Path, pingURL.RawQuery = "/v2/", ""
		ping, err := http.NewRequestWithContext(ctx, http.MethodGet, pingURL.String(), nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(ping)
		if err != nil {
			return nil, fmt.Errorf("authenticating registry search: %w", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, httpx.Unauthorized(fmt.Errorf("registry search authentication returned %s", response.Status))
		}
	}
	ctx = ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
		RequiredScope: ociauth.ParseScope("registry:catalog:search"),
	})
	request = request.WithContext(ctx)
	if auth != nil && auth.Username != "" && auth.IdentityToken == "" && auth.RegistryToken == "" {
		request.SetBasicAuth(auth.Username, auth.Password)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("searching registry %s: %w", endpoint.Host, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		err := fmt.Errorf("registry %s search returned %s", endpoint.Host, response.Status)
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return nil, httpx.Unauthorized(err)
		case http.StatusForbidden:
			return nil, httpx.Forbidden(err)
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			return nil, httpx.NotFound(fmt.Errorf("registry %s does not provide Docker-compatible search: %w", endpoint.Host, err))
		default:
			return nil, err
		}
	}
	var data registrytypes.SearchResults
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decoding registry search response: %w", err)
	}
	if data.Results == nil {
		return nil, errors.New("registry search response is missing a results array")
	}
	for _, result := range data.Results {
		if searchFilters.Contains("is-official") && result.IsOfficial != official || result.StarCount < stars {
			continue
		}
		result.IsAutomated = false //nolint:staticcheck // Docker deprecated this field.
		results = append(results, result)
	}
	return results, nil
}

func searchEndpoint(term string) (*url.URL, error) {
	if strings.TrimSpace(term) == "" || strings.Contains(term, "://") {
		return nil, errors.New("search term is required and must not include a URL scheme")
	}
	host, remote := "index.docker.io", term
	if first, rest, ok := strings.Cut(term, "/"); ok &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		host, remote = first, rest
		if host != "localhost" && !ociref.IsValidHost(host) {
			return nil, fmt.Errorf("invalid registry host %q", host)
		}
	}
	if host == "docker.io" || host == "registry-1.docker.io" {
		host = "index.docker.io"
	}
	if host == "index.docker.io" {
		remote = strings.TrimPrefix(remote, "library/")
	}
	if strings.TrimSpace(remote) == "" {
		return nil, errors.New("search term must include a repository query")
	}
	scheme := "https"
	if isLoopback(host) {
		scheme = "http"
	}
	return &url.URL{Scheme: scheme, Host: host, Path: "/v1/search", RawQuery: url.Values{"q": {remote}}.Encode()}, nil
}

type searchCredentials struct {
	host string
	auth *registrytypes.AuthConfig
}

func (s searchCredentials) EntryForRegistry(host string) (ociauth.ConfigEntry, error) {
	if host != s.host {
		return ociauth.ConfigEntry{}, nil
	}
	return (authConfigSource{host: host, auth: s.auth}).EntryForRegistry(host)
}
