package buildkit

import (
	"strings"

	"github.com/sysson/dink/core/identity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (g *Gateway) validateFrontendSessions(id identity.Identity, attrs map[string]string, primary string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, value := range attrs {
		if !strings.HasPrefix(key, "local-sessionid:") || value == primary && primary != "" {
			continue
		}
		b := g.sessions[value]
		if b == nil || b.id != id {
			return status.Error(codes.PermissionDenied, "frontend local session is not owned by this identity")
		}
	}
	return nil
}
