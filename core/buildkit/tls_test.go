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
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	control "github.com/moby/buildkit/api/services/control"
	"github.com/sysson/dink/core/buildkit"
	"github.com/sysson/dink/core/config"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/registry/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type infoBackend struct {
	control.UnimplementedControlServer
}

func (*infoBackend) Info(context.Context, *control.InfoRequest) (*control.InfoResponse, error) {
	return &control.InfoResponse{}, nil
}

func TestBackendMutualTLSAndServerName(t *testing.T) {
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
	serverCert, _, _ := issue("buildkit.test", x509.ExtKeyUsageServerAuth, 2)
	_, clientCert, clientKey := issue("dink-client", x509.ExtKeyUsageClientAuth, 3)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})) {
		t.Fatal("invalid test CA")
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert},
		ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert,
	})))
	control.RegisterControlServer(server, &infoBackend{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	cfg := config.BuildKit{
		URL: "tcp://" + listener.Addr().String(), CAFile: caFile,
		CertFile: clientCert, KeyFile: clientKey, ServerName: "buildkit.test",
	}
	publisher := api.Unavailable(context.Canceled)
	g, err := buildkit.New(cfg, "https://dinki.test:5000", publisher)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	ctx, cancel := context.WithTimeout(identity.NewContext(context.Background(), identity.Identity{Namespace: "tenant"}), 5*time.Second)
	defer cancel()
	if _, err := g.Info(ctx, &control.InfoRequest{}); err != nil {
		t.Fatalf("mTLS backend: %v", err)
	}
	cfg.ServerName = "wrong.test"
	wrong, err := buildkit.New(cfg, "https://dinki.test:5000", publisher)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wrong.Close() }()
	if _, err := wrong.Info(ctx, &control.InfoRequest{}); err == nil {
		t.Fatal("accepted a backend with the wrong TLS server name")
	}
	cfg.CAFile = filepath.Join(dir, "missing.pem")
	if _, err := buildkit.New(cfg, "https://dinki.test:5000", publisher); err == nil {
		t.Fatal("accepted a missing CA")
	}
}
