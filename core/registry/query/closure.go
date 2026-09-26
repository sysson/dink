package query

import (
	"context"
	"fmt"

	"github.com/docker/oci"
	"github.com/sysson/dink/core/registry/backend"
)

type closureEntry struct {
	descriptor oci.Descriptor
	role       string
	root       bool
	tagged     bool
}

// Closure walks the manifest and everything it transitively references in
// its repository, then classifies each digest against the dependent and
// location indexes. Child manifests absent from the repository are skipped.
func (m *Manifest) Closure(ctx context.Context) ([]*ClosureEntry, error) {
	entries, err := m.root.walkClosure(ctx, m.repository, m.record)
	if err != nil {
		return nil, err
	}
	members := make(map[oci.Digest]bool, len(entries))
	for _, entry := range entries {
		if entry.role == "MANIFEST" {
			members[entry.descriptor.Digest] = true
		}
	}
	result := make([]*ClosureEntry, 0, len(entries))
	for _, entry := range entries {
		resolved, err := m.root.classify(ctx, m.repository, entry, members)
		if err != nil {
			return nil, err
		}
		result = append(result, resolved)
	}
	return result, nil
}

func (r *resolver) walkClosure(ctx context.Context, repository string, root backend.ManifestRecord) ([]closureEntry, error) {
	var entries []closureEntry
	seen := make(map[oci.Digest]bool)
	add := func(entry closureEntry) error {
		if seen[entry.descriptor.Digest] {
			return nil
		}
		if len(entries) >= maxClosure {
			return fmt.Errorf("manifest closure exceeds %d entries", maxClosure)
		}
		seen[entry.descriptor.Digest] = true
		entries = append(entries, entry)
		return nil
	}
	queue := []backend.ManifestRecord{root}
	if err := add(closureEntry{descriptor: root.Descriptor, role: "MANIFEST", root: true}); err != nil {
		return nil, err
	}
	for len(queue) > 0 {
		record := queue[0]
		queue = queue[1:]
		for _, child := range record.Manifests {
			if seen[child.Digest] {
				continue
			}
			childRecord, err := r.metadata.Manifest(ctx, repository, child.Digest)
			if err != nil {
				if isNotFound(err) {
					continue
				}
				return nil, err
			}
			if err := add(closureEntry{descriptor: childRecord.Descriptor, role: "MANIFEST", tagged: len(childRecord.Tags) > 0}); err != nil {
				return nil, err
			}
			queue = append(queue, childRecord)
		}
		if record.Config != nil {
			if err := add(closureEntry{descriptor: *record.Config, role: "CONFIG"}); err != nil {
				return nil, err
			}
		}
		for _, layer := range record.Layers {
			if len(layer.URLs) > 0 {
				continue
			}
			if err := add(closureEntry{descriptor: layer, role: "LAYER"}); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}

func (r *resolver) classify(ctx context.Context, repository string, entry closureEntry, members map[oci.Digest]bool) (*ClosureEntry, error) {
	digest := entry.descriptor.Digest
	dependents, err := r.metadata.Dependents(ctx, digest, closureDependents)
	if err != nil {
		return nil, err
	}
	outside := make([]backend.Dependent, 0, len(dependents))
	retained := entry.tagged && !entry.root
	shared := false
	for _, dependent := range dependents {
		if dependent.Repository == repository && members[dependent.Manifest] {
			continue
		}
		outside = append(outside, dependent)
		if dependent.Repository == repository {
			retained = true
		} else {
			shared = true
		}
	}
	// A full page may hide outside dependents; stay conservative.
	if len(dependents) == closureDependents {
		retained = true
	}
	locations, err := r.metadata.Locations(ctx, digest)
	if err != nil {
		return nil, err
	}
	for _, location := range locations {
		if location.Repository != repository {
			shared = true
		}
	}
	return &ClosureEntry{
		root:      r,
		entry:     entry,
		retained:  retained,
		shared:    shared || retained,
		outside:   outside,
		locations: locations,
	}, nil
}

// ClosureEntry is the GraphQL ClosureEntry type.
type ClosureEntry struct {
	root      *resolver
	entry     closureEntry
	retained  bool
	shared    bool
	outside   []backend.Dependent
	locations []backend.Location
}

func (c *ClosureEntry) Digest() string             { return string(c.entry.descriptor.Digest) }
func (c *ClosureEntry) Role() string               { return c.entry.role }
func (c *ClosureEntry) MediaType() string          { return c.entry.descriptor.MediaType }
func (c *ClosureEntry) Size() int64                { return c.entry.descriptor.Size }
func (c *ClosureEntry) RetainedInRepository() bool { return c.retained }
func (c *ClosureEntry) Shared() bool               { return c.shared }
func (c *ClosureEntry) SharedWith() []*Dependent   { return c.root.dependentModels(c.outside) }
func (c *ClosureEntry) Locations() []*Location     { return locationModels(c.locations) }
