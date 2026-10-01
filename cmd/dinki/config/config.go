package config

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"time"

	"github.com/sysson/ocistore/blobstore/fileblob"
	"github.com/sysson/ocistore/kv/boltkv"
)

const DefaultFile = "/etc/dinki/config.json"

type Config struct {
	Log       Log       `json:"log"`
	AccessLog AccessLog `json:"accessLog"`
	Server    Server    `json:"server"`
	TLS       TLS       `json:"tls"`
	Storage   Storage   `json:"storage"`
	Metadata  Metadata  `json:"metadata"`
	GC        GC        `json:"gc"`
	GraphQL   GraphQL   `json:"graphql"`
	API       API       `json:"api"`
}

type Storage struct {
	Path string `json:"path"`
}

type Metadata struct {
	Path string `json:"path"`
}

type Log struct {
	Level string `json:"level"`
}

type AccessLog struct {
	Enabled bool   `json:"enabled"`
	Level   string `json:"level"`
}

type Server struct {
	Host       string `json:"host"`
	Port       string `json:"port"`
	HealthPort string `json:"healthPort"`
}

type TLS struct {
	Disabled      bool   `json:"disabled"`
	CertFile      string `json:"certFile"`
	KeyFile       string `json:"keyFile"`
	MinTLSVersion string `json:"minTLSVersion"`
}

// GC schedules garbage collection. UploadExpiry is how long an unfinished
// upload or content reservation is kept before it is reclaimed.
type GC struct {
	Interval     Duration `json:"interval"`
	UploadExpiry Duration `json:"uploadExpiry"`
}

// Duration is a time.Duration written as a string such as "90s" or "24h".
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// GraphQL controls the metadata query endpoint at /v2/_dinki/ext/graphql,
// which is authorized like the internal API.
type GraphQL struct {
	Enabled bool `json:"enabled"`
}

// API is the internal RegistryService dink calls on the registry listener.
// Its requests must present a client certificate signed by ClientCAFile whose
// subject matches ClientOrganization and ClientCommonName. Tenant client
// certificates share the CA, so the subject check is what keeps tenants off
// this API.
type API struct {
	Disabled           bool   `json:"disabled"`
	ClientCAFile       string `json:"clientCAFile"`
	ClientOrganization string `json:"clientOrganization"`
	ClientCommonName   string `json:"clientCommonName"`
}

func Default() Config {
	return Config{
		Log:       Log{Level: "info"},
		AccessLog: AccessLog{Enabled: true, Level: "error"},
		Server: Server{
			Port:       "5000",
			HealthPort: "8080",
		},
		TLS: TLS{
			CertFile:      "/etc/dinki/tls/tls.crt",
			KeyFile:       "/etc/dinki/tls/tls.key",
			MinTLSVersion: "1.3",
		},
		Storage:  Storage{Path: "/var/lib/dinki/blobs"},
		Metadata: Metadata{Path: "/var/lib/dinki/metadata.db"},
		GC: GC{
			Interval:     Duration(time.Minute),
			UploadExpiry: Duration(24 * time.Hour),
		},
		API: API{
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
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decoding config file %q: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, fmt.Errorf("decoding config file %q: unexpected trailing data", path)
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
	if _, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(c.Server.Host, c.Server.HealthPort)); err != nil {
		errs = append(errs, fmt.Errorf("health server address: %w", err))
	}
	if c.Server.Port == c.Server.HealthPort {
		errs = append(errs, errors.New("server.port and server.healthPort must differ"))
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
	if err := (fileblob.Config{Path: c.Storage.Path}).Validate(); err != nil {
		errs = append(errs, fmt.Errorf("storage: %w", err))
	}
	if err := (boltkv.Config{Path: c.Metadata.Path}).Validate(); err != nil {
		errs = append(errs, fmt.Errorf("metadata: %w", err))
	}
	if c.GC.Interval <= 0 {
		errs = append(errs, errors.New("gc.interval must be positive"))
	}
	if c.GC.UploadExpiry <= 0 {
		errs = append(errs, errors.New("gc.uploadExpiry must be positive"))
	}
	if c.GraphQL.Enabled && c.API.Disabled {
		errs = append(errs, errors.New("graphql requires the client-authenticated api"))
	}
	if !c.API.Disabled {
		if c.TLS.Disabled {
			errs = append(errs, errors.New("api requires TLS to enforce client certificate authentication"))
		} else if c.API.ClientCAFile == "" {
			errs = append(errs, errors.New("api.clientCAFile must not be empty when TLS is enabled"))
		} else if c.API.ClientOrganization == "" && c.API.ClientCommonName == "" {
			errs = append(errs, errors.New("api.clientOrganization or api.clientCommonName must be set when TLS is enabled"))
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
