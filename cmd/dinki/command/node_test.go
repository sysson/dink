package command

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteHostsConfig(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.crt")
	certsDir := filepath.Join(dir, "certs.d")
	if err := os.WriteFile(caFile, []byte("first CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeHostsConfig(certsDir, "localhost:5000", caFile); err != nil {
		t.Fatal(err)
	}
	hostDir := filepath.Join(certsDir, "localhost:5000")
	hosts, err := os.ReadFile(filepath.Join(hostDir, "hosts.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`server = "https://localhost:5000"`,
		`[host."https://localhost:5000"]`,
		`capabilities = ["pull", "resolve"]`,
		`ca = "` + filepath.Join(hostDir, "ca.crt") + `"`,
	} {
		if !strings.Contains(string(hosts), want) {
			t.Fatalf("hosts.toml = %s, missing %s", hosts, want)
		}
	}

	if err := os.WriteFile(caFile, []byte("rotated CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeHostsConfig(certsDir, "localhost:5000", caFile); err != nil {
		t.Fatal(err)
	}
	if ca, err := os.ReadFile(filepath.Join(hostDir, "ca.crt")); err != nil || string(ca) != "rotated CA" {
		t.Fatalf("ca.crt = %q, %v; want the rotated CA", ca, err)
	}
	entries, err := os.ReadDir(hostDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("host dir entries = %v, %v; want only ca.crt and hosts.toml", entries, err)
	}
}

func TestProxyForwardsBothDirections(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		request, _ := io.ReadAll(conn)
		_, _ = conn.Write([]byte("reply to " + string(request)))
	}()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proxy(ctx, listener, upstream.Addr().String()) }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	// Half-closing must reach upstream so it can answer after reading everything.
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil || string(reply) != "reply to hello" {
		t.Fatalf("reply = %q, %v", reply, err)
	}
	_ = conn.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("proxy returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy did not stop after cancel")
	}
}
