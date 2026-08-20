// Package identity resolves the tenant an incoming request belongs to.
package identity

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// Identity is the tenant resolved for a request. Anonymous is true when no
// client certificate was presented, in which case Namespace is the base
// namespace rather than a derived per-tenant one.
type Identity struct {
	Namespace    string
	Organization string
	CommonName   string
	Anonymous    bool
}

// reserved holds namespace suffixes that must never be produced by cert
// input, since they could collide with cluster or dink control namespaces.
var reserved = map[string]bool{
	"system":          true,
	"default":         true,
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

var invalidLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

const maxNamespaceLength = 63

// Resolve derives the tenant identity for base namespace, from the
// Organization of a verified mTLS client certificate. A nil cert (plain TLS
// or plaintext) resolves to the anonymous identity in the base namespace.
func Resolve(base string, cert *x509.Certificate) (Identity, error) {
	if cert == nil || len(cert.Subject.Organization) == 0 || cert.Subject.Organization[0] == "" {
		id := Identity{Namespace: base, Anonymous: true}
		if cert != nil {
			id.CommonName = cert.Subject.CommonName
		}
		return id, nil
	}

	org := cert.Subject.Organization[0]
	suffix := sanitize(org)
	if suffix == "" {
		return Identity{}, fmt.Errorf("certificate organization %q has no usable namespace suffix", org)
	}
	if reserved[suffix] {
		return Identity{}, fmt.Errorf("certificate organization %q maps to a reserved namespace suffix", org)
	}

	namespace := base + "-" + suffix
	if len(namespace) > maxNamespaceLength {
		return Identity{}, fmt.Errorf("derived namespace %q exceeds %d characters", namespace, maxNamespaceLength)
	}

	return Identity{Namespace: namespace, Organization: org, CommonName: cert.Subject.CommonName}, nil
}

func sanitize(org string) string {
	s := invalidLabelChars.ReplaceAllString(strings.ToLower(org), "-")
	return strings.Trim(s, "-")
}

type contextKey struct{}

// NewContext returns a copy of ctx carrying id.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the Identity previously stored with NewContext.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// PeerCertificate returns the verified client certificate presented on r's
// TLS connection, or nil if the request wasn't made over mTLS.
func PeerCertificate(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

// NamespaceEnsurer provisions a tenant namespace (and its RBAC) on first use.
type NamespaceEnsurer interface {
	EnsureNamespace(ctx context.Context, namespace, owner string) error
}

// MiddlewareConfig controls how per-request tenant identity is resolved from
// mTLS client certificates.
type MiddlewareConfig struct {
	// BaseNamespace is the namespace used for anonymous (non-mTLS) requests,
	// and the prefix tenant namespaces are derived from.
	BaseNamespace string
	// DisableNamespaceCreation requires a tenant's namespace to already
	// exist instead of provisioning it on first use.
	DisableNamespaceCreation bool
	Ensurer                  NamespaceEnsurer
	Logger                   *slog.Logger
}

// Middleware resolves the caller's Identity from its client certificate (if
// any) and stores it in the request context, provisioning the tenant
// namespace on first use unless namespace creation is disabled.
func Middleware(cfg MiddlewareConfig) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := Resolve(cfg.BaseNamespace, PeerCertificate(r))
			if err != nil {
				http.Error(w, fmt.Sprintf("resolving identity: %v", err), http.StatusForbidden)
				return
			}

			if !id.Anonymous && !cfg.DisableNamespaceCreation && cfg.Ensurer != nil {
				if err := cfg.Ensurer.EnsureNamespace(r.Context(), id.Namespace, id.Organization); err != nil {
					cfg.Logger.ErrorContext(r.Context(), "provisioning tenant namespace", "namespace", id.Namespace, "error", err)
					http.Error(w, "provisioning tenant namespace", http.StatusInternalServerError)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(NewContext(r.Context(), id)))
		})
	}
}
