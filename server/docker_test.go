package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// openDockerSessionFor opens a docker session as the given user on a host whose
// agent is connected on control, and returns the session id.
func openDockerSessionFor(t *testing.T, w *permWorld, who, host string, control *websocket.Conn) string {
	t.Helper()
	code, id := openSession(t, w.clients[who], w.srv, w.hosts[host], map[string]any{"kind": "docker"})
	if code != http.StatusCreated {
		t.Fatalf("%s opening a docker session on %s: status = %d", who, host, code)
	}
	readOpen(t, control)
	return id
}

func reportDockerChange(t *testing.T, control *websocket.Conn, c protocol.DockerChange) {
	t.Helper()
	sendMessage(t, control, protocol.TypeDockerChange, c)
}

// dockerRows are the audit lines the docker change reports produce, in order.
func dockerRows(t *testing.T, s *Server) []auditRow {
	t.Helper()
	var out []auditRow
	for _, r := range auditRows(t, s.db) {
		if strings.HasPrefix(r.Action, "docker_") {
			out = append(out, r)
		}
	}
	return out
}

func waitForDockerRows(t *testing.T, s *Server, n int) []auditRow {
	t.Helper()
	waitFor(t, "the docker audit lines", func() bool { return len(dockerRows(t, s)) >= n })
	return dockerRows(t, s)
}

// The Docker socket is root on the host, so a docker session needs the role of a
// shell: a viewer is refused, like for a shell.
func TestOpenDockerSessionNeedsTheRoleOfAShell(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	request := map[string]any{"kind": "docker", "params": map[string]any{"anything": "ignored"}}

	for _, who := range []string{"alice", "root"} { // an operator, and an administrator
		code, id := openSession(t, w.clients[who], w.srv, web, request)
		if code != http.StatusCreated || !strings.HasPrefix(id, "s_") {
			t.Fatalf("%s: status = %d, id = %q, want 201 and an s_ id", who, code, id)
		}
		open := readOpen(t, control)
		if open.SessionID != id || open.Kind != protocol.KindDocker {
			t.Errorf("%s: the agent got %+v, want a docker session %s", who, open, id)
		}
		if strings.Contains(string(open.Params), "anything") {
			t.Errorf("%s: the browser's params reached the agent: %s", who, open.Params)
		}
	}
	// bob is a viewer: he can see the host, but not use Docker on it.
	if code, _ := openSession(t, w.clients["bob"], w.srv, web, request); code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", code)
	}
	// carol and dave cannot see the host at all.
	for _, who := range []string{"carol", "dave"} {
		if code, _ := openSession(t, w.clients[who], w.srv, web, request); code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", who, code)
		}
	}
	if !hasAudit(t, w.s, "session_open") {
		t.Error("opening a docker session should be audited as session_open")
	}
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "session_open" && !strings.HasPrefix(r.Detail, "kind=docker") {
			t.Errorf("session_open detail = %q, want it to start with kind=docker", r.Detail)
		}
	}
}

func TestOpenDockerSessionOnAnOfflineHost(t *testing.T) {
	w := newPermWorld(t)
	if code, _ := openSession(t, w.clients["alice"], w.srv, w.hosts["web-01"], map[string]any{"kind": "docker"}); code != http.StatusConflict {
		t.Errorf("offline host: status = %d, want 409", code)
	}
}

