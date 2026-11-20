package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// fileServer stands in for the headnode server's /agent/files/{id}: it checks
// the agent's credentials and serves body at /agent/files/f_1.
func fileServer(t *testing.T, body string) (*httptest.Server, Config) {
	t.Helper()
	cfg := Config{AgentID: "a_test", AgentSecret: "secret"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer a_test.secret" {
			http.Error(w, "no credentials", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/agent/files/f_1" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	cfg.Server = srv.URL
	return srv, cfg
}

// partFiles lists the temporary files a fetch may have left next to dest.
func partFiles(t *testing.T, dest string) []string {
	t.Helper()
	found, err := filepath.Glob(dest + ".*.part")
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func command(dest, mode, body string) protocol.Fetch {
	return protocol.Fetch{TransferID: "t_1", FileID: "f_1", DestPath: dest, Mode: mode, SHA256: sum(body), Size: int64(len(body))}
}

func TestFetchPlacesTheVerifiedFile(t *testing.T) {
	body := "the program"
	_, cfg := fileServer(t, body)
	dest := filepath.Join(t.TempDir(), "tool")

	if err := fetchFile(context.Background(), cfg, command(dest, "0755", body)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != body {
		t.Fatalf("destination holds %q, %v, want the file", got, err)
	}
	if runtime.GOOS != "windows" { // Windows has no Unix permission bits
		if info, _ := os.Stat(dest); info.Mode().Perm() != 0o755 {
			t.Errorf("mode = %v, want 0755", info.Mode().Perm())
		}
	}
	if left := partFiles(t, dest); len(left) != 0 {
		t.Errorf("temporary files were left behind: %v", left)
	}
}

func TestFetchReplacesAnExistingFile(t *testing.T) {
	body := "version 2"
	_, cfg := fileServer(t, body)
	dest := filepath.Join(t.TempDir(), "tool")
	write(t, dest, "version 1")

	if err := fetchFile(context.Background(), cfg, command(dest, "0644", body)); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != body {
		t.Errorf("destination holds %q, want the new version", got)
	}
}

// The point of the .part file: whatever goes wrong, the destination holds the
// complete new file or its old content, never half of anything.
func TestFetchLeavesTheDestinationAloneWhenTheFileIsWrong(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*protocol.Fetch)
		wantErr error
	}{
		{"wrong checksum", func(c *protocol.Fetch) { c.SHA256 = sum("something else") }, errBadChecksum},
		{"wrong size", func(c *protocol.Fetch) { c.Size += 10 }, errBadSize},
	}
	for _, c := range cases {
		body := "the new file"
		_, cfg := fileServer(t, body)
		dest := filepath.Join(t.TempDir(), "tool")
		write(t, dest, "the old file")

		cmd := command(dest, "0644", body)
		c.mutate(&cmd)
		if err := fetchFile(context.Background(), cfg, cmd); !errors.Is(err, c.wantErr) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
		}
		if got, _ := os.ReadFile(dest); string(got) != "the old file" {
			t.Errorf("%s: the destination now holds %q, want the old file untouched", c.name, got)
		}
		if left := partFiles(t, dest); len(left) != 0 {
			t.Errorf("%s: a failed attempt left temporary files: %v", c.name, left)
		}
	}
}

func TestFetchRefusals(t *testing.T) {
	body := "data"
	_, cfg := fileServer(t, body)
	dir := t.TempDir()

	cases := []struct {
		name    string
		cmd     protocol.Fetch
		wantErr string
	}{
		{"a relative destination", command("relative/tool", "0644", body), "path must be absolute"},
		{"a setuid mode", command(filepath.Join(dir, "a"), "4755", body), "mode must be at most 0777"},
		{"a mode that is not octal", command(filepath.Join(dir, "b"), "999", body), "mode must be at most 0777"},
		{"no checksum", func() protocol.Fetch { c := command(filepath.Join(dir, "c"), "0644", body); c.SHA256 = ""; return c }(), "no usable checksum"},
		{"a folder that does not exist", command(filepath.Join(dir, "no", "such", "tool"), "0644", body), ""},
		{"a file the server does not have", func() protocol.Fetch { c := command(filepath.Join(dir, "d"), "0644", body); c.FileID = "f_2"; return c }(), "HTTP 404"},
	}
	for _, c := range cases {
		err := fetchFile(context.Background(), cfg, c.cmd)
		if err == nil || !strings.Contains(describe(err), c.wantErr) {
			t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.wantErr)
		}
	}
	// Nothing was created by any of them.
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the directory holds %d entries after refused fetches, want none", len(entries))
	}
}

