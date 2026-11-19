package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"headnode/protocol"
)

func TestRenameAndMoveHost(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]
	web := w.hosts["web-01"]
	path := "/api/hosts/" + web
	lab := w.groups["Lab"]

	// Rename. The name the agent reported (hostname) must stay as it was.
	var updated host
	if code := sendJSON(t, root, w.srv, "PATCH", path, map[string]any{"name": "  web-renamed "}, &updated); code != http.StatusOK {
		t.Fatalf("rename: status = %d, want 200", code)
	}
	if updated.Name != "web-renamed" || updated.Hostname != "web-01" {
		t.Errorf("name=%q hostname=%q, want web-renamed / web-01", updated.Name, updated.Hostname)
	}

	// Move to Lab: alice (Office) loses sight of it, carol (Lab) gains it.
	if code := sendJSON(t, root, w.srv, "PATCH", path, map[string]any{"group_id": lab}, &updated); code != http.StatusOK {
		t.Fatalf("move: status = %d, want 200", code)
	}
	if updated.GroupName == nil || *updated.GroupName != "Lab" {
		t.Errorf("group_name = %v, want Lab", updated.GroupName)
	}
	if code := getJSON(t, w.clients["alice"], w.srv, path, nil); code != http.StatusNotFound {
		t.Errorf("alice after the move: status = %d, want 404", code)
	}
	if code := getJSON(t, w.clients["carol"], w.srv, path, nil); code != http.StatusOK {
		t.Errorf("carol after the move: status = %d, want 200", code)
	}

	// null takes it out of every group: now only admins see it.
	updated = host{}
	if code := sendJSON(t, root, w.srv, "PATCH", path, map[string]any{"group_id": nil}, &updated); code != http.StatusOK {
		t.Fatalf("ungroup: status = %d, want 200", code)
	}
	if updated.GroupID != nil {
		t.Errorf("group_id = %v, want null", *updated.GroupID)
	}
	if code := getJSON(t, w.clients["carol"], w.srv, path, nil); code != http.StatusNotFound {
		t.Errorf("carol after ungrouping: status = %d, want 404", code)
	}
	if code := getJSON(t, root, w.srv, path, nil); code != http.StatusOK {
		t.Errorf("root after ungrouping: status = %d, want 200", code)
	}

	// Both in one request.
	office := w.groups["Office"]
	if code := sendJSON(t, root, w.srv, "PATCH", path, map[string]any{"name": "web-final", "group_id": office}, &updated); code != http.StatusOK {
		t.Fatalf("rename + move: status = %d, want 200", code)
	}
	if updated.Name != "web-final" || updated.GroupID == nil || *updated.GroupID != office {
		t.Errorf("after rename + move: %+v", updated)
	}

	// Bad requests are refused, and a request that is half good changes nothing.
	bad := []struct {
		body map[string]any
		want int
	}{
		{map[string]any{}, http.StatusBadRequest},
		{map[string]any{"name": ""}, http.StatusBadRequest},
		{map[string]any{"name": "line\nbreak"}, http.StatusBadRequest},
		{map[string]any{"group_id": "Lab"}, http.StatusBadRequest},
		{map[string]any{"group_id": 0}, http.StatusBadRequest},
		{map[string]any{"group_id": 9999}, http.StatusNotFound},
		{map[string]any{"name": "should-not-stick", "group_id": 9999}, http.StatusNotFound},
	}
	for _, c := range bad {
		if code := sendJSON(t, root, w.srv, "PATCH", path, c.body, nil); code != c.want {
			t.Errorf("PATCH %v: status = %d, want %d", c.body, code, c.want)
		}
	}
	var after host
	getJSON(t, root, w.srv, path, &after)
	if after.Name != "web-final" {
		t.Errorf("name = %q after refused requests, want web-final", after.Name)
	}

	if code := sendJSON(t, root, w.srv, "PATCH", "/api/hosts/a_nobody", map[string]any{"name": "x"}, nil); code != http.StatusNotFound {
		t.Errorf("unknown host: status = %d, want 404", code)
	}

	// Real changes are audited, and only real changes: no-ops are not.
	counts := map[string]int{}
	for _, r := range auditRows(t, w.s.db) {
		counts[r.Action]++
	}
	if counts["host_rename"] != 2 || counts["host_move"] != 3 {
		t.Errorf("audit: %d renames and %d moves, want 2 and 3", counts["host_rename"], counts["host_move"])
	}
}

