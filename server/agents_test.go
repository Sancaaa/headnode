package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// newTestServer starts the real router with one enrolled agent.
func newTestServer(t *testing.T) (s *Server, srv *httptest.Server, agentID, secret string) {
	t.Helper()
	db := openTestDB(t)
	s = New(db)
	srv = httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	token, _, err := CreateEnrollToken(db)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := EnrollAgent(db, protocol.EnrollRequest{Token: token, Hostname: "enrolled-name", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	return s, srv, resp.AgentID, resp.AgentSecret
}

func dialAgent(srv *httptest.Server, agentID, secret string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel() // the context only bounds the handshake, not the connection's life
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/connect"
	return websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + agentID + "." + secret}},
	})
}

func sendMessage(t *testing.T, conn *websocket.Conn, msgType string, data any) {
	t.Helper()
	msg, err := protocol.NewMessage(msgType, data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func validHello() protocol.Hello {
	return protocol.Hello{Protocol: protocol.Version, AgentVersion: "9.9.9", Hostname: "web-01", OS: "linux", Arch: "amd64"}
}

func TestAgentConnect(t *testing.T) {
	s, srv, id, secret := newTestServer(t)

	conn, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	sendMessage(t, conn, protocol.TypeHello, validHello())
	waitFor(t, "agent to be online", func() bool { online, _ := s.agents.status(id); return online })

	sendMessage(t, conn, protocol.TypeHeartbeat, protocol.Heartbeat{CPUPercent: 12.5, MemUsed: 100, MemTotal: 200})
	waitFor(t, "heartbeat to be stored", func() bool { _, hb := s.agents.status(id); return hb != nil })
	if _, hb := s.agents.status(id); hb.CPUPercent != 12.5 {
		t.Errorf("cpu_percent = %v, want 12.5", hb.CPUPercent)
	}

	// hello is stored in the database, and last_seen_at is set.
	var hostname, version, info string
	var lastSeen int64
	err = s.db.QueryRow(`SELECT hostname, version, info_json, last_seen_at FROM agents WHERE id = ?`, id).
		Scan(&hostname, &version, &info, &lastSeen)
	if err != nil {
		t.Fatal(err)
	}
	if hostname != "web-01" || version != "9.9.9" || !strings.Contains(info, `"hostname":"web-01"`) || lastSeen == 0 {
		t.Errorf("stored hello looks wrong: hostname=%q version=%q info=%q last_seen_at=%d", hostname, version, info, lastSeen)
	}

	// The WAN address is the server's own view of the connection (D-29). The
	// test client connects from 127.0.0.1.
	var wan string
	if err := s.db.QueryRow(`SELECT wan_addr FROM agents WHERE id = ?`, id).Scan(&wan); err != nil {
		t.Fatal(err)
	}
	if wan != "127.0.0.1" {
		t.Errorf("wan_addr = %q, want 127.0.0.1", wan)
	}

	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "agent to be offline", func() bool { online, _ := s.agents.status(id); return !online })
}

func TestAgentConnectRejectsBadCredentials(t *testing.T) {
	_, srv, id, _ := newTestServer(t)

	_, resp, err := dialAgent(srv, id, "wrong-secret")
	if err == nil {
		t.Fatal("connected with a wrong secret")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("response = %v, want HTTP 401", resp)
	}
}

func TestAgentConnectProtocolMismatch(t *testing.T) {
	s, srv, id, secret := newTestServer(t)

	conn, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	hello := validHello()
	hello.Protocol = 99
	sendMessage(t, conn, protocol.TypeHello, hello)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx) // the server answers with a close frame
	if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
		t.Errorf("close status = %d, want %d (policy violation); err = %v", got, websocket.StatusPolicyViolation, err)
	}
	if online, _ := s.agents.status(id); online {
		t.Error("agent with a wrong protocol version was registered as online")
	}
}

func TestSecondConnectionReplacesFirst(t *testing.T) {
	s, srv, id, secret := newTestServer(t)

	first, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer first.CloseNow()
	sendMessage(t, first, protocol.TypeHello, validHello())
	waitFor(t, "first connection to be online", func() bool { online, _ := s.agents.status(id); return online })

	second, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer second.CloseNow()
	sendMessage(t, second, protocol.TypeHello, validHello())

	// The server closes the first connection, so reading from it must fail.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := first.Read(ctx); err == nil {
		t.Fatal("first connection is still open after a second one took over")
	}

	// The first connection's cleanup must not have removed the second one.
	time.Sleep(100 * time.Millisecond)
	if online, _ := s.agents.status(id); !online {
		t.Error("agent went offline when its old connection was replaced")
	}
}
