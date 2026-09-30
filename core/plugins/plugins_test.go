package plugins

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sysson/dink/sdk/auth"
	"github.com/sysson/dink/sdk/plugin"
)

func registration(namespace, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "dink.io/v1alpha1",
		"kind":       "Plugin",
		"metadata":   map[string]any{"name": name, "namespace": namespace, "uid": namespace + "/" + name, "generation": int64(1)},
		"spec":       spec,
	}}
}

func authSpec() map[string]any {
	return map[string]any{
		"types":   []any{"auth"},
		"service": map[string]any{"name": "svc", "port": int64(8443)},
	}
}

func newRegistry(t *testing.T, static ...Static) *Registry {
	t.Helper()
	r, err := New("dink-system", static, &tls.Config{MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func markReady(r *Registry) {
	for _, ns := range r.byNamespace {
		for _, p := range ns {
			p.ready = true
		}
	}
}

func TestEndpointStaysInNamespace(t *testing.T) {
	r := newRegistry(t)
	p, err := r.fromObject(registration("tenant-a", "authz", authSpec()))
	if err != nil {
		t.Fatal(err)
	}
	if p.baseURL != "https://svc.tenant-a.svc:8443" {
		t.Fatalf("baseURL = %q", p.baseURL)
	}

	insecure := authSpec()
	insecure["insecure"] = true
	p, err = r.fromObject(registration("tenant-a", "authz", insecure))
	if err != nil {
		t.Fatal(err)
	}
	if p.baseURL != "http://svc.tenant-a.svc:8443" {
		t.Fatalf("baseURL = %q", p.baseURL)
	}
}

func TestHTTPSNeedsClientCertificate(t *testing.T) {
	r, _ := New("dink-system", nil, nil)
	if _, err := r.fromObject(registration("tenant-a", "authz", authSpec())); err == nil {
		t.Fatal("expected an https plugin to be rejected without a TLS client configuration")
	}
}

func TestFromObjectRejectsInvalid(t *testing.T) {
	r := newRegistry(t)
	for name, spec := range map[string]map[string]any{
		"no service":   {"types": []any{"auth"}},
		"bad port":     {"types": []any{"auth"}, "service": map[string]any{"name": "svc", "port": int64(0)}},
		"unknown type": {"types": []any{"network"}, "service": map[string]any{"name": "svc", "port": int64(1)}},
		"no types":     {"service": map[string]any{"name": "svc", "port": int64(1)}},
		"bad timeout":  {"types": []any{"auth"}, "service": map[string]any{"name": "svc", "port": int64(1)}, "timeout": "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.fromObject(registration("ns", "p", spec)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := r.fromObject(registration("ns", "k8s", authSpec())); err == nil {
		t.Fatal("expected reserved name to be rejected")
	}
}

func TestInvalidRegistrationFailsClosed(t *testing.T) {
	r := newRegistry(t)
	r.upsert(registration("tenant-a", "broken", map[string]any{"types": []any{"auth"}}))
	if _, err := r.Lookup(t.Context(), "tenant-a", "broken", TypeAuth); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("expected registration error, got %v", err)
	}
	if _, err := r.Auth(t.Context(), "tenant-a"); err == nil {
		t.Fatal("expected auth chain to fail closed on an invalid registration")
	}
	if _, err := r.Auth(t.Context(), "tenant-b"); err != nil {
		t.Fatalf("other tenants must be unaffected: %v", err)
	}
}

func TestStatusOnlyUpdateKeepsHealth(t *testing.T) {
	r := newRegistry(t)
	obj := registration("tenant-a", "authz", authSpec())
	r.upsert(obj)
	markReady(r)

	obj.Object["status"] = map[string]any{"ready": true}
	r.upsert(obj)
	if ready, _, _ := r.byNamespace["tenant-a"]["authz"].Health(); !ready {
		t.Fatal("a status-only update must not reset the plugin")
	}

	obj.SetGeneration(2)
	r.upsert(obj)
	if ready, _, _ := r.byNamespace["tenant-a"]["authz"].Health(); ready {
		t.Fatal("a spec change must replace the plugin")
	}
}

func TestAuthOrderAndScope(t *testing.T) {
	r := newRegistry(t)
	ctx := t.Context()
	r.upsert(registration("tenant-a", "b-tenant", authSpec()))
	r.upsert(registration("tenant-b", "other", authSpec()))
	r.upsert(registration("dink-system", "z-cluster", authSpec()))
	r.upsert(registration("dink-system", "a-cluster", authSpec()))
	markReady(r)

	got, err := r.Auth(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.String())
	}
	want := []string{"dink-system/a-cluster", "dink-system/z-cluster", "tenant-a/b-tenant"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}

	r.remove(registration("tenant-a", "b-tenant", nil))
	if got, _ := r.Auth(ctx, "tenant-a"); len(got) != 2 {
		t.Fatalf("expected tenant plugin removed, got %d plugins", len(got))
	}
}

func TestLookupPrefersTenant(t *testing.T) {
	r := newRegistry(t)
	ctx := t.Context()
	secretsSpec := map[string]any{"types": []any{"secrets"}, "service": map[string]any{"name": "svc", "port": int64(1)}}
	r.upsert(registration("tenant-a", "vault", secretsSpec))
	r.upsert(registration("dink-system", "vault", secretsSpec))
	markReady(r)

	p, err := r.Lookup(ctx, "tenant-a", "vault", TypeSecrets)
	if err != nil || p.Namespace != "tenant-a" {
		t.Fatalf("got %v, %v", p, err)
	}
	p, err = r.Lookup(ctx, "tenant-b", "vault", TypeSecrets)
	if err != nil || p.Namespace != "dink-system" {
		t.Fatalf("got %v, %v", p, err)
	}
	if _, err := r.Lookup(ctx, "tenant-a", "vault", TypeAuth); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for wrong type, got %v", err)
	}
}

type allowAll struct{}

func (allowAll) AuthorizeRequest(context.Context, *auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func (allowAll) AuthorizeResponse(context.Context, *auth.Response) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func allowServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := plugin.Handler(plugin.Info{Name: "allow", Version: "1.2.3"}, auth.Plugin(allowAll{}))
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewUnstartedServer(h)
}

func TestHandshake(t *testing.T) {
	srv := allowServer(t)
	srv.Start()
	defer srv.Close()

	r := newRegistry(t, Static{Name: "allow", Types: []Type{TypeAuth}, Endpoint: srv.URL})
	plugins, err := r.Auth(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}
	if _, version, _ := plugins[0].Health(); version != "1.2.3" {
		t.Fatalf("version = %q", version)
	}

	r = newRegistry(t, Static{Name: "allow", Types: []Type{TypeSecrets}, Endpoint: srv.URL})
	if _, err := r.Lookup(t.Context(), "tenant-a", "allow", TypeSecrets); err == nil {
		t.Fatal("expected handshake to reject a plugin that does not implement its registered type")
	}
}

func TestUnreachablePluginIsRetriedAfterBackoff(t *testing.T) {
	srv := allowServer(t)
	srv.Start()
	url := srv.URL
	srv.Close()

	r := newRegistry(t, Static{Name: "allow", Types: []Type{TypeAuth}, Endpoint: url})
	if _, err := r.Auth(t.Context(), ""); err == nil {
		t.Fatal("expected an unreachable plugin to fail")
	}
	p := r.static[0]
	checkedAt := p.checkedAt
	if _, err := r.Auth(t.Context(), ""); err == nil || p.checkedAt != checkedAt {
		t.Fatal("a failed plugin must not be handshaken again before retryAfter")
	}

	p.ready, p.lastErr = true, nil
	p.Observe(connect.NewError(connect.CodeUnavailable, errors.New("gone")))
	if ready, _, _ := p.Health(); ready {
		t.Fatal("an unavailable call must mark the plugin for another handshake")
	}
	p.ready = true
	p.Observe(connect.NewError(connect.CodePermissionDenied, errors.New("no")))
	if ready, _, _ := p.Health(); !ready {
		t.Fatal("an application error must not mark the plugin unavailable")
	}
}

func TestMutualTLS(t *testing.T) {
	ca := newTestCA(t)
	serverCert := ca.issue(t, pkix.Name{CommonName: "plugin"})
	serverTLS, err := plugin.MutualTLSConfig(ca.writeDir(t, serverCert))
	if err != nil {
		t.Fatal(err)
	}
	srv := allowServer(t)
	srv.TLS = serverTLS
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	dink := ca.issue(t, pkix.Name{CommonName: plugin.DefaultClient.CommonName, Organization: []string{plugin.DefaultClient.Organization}})
	r, err := New("dink-system", []Static{{Name: "allow", Types: []Type{TypeAuth}, Endpoint: srv.URL}},
		&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{dink}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Auth(t.Context(), "tenant-a"); err != nil {
		t.Fatalf("dink should reach the plugin over mutual TLS: %v", err)
	}

	tenant := ca.issue(t, pkix.Name{CommonName: "default", Organization: []string{"tenant-a"}})
	r, _ = New("dink-system", []Static{{Name: "allow", Types: []Type{TypeAuth}, Endpoint: srv.URL}},
		&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{tenant}})
	if _, err := r.Auth(t.Context(), "tenant-a"); err == nil {
		t.Fatal("a tenant certificate must not be accepted by the plugin")
	}
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
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
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) issue(t *testing.T, subject pkix.Name) tls.Certificate {
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
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (ca *testCA) writeDir(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"tls.crt": {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		"tls.key": {Type: "PRIVATE KEY", Bytes: keyDER},
		"ca.crt":  {Type: "CERTIFICATE", Bytes: ca.cert.Raw},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