func TestFetchWithWrongCredentialsFails(t *testing.T) {
	body := "data"
	_, cfg := fileServer(t, body)
	cfg.AgentSecret = "wrong"
	dest := filepath.Join(t.TempDir(), "tool")

	err := fetchFile(context.Background(), cfg, command(dest, "0644", body))
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("err = %v, want HTTP 401", err)
	}
	if exists(dest) || len(partFiles(t, dest)) != 0 {
		t.Error("a refused fetch created files")
	}
}

// A destination that is a directory cannot be replaced by a file: the rename
// fails, and the .part file must not be left lying next to it.
func TestFetchOntoADirectoryFailsCleanly(t *testing.T) {
	body := "data"
	_, cfg := fileServer(t, body)
	dest := t.TempDir() // an existing directory

	if err := fetchFile(context.Background(), cfg, command(dest, "0644", body)); err == nil {
		t.Fatal("replaced a directory with a file")
	}
	if left := partFiles(t, dest); len(left) != 0 {
		t.Errorf("temporary files were left behind: %v", left)
	}
}

// The reason the temporary name is random: several transfers to the same path,
// as when three agents run on one machine, must not trample each other.
func TestSeveralFetchesToTheSamePathAtOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to replace a file that another rename is replacing at the same moment")
	}
	body := strings.Repeat("shared content ", 5000)
	_, cfg := fileServer(t, body)
	dest := filepath.Join(t.TempDir(), "tool")

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errs <- fetchFile(context.Background(), cfg, command(dest, "0755", body)) }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Errorf("fetch %d failed: %v", i, err)
		}
	}
	if got, _ := os.ReadFile(dest); string(got) != body {
		t.Errorf("destination holds %d bytes that differ from the file's %d", len(got), len(body))
	}
	if left := partFiles(t, dest); len(left) != 0 {
		t.Errorf("temporary files were left behind: %v", left)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]os.FileMode{"": 0o644, "0644": 0o644, "644": 0o644, "0755": 0o755, "0777": 0o777, "0600": 0o600} {
		if got, err := parseMode(in); err != nil || got != want {
			t.Errorf("parseMode(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"4755", "2755", "1777", "999", "abc", "-1", "07777"} {
		if _, err := parseMode(in); err == nil {
			t.Errorf("parseMode(%q) accepted it", in)
		}
	}
}

// runFetch must report what happened on the control connection.
func TestRunFetchReportsTheResult(t *testing.T) {
	body := "reported"
	_, cfg := fileServer(t, body)

	// A control connection whose far end hands us the first message it gets.
	messages := make(chan []byte, 1)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, data, err := conn.Read(r.Context())
		if err == nil {
			messages <- data
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer control.Close()

	run := func(cmd protocol.Fetch) protocol.FetchResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(control.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		runFetch(ctx, cfg, conn, cmd)

		var data []byte
		select {
		case data = <-messages:
		case <-ctx.Done():
			t.Fatal("no fetch_result arrived")
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeFetchResult {
			t.Fatalf("message = %s, want a fetch_result", data)
		}
		var result protocol.FetchResult
		if err := json.Unmarshal(env.Data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	dir := t.TempDir()
	ok := run(command(filepath.Join(dir, "good"), "0644", body))
	if !ok.OK || ok.TransferID != "t_1" || ok.Error != "" {
		t.Errorf("success result = %+v", ok)
	}

	bad := command(filepath.Join(dir, "bad"), "0644", body)
	bad.SHA256 = sum("nope")
	failed := run(bad)
	if failed.OK || failed.TransferID != "t_1" || !strings.Contains(failed.Error, "checksum") {
		t.Errorf("failure result = %+v, want not ok and a checksum message", failed)
	}
}
