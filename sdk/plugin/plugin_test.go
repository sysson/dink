package plugin_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/sysson/dink/sdk/plugin"
	pluginv1 "github.com/sysson/dink/sdk/plugin/v1"
	"github.com/sysson/dink/sdk/plugin/v1/pluginconnect"
	"github.com/sysson/dink/sdk/secrets"
	secretsv1 "github.com/sysson/dink/sdk/secrets/v1"
	"github.com/sysson/dink/sdk/secrets/v1/secretsconnect"
)

type resolver struct{}

func (resolver) Resolve(_ context.Context, req *secrets.Request) ([]secrets.Secret, error) {
	out := make([]secrets.Secret, 0, len(req.Refs))
	for _, r := range req.Refs {
		if r.Ref == "missing" {
			out = append(out, secrets.Secret{Name: r.Name, Err: errors.New("not found")})
			continue
		}
		out = append(out, secrets.Secret{Name: r.Name, Value: req.Namespace + "/" + r.Ref})
	}
	return out, nil
}

func TestServe(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "plugin.sock")
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		errc <- plugin.Serve(ctx, plugin.Info{Name: "test", Version: "1.0.0"},
			plugin.WithAddr("unix://"+sock), secrets.Plugin(resolver{}))
	}()

	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}

	info := pluginconnect.NewPluginServiceClient(httpClient, "http://plugin")
	var (
		resp *pluginv1.InfoResponse
		err  error
	)
	for range 50 {
		if resp, err = info.Info(t.Context(), &pluginv1.InfoRequest{}); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if resp.GetName() != "test" || resp.GetApiVersion() != plugin.APIVersion ||
		len(resp.GetTypes()) != 1 || resp.GetTypes()[0] != pluginv1.PluginType_PLUGIN_TYPE_SECRETS {
		t.Fatalf("unexpected info: %v", resp)
	}

	sc := secretsconnect.NewSecretsPluginServiceClient(httpClient, "http://plugin")
	res, err := sc.Resolve(t.Context(), &secretsv1.ResolveRequest{
		Namespace: "tenant",
		Ref: []*secretsv1.ResolveRequest_Secret{
			{Name: "A", Ref: "a"},
			{Name: "B", Ref: "missing"},
		},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := res.GetSecret()
	if len(got) != 2 || got[0].GetValue() != "tenant/a" || got[1].GetError() != "not found" {
		t.Fatalf("unexpected secrets: %v", got)
	}

	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServeRejectsDuplicateTypes(t *testing.T) {
	err := plugin.Serve(t.Context(), plugin.Info{Name: "test"},
		plugin.WithAddr("127.0.0.1:0"), secrets.Plugin(resolver{}), secrets.Plugin(resolver{}))
	if err == nil {
		t.Fatal("expected error for duplicate plugin type")
	}
}
