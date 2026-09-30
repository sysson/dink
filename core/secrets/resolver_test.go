package secrets

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/sdk/plugin"
	sdksecrets "github.com/sysson/dink/sdk/secrets"
)

type echoResolver struct {
	got *sdksecrets.Request
}

func (e *echoResolver) Resolve(_ context.Context, req *sdksecrets.Request) ([]sdksecrets.Secret, error) {
	e.got = req
	out := make([]sdksecrets.Secret, 0, len(req.Refs))
	for _, r := range req.Refs {
		if r.Ref == "missing" {
			out = append(out, sdksecrets.Secret{Name: r.Name, Err: errors.New("not found")})
			continue
		}
		out = append(out, sdksecrets.Secret{Name: r.Name, Value: "value-of-" + r.Ref})
	}
	return out, nil
}

func newResolver(t *testing.T, objects ...*corev1.Secret) (*Resolver, *echoResolver) {
	t.Helper()
	echo := &echoResolver{}
	h, err := plugin.Handler(plugin.Info{Name: "vault"}, sdksecrets.Plugin(echo))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	reg, err := plugins.New("dink-system", []plugins.Static{{Name: "vault", Types: []plugins.Type{plugins.TypeSecrets}, Endpoint: srv.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset()
	for _, s := range objects {
		if _, err := client.CoreV1().Secrets(s.Namespace).Create(t.Context(), s, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return NewResolver(client, reg), echo
}

func store(namespace string, labelled bool, data map[string][]byte) *corev1.Secret {
	s := &corev1.Secret{Name: StoreName, Namespace: namespace, Data: data}
	if labelled {
		s.Labels = map[string]string{StoreLabel: "true"}
	}
	return s
}

var tenant = identity.Identity{Namespace: "tenant-a", CommonName: "alice"}

func TestResolveBuiltin(t *testing.T) {
	r, _ := newResolver(t, store("tenant-a", true, map[string][]byte{"password": []byte("x")}))
	env, err := r.Resolve(t.Context(), tenant, "db", []corev1.EnvVar{
		{Name: "PLAIN", Value: "1"},
		{Name: "POSTGRES_PASSWORD", Value: "se://k8s/password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env.Vars[0].Value != "1" || env.Vars[0].ValueFrom != nil {
		t.Fatalf("plain var changed: %+v", env.Vars[0])
	}
	ref := env.Vars[1].ValueFrom.SecretKeyRef
	if env.Vars[1].Value != "" || ref.Name != StoreName || ref.Key != "password" {
		t.Fatalf("unexpected var: %+v", env.Vars[1])
	}
	if len(env.Values) != 0 {
		t.Fatalf("builtin values must not be copied: %v", env.Values)
	}
}

func TestResolveBuiltinRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		store *corev1.Secret
		value string
	}{
		"missing key":        {store("tenant-a", true, map[string][]byte{"other": nil}), "se://k8s/password"},
		"unlabelled store":   {store("tenant-a", false, map[string][]byte{"password": nil}), "se://k8s/password"},
		"other tenant store": {store("tenant-b", true, map[string][]byte{"password": nil}), "se://k8s/password"},
		"malformed":          {store("tenant-a", true, nil), "se://k8s"},
		"unknown provider":   {store("tenant-a", true, nil), "se://nope/password"},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newResolver(t, tc.store)
			_, err := r.Resolve(t.Context(), tenant, "db", []corev1.EnvVar{{Name: "P", Value: tc.value}})
			if !errors.Is(err, ErrInvalidRef) {
				t.Fatalf("expected ErrInvalidRef, got %v", err)
			}
		})
	}
}

func TestResolvePlugin(t *testing.T) {
	r, echo := newResolver(t)
	env, err := r.Resolve(t.Context(), tenant, "db", []corev1.EnvVar{{Name: "TOKEN", Value: "se://vault/app/token"}})
	if err != nil {
		t.Fatal(err)
	}
	if echo.got.Namespace != "tenant-a" || echo.got.IdentityName != "alice" ||
		len(echo.got.Refs) != 1 || echo.got.Refs[0] != (sdksecrets.Ref{Name: "TOKEN", Ref: "app/token"}) {
		t.Fatalf("plugin got %+v", echo.got)
	}
	ref := env.Vars[0].ValueFrom.SecretKeyRef
	if ref.Name != "dink-env-db" || ref.Key != "TOKEN" || string(env.Values["TOKEN"]) != "value-of-app/token" {
		t.Fatalf("unexpected env: %+v %v", env.Vars[0], env.Values)
	}

	if _, err := r.Resolve(t.Context(), tenant, "db", []corev1.EnvVar{{Name: "T", Value: "se://vault/missing"}}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("expected per-secret error to be ErrInvalidRef, got %v", err)
	}
}
