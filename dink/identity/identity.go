package identity

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/sysson/dink/pkg/httputils"
	"github.com/sysson/dink/pkg/log"
)

type Identity struct {
	Namespace    string
	Organization string
	CommonName   string
	Anonymous    bool
}

var reserved = map[string]struct{}{
	"system":          {},
	"default":         {},
	"kube-system":     {},
	"kube-public":     {},
	"kube-node-lease": {},
}

var invalidLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

const maxNamespaceLength = 63

func Resolve(cert *x509.Certificate, dinkNamespaces map[string]struct{}) (Identity, error) {
	if cert == nil || len(cert.Subject.Organization) == 0 || cert.Subject.Organization[0] == "" {
		id := Identity{Anonymous: true}
		if cert != nil {
			id.CommonName = cert.Subject.CommonName
		}
		return id, nil
	}

	org := cert.Subject.Organization[0]
	namespace := sanitize(org)
	if namespace == "" {
		return Identity{}, fmt.Errorf("certificate organization %q has no usable namespace", org)
	}
	if _, ok := reserved[namespace]; ok {
		return Identity{}, fmt.Errorf("certificate organization %q maps to a reserved namespace", org)
	}
	if _, ok := dinkNamespaces[namespace]; ok {
		return Identity{}, fmt.Errorf("certificate organization %q maps to a reserved dink namespace", org)
	}

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
	EnsureNamespace(ctx context.Context, namespace string) error
}

type MiddlewareConfig struct {
	DefaultNamespace string
	SystemNamespace  string
	Ensurer          NamespaceEnsurer
}

func Middleware(cfg MiddlewareConfig) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := Resolve(PeerCertificate(r), map[string]struct{}{cfg.SystemNamespace: {}, cfg.DefaultNamespace: {}})
			if err != nil {
				_ = httputils.Forbidden(fmt.Errorf("resolving identity: %w", err)).Write(w)
				return
			}
			if id.Anonymous {
				id.Namespace = cfg.DefaultNamespace
			} else {
				if err := cfg.Ensurer.EnsureNamespace(r.Context(), id.Namespace); err != nil {
					log.G(r.Context()).WithError(err).Error("error accessing tenant namespace", "namespace", id.Namespace)
					_ = httputils.InternalServerError(fmt.Errorf("accessing tenant namespace: %w", err)).Write(w)
					return
				}
			}

			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, id)))
		})
	}
}
