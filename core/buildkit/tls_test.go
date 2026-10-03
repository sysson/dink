package buildkit_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/registry/api"
)

func TestBackendTLSConfigurationLoadsCertificates(t *testing.T) {
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caKey.Public(), caKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(name string, usage x509.ExtKeyUsage, serial int64) (tls.Certificate, string, string) {
		t.Helper()
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), DNSNames: []string{name},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, key.Public(), caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certFile, keyFile := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
			t.Fatal(err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return cert, certFile, keyFile
	}
	_, _, wrongKey := issue("buildkit.test", x509.ExtKeyUsageServerAuth, 2)
	_, clientCert, clientKey := issue("dink-client", x509.ExtKeyUsageClientAuth, 3)
	cfg := config.BuildKit{
		URL: "tcp://buildkit.test:1234", CAFile: caFile,
		CertFile: clientCert, KeyFile: clientKey, ServerName: "buildkit.test",
	}
	publisher := api.Unavailable(context.Canceled)
	g, err := buildkit.New(cfg, "https://dinki.test:5000", publisher)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	invalidCA := filepath.Join(dir, "invalid.pem")
	if err := os.WriteFile(invalidCA, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*config.BuildKit)
	}{
		{"missing CA", func(c *config.BuildKit) { c.CAFile = filepath.Join(dir, "missing.pem") }},
		{"invalid CA", func(c *config.BuildKit) { c.CAFile = invalidCA }},
		{"missing certificate", func(c *config.BuildKit) { c.CertFile = filepath.Join(dir, "missing.pem") }},
		{"missing key", func(c *config.BuildKit) { c.KeyFile = filepath.Join(dir, "missing.key") }},
		{"mismatched key", func(c *config.BuildKit) { c.KeyFile = wrongKey }},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := cfg
			test.change(&bad)
			g, err := buildkit.New(bad, "https://dinki.test:5000", publisher)
			if g != nil {
				_ = g.Close()
			}
			if err == nil {
				t.Fatal("accepted invalid TLS configuration")
			}
		})
	}
}
