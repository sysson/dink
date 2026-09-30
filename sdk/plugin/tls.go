package plugin

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Client identifies a caller by its certificate subject.
type Client struct {
	Organization string
	CommonName   string
}

// DefaultClient is the certificate subject dink presents to plugins.
var DefaultClient = Client{Organization: "dink-system", CommonName: "dink.dink-system.svc"}

// MutualTLSConfig loads tls.crt, tls.key and ca.crt from dir, as mounted from a
// Kubernetes TLS Secret issued by `dinkle plugin issue`. Clients must present a
// certificate signed by ca.crt whose subject is one of clients (default DefaultClient).
func MutualTLSConfig(dir string, clients ...Client) (*tls.Config, error) {
	if len(clients) == 0 {
		clients = []Client{DefaultClient}
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"))
	if err != nil {
		return nil, fmt.Errorf("loading plugin certificate: %w", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("loading plugin CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificates found in ca.crt")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		// The CA also signs tenant certificates, so a valid chain alone does not mean the caller is dink.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 && allowed(cs.PeerCertificates[0], clients) {
				return nil
			}
			return errors.New("client certificate is not allowed to call this plugin")
		},
	}, nil
}

func allowed(cert *x509.Certificate, clients []Client) bool {
	for _, c := range clients {
		if cert.Subject.CommonName == c.CommonName && slices.Contains(cert.Subject.Organization, c.Organization) {
			return true
		}
	}
	return false
}
