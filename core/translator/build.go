package translator

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	buildtypes "github.com/moby/moby/api/types/build"
	"github.com/moby/moby/v2/daemon/server/buildbackend"
)

func (b *Builder) Build(context.Context, buildbackend.BuildConfig) (string, error) {
	return "", ErrNotImplemented
}

var buildCachePruneFilters = map[string]bool{
	"id": true, "parent": true, "type": true, "description": true,
	"inuse": true, "shared": true, "private": true, "mutable": true,
	"immutable": true, "label": true, "label!": true, "until": true,
	"unused-for": true,
}

func buildKitPruneRequest(options buildbackend.CachePruneOptions) (*control.PruneRequest, error) {
	if err := options.Filters.Validate(buildCachePruneFilters); err != nil {
		return nil, InvalidArgument(err)
	}
	untilValues := options.Filters.Get("until")
	unusedForValues := options.Filters.Get("unused-for")
	if len(untilValues) > 0 && len(unusedForValues) > 0 {
		return nil, InvalidArgument(fmt.Errorf("conflicting filters: %q and %q", "until", "unused-for"))
	}
	if len(untilValues)+len(unusedForValues) > 1 {
		return nil, InvalidArgument(fmt.Errorf("until filter expects a single value"))
	}
	var keepDuration time.Duration
	if len(untilValues)+len(unusedForValues) == 1 {
		value := untilValues
		if len(value) == 0 {
			value = unusedForValues
		}
		cutoff, err := parsePruneTimestamp(value[0], time.Now())
		if err != nil {
			return nil, InvalidArgument(fmt.Errorf("until filter expects a duration or timestamp: %w", err))
		}
		keepDuration = max(time.Since(cutoff), 0)
	}

	var buildKitFilters []string
	for _, key := range slices.Sorted(slices.Values(options.Filters.Keys())) {
		if key == "until" || key == "unused-for" {
			continue
		}
		values := options.Filters.Get(key)
		if len(values) > 1 {
			return nil, InvalidArgument(fmt.Errorf("filter %q expects a single value", key))
		}
		if len(values) == 0 {
			buildKitFilters = append(buildKitFilters, key)
		} else if key == "id" {
			buildKitFilters = append(buildKitFilters, key+"~="+values[0])
		} else {
			buildKitFilters = append(buildKitFilters, key+"=="+values[0])
		}
	}
	filter := strings.Join(buildKitFilters, ",")
	return &control.PruneRequest{
		Filter:        []string{filter},
		All:           options.All,
		KeepDuration:  int64(keepDuration),
		ReservedSpace: options.ReservedSpace,
		MaxUsedSpace:  options.MaxUsedSpace,
		MinFreeSpace:  options.MinFreeSpace,
	}, nil
}

func parsePruneTimestamp(value string, reference time.Time) (time.Time, error) {
	if duration, err := time.ParseDuration(value); value != "0" && err == nil {
		return reference.Add(-duration), nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02T15",
		"2006-01-02",
	} {
		if parsed, err := time.ParseInLocation(layout, value, reference.Location()); err == nil {
			return parsed, nil
		}
	}
	seconds, fraction, hasFraction := strings.Cut(value, ".")
	unixSeconds, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	var nanoseconds int64
	if hasFraction && fraction != "" {
		if len(fraction) > 20 {
			return time.Time{}, fmt.Errorf("timestamp fraction is too long")
		}
		for i := range len(fraction) {
			if fraction[i] < '0' || fraction[i] > '9' {
				return time.Time{}, fmt.Errorf("invalid timestamp fraction")
			}
		}
		if len(fraction) > 9 {
			fraction = fraction[:9]
		}
		fraction += strings.Repeat("0", 9-len(fraction))
		nanoseconds, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Unix(unixSeconds, nanoseconds), nil
}

// PruneCache is empty when builds are disabled.
func (b *Builder) PruneCache(ctx context.Context, options buildbackend.CachePruneOptions) (*buildtypes.CachePruneReport, error) {
	if b.gateway != nil && b.gateway.Enabled() {
		request, err := buildKitPruneRequest(options)
		if err != nil {
			return nil, err
		}
		records, err := b.gateway.PruneCache(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("pruning BuildKit cache: %w", err)
		}
		report := &buildtypes.CachePruneReport{CachesDeleted: make([]string, 0, len(records))}
		for _, record := range records {
			report.CachesDeleted = append(report.CachesDeleted, record.ID)
			if record.Size <= 0 {
				continue
			}
			if uint64(record.Size) > ^uint64(0)-report.SpaceReclaimed {
				return nil, fmt.Errorf("BuildKit prune reclaimed size overflows uint64")
			}
			report.SpaceReclaimed += uint64(record.Size)
		}
		return report, nil
	}
	return &buildtypes.CachePruneReport{CachesDeleted: []string{}}, nil
}

func (b *Builder) Cancel(context.Context, string) error {
	return ErrNotImplemented
}
