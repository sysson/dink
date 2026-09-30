// Package secrets lets a plugin resolve secret references into values.
package secrets

import (
	"context"

	"github.com/sysson/dink/sdk/plugin"
	secretsv1 "github.com/sysson/dink/sdk/secrets/v1"
	"github.com/sysson/dink/sdk/secrets/v1/secretsconnect"
)

type Ref struct {
	// Name is the name the resolved value is returned under, e.g. an env var name.
	Name string
	Ref  string
}

type Request struct {
	IdentityName string
	Namespace    string
	Refs         []Ref
}

type Secret struct {
	Name  string
	Value string
	// Err reports a failure to resolve this secret alone.
	Err error
}

// Resolver is implemented by secrets plugins. Returning an error fails the whole request.
type Resolver interface {
	Resolve(ctx context.Context, req *Request) ([]Secret, error)
}

// Plugin serves r as the plugin's secrets implementation.
func Plugin(r Resolver) plugin.Option {
	path, h := secretsconnect.NewSecretsPluginServiceHandler(handler{r: r})
	return plugin.WithService(plugin.TypeSecrets, path, h)
}

type handler struct {
	r Resolver
}

func (h handler) Resolve(ctx context.Context, in *secretsv1.ResolveRequest) (*secretsv1.ResolveResponse, error) {
	req := &Request{
		IdentityName: in.GetIdentityName(),
		Namespace:    in.GetNamespace(),
		Refs:         make([]Ref, 0, len(in.GetRef())),
	}
	for _, r := range in.GetRef() {
		req.Refs = append(req.Refs, Ref{Name: r.GetName(), Ref: r.GetRef()})
	}

	secrets, err := h.r.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}

	out := &secretsv1.ResolveResponse{Secret: make([]*secretsv1.ResolveResponse_Secret, 0, len(secrets))}
	for _, s := range secrets {
		ps := &secretsv1.ResolveResponse_Secret{Name: s.Name, Value: s.Value}
		if s.Err != nil {
			ps.Error = s.Err.Error()
		}
		out.Secret = append(out.Secret, ps)
	}
	return out, nil
}
