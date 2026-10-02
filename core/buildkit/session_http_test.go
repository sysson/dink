package buildkit_test

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sysson/dink/core/identity"
)

func TestHTTPUpgradedSessionFailureDoesNotReturnHTTPError(t *testing.T) {
	_, address := newBackend(t)
	f := newFixture(t, address)
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := identity.NewContext(r.Context(), identity.Identity{Namespace: "tenant", CommonName: "client"})
		done <- f.gateway.HandleHTTPRequest(ctx, w, r)
	}))
	defer server.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// A malformed nonempty UUID fails after the HTTP connection has been upgraded.
	if _, err := fmt.Fprintf(conn, "POST /session HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: h2c\r\nX-Docker-Expose-Session-Uuid: invalid:uuid\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("upgraded session returned an HTTP error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session handler did not finish")
	}
}
