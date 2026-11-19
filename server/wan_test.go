package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Behind Caddy every connection arrives from 127.0.0.1, so the real address is
// in X-Forwarded-For. The test server's clients also connect from 127.0.0.1,
// which makes them look like the local proxy, so the header is believed.
func TestWANAddressBehindTheProxy(t *testing.T) {
	s, srv, id, secret := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL(srv, "/agent/connect"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + id + "." + secret},
			// The client wrote the first entry, our proxy appended the second.
			"X-Forwarded-For": {"10.9.9.9, 203.0.113.5"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	sendMessage(t, conn, "hello", validHello())
	waitFor(t, "agent to be online", func() bool { online, _ := s.agents.status(id); return online })

	var wan string
	if err := s.db.QueryRow(`SELECT wan_addr FROM agents WHERE id = ?`, id).Scan(&wan); err != nil {
		t.Fatal(err)
	}
	if wan != "203.0.113.5" {
		t.Errorf("wan_addr = %q, want 203.0.113.5 (the last entry, added by our own proxy)", wan)
	}

	// It reaches the API, for the Hosts page.
	admin := loggedInClient(t, s, srv)
	var h host
	if code := getJSON(t, admin, srv, "/api/hosts/"+id, &h); code != http.StatusOK || h.WANAddr != "203.0.113.5" {
		t.Errorf("API: status = %d, wan_addr = %q, want 200 and 203.0.113.5", code, h.WANAddr)
	}
}

// The reason the trust check matters: anyone can send an X-Forwarded-For
// header. If it were believed from everybody, an agent could claim any
// address, and a login attacker could rotate fake addresses to dodge the rate
// limit.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name, remote, forwarded, want string
	}{
		{"a client connecting directly", "203.0.113.9:4000", "", "203.0.113.9"},
		{"through the proxy on this machine", "127.0.0.1:5000", "198.51.100.7", "198.51.100.7"},
		{"the proxy adds to what the client wrote: the last entry wins", "127.0.0.1:5000", "10.0.0.1, 198.51.100.7", "198.51.100.7"},
		{"the proxy over IPv6 loopback", "[::1]:5000", "198.51.100.7", "198.51.100.7"},
		{"a header from a stranger is ignored", "203.0.113.9:4000", "10.0.0.1", "203.0.113.9"},
		{"loopback without the header", "127.0.0.1:5000", "", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-For", c.forwarded)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s: clientIP = %q, want %q", c.name, got, c.want)
		}
	}
}
