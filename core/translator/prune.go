package translator

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func pruneBefore(values []string) (time.Time, error) {
	if len(values) == 0 {
		return time.Time{}, nil
	}
	if len(values) != 1 {
		return time.Time{}, fmt.Errorf("until filter requires one value")
	}
	value := values[0]
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp, nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		return time.Unix(0, int64(seconds*float64(time.Second))), nil
	}
	if duration, err := time.ParseDuration(value); err == nil && duration >= 0 {
		return time.Now().Add(-duration), nil
	}
	return time.Time{}, fmt.Errorf("invalid until filter %q", value)
}

func matchExcludedLabels(excluded []string, labels map[string]string) bool {
	for _, entry := range excluded {
		key, value, hasValue := strings.Cut(entry, "=")
		if actual, ok := labels[key]; ok && (!hasValue || actual == value) {
			return false
		}
	}
	return true
}
