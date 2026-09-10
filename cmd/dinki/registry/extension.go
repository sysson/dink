package registry

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/docker/oci/ocilayout"
)

func Catalog(db *ocilayout.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()

		// Parse query parameters
		q := req.URL.Query()
		nStr := q.Get("n")
		last := q.Get("last")

		// Default page size
		n := 100
		if nStr != "" {
			if v, err := strconv.Atoi(nStr); err == nil && v > 0 {
				n = v
			}
		}

		// Collect all repositories from the backend
		all := []string{}
		for repo, err := range db.Repositories(ctx, last) {
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			all = append(all, repo)
		}

		// Sort for stable pagination
		sort.Strings(all)

		// Apply "last" pagination cursor
		start := 0
		if last != "" {
			for i, repo := range all {
				if repo == last {
					start = i + 1
					break
				}
			}
		}

		// Slice the page
		end := start + n
		if end > len(all) {
			end = len(all)
		}

		page := all[start:end]

		// Compute "next" cursor
		var next string
		if end < len(all) {
			next = all[end-1]
		}

		// Build response
		resp := map[string]any{
			"repositories": page,
		}
		if next != "" {
			resp["next"] = next
		}

		// Write JSON
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
