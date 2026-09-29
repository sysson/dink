package types

import "github.com/sysson/dink/pkg/filters"

type ImageListOptions struct {
	All        bool
	Filters    filters.Args
	SharedSize bool
	Manifests  bool
	Identity   bool
}
