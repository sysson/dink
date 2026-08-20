package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

type TLSConfigOptions struct {
	DisableTLS    bool
	TLSCertFile   string
	TLSKeyFile    string
	MinTLSVersion string
	ClientCAFile  string
}

func Load(cfg *TLSConfigOptions) (*tls.Config, error) {
	if cfg.DisableTLS {
		return nil, nil
	}

	if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
		return nil, fmt.Errorf("TLS is enabled but tlsCertFile/tlsKeyFile are not configured; set them or set disableTLS")
	}

	cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server certificate/key: %w", err)
	}

	minVersion, err := parseMinVersion(cfg.MinTLSVersion)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		MinVersion:   minVersion,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}

	if cfg.ClientCAFile == "" {
		return tlsConfig, nil
	}

	clientCAs, err := loadCertPool(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("loading client CA: %w", err)
	}
	tlsConfig.ClientCAs = clientCAs
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert

	return tlsConfig, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("%q contains no PEM certificates", path)
	}
	return pool, nil
}

func parseMinVersion(v string) (uint16, error) {
	switch v {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("unsupported minTLSVersion %q: must be \"1.2\" or \"1.3\"", v)
	}
}
