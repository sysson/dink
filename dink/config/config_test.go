package config

import (
	"strings"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config is invalid: %v", err)
	}
}

func TestServerValidate(t *testing.T) {
	tests := []struct {
		name    string
		server  Server
		wantErr string
	}{
		{
			name:   "tls only",
			server: Server{Port: "2375", TLSPort: "2376"},
		},
		{
			name:   "tls only ignores an invalid unused plaintext port",
			server: Server{Port: "not-a-port", TLSPort: "2376"},
		},
		{
			name:   "tls and plaintext on distinct ports",
			server: Server{Port: "2375", TLSPort: "2376", AllowPlaintextWithTLS: new(true)},
		},
		{
			name:   "plaintext only when tls is disabled",
			server: Server{Port: "2375", DisableTLS: new(true)},
		},
		{
			name:   "plaintext only ignores an unused tls port collision",
			server: Server{Port: "2375", TLSPort: "2375", DisableTLS: new(true)},
		},
		{
			name:   "explicit host",
			server: Server{Host: "127.0.0.1", Port: "2375", TLSPort: "2376"},
		},
		{
			name:    "port collision when both listeners are enabled",
			server:  Server{Port: "2375", TLSPort: "2375", AllowPlaintextWithTLS: new(true)},
			wantErr: "tlsPort must differ from port",
		},
		{
			name:    "missing tls port",
			server:  Server{Port: "2375"},
			wantErr: "tlsPort must not be empty",
		},
		{
			name:    "missing plaintext port when tls is disabled",
			server:  Server{DisableTLS: new(true)},
			wantErr: "port must not be empty",
		},
		{
			name:    "privileged tls port",
			server:  Server{Port: "2375", TLSPort: "443"},
			wantErr: "expected range 1024-65535",
		},
		{
			name:    "out of range tls port",
			server:  Server{Port: "2375", TLSPort: "70000"},
			wantErr: "expected range 1024-65535",
		},
		{
			name:    "non numeric tls port",
			server:  Server{Port: "2375", TLSPort: "https"},
			wantErr: "expected a numeric port",
		},
		{
			name:    "invalid host",
			server:  Server{Host: "not a host", Port: "2375", TLSPort: "2376"},
			wantErr: "host=\"not a host\" is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.server.Validate()
			assertError(t, err, tt.wantErr)
		})
	}
}

func TestServerListenerFlags(t *testing.T) {
	tests := []struct {
		name      string
		server    Server
		tls       bool
		plaintext bool
	}{
		{name: "unset defaults to tls only", tls: true},
		{name: "tls with plaintext", server: Server{AllowPlaintextWithTLS: new(true)}, tls: true, plaintext: true},
		{name: "tls disabled implies plaintext", server: Server{DisableTLS: new(true)}, plaintext: true},
		{name: "tls disabled with plaintext allowed", server: Server{DisableTLS: new(true), AllowPlaintextWithTLS: new(true)}, plaintext: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.server.TLSEnabled(); got != tt.tls {
				t.Errorf("TLSEnabled() = %v, want %v", got, tt.tls)
			}
			if got := tt.server.PlaintextEnabled(); got != tt.plaintext {
				t.Errorf("PlaintextEnabled() = %v, want %v", got, tt.plaintext)
			}
		})
	}
}

func TestTLSValidate(t *testing.T) {
	tests := []struct {
		name    string
		tls     TLS
		wantErr string
	}{
		{
			name: "cert and key with an empty version",
			tls:  TLS{CertFile: "/certs/tls.crt", KeyFile: "/certs/tls.key"},
		},
		{
			name: "explicit version",
			tls:  TLS{CertFile: "/certs/tls.crt", KeyFile: "/certs/tls.key", MinTLSVersion: "1.2"},
		},
		{
			name: "nonexistent paths are accepted because loading validates them",
			tls:  TLS{CertFile: "/no/such/tls.crt", KeyFile: "/no/such/tls.key"},
		},
		{
			name:    "missing cert",
			tls:     TLS{KeyFile: "/certs/tls.key"},
			wantErr: "tlsCertFile must not be empty",
		},
		{
			name:    "missing key",
			tls:     TLS{CertFile: "/certs/tls.crt"},
			wantErr: "tlsKeyFile must not be empty",
		},
		{
			name:    "unsupported version",
			tls:     TLS{CertFile: "/certs/tls.crt", KeyFile: "/certs/tls.key", MinTLSVersion: "1.1"},
			wantErr: "minTLSVersion=\"1.1\" is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, tt.tls.Validate(), tt.wantErr)
		})
	}
}

func TestKubernetesValidate(t *testing.T) {
	tests := []struct {
		name       string
		kubernetes Kubernetes
		wantErr    string
	}{
		{
			name:       "namespace and health port",
			kubernetes: Kubernetes{Namespace: "dink", HealthPort: "8080"},
		},
		{
			name:       "missing namespace",
			kubernetes: Kubernetes{HealthPort: "8080"},
			wantErr:    "namespace must not be empty",
		},
		{
			name:       "privileged health port",
			kubernetes: Kubernetes{Namespace: "dink", HealthPort: "80"},
			wantErr:    "healthPort=\"80\" is invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, tt.kubernetes.Validate(), tt.wantErr)
		})
	}
}

