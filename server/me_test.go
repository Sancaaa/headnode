package server

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// A new uploads directory is private to its owner (an upload can be a program or
// a secret). An existing directory keeps its mode, see the M6 deploy checklist.
func TestNewFilesDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "files")
	if err := New(openTestDB(t)).UseFilesDir(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("files directory mode = %o, want 700", got)
	}
}

// The sidebar's "headnode 0.1.0 · up 3h 12m" line comes from /api/me: the
// version, and the start time so the browser can count the uptime itself.
func TestMeCarriesTheServerVersionAndStartTime(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	client := loggedInClient(t, s, srv)

	var me struct {
		Username        string    `json:"username"`
		ServerVersion   string    `json:"server_version"`
		ServerStartedAt time.Time `json:"server_started_at"`
	}
	if code := getJSON(t, client, srv, "/api/me", &me); code != http.StatusOK {
		t.Fatalf("/api/me: status = %d", code)
	}
	if me.Username != "tester" {
		t.Errorf("username = %q: the fields that were already there must still be", me.Username)
	}
	if me.ServerVersion != Version || Version == "" {
		t.Errorf("server_version = %q, want %q", me.ServerVersion, Version)
	}
	// The server was created a moment ago in this test.
	if age := time.Since(me.ServerStartedAt); age < 0 || age > time.Minute {
		t.Errorf("server_started_at = %v (%v ago), want a moment ago", me.ServerStartedAt, age)
	}
}
