package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// getJSON fetches a path as the given (logged-in) client.
func getJSON(t *testing.T, client *http.Client, srv *httptest.Server, path string, into any) int {
	t.Helper()
	resp, err := client.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestHostsAPI(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	client := loggedInClient(t, s, srv)

	// 1. Enrolled but never connected.
	var list []host
	if code := getJSON(t, client, srv, "/api/hosts", &list); code != http.StatusOK {
		t.Fatalf("list status = %d", code)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v, want the one enrolled host", list)
	}
	if list[0].Online || list[0].LastSeenAt != nil || list[0].Heartbeat != nil || list[0].Info != nil {
		t.Errorf("never-connected host: online=%v last_seen_at=%v heartbeat=%v info=%v, want false/nil/nil/nil",
			list[0].Online, list[0].LastSeenAt, list[0].Heartbeat, list[0].Info)
	}

	// 2. Connected, with hello and one heartbeat.
	conn, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	hello := validHello()
	hello.Platform = "Debian GNU/Linux 12 (bookworm)"
	hello.LANIPs = []string{"192.168.10.2"}
	sendMessage(t, conn, protocol.TypeHello, hello)
	sendMessage(t, conn, protocol.TypeHeartbeat, protocol.Heartbeat{CPUPercent: 12.5, MemUsed: 100, MemTotal: 200})
	waitFor(t, "heartbeat to be stored", func() bool { _, hb := s.agents.status(id); return hb != nil })

	var detail host
	if code := getJSON(t, client, srv, "/api/hosts/"+id, &detail); code != http.StatusOK {
		t.Fatalf("detail status = %d", code)
	}
	if !detail.Online || detail.Heartbeat == nil || detail.Heartbeat.CPUPercent != 12.5 {
		t.Errorf("online host: online=%v heartbeat=%+v", detail.Online, detail.Heartbeat)
	}
	if detail.Info == nil || detail.Info.Platform != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("info = %+v, want the hello data", detail.Info)
	}
	if detail.Version != "9.9.9" || detail.LastSeenAt == nil {
		t.Errorf("version=%q last_seen_at=%v, want 9.9.9 and a time", detail.Version, detail.LastSeenAt)
	}
	if detail.WANAddr != "127.0.0.1" {
		t.Errorf("wan_addr = %q, want 127.0.0.1 (where the test client connects from)", detail.WANAddr)
	}
	if !detail.CanShell {
		t.Error("can_shell = false for an admin, want true")
	}

	// The Hosts page needs the addresses, so the list must carry info too.
	var list2 []host
	getJSON(t, client, srv, "/api/hosts", &list2)
	if len(list2) != 1 || list2[0].Info == nil || len(list2[0].Info.LANIPs) != 1 {
		t.Errorf("list entry should include info with lan_ips, got %+v", list2)
	}

	// 3. Disconnected: offline again, no stale heartbeat, but last_seen_at stays.
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "host to go offline", func() bool { online, _ := s.agents.status(id); return !online })
	detail = host{}
	if code := getJSON(t, client, srv, "/api/hosts/"+id, &detail); code != http.StatusOK {
		t.Fatalf("detail status = %d", code)
	}
	if detail.Online || detail.Heartbeat != nil || detail.LastSeenAt == nil {
		t.Errorf("offline host: online=%v heartbeat=%v last_seen_at=%v", detail.Online, detail.Heartbeat, detail.LastSeenAt)
	}
}

func TestHostNotFound(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	client := loggedInClient(t, s, srv)
	if code := getJSON(t, client, srv, "/api/hosts/a_doesnotexist", nil); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestListHostsEmpty(t *testing.T) {
	s := New(openTestDB(t))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	client := loggedInClient(t, s, srv)

	resp, err := client.Get(srv.URL + "/api/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" { // the page does list.map(...), which breaks on null
		t.Errorf("body = %s, want []", raw)
	}
}
