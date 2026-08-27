package identity

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/sysson/dink/dink/pkg/httputils"
	"github.com/sysson/dink/dink/pkg/log"
)

type Identity struct {
	Namespace    string
	Organization string
	CommonName   string
	Anonymous    bool
}

var reserved = map[string]bool{
	"system":          true,
	"default":         true,
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

var invalidLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

const maxNamespaceLength = 63

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

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

func PeerCertificate(r *http.Request) *x509.Certificate {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	return r.TLS.PeerCertificates[0]
}

type NamespaceEnsurer interface {
	EnsureNamespace(ctx context.Context, namespace, owner string) error
}

type MiddlewareConfig struct {
	BaseNamespace            string
	DisableNamespaceCreation bool
	Ensurer                  NamespaceEnsurer
}

func Middleware(cfg MiddlewareConfig) func(next httputils.HTTPFunc) httputils.HTTPFunc {
	return func(next httputils.HTTPFunc) httputils.HTTPFunc {
		return func(w http.ResponseWriter, r *http.Request) error {
			id, err := Resolve(cfg.BaseNamespace, PeerCertificate(r))
			if err != nil {
				return httputils.Forbidden(fmt.Errorf("resolving identity: %w", err))
			}

			if !id.Anonymous && !cfg.DisableNamespaceCreation && cfg.Ensurer != nil {
				if err := cfg.Ensurer.EnsureNamespace(r.Context(), id.Namespace, id.Organization); err != nil {
					log.G(r.Context()).WithError(err).Error("provisioning tenant namespace", "namespace", id.Namespace)
					return httputils.ServerError(fmt.Errorf("provisioning tenant namespace: %w", err))
				}
			}

			return next(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
		}
	}
}