// A change is audited under the session's user, a refusal as FAILED with Docker's own words.
func TestDockerChangeReportsAreAuditedUnderTheSessionsUser(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	alice := w.user(t, "alice")
	id := openDockerSessionFor(t, w, "alice", "web-01", control)

	refusal := "You cannot remove a running container 3f2a. Stop the container before attempting removal or force remove"
	reportDockerChange(t, control, protocol.DockerChange{SessionID: id, Op: protocol.DockerOpStop, Command: "docker stop web", OK: true})
	reportDockerChange(t, control, protocol.DockerChange{SessionID: id, Op: protocol.DockerOpRemove, Command: "docker rm web", OK: false, Error: refusal})
	reportDockerChange(t, control, protocol.DockerChange{SessionID: id, Op: protocol.DockerOpStack, Command: "docker compose -p shop down", OK: true})
	// Text from an agent is not trusted: control characters go, a long error is cut.
	reportDockerChange(t, control, protocol.DockerChange{SessionID: id, Op: protocol.DockerOpPrune,
		Command: "docker container prune -f\n", OK: false, Error: strings.Repeat("é", 500)})

	rows := waitForDockerRows(t, w.s, 4)
	want := []struct{ action, detail string }{
		{"docker_stop", "command=docker stop web"},
		{"docker_remove", "command=docker rm web FAILED: " + refusal},
		{"docker_stack", "command=docker compose -p shop down"},
		{"docker_prune", "command=docker container prune -f FAILED: " + strings.Repeat("é", maxAuditError) + "..."},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d docker rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, r := range rows {
		if r.Action != want[i].action || r.Detail != want[i].detail {
			t.Errorf("row %d = %s %q, want %s %q", i, r.Action, r.Detail, want[i].action, want[i].detail)
		}
		// The person is the one the session was opened for, the host the one it ran on.
		if !r.UserID.Valid || r.UserID.Int64 != alice.ID || !r.AgentID.Valid || r.AgentID.String != web {
			t.Errorf("row %d: user %v, agent %v, want alice (%d) on %s", i, r.UserID, r.AgentID, alice.ID, web)
		}
	}
}

func TestDockerChangeForAnUnknownOrWrongSessionIsDropped(t *testing.T) {
	w := newPermWorld(t)
	web, lab := w.hosts["web-01"], w.hosts["lab-01"]
	webControl := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	labControl := connectAgent(t, w.s, w.srv, lab, w.secrets["lab-01"])

	mine := openDockerSessionFor(t, w, "alice", "web-01", webControl)
	labs := openDockerSessionFor(t, w, "root", "lab-01", labControl)
	files := openFilesSessionFor(t, w, "alice", "web-01", webControl)
	code, shell := openSession(t, w.clients["alice"], w.srv, web, shellRequest(80, 24))
	if code != http.StatusCreated {
		t.Fatalf("opening a shell: status = %d", code)
	}
	readOpen(t, webControl)

	// web-01's agent claims changes in a session that does not exist, in one that
	// belongs to another host, in a files session and in a shell.
	for _, bad := range []string{"s_does_not_exist", labs, files, shell, ""} {
		reportDockerChange(t, webControl, protocol.DockerChange{SessionID: bad, Op: protocol.DockerOpRemove, Command: "docker rm forged", OK: true})
	}
	// An operation that is not a change, and an unknown one, in a real session.
	reportDockerChange(t, webControl, protocol.DockerChange{SessionID: mine, Op: protocol.DockerOpLogs, Command: "docker logs forged", OK: true})
	reportDockerChange(t, webControl, protocol.DockerChange{SessionID: mine, Op: "format-disk", Command: "forged", OK: true})
	// And a docker report must not pass for a files report either.
	reportChange(t, webControl, protocol.FileChange{SessionID: mine, Op: protocol.OpMkdir, Path: "/forged", OK: true})
	// Messages on one connection are handled in order, so once this one is
	// recorded, every report above has been looked at.
	reportDockerChange(t, webControl, protocol.DockerChange{SessionID: mine, Op: protocol.DockerOpStart, Command: "docker start real", OK: true})

	rows := waitForDockerRows(t, w.s, 1)
	if len(rows) != 1 || rows[0].Detail != "command=docker start real" {
		t.Errorf("rows = %+v, want only the one real report", rows)
	}
	if forged := changeRows(t, w.s); len(forged) != 0 {
		t.Errorf("a file change about a docker session was audited: %+v", forged)
	}
}

// The report travels on the control connection and the relay on another, so it
// may arrive after the browser left and the session was removed.
func TestDockerChangeAfterTheSessionEndedIsStillAttributed(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	alice := w.user(t, "alice")
	id := openDockerSessionFor(t, w, "alice", "web-01", control)

	sess := w.s.sessions.get(id)
	if sess == nil {
		t.Fatal("the session should be live")
	}
	w.s.sessions.end(sess)

	reportDockerChange(t, control, protocol.DockerChange{SessionID: id, Op: protocol.DockerOpRestart, Command: "docker restart web", OK: true})
	rows := waitForDockerRows(t, w.s, 1)
	if r := rows[0]; r.Detail != "command=docker restart web" || !r.UserID.Valid || r.UserID.Int64 != alice.ID || r.AgentID.String != web {
		t.Errorf("late report row = %+v, want it attributed to alice on web-01", r)
	}
}
