package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// openFilesSessionFor opens a files session as the given user on a host whose
// agent is connected on control, and returns the session id.
func openFilesSessionFor(t *testing.T, w *permWorld, who, host string, control *websocket.Conn) string {
	t.Helper()
	code, id := openSession(t, w.clients[who], w.srv, w.hosts[host], map[string]any{"kind": "files"})
	if code != http.StatusCreated {
		t.Fatalf("%s opening a files session on %s: status = %d", who, host, code)
	}
	readOpen(t, control)
	return id
}

func reportChange(t *testing.T, control *websocket.Conn, c protocol.FileChange) {
	t.Helper()
	sendMessage(t, control, protocol.TypeFileChange, c)
}

// changeRows are the audit lines the file change reports produce, in order.
func changeRows(t *testing.T, s *Server) []auditRow {
	t.Helper()
	var out []auditRow
	for _, r := range auditRows(t, s.db) {
		if r.Action == "file_mkdir" || r.Action == "file_rename" || r.Action == "file_delete" ||
			r.Action == "file_copy" || r.Action == "file_chmod" || r.Action == "file_write" {
			out = append(out, r)
		}
	}
	return out
}

// The two operations added with the file manager polish report like the others.
func TestCopyAndChmodReportsAreAudited(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	id := openFilesSessionFor(t, w, "alice", "web-01", control)

	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpCopy, Path: "/etc/app", To: "/tmp/app (1)", OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpChmod, Path: "/opt/run.sh", Mode: "0755", OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpChmod, Path: "/etc/passwd", Mode: "0777", OK: false, Error: "operation not permitted"})

	rows := waitForChangeRows(t, w.s, 3)
	want := []struct{ action, detail string }{
		{"file_copy", "path=/etc/app -> /tmp/app (1)"},
		{"file_chmod", "path=/opt/run.sh mode=0755"},
		{"file_chmod", "path=/etc/passwd mode=0777 FAILED: operation not permitted"},
	}
	for i, r := range rows {
		if r.Action != want[i].action || r.Detail != want[i].detail {
			t.Errorf("row %d = %s %q, want %s %q", i, r.Action, r.Detail, want[i].action, want[i].detail)
		}
	}
}

// A saved file is audited by its size, and the audit has no way to carry the text.
func TestWriteReportIsAuditedBySizeOnly(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	id := openFilesSessionFor(t, w, "alice", "web-01", control)

	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpWrite, Path: "/etc/app.conf", Size: 120, OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpWrite, Path: "/etc/app.conf", Size: 9, OK: false, Error: "the file changed on the host since it was opened"})

	rows := waitForChangeRows(t, w.s, 2)
	want := []string{
		"path=/etc/app.conf size=120",
		"path=/etc/app.conf size=9 FAILED: the file changed on the host since it was opened",
	}
	for i, r := range rows {
		if r.Action != "file_write" || r.Detail != want[i] {
			t.Errorf("row %d = %s %q, want file_write %q", i, r.Action, r.Detail, want[i])
		}
	}
}

func waitForChangeRows(t *testing.T, s *Server, n int) []auditRow {
	t.Helper()
	waitFor(t, "the file change audit lines", func() bool { return len(changeRows(t, s)) >= n })
	return changeRows(t, s)
}

