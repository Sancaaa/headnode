package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The agent's update command reads the newest version from /download/VERSION, with no login.
func TestDownloadVersion(t *testing.T) {
	_, srv, _, _ := newTestServer(t)
	if resp, _ := get(t, srv.URL+"/download/VERSION", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("with no agents folder: status %d, want 404", resp.StatusCode)
	}
}

// The host list marks the hosts that are behind the VERSION file, reads the file
// again each time, and is quiet when there is none.
func TestHostsAreMarkedOutdated(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	connectAgent(t, w.s, w.srv, web, w.secrets["web-01"]) // its hello says version 9.9.9

	list := func() (bool, string) {
		t.Helper()
		var hosts []struct {
			ID              string `json:"id"`
			UpdateAvailable bool   `json:"update_available"`
			LatestVersion   string `json:"latest_version"`
		}
		if code := sendJSON(t, w.clients["alice"], w.srv, "GET", "/api/hosts", nil, &hosts); code != http.StatusOK {
			t.Fatalf("GET /api/hosts: status %d", code)
		}
		for _, h := range hosts {
			if h.ID == web {
				return h.UpdateAvailable, h.LatestVersion
			}
		}
		t.Fatal("web-01 is not in the list")
		return false, ""
	}

	// No agents folder: nothing is marked.
	if outdated, _ := list(); outdated {
		t.Error("marked outdated with no agents folder")
	}

	dir := t.TempDir()
	w.s.UseAgentDir(dir)
	if outdated, _ := list(); outdated {
		t.Error("marked outdated with no VERSION file")
	}

	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("10.0.0\n")
	if outdated, latest := list(); !outdated || latest != "10.0.0" {
		t.Errorf("9.9.9 against 10.0.0: outdated = %v, latest = %q; want true, 10.0.0", outdated, latest)
	}
	// Rebuilt without restarting the server: the next list sees the new file.
	write("9.9.9\n")
	if outdated, _ := list(); outdated {
		t.Error("a host with the newest version is marked outdated")
	}
	write("not a version")
	if outdated, _ := list(); outdated {
		t.Error("a VERSION file that is not a version must mark nothing")
	}
}
