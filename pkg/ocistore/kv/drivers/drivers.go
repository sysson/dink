// Package drivers selects a metadata kv driver from typed configuration:
// exactly one of its fields is set, and that driver's own Config validates
// and opens the store.
package drivers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sysson/dink/core/registry/backend/kv"
	"github.com/sysson/dink/core/registry/backend/kv/boltkv"
	"github.com/sysson/dink/core/registry/backend/kv/etcdkv"
	"github.com/sysson/dink/core/registry/backend/kv/memkv"
	"github.com/sysson/dink/core/registry/backend/kv/natskv"
)

// Config names one metadata driver and its settings, e.g.
//
//	{"bbolt": {"path": "/var/lib/dinki/metadata.db"}}
//	{"etcd": {"endpoints": ["etcd-0:2379"], "tls": {"caFile": "/etc/etcd/ca.crt"}}}
type Config struct {
	Mem   *memkv.Config  `json:"mem,omitempty"`
	BBolt *boltkv.Config `json:"bbolt,omitempty"`
	Etcd  *etcdkv.Config `json:"etcd,omitempty"`
	NATS  *natskv.Config `json:"nats,omitempty"`
}

// UnmarshalJSON replaces c rather than merging into it, so a configured
// driver replaces the default one instead of adding a second.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*c = Config(decoded)
	return nil
}

// Driver returns the name and configuration of the selected driver.
func (c Config) Driver() (string, kv.Config, error) {
	var names []string
	var selected kv.Config
	add := func(name string, cfg kv.Config) {
		names = append(names, name)
		selected = cfg
	}
	if c.Mem != nil {
		add("mem", *c.Mem)
	}
	if c.BBolt != nil {
		add("bbolt", *c.BBolt)
	}
	if c.Etcd != nil {
		add("etcd", *c.Etcd)
	}
	if c.NATS != nil {
		add("nats", *c.NATS)
	}
	switch len(names) {
	case 0:
		return "", nil, errors.New("exactly one driver must be configured: mem, bbolt, etcd, or nats")
	case 1:
		return names[0], selected, nil
	default:
		return "", nil, fmt.Errorf("exactly one driver must be configured, got %s", strings.Join(names, ", "))
	}
}

func (c Config) Validate() error {
	_, cfg, err := c.Driver()
	if err != nil {
		return err
	}
	return cfg.Validate()
}

// Open validates the selected driver's settings and opens its store.
func (c Config) Open(ctx context.Context) (kv.Store, error) {
	name, cfg, err := c.Driver()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	store, err := cfg.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening %s metadata store: %w", name, err)
	}
	return store, nil
}