func TestFileChangeReportsAreAuditedUnderTheSessionsUser(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	alice := w.user(t, "alice")
	id := openFilesSessionFor(t, w, "alice", "web-01", control)

	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpMkdir, Path: "/home/x/new", OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpRename, Path: "/a.txt", To: "/b.txt", OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpDelete, Path: "/var/backups", Recursive: true, OK: true})
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpDelete, Path: "/etc/shadow", OK: false, Error: "permission denied"})
	// Text from an agent is not trusted: control characters go, a long error is cut.
	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpMkdir, Path: "/tmp/a\nb", OK: false, Error: strings.Repeat("é", 500)})

	rows := waitForChangeRows(t, w.s, 5)
	want := []struct{ action, detail string }{
		{"file_mkdir", "path=/home/x/new"},
		{"file_rename", "path=/a.txt -> /b.txt"},
		{"file_delete", "path=/var/backups recursive=true"},
		{"file_delete", "path=/etc/shadow FAILED: permission denied"},
		{"file_mkdir", "path=/tmp/ab FAILED: " + strings.Repeat("é", maxAuditError) + "..."},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d change rows, want %d: %+v", len(rows), len(want), rows)
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

func TestFileChangeForAnUnknownOrForeignSessionIsDropped(t *testing.T) {
	w := newPermWorld(t)
	web, lab := w.hosts["web-01"], w.hosts["lab-01"]
	webControl := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	labControl := connectAgent(t, w.s, w.srv, lab, w.secrets["lab-01"])

	mine := openFilesSessionFor(t, w, "alice", "web-01", webControl)
	labs := openFilesSessionFor(t, w, "root", "lab-01", labControl)

	// A shell session is not a files session: nothing it could report is real.
	code, shell := openSession(t, w.clients["alice"], w.srv, web, shellRequest(80, 24))
	if code != http.StatusCreated {
		t.Fatalf("opening a shell: status = %d", code)
	}
	readOpen(t, webControl)

	// web-01's agent claims changes in a session that does not exist, in one that
	// belongs to another host, and in a shell.
	for _, bad := range []string{"s_does_not_exist", labs, shell, ""} {
		reportChange(t, webControl, protocol.FileChange{SessionID: bad, Op: protocol.OpDelete, Path: "/forged", OK: true})
	}
	// An unknown operation in a real session is dropped too.
	reportChange(t, webControl, protocol.FileChange{SessionID: mine, Op: "format-disk", Path: "/forged", OK: true})
	// Messages on one connection are handled in order, so once this one is
	// recorded, every report above has been looked at.
	reportChange(t, webControl, protocol.FileChange{SessionID: mine, Op: protocol.OpMkdir, Path: "/real", OK: true})

	rows := waitForChangeRows(t, w.s, 1)
	if len(rows) != 1 || rows[0].Detail != "path=/real" {
		t.Errorf("rows = %+v, want only the one real report", rows)
	}
}

// The report travels on the control connection and the relay on another, so it
// may arrive after the browser left and the session was removed.
func TestFileChangeAfterTheSessionEndedIsStillAttributed(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	alice := w.user(t, "alice")
	id := openFilesSessionFor(t, w, "alice", "web-01", control)

	sess := w.s.sessions.get(id)
	if sess == nil {
		t.Fatal("the session should be live")
	}
	w.s.sessions.end(sess)
	if w.s.sessions.get(id) != nil {
		t.Fatal("the session should be gone from the table")
	}

	reportChange(t, control, protocol.FileChange{SessionID: id, Op: protocol.OpRename, Path: "/late", To: "/later", OK: true})
	rows := waitForChangeRows(t, w.s, 1)
	if r := rows[0]; r.Detail != "path=/late -> /later" || !r.UserID.Valid || r.UserID.Int64 != alice.ID || r.AgentID.String != web {
		t.Errorf("late report row = %+v, want it attributed to alice on web-01", r)
	}
}

func TestFinishedFilesSessionsAreRememberedBriefly(t *testing.T) {
	table := newSessionTable(time.Hour)
	table.keepFor = 50 * time.Millisecond

	files, err := table.create(7, "agent-a", protocol.KindFiles, actionFilesWrite, "")
	if err != nil {
		t.Fatal(err)
	}
	shell, err := table.create(7, "agent-a", protocol.KindShell, actionShell, "")
	if err != nil {
		t.Fatal(err)
	}
	table.end(files)
	table.end(shell)

	if user, agent, ok := table.changeOwner(files.id, protocol.KindFiles); !ok || user != 7 || agent != "agent-a" {
		t.Errorf("owner of the ended files session = %d, %q, %v, want 7, agent-a, true", user, agent, ok)
	}
	if _, _, ok := table.changeOwner(files.id, protocol.KindDocker); ok {
		t.Error("a files session must not pass as a docker session")
	}
	if _, _, ok := table.changeOwner(shell.id, protocol.KindFiles); ok {
		t.Error("an ended shell session must not be remembered: shells report no changes")
	}

	time.Sleep(80 * time.Millisecond)
	if _, _, ok := table.changeOwner(files.id, protocol.KindFiles); ok {
		t.Error("the owner should be forgotten after the keep time")
	}

	// The cleanup runs when the next session ends, so the map stays small.
	next, err := table.create(8, "agent-b", protocol.KindFiles, actionFilesWrite, "")
	if err != nil {
		t.Fatal(err)
	}
	table.end(next)
	table.mu.Lock()
	kept := len(table.finished)
	table.mu.Unlock()
	if kept != 1 {
		t.Errorf("%d finished sessions kept, want only the newest", kept)
	}
}

func TestClipIsRuneSafe(t *testing.T) {
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip(short) = %q", got)
	}
	if got := clip("ééééé", 3); got != "ééé..." {
		t.Errorf("clip = %q, want three whole characters and the dots", got)
	}
}
