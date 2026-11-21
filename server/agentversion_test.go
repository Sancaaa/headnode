package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionOlder(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.9.2", "0.10.0", true}, // as text "0.9.2" comes after "0.10.0": numbers are compared, not letters
		{"0.10.0", "0.9.2", false},
		{"0.9.2", "0.9.2", false},
		{"1.2", "1.2.0", false}, // a missing number is 0
		{"1.2", "1.2.1", true},
		{"9.9.9", "10.0.0", true},
		{"", "1.0.0", false}, // a host whose version is not known is not told to update
		{"1.0.0", "", false},
		{"0.9.x", "1.0.0", false},
		{"v1.0.0", "1.0.1", false},
		{"1..0", "1.0.1", false},
		{"-1.0", "1.0", false},
		{"01.0", "1.0", false}, // no leading zeros: "01" is not how a version is written
	}
	for _, tc := range tests {
		if got := versionOlder(tc.a, tc.b); got != tc.want {
			t.Errorf("versionOlder(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
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
