// Package secrets resolves se://<provider>/<key> environment values.
//
// The value stays out of the container's config: dink only records the
// reference, and the container receives the resolved value as a normal env var.
// The namespace always comes from the caller's identity, never from the reference.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
)

const Scheme = "se://"

// ErrInvalidRef marks failures caused by the reference itself rather than an unavailable provider.
var ErrInvalidRef = errors.New("invalid secret reference")

type Resolver struct {
	k8s     kubernetes.Interface
	plugins *plugins.Registry
}

func NewResolver(k kubernetes.Interface, registry *plugins.Registry) *Resolver {
	return &Resolver{k8s: k, plugins: registry}
}

type ref struct {
	index int
	key   string
}

// Env is a container's environment after resolution.
type Env struct {
	Vars []corev1.EnvVar
	// Values holds plugin-resolved values, keyed by env name, to be stored in the Secret named SecretName.
	Values     map[string][]byte
	SecretName string
}

func IsRef(value string) bool {
	return strings.HasPrefix(value, Scheme)
}

// EnvSecretName is the Secret holding a container's plugin-resolved env values.
func EnvSecretName(container string) string {
	return "dink-env-" + container
}

// Resolve replaces every se:// value in vars with a reference to where its value is stored.
func (r *Resolver) Resolve(ctx context.Context, id identity.Identity, container string, vars []corev1.EnvVar) (Env, error) {
	env := Env{Vars: vars, SecretName: EnvSecretName(container)}
	byProvider := map[string][]ref{}
	for i, v := range vars {
		if !IsRef(v.Value) {
			continue
		}
		provider, key, ok := strings.Cut(strings.TrimPrefix(v.Value, Scheme), "/")
		if !ok || provider == "" || key == "" {
			return Env{}, fmt.Errorf("%w: %s=%s: expected %s<provider>/<key>", ErrInvalidRef, v.Name, v.Value, Scheme)
		}
		byProvider[provider] = append(byProvider[provider], ref{index: i, key: key})
	}

	for provider, refs := range byProvider {
		var err error
		if provider == builtinProvider {
			err = r.resolveBuiltin(ctx, id.Namespace, &env, refs)
		} else {
			err = r.resolvePlugin(ctx, id, provider, &env, refs)
		}
		if err != nil {
			return Env{}, err
		}
	}
	return env, nil
}

func secretKeyRef(name, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		Name: name,
		Key:  key,
	}}
}
