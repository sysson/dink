package server

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/docker/oci"
	"github.com/docker/oci/ociserver"
)

// GraphQLPath is where the optional GraphQL metadata query endpoint is served.
const GraphQLPath = "/v2/_dinki/ext/graphql"

type Handler struct {
	backend oci.Interface
	api     http.Handler
	graphql http.Handler
}

// Option configures a Handler.
type Option func(*Handler)

// WithGraphQL serves handler at GraphQLPath.
func WithGraphQL(handler http.Handler) Option {
	return func(h *Handler) { h.graphql = handler }
}

func New(backend oci.Interface, options ...Option) (*Handler, error) {
	if backend == nil {
		return nil, fmt.Errorf("OCI backend is required")
	}

	api, err := ociserver.New(backend, &ociserver.ServerConfig{
		Logger: slog.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("creating OCI protocol server: %w", err)
	}
	h := &Handler{backend: backend, api: api}
	for _, option := range options {
		option(h)
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if h.graphql != nil && r.URL.Path == GraphQLPath {
		h.graphql.ServeHTTP(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v2/_catalog" {
		h.catalog(w, r)
		return
	}
	if r.Method == http.MethodHead && r.URL.Path == "/v2/" {
		h.versionHead(w)
		return
	}
	h.api.ServeHTTP(w, r)
}

func (h *Handler) versionHead(w http.ResponseWriter) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	limit, err := catalogLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid catalog page size")
		return
	}
	items, hasMore, err := collectPage(r.Context(), h.backend.Repositories(r.Context(), r.URL.Query().Get("last")), limit)
	if err != nil {
		slog.ErrorContext(r.Context(), "listing registry repositories", "error", err)
		writeError(w, http.StatusInternalServerError, "SERVER_ERROR", "unable to list repositories")
		return
	}

	h.writePage(w, "/v2/_catalog", limit, items, hasMore, struct {
		Repositories []string `json:"repositories"`
	}{Repositories: items})
}

func (h *Handler) writePage(w http.ResponseWriter, path string, limit int, items []string, hasMore bool, body any) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	if hasMore {
		query := url.Values{}
		query.Set("n", strconv.Itoa(limit))
		query.Set("last", items[len(items)-1])
		w.Header().Set("Link", "<"+path+"?"+query.Encode()+">; rel=\"next\"")
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("writing registry listing response", "error", err)
	}
}

func catalogLimit(r *http.Request) (int, error) {
	value := r.URL.Query().Get("n")
	if value == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit == int(^uint(0)>>1) {
		return 0, fmt.Errorf("invalid page size %q", value)
	}
	return limit, nil
}

func collectPage(ctx context.Context, sequence iter.Seq2[string, error], limit int) ([]string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	items := make([]string, 0)
	hasMore := false
	for item, err := range sequence {
		if err != nil {
			return nil, false, err
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if limit > 0 && len(items) == limit {
			hasMore = true
			break
		}
		items = append(items, item)
	}
	return items, hasMore, nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}{
		Errors: []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{{Code: code, Message: message}},
	})
}
