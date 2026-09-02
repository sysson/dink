package certs

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func testOptions(kt KeyType) Options {
	return Options{
		KeyType:       kt,
		ServiceName:   "dink",
		Namespace:     "dink-system",
		ClusterDomain: "cluster.local",
	}
}

func TestGenerateVerifies(t *testing.T) {
	t.Parallel()

	for _, kt := range []KeyType{KeyTypeECDSA, KeyTypeEd25519, KeyTypeRSA} {
		t.Run(string(kt), func(t *testing.T) {
			t.Parallel()

			opts := testOptions(kt)
			if kt == KeyTypeRSA {
				opts.RSABits = 2048
			}

			bundle, err := Generate(opts)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			pool, err := bundle.CertPool()
			if err != nil {
				t.Fatalf("CertPool: %v", err)
			}

			server := parseCert(t, bundle.Server.Cert)
			if _, err := server.Verify(x509.VerifyOptions{
				Roots:     pool,
				DNSName:   "dink.dink-system.svc",
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				t.Errorf("verifying server certificate: %v", err)
			}

			client := parseCert(t, bundle.Client.Cert)
			if _, err := client.Verify(x509.VerifyOptions{
				Roots:     pool,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			}); err != nil {
				t.Errorf("verifying client certificate: %v", err)
			}

			if _, err := bundle.ServerTLSConfig(); err != nil {
				t.Errorf("ServerTLSConfig: %v", err)
			}
			if _, err := bundle.ClientTLSConfig(); err != nil {
				t.Errorf("ClientTLSConfig: %v", err)
			}
		})
	}
}

func TestServerCertificateSANs(t *testing.T) {
	t.Parallel()

	opts := testOptions(KeyTypeECDSA)
	opts.ExtraDNSNames = []string{"dink.example.com"}
	opts.ExtraIPs = []net.IP{net.ParseIP("10.0.0.5")}

	bundle, err := Generate(opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cert := parseCert(t, bundle.Server.Cert)

	want := []string{
		"dink",
		"dink.dink-system",
		"dink.dink-system.svc",
		"dink.dink-system.svc.cluster.local",
		"localhost",
		"dink.example.com",
	}
	for _, name := range want {
		if !slices.Contains(cert.DNSNames, name) {
			t.Errorf("server certificate missing DNS SAN %q, got %v", name, cert.DNSNames)
		}
	}
	if !containsIP(cert.IPAddresses, net.ParseIP("127.0.0.1")) {
		t.Errorf("server certificate missing loopback IP SAN, got %v", cert.IPAddresses)
	}
	if !containsIP(cert.IPAddresses, net.ParseIP("10.0.0.5")) {
		t.Errorf("server certificate missing extra IP SAN, got %v", cert.IPAddresses)
	}
}

// The bash implementation this replaced needed care to avoid emitting a
// duplicate basicConstraints that made the CA unusable as an issuer.
func TestCAConstraints(t *testing.T) {
	t.Parallel()

	ca, err := NewAuthority(testOptions(KeyTypeECDSA))
	if err != nil {
		t.Fatalf("NewAuthority: %v", err)
	}
	cert := ca.Certificate()

	if !cert.IsCA {
		t.Error("CA certificate does not have IsCA set")
	}
	if !cert.BasicConstraintsValid {
		t.Error("CA certificate has invalid basic constraints")
	}
	if !cert.MaxPathLenZero {
		t.Error("CA certificate should have pathlen:0")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA certificate cannot sign certificates")
	}
}

func TestLeafKeyUsageByAlgorithm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		keyType          KeyType
		wantEncipherment bool
	}{
		{KeyTypeECDSA, false},
		{KeyTypeEd25519, false},
		{KeyTypeRSA, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.keyType), func(t *testing.T) {
			t.Parallel()

			opts := testOptions(tt.keyType)
			if tt.keyType == KeyTypeRSA {
				opts.RSABits = 2048
			}
			bundle, err := Generate(opts)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			cert := parseCert(t, bundle.Server.Cert)
			if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
				t.Error("leaf certificate missing digitalSignature")
			}
			got := cert.KeyUsage&x509.KeyUsageKeyEncipherment != 0
			if got != tt.wantEncipherment {
				t.Errorf("keyEncipherment = %v, want %v", got, tt.wantEncipherment)
			}
		})
	}
}

