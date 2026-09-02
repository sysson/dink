package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

// CertPool returns a pool trusting the bundle's CA.
func (b *Bundle) CertPool() (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b.CA.Cert) {
		return nil, fmt.Errorf("bundle CA certificate contains no usable PEM block")
	}
	return pool, nil
}

// ServerTLSConfig builds a config requiring and verifying client certificates
// issued by this bundle's CA.
func (b *Bundle) ServerTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(b.Server.Cert, b.Server.Key)
	if err != nil {
		return nil, fmt.Errorf("loading server keypair: %w", err)
	}
	pool, err := b.CertPool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

// ClientTLSConfig builds a config presenting this bundle's client certificate
// and trusting its CA.
func (b *Bundle) ClientTLSConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(b.Client.Cert, b.Client.Key)
	if err != nil {
		return nil, fmt.Errorf("loading client keypair: %w", err)
	}
	pool, err := b.CertPool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
	}, nil
}
