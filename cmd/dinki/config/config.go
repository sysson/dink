package config

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"github.com/sysson/dink/core/registry/backend/blobstore"
	"github.com/sysson/dink/core/registry/backend/kv/boltkv"
	"github.com/sysson/dink/core/registry/backend/kv/drivers"
)

const DefaultFile = "/etc/dinki/config.json"

type Config struct {
	Log       Log              `json:"log"`
	AccessLog AccessLog        `json:"accessLog"`
	Server    Server           `json:"server"`
	TLS       TLS              `json:"tls"`
	Storage   blobstore.Config `json:"storage"`
	Metadata  drivers.Config   `json:"metadata"`
	GraphQL   GraphQL          `json:"graphql"`
	API       API              `json:"api"`
}

type Log struct {
	Level string `json:"level"`
}

type AccessLog struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
}

type Server struct {
	Host string `json:"host"`
	Port string `json:"port"`
}

type TLS struct {
	Disabled      bool   `json:"disabled"`
	CertFile      string `json:"certFile"`
	KeyFile       string `json:"keyFile"`
	MinTLSVersion string `json:"minTLSVersion"`
}

// GraphQL controls the metadata query endpoint on the client-authenticated
// internal API listener at /v2/_dinki/ext/graphql.
type GraphQL struct {
	Enabled bool `json:"enabled"`
}

// API is the internal RegistryService listener dink calls. It shares the
// registry certificate and, unless TLS is disabled, requires client
// certificates signed by ClientCAFile whose subject matches ClientOrganization
// and ClientCommonName. Tenant client certificates share the CA, so the
// subject check is what keeps tenants off this API.
type API struct {
	Disabled           bool   `json:"disabled"`
	Host               string `json:"host"`
	Port               string `json:"port"`
	ClientCAFile       string `json:"clientCAFile"`
	ClientOrganization string `json:"clientOrganization"`
	ClientCommonName   string `json:"clientCommonName"`
}

func Default() Config {
	return Config{
		Log:       Log{Level: "info"},
		AccessLog: AccessLog{Enabled: true, Level: "error"},
		Server: Server{
			Port: "5000",
		},
		TLS: TLS{
			CertFile:      "/etc/dinki/tls/tls.crt",
			KeyFile:       "/etc/dinki/tls/tls.key",
			MinTLSVersion: "1.3",
		},
		Storage:  blobstore.Config{File: &blobstore.FileConfig{Path: "/var/lib/dinki/blobs"}},
		Metadata: drivers.Config{BBolt: &boltkv.Config{Path: "/var/lib/dinki/metadata.db"}},
		API: API{
			Port:               "5001",
			ClientCAFile:       "/etc/dinki/tls/ca.crt",
			ClientOrganization: "dink-system",
			ClientCommonName:   "dink.dink-system.svc",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && path == DefaultFile {
			return cfg, cfg.Validate()
		}
		return Config{}, fmt.Errorf("reading config file %q: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decoding config file %q: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []error
	if c.Log.Level == "" {
		errs = append(errs, errors.New("log.level must not be empty"))
	}
	if c.AccessLog.Level == "" {
		errs = append(errs, errors.New("accessLog.level must not be empty"))
	}
	if _, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(c.Server.Host, c.Server.Port)); err != nil {
		errs = append(errs, fmt.Errorf("server address: %w", err))
	}
	if !c.TLS.Disabled {
		if c.TLS.CertFile == "" {
			errs = append(errs, errors.New("tls.certFile must not be empty"))
		}
		if c.TLS.KeyFile == "" {
			errs = append(errs, errors.New("tls.keyFile must not be empty"))
		}
	}
	if _, err := TLSVersion(c.TLS.MinTLSVersion); err != nil {
		errs = append(errs, err)
	}
	if err := c.Storage.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("storage: %w", err))
	}
	if err := c.Metadata.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("metadata: %w", err))
	}
	if c.GraphQL.Enabled && c.API.Disabled {
		errs = append(errs, errors.New("graphql requires the client-authenticated api listener"))
	}
	if !c.API.Disabled {
		if _, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(c.API.Host, c.API.Port)); err != nil {
			errs = append(errs, fmt.Errorf("api address: %w", err))
		}
		if c.TLS.Disabled {
			errs = append(errs, errors.New("api requires TLS to enforce client certificate authentication"))
		} else if c.API.ClientCAFile == "" {
			errs = append(errs, errors.New("api.clientCAFile must not be empty when TLS is enabled"))
		} else if c.API.ClientOrganization == "" && c.API.ClientCommonName == "" {
			errs = append(errs, errors.New("api.clientOrganization or api.clientCommonName must be set when TLS is enabled"))
		}
		if c.API.Host == c.Server.Host && c.API.Port == c.Server.Port {
			errs = append(errs, errors.New("api and server must listen on different addresses"))
		}
	}
	return errors.Join(errs...)
}

func TLSVersion(version string) (uint16, error) {
	switch version {
	case "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("tls.minTLSVersion must be 1.2 or 1.3")
	}
}
