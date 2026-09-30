package translator

import (
	"context"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/dink/sdk/auth"
	"github.com/sysson/dink/sdk/plugin"
)

type allowAll struct{}

func (allowAll) AuthorizeRequest(context.Context, *auth.Request) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func (allowAll) AuthorizeResponse(context.Context, *auth.Response) (auth.Decision, error) {
	return auth.Decision{Allow: true}, nil
}

func newPluginDocker(t *testing.T) *Docker {
	t.Helper()
	h, err := plugin.Handler(plugin.Info{Name: "policy", Version: "2.0.0"}, auth.Plugin(allowAll{}))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	reg, err := plugins.New("dink-system", []plugins.Static{{Name: "policy", Types: []plugins.Type{plugins.TypeAuth}, Endpoint: srv.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var objects []runtime.Object
	for ns, name := range map[string]string{"tenant": "vault", "other": "private", "dink-system": "shared"} {
		obj, err := plugins.Spec{
			Types:    []plugins.Type{plugins.TypeSecrets},
			Service:  plugins.ServiceRef{Name: name, Port: 8443},
			Insecure: true,
		}.Object(ns, name)
		if err != nil {
			t.Fatal(err)
		}
		obj.SetUID(types.UID("uid-" + name))
		objects = append(objects, obj)
	}
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{plugins.Resource: "PluginList"}, objects...)
	if err := reg.Watch(t.Context(), dc); err != nil {
		t.Fatal(err)
	}
	// Handshake the static plugin so it reports as enabled.
	if _, err := reg.Auth(t.Context(), "tenant"); err != nil {
		t.Fatal(err)
	}
	return &Docker{plugins: reg}
}

func TestListPluginsIsScopedToTenant(t *testing.T) {
	d := newPluginDocker(t)
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"})

	list, err := d.ListPlugins(ctx, filters.NewArgs())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range list {
		names[p.Name] = true
	}
	if len(list) != 3 || !names["policy"] || !names["shared"] || !names["vault"] || names["private"] {
		t.Fatalf("unexpected plugins: %v", names)
	}

	authz, err := d.ListPlugins(ctx, filters.NewArgs(filters.Arg("capability", "authz")))
	if err != nil {
		t.Fatal(err)
	}
	if len(authz) != 1 || authz[0].Name != "policy" || !authz[0].Enabled {
		t.Fatalf("unexpected authz plugins: %+v", authz)
	}
	if _, err := d.ListPlugins(ctx, filters.NewArgs(filters.Arg("bogus", "x"))); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("expected invalid filter to be rejected, got %v", err)
	}
}

func TestInspectPlugin(t *testing.T) {
	d := newPluginDocker(t)
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant"})

	p, err := d.InspectPlugin(ctx, "vault:latest")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "vault" || p.Config.Interface.Types[0].Capability != "secretprovider" {
		t.Fatalf("unexpected plugin: %+v", p)
	}
	if _, err := d.InspectPlugin(ctx, "private"); !IsKind(err, KindNotFound) {
		t.Fatalf("another tenant's plugin must not be visible, got %v", err)
	}
}
