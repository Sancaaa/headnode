package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func get(t *testing.T, url string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestAgentBinaryIsServedWithoutLogin(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	s.UseAgentDir(dir)
	program := "\x7fELF pretend this is a program"
	if err := os.WriteFile(filepath.Join(dir, "headnode-agent-linux-amd64"), []byte(program), 0o755); err != nil {
		t.Fatal(err)
	}

	// No cookie, no Authorization header: a host that is about to enroll has neither.
	resp, body := get(t, srv.URL+"/download/headnode-agent-linux-amd64", nil)
	if resp.StatusCode != http.StatusOK || body != program {
		t.Fatalf("status = %d, body %q, want 200 and the program", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), "headnode-agent-linux-amd64") {
		t.Errorf("Content-Disposition = %q, want the file name", resp.Header.Get("Content-Disposition"))
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", resp.Header.Get("Cache-Control"))
	}

	// An interrupted download can be resumed.
	resp, body = get(t, srv.URL+"/download/headnode-agent-linux-amd64", map[string]string{"Range": "bytes=4-7"})
	if resp.StatusCode != http.StatusPartialContent || body != program[4:8] {
		t.Errorf("range request: status = %d, body %q, want 206 and %q", resp.StatusCode, body, program[4:8])
	}
}

// The Windows program has an .exe in its name, and that is the name that is served.
func TestWindowsAgentIsServedByItsExeName(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	s.UseAgentDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "headnode-agent-windows-amd64.exe"), []byte("MZ pretend"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, body := get(t, srv.URL+"/download/headnode-agent-windows-amd64.exe", nil)
	if resp.StatusCode != http.StatusOK || body != "MZ pretend" {
		t.Errorf("status = %d, body %q, want 200 and the program", resp.StatusCode, body)
	}
	// Without the suffix it is not a name on the list.
	if resp, _ := get(t, srv.URL+"/download/headnode-agent-windows-amd64", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("without .exe: status = %d, want 404", resp.StatusCode)
	}
}

func TestAgentBinaryRefusesAnythingNotOnTheList(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	s.UseAgentDir(dir)
	// A file that is in the folder but not on the list must not be reachable,
	// and neither must anything outside the folder.
	for _, name := range []string{"headnode-agent-linux-amd64", "secret.txt", "headnode-agent-windows-amd64"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	for _, path := range []string{
		"/download/secret.txt",
		"/download/headnode-agent-windows-amd64",
		"/download/..%2Fsecret.txt",
		"/download/%2e%2e%2fheadnode.db",
		"/download/headnode-agent-linux-amd64.sha256",
		"/download/",
	} {
		if resp, _ := get(t, srv.URL+path, nil); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestAgentBinaryThatWasNeverBuiltSaysHowToBuildIt(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	s.UseAgentDir(t.TempDir()) // an empty folder

	resp, body := get(t, srv.URL+"/download/headnode-agent-linux-arm64", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "scripts/build-agents") {
		t.Errorf("status = %d, body %q, want 404 that names scripts/build-agents", resp.StatusCode, body)
	}
}

func TestAgentDownloadsAreOffWithoutAFolder(t *testing.T) {
	_, srv, _, _ := newTestServer(t) // UseAgentDir was never called
	if resp, _ := get(t, srv.URL+"/download/headnode-agent-linux-amd64", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A folder with the right name is not a program.
func TestAgentBinaryThatIsAFolderIsRefused(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	dir := t.TempDir()
	s.UseAgentDir(dir)
	if err := os.Mkdir(filepath.Join(dir, "headnode-agent-linux-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if resp, _ := get(t, srv.URL+"/download/headnode-agent-linux-amd64", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
