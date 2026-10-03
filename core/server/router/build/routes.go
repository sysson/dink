package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
	"github.com/sysson/dink/core/translator"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/syskit/httpx"
)

func (br *buildRouter) postBuild(w http.ResponseWriter, r *http.Request) error {
	return translator.Unsupported(errors.New("POST /build is not supported; use Docker Buildx with the Docker driver"))
}

func (br *buildRouter) postPrune(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest(err)
	}
	options := buildbackend.CachePruneOptions{}
	var err error
	filterJSON := r.Form.Get("filters")
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &options.Filters); err != nil {
			legacy := map[string][]string{}
			if legacyErr := json.Unmarshal([]byte(filterJSON), &legacy); legacyErr != nil {
				return httpx.BadRequest(errors.New("invalid filters"))
			}
			filterMap := make(map[string]map[string]bool, len(legacy))
			for key, values := range legacy {
				filterMap[key] = make(map[string]bool, len(values))
				for _, value := range values {
					filterMap[key][value] = true
				}
			}
			encoded, err := json.Marshal(filterMap)
			if err != nil {
				return fmt.Errorf("encoding build cache filters: %w", err)
			}
			if err := json.Unmarshal(encoded, &options.Filters); err != nil {
				return httpx.BadRequest(err)
			}
		}
	}
	all := false
	if value := r.Form.Get("all"); value != "" {
		all, err = strconv.ParseBool(value)
		if err != nil {
			return httpx.BadRequest(fmt.Errorf("invalid all parameter: %w", err))
		}
	}
	options.All = all
	parseBytes := func(name string) (int64, error) {
		value := r.Form.Get(name)
		if value == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			if err == nil {
				err = errors.New("must not be negative")
			}
			return 0, httpx.BadRequest(fmt.Errorf("%s is in bytes and expects a non-negative integer: %w", name, err))
		}
		return parsed, nil
	}
	apiVersion := version.VersionFromRequest(r)
	if versions.GreaterThanOrEqualTo(apiVersion, "1.48") {
		options.ReservedSpace, err = parseBytes("reserved-space")
		if err != nil {
			return err
		}
		if options.ReservedSpace == 0 {
			options.ReservedSpace, err = parseBytes("keep-storage")
			if err != nil {
				return err
			}
		}
		options.MaxUsedSpace, err = parseBytes("max-used-space")
		if err != nil {
			return err
		}
		options.MinFreeSpace, err = parseBytes("min-free-space")
		if err != nil {
			return err
		}
	} else {
		options.ReservedSpace, err = parseBytes("keep-storage")
		if err != nil {
			return err
		}
	}
	report, err := br.translator.PruneCache(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, report)
}

func (br *buildRouter) postCancel(w http.ResponseWriter, r *http.Request) error {
	return translator.Unsupported(errors.New("POST /build/cancel is not supported; cancel the Buildx request"))
}