func TestMovingToTheSameGroupIsNotAudited(t *testing.T) {
	w := newPermWorld(t)
	path := "/api/hosts/" + w.hosts["web-01"]
	office := w.groups["Office"] // web-01 is already there

	if code := sendJSON(t, w.clients["root"], w.srv, "PATCH", path, map[string]any{"group_id": office}, nil); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "host_move" {
			t.Errorf("a move to the same group was audited: %+v", r)
		}
	}
}

func TestDeleteHost(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)

	// The agent is connected when the admin deletes it.
	conn, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	sendMessage(t, conn, protocol.TypeHello, validHello())
	waitFor(t, "agent to be online", func() bool { online, _ := s.agents.status(id); return online })

	if code := sendJSON(t, admin, srv, "DELETE", "/api/hosts/"+id, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", code)
	}

	// Its live connection is cut at once...
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Error("the deleted host's connection is still open")
	}
	waitFor(t, "agent to be offline", func() bool { online, _ := s.agents.status(id); return !online })

	// ...it is gone from the list, and deleting twice is "not found"...
	var list []host
	getJSON(t, admin, srv, "/api/hosts", &list)
	if len(list) != 0 {
		t.Errorf("list = %+v, want it empty", list)
	}
	if code := sendJSON(t, admin, srv, "DELETE", "/api/hosts/"+id, nil, nil); code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", code)
	}

	// ...its secret no longer works (the roadmap's wording)...
	if _, resp, err := dialAgent(srv, id, secret); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect with the deleted host's secret: err=%v response=%v, want HTTP 401", err, resp)
	}

	// ...and the audit log still names it, though the host is gone.
	var found bool
	for _, r := range auditRows(t, s.db) {
		if r.Action == "host_delete" && r.AgentID.String == id {
			found = true
		}
	}
	if !found {
		t.Error("no host_delete entry for the deleted host in the audit log")
	}
}

type tokenReply struct {
	Token          string    `json:"token"`
	ExpiresAt      time.Time `json:"expires_at"`
	GroupID        *int64    `json:"group_id"`
	GroupName      *string   `json:"group_name"`
	InstallCommand string    `json:"install_command"`
}

func TestEnrollTokenForGroup(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]
	office := w.groups["Office"]

	var reply tokenReply
	path := "/api/enroll-tokens"
	if code := sendJSON(t, root, w.srv, "POST", path, map[string]any{"group_id": office}, &reply); code != http.StatusCreated {
		t.Fatalf("create token: status = %d, want 201", code)
	}
	if len(reply.Token) != 64 || reply.GroupName == nil || *reply.GroupName != "Office" ||
		reply.GroupID == nil || *reply.GroupID != office || time.Until(reply.ExpiresAt) < 23*time.Hour {
		t.Errorf("reply = %+v", reply)
	}
	if !strings.Contains(reply.InstallCommand, reply.Token) || !strings.Contains(reply.InstallCommand, w.srv.URL) {
		t.Errorf("install_command = %q, want it to contain the token and %s", reply.InstallCommand, w.srv.URL)
	}
	if n := count(t, w.s.db, `SELECT COUNT(*) FROM enroll_tokens WHERE token_hash = ?`, reply.Token); n != 0 {
		t.Error("plain-text token found in the database")
	}

	// Hosts that enroll with it join Office, and the token can be used again.
	var newIDs []string
	for i := 0; i < 2; i++ {
		resp := postEnroll(t, w.srv, protocol.EnrollRequest{Token: reply.Token, Hostname: fmt.Sprintf("new-%d", i), OS: "linux", Arch: "amd64"})
		var out protocol.EnrollResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("enroll %d: status = %d, err = %v", i, resp.StatusCode, err)
		}
		newIDs = append(newIDs, out.AgentID)
	}
	for _, id := range newIDs {
		var h host
		if code := getJSON(t, root, w.srv, "/api/hosts/"+id, &h); code != http.StatusOK {
			t.Fatalf("GET new host: status = %d", code)
		}
		if h.GroupName == nil || *h.GroupName != "Office" {
			t.Errorf("new host group = %v, want Office", h.GroupName)
		}
		// alice is an operator in Office, carol is only in Lab.
		if code := getJSON(t, w.clients["alice"], w.srv, "/api/hosts/"+id, nil); code != http.StatusOK {
			t.Errorf("alice sees new host: status = %d, want 200", code)
		}
		if code := getJSON(t, w.clients["carol"], w.srv, "/api/hosts/"+id, nil); code != http.StatusNotFound {
			t.Errorf("carol sees new host: status = %d, want 404", code)
		}
	}

	// A token made on the command line has no group: the host starts hidden.
	cliToken, _, err := CreateEnrollToken(w.s.db)
	if err != nil {
		t.Fatal(err)
	}
	resp := postEnroll(t, w.srv, protocol.EnrollRequest{Token: cliToken, Hostname: "cli-host", OS: "linux", Arch: "amd64"})
	var out protocol.EnrollResponse
	json.NewDecoder(resp.Body).Decode(&out)
	var h host
	getJSON(t, root, w.srv, "/api/hosts/"+out.AgentID, &h)
	if h.GroupID != nil {
		t.Errorf("host from a command-line token has group %v, want none", *h.GroupID)
	}

	if code := sendJSON(t, root, w.srv, "POST", path, map[string]any{"group_id": 9999}, nil); code != http.StatusNotFound {
		t.Errorf("unknown group: status = %d, want 404", code)
	}

	// Creating a token is audited, and the entry names the group, not the token.
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "token_create" {
			found = true
			if r.Detail != "group=Office" {
				t.Errorf("token_create detail = %q, want group=Office", r.Detail)
			}
		}
	}
	if !found {
		t.Error("no token_create entry in the audit log")
	}
}

