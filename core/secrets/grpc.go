package secrets

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	secretsv1 "github.com/sysson/dink/sdk/secrets/v1"
)

// resolvePlugin asks the named plugin for the values, which the caller stores in the container's env Secret.
func (r *Resolver) resolvePlugin(ctx context.Context, id identity.Identity, provider string, env *Env, refs []ref) error {
	plugin, err := r.plugins.Lookup(ctx, id.Namespace, provider, plugins.TypeSecrets)
	if errors.Is(err, plugins.ErrNotFound) {
		return fmt.Errorf("%w: %w", ErrInvalidRef, err)
	}
	if err != nil {
		return err
	}

	req := &secretsv1.ResolveRequest{IdentityName: id.CommonName, Namespace: id.Namespace}
	for _, ref := range refs {
		name := env.Vars[ref.index].Name
		if errs := validation.IsConfigMapKey(name); len(errs) > 0 {
			return fmt.Errorf("%w: env name %q cannot hold a secret: %v", ErrInvalidRef, name, errs)
		}
		req.Ref = append(req.Ref, &secretsv1.ResolveRequest_Secret{Name: name, Ref: ref.key})
	}

	res, err := plugin.Secrets().Resolve(ctx, req)
	plugin.Observe(err)
	if err != nil {
		return fmt.Errorf("secrets plugin %s: %w", plugin, err)
	}
	resolved := make(map[string]*secretsv1.ResolveResponse_Secret, len(res.GetSecret()))
	for _, s := range res.GetSecret() {
		resolved[s.GetName()] = s
	}

	if env.Values == nil {
		env.Values = map[string][]byte{}
	}
	for _, ref := range refs {
		v := &env.Vars[ref.index]
		s, ok := resolved[v.Name]
		if !ok {
			return fmt.Errorf("%w: secrets plugin %s did not resolve %s", ErrInvalidRef, plugin, v.Name)
		}
		if s.GetError() != "" {
			return fmt.Errorf("%w: secrets plugin %s: %s: %s", ErrInvalidRef, plugin, v.Name, s.GetError())
		}
		env.Values[v.Name] = []byte(s.GetValue())
		v.Value = ""
		v.ValueFrom = secretKeyRef(env.SecretName, v.Name)
	}
	return nil
}