func TestReloadAuthorityIssuesTrustedLeaves(t *testing.T) {
	t.Parallel()

	opts := testOptions(KeyTypeECDSA)
	first, err := NewAuthority(opts)
	if err != nil {
		t.Fatalf("NewAuthority: %v", err)
	}
	bundle, err := first.Bundle()
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	dir := t.TempDir()
	if err := SaveCA(dir, first.KeyPair()); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}

	reloaded, err := LoadAuthorityDir(dir, opts)
	if err != nil {
		t.Fatalf("LoadAuthorityDir: %v", err)
	}
	if !reloaded.Certificate().Equal(first.Certificate()) {
		t.Fatal("reloaded CA differs from the original")
	}

	reissued, err := reloaded.Bundle()
	if err != nil {
		t.Fatalf("reissuing bundle: %v", err)
	}

	// A client that already trusts the original CA must accept the new leaves.
	pool, err := bundle.CertPool()
	if err != nil {
		t.Fatalf("CertPool: %v", err)
	}
	if _, err := parseCert(t, reissued.Server.Cert).Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "localhost",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("reissued server certificate not trusted by original CA: %v", err)
	}
}

func TestLoadAuthorityDirMissing(t *testing.T) {
	t.Parallel()

	_, err := LoadAuthorityDir(t.TempDir(), testOptions(KeyTypeECDSA))
	if !errors.Is(err, ErrNoAuthority) {
		t.Fatalf("err = %v, want ErrNoAuthority", err)
	}
}

func TestSavePermissions(t *testing.T) {
	t.Parallel()

	bundle, err := Generate(testOptions(KeyTypeECDSA))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := t.TempDir()
	if err := SaveCA(dir, bundle.CA); err != nil {
		t.Fatalf("SaveCA: %v", err)
	}
	if err := SaveServer(dir, bundle.Server); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}
	clientDir := filepath.Join(dir, "docker")
	if err := SaveClientDir(clientDir, bundle.CA, bundle.Client); err != nil {
		t.Fatalf("SaveClientDir: %v", err)
	}

	private := []string{filepath.Join(dir, CAKeyFile), filepath.Join(dir, ServerKeyFile), filepath.Join(clientDir, DockerKeyFile)}
	for _, path := range private {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, perm)
		}
	}
}

func TestLeafNotAfterClampedToCA(t *testing.T) {
	t.Parallel()

	opts := testOptions(KeyTypeECDSA)
	opts.CADuration = 24 * time.Hour
	opts.Duration = 365 * 24 * time.Hour

	ca, err := NewAuthority(opts)
	if err != nil {
		t.Fatalf("NewAuthority: %v", err)
	}
	pair, err := ca.IssueServer()
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	if got := parseCert(t, pair.Cert).NotAfter; got.After(ca.Certificate().NotAfter) {
		t.Errorf("leaf NotAfter %v outlives CA NotAfter %v", got, ca.Certificate().NotAfter)
	}
}

func TestOptionsValidation(t *testing.T) {
	t.Parallel()

	tests := map[string]Options{
		"unsupported key type": {KeyType: "dsa", ServiceName: "dink", Namespace: "ns"},
		"missing service name": {Namespace: "ns"},
		"missing namespace":    {ServiceName: "dink"},
		"weak rsa key":         {KeyType: KeyTypeRSA, RSABits: 512, ServiceName: "dink", Namespace: "ns"},
		"negative duration":    {ServiceName: "dink", Namespace: "ns", Duration: -time.Hour},
	}

	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Generate(opts); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func parseCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block found")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing certificate: %v", err)
	}
	return cert
}

func containsIP(haystack []net.IP, needle net.IP) bool {
	return slices.ContainsFunc(haystack, needle.Equal)
}