func TestLogValidate(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if err := (&Log{Level: level}).Validate(); err != nil {
			t.Errorf("level %q: %v", level, err)
		}
		if err := (&AccessLog{Level: level}).Validate(); err != nil {
			t.Errorf("access log level %q: %v", level, err)
		}
	}
	assertError(t, (&Log{Level: "trace"}).Validate(), "logLevel=\"trace\" is invalid")
	assertError(t, (&Log{}).Validate(), "logLevel=\"\" is invalid")
	assertError(t, (&AccessLog{Level: "trace"}).Validate(), "accessLogLevel=\"trace\" is invalid")
}

func TestAuthValidate(t *testing.T) {
	valid := Auth{Plugins: []AuthPlugin{{Name: "opa", Path: "/plugins/opa"}}}
	assertError(t, valid.Validate(), "")
	assertError(t, (&Auth{}).Validate(), "")

	missingName := Auth{Plugins: []AuthPlugin{{Path: "/plugins/opa"}}}
	assertError(t, missingName.Validate(), "authPlugins[0].name must not be empty")

	missingPath := Auth{Plugins: []AuthPlugin{{Name: "opa", Path: "/plugins/opa"}, {Name: "other"}}}
	assertError(t, missingPath.Validate(), "authPlugins[1].path must not be empty")
}

func TestBuildKitValidate(t *testing.T) {
	tests := []struct {
		name     string
		buildKit BuildKit
		wantErr  string
	}{
		{
			name:     "empty url disables builds",
			buildKit: BuildKit{},
		},
		{
			name:     "tcp host and port",
			buildKit: BuildKit{URL: "tcp://buildkitd:1234"},
		},
		{
			name:     "tcp fully qualified service name",
			buildKit: BuildKit{URL: "tcp://buildkitd.dink.svc.cluster.local:1234"},
		},
		{
			name:     "tcp ip address",
			buildKit: BuildKit{URL: "tcp://10.0.0.1:1234"},
		},
		{
			name:     "tcp privileged port is allowed for a remote endpoint",
			buildKit: BuildKit{URL: "tcp://buildkitd:443"},
		},
		{
			name:     "unix socket",
			buildKit: BuildKit{URL: "unix:///run/buildkit/buildkitd.sock"},
		},
		{
			name:     "missing port",
			buildKit: BuildKit{URL: "tcp://buildkitd"},
			wantErr:  "expected tcp://host:port",
		},
		{
			name:     "missing host",
			buildKit: BuildKit{URL: "tcp://:1234"},
			wantErr:  "host must not be empty",
		},
		{
			name:     "invalid host",
			buildKit: BuildKit{URL: "tcp://build_kitd:1234"},
			wantErr:  "is invalid",
		},
		{
			name:     "non numeric port",
			buildKit: BuildKit{URL: "tcp://buildkitd:grpc"},
			wantErr:  "is invalid",
		},
		{
			name:     "out of range port",
			buildKit: BuildKit{URL: "tcp://buildkitd:70000"},
			wantErr:  "port must be in range 1-65535",
		},
		{
			name:     "unix without a path",
			buildKit: BuildKit{URL: "unix://"},
			wantErr:  "expected unix:///path/to/socket",
		},
		{
			name:     "unsupported scheme",
			buildKit: BuildKit{URL: "http://buildkitd:1234"},
			wantErr:  "expected a tcp:// or unix:// address",
		},
		{
			name:     "missing scheme",
			buildKit: BuildKit{URL: "buildkitd:1234"},
			wantErr:  "expected a tcp:// or unix:// address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, tt.buildKit.Validate(), tt.wantErr)
		})
	}
}

func TestConfigValidateSkipsTLSWhenDisabled(t *testing.T) {
	cfg := Default()
	cfg.Server.DisableTLS = new(true)
	cfg.TLS = TLS{}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("plaintext-only config should not require TLS material: %v", err)
	}

	cfg.Server.DisableTLS = new(false)
	assertError(t, cfg.Validate(), "tlsCertFile must not be empty")
}

func TestConfigValidateAggregatesErrors(t *testing.T) {
	cfg := &Config{
		Log:        Log{Level: "trace"},
		AccessLog:  AccessLog{Level: "verbose"},
		Kubernetes: Kubernetes{HealthPort: "8080"},
		Server:     Server{Port: "2375", TLSPort: "2375", AllowPlaintextWithTLS: new(true)},
		TLS:        TLS{CertFile: "/certs/tls.crt", KeyFile: "/certs/tls.key"},
		BuildKit:   BuildKit{URL: "http://buildkitd:1234"},
		Auth:       Auth{Plugins: []AuthPlugin{{}}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"logLevel=\"trace\"",
		"accessLogLevel=\"verbose\"",
		"namespace must not be empty",
		"tlsPort must differ from port",
		"authPlugins[0].name must not be empty",
		"expected a tcp:// or unix:// address",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func assertError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected an error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}