// "Add host" on the Hosts page lets the admin choose no group at all.
func TestEnrollTokenWithoutGroup(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]

	// Both an explicit null and a missing field mean "no group".
	for _, body := range []map[string]any{{"group_id": nil}, {}} {
		var reply tokenReply
		if code := sendJSON(t, root, w.srv, "POST", "/api/enroll-tokens", body, &reply); code != http.StatusCreated {
			t.Fatalf("body %v: status = %d, want 201", body, code)
		}
		if reply.GroupID != nil || reply.GroupName != nil {
			t.Errorf("body %v: reply has group %v / %v, want none", body, reply.GroupID, reply.GroupName)
		}

		// A host that enrolls with it starts ungrouped: admins see it, nobody else does.
		resp := postEnroll(t, w.srv, protocol.EnrollRequest{Token: reply.Token, Hostname: "loose", OS: "linux", Arch: "amd64"})
		var out protocol.EnrollResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("enroll: status = %d, err = %v", resp.StatusCode, err)
		}
		var h host
		getJSON(t, root, w.srv, "/api/hosts/"+out.AgentID, &h)
		if h.GroupID != nil {
			t.Errorf("host has group %v, want none", *h.GroupID)
		}
		for _, who := range []string{"alice", "bob", "carol", "dave"} {
			if code := getJSON(t, w.clients[who], w.srv, "/api/hosts/"+out.AgentID, nil); code != http.StatusNotFound {
				t.Errorf("%s sees an ungrouped host: status = %d, want 404", who, code)
			}
		}
	}

	// The audit entry says there was no group.
	var details []string
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "token_create" {
			details = append(details, r.Detail)
		}
	}
	if len(details) != 2 || details[0] != "group=(none)" || details[1] != "group=(none)" {
		t.Errorf("token_create details = %v, want two entries of group=(none)", details)
	}

	// A token needs a valid JSON object.
	if code := sendJSON(t, root, w.srv, "POST", "/api/enroll-tokens", nil, nil); code != http.StatusBadRequest {
		t.Errorf("empty body: status = %d, want 400", code)
	}
}

func TestServerURL(t *testing.T) {
	cases := []struct {
		name, remote, proto, want string
	}{
		{"plain HTTP", "203.0.113.9:1000", "", "http://headnode.example.com"},
		{"HTTPS through the proxy on this machine", "127.0.0.1:5555", "https", "https://headnode.example.com"},
		{"the header from a stranger is not believed", "203.0.113.9:1000", "https", "http://headnode.example.com"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "headnode.example.com"
		r.RemoteAddr = c.remote
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := serverURL(r); got != c.want {
			t.Errorf("%s: serverURL = %q, want %q", c.name, got, c.want)
		}
	}
}
