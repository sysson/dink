package plugin_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sysson/dink/sdk/plugin"
	pluginv1 "github.com/sysson/dink/sdk/plugin/v1"
	"github.com/sysson/dink/sdk/plugin/v1/pluginconnect"
	"github.com/sysson/dink/sdk/secrets"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) issue(t *testing.T, subject pkix.Name, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      subject,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func writeTLSDir(t *testing.T, ca *testCA, cert tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		"ca.crt":  ca.pem,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMutualTLS(t *testing.T) {
	ca := newTestCA(t)
	serverCert := ca.issue(t, pkix.Name{CommonName: "plugin"}, x509.ExtKeyUsageServerAuth)
	tlsConfig, err := plugin.MutualTLSConfig(writeTLSDir(t, ca, serverCert))
	if err != nil {
		t.Fatal(err)
	}

	h, err := plugin.Handler(plugin.Info{Name: "test"}, secrets.Plugin(resolver{}))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = tlsConfig
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	call := func(cert *tls.Certificate) error {
		cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		_, err := pluginconnect.NewPluginServiceClient(client, srv.URL).Info(t.Context(), &pluginv1.InfoRequest{})
		return err
	}

	dink := ca.issue(t, pkix.Name{CommonName: plugin.DefaultClient.CommonName, Organization: []string{plugin.DefaultClient.Organization}}, x509.ExtKeyUsageClientAuth)
	if err := call(&dink); err != nil {
		t.Fatalf("dink should be allowed: %v", err)
	}
	if err := call(nil); err == nil {
		t.Fatal("a caller without a certificate must be rejected")
	}
	tenant := ca.issue(t, pkix.Name{CommonName: plugin.DefaultClient.CommonName, Organization: []string{"tenant-a"}}, x509.ExtKeyUsageClientAuth)
	if err := call(&tenant); err == nil {
		t.Fatal("a tenant certificate with dink's common name must be rejected")
	}
	other := newTestCA(t)
	forged := other.issue(t, pkix.Name{CommonName: plugin.DefaultClient.CommonName, Organization: []string{plugin.DefaultClient.Organization}}, x509.ExtKeyUsageClientAuth)
	if err := call(&forged); err == nil {
		t.Fatal("a certificate from another CA must be rejected")
	}
}
