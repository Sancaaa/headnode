package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseForward(t *testing.T) {
	good := map[string]forwardSpec{
		"2222:web-01:127.0.0.1:22":          {2222, "web-01", "127.0.0.1:22"},
		"8080:a_4411ec05776b5fa5:localhost:80": {8080, "a_4411ec05776b5fa5", "localhost:80"},
		"5432:db:[::1]:5432":                {5432, "db", "[::1]:5432"},
	}
	for in, want := range good {
		got, err := parseForward(in)
		if err != nil || got != want {
			t.Errorf("parseForward(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}

	for _, in := range []string{
		"", "2222", "2222:web", "2222:web:", "x:web:127.0.0.1:22", "0:web:127.0.0.1:22", "70000:web:127.0.0.1:22",
		"2222::127.0.0.1:22", "2222:web:127.0.0.1",
		// Only the host itself may be a target: refused here, before any connection.
		"2222:web:10.0.0.5:22", "2222:web:example.com:80", "2222:web:169.254.169.254:80",
	} {
		if got, err := parseForward(in); err == nil {
			t.Errorf("parseForward(%q) accepted it as %+v", in, got)
		}
	}
}

func TestResolveHost(t *testing.T) {
	hosts := []hostInfo{
		{ID: "a_1", Name: "web-01"},
		{ID: "a_2", Name: "Lab"},
		{ID: "a_3", Name: "dup"},
		{ID: "a_4", Name: "DUP"},
	}
	for ref, want := range map[string]string{"a_1": "a_1", "web-01": "a_1", "WEB-01": "a_1", "lab": "a_2", "a_4": "a_4"} {
		if got, err := resolveHost(hosts, ref); err != nil || got.ID != want {
			t.Errorf("resolveHost(%q) = %+v, %v, want %s", ref, got, err, want)
		}
	}
	// Two hosts with the same name: refuse, and say which ids to use, so a
	// tunnel never goes to the wrong machine.
	if _, err := resolveHost(hosts, "dup"); err == nil || !strings.Contains(err.Error(), "a_3") || !strings.Contains(err.Error(), "a_4") {
		t.Errorf("two hosts named dup: err = %v, want it to list both ids", err)
	}
	if _, err := resolveHost(hosts, "nope"); err == nil {
		t.Error("an unknown host was found")
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":          "http://127.0.0.1:8080",
		"https://headnode.example.com/":  "https://headnode.example.com",
		"  http://172.26.160.1:8080//  ": "http://172.26.160.1:8080",
	} {
		if got, err := normalizeServer(in); err != nil || got != want {
			t.Errorf("normalizeServer(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	// Guessing http or https could send a password in the clear by accident.
	for _, in := range []string{"", "headnode.example.com", "127.0.0.1:8080", "ftp://x", "https://x/api"} {
		if got, err := normalizeServer(in); err == nil {
			t.Errorf("normalizeServer(%q) accepted it as %q", in, got)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("HNCTL_CONFIG", path)

	if _, err := loadConfig(); err != errNotLoggedIn {
		t.Errorf("before logging in: err = %v, want errNotLoggedIn", err)
	}
	want := config{Server: "http://127.0.0.1:8080", Token: "tok", Username: "sanca"}
	if err := saveConfig(want); err != nil {
		t.Fatal(err)
	}
	if got, err := loadConfig(); err != nil || got != want {
		t.Errorf("loaded %+v, %v, want %+v", got, err, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("config file mode = %v, %v, want 0600: it holds a login token", info.Mode().Perm(), err)
		}
	}
	if err := deleteConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(); err != errNotLoggedIn {
		t.Errorf("after logout: err = %v, want errNotLoggedIn", err)
	}
	if err := deleteConfig(); err != nil {
		t.Errorf("deleting a missing config: %v, want it to be fine", err)
	}
}

func TestLoginAsksForACLIToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password, Client string }
		json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/api/login" || req.Client != "cli" {
			http.Error(w, `{"error":"unexpected request"}`, http.StatusBadRequest)
			return
		}
		if req.Username != "sanca" || req.Password != "correct horse" {
			http.Error(w, `{"error":"invalid username or password"}`, http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"id":1,"username":"sanca","is_admin":true,"token":"abc123"}`)
	}))
	defer srv.Close()

	cfg, err := login(srv.URL, "sanca", "correct horse")
	if err != nil || cfg.Token != "abc123" || cfg.Username != "sanca" || cfg.Server != srv.URL {
		t.Errorf("login = %+v, %v, want the token and username", cfg, err)
	}
	if _, err := login(srv.URL, "sanca", "wrong"); err == nil || !strings.Contains(err.Error(), "invalid username or password") {
		t.Errorf("wrong password: err = %v, want the server's message", err)
	}
}

func TestExpiredLoginTellsYouToLogInAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"login required"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := newClient(config{Server: srv.URL, Token: "old"}).do("GET", "/api/hosts", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "hnctl login") {
		t.Errorf("err = %v, want a hint to run hnctl login", err)
	}
}

// fakeServer stands in for headnode: the API calls hnctl makes, and a session
// WebSocket that echoes whatever it receives.
type fakeServer struct {
	*httptest.Server
	sessions atomic.Int32
	target   atomic.Value // the target of the last tunnel request
}

func newFakeServer(t *testing.T, sessionStatus int) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	mux := http.NewServeMux()
	authorized := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer tok" }

	mux.HandleFunc("POST /api/hosts/{id}/sessions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			http.Error(w, `{"error":"login required"}`, http.StatusUnauthorized)
			return
		}
		if sessionStatus != http.StatusCreated {
			http.Error(w, `{"error":"you do not have permission to open a tunnel to this host"}`, sessionStatus)
			return
		}
		var req struct {
			Kind   string `json:"kind"`
			Params struct {
				Target string `json:"target"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Kind != "tunnel" || r.PathValue("id") != "a_1" {
			http.Error(w, `{"error":"unexpected request"}`, http.StatusBadRequest)
			return
		}
		f.target.Store(req.Params.Target)
		n := f.sessions.Add(1)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "s_" + string(rune('0'+n))})
	})
	// The browser-facing WebSocket: here it is the "host", and echoes.
	mux.HandleFunc("GET /ui/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) || r.Header.Get("Origin") != "" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		nc := websocket.NetConn(r.Context(), conn, websocket.MessageBinary)
		defer nc.Close()
		io.Copy(nc, nc)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func startForward(t *testing.T, f *fakeServer) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := newClient(config{Server: f.URL, Token: "tok"})
	done := make(chan struct{})
	go func() {
		serveForward(ctx, ln, c, hostInfo{ID: "a_1", Name: "web-01"}, "127.0.0.1:22")
		close(done)
	}()
	return ln.Addr().String(), func() {
		cancel()
		ln.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serveForward did not return after the listener was closed")
		}
	}
}

func TestForwardCarriesBytesThroughTheServer(t *testing.T) {
	f := newFakeServer(t, http.StatusCreated)
	addr, stop := startForward(t, f)
	defer stop()

	// Two connections at once, like an ssh and an scp: two sessions, and each
	// one gets its own bytes back.
	for _, message := range []string{"first connection", "second connection"} {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, message); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(message))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != message {
			t.Fatalf("echo of %q = %q, %v", message, got, err)
		}
	}
	if n := f.sessions.Load(); n != 2 {
		t.Errorf("sessions opened = %d, want one per connection (2)", n)
	}
	if target := f.target.Load(); target != "127.0.0.1:22" {
		t.Errorf("target sent to the server = %v, want 127.0.0.1:22", target)
	}
}

func TestForwardClosesTheLocalConnectionWhenTheTunnelIsRefused(t *testing.T) {
	f := newFakeServer(t, http.StatusForbidden)
	addr, stop := startForward(t, f)
	defer stop()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// ssh must see the connection end, not hang: reading gives EOF.
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read on a refused tunnel: %v, want io.EOF (the connection closed)", err)
	}
}

func TestForwardWithoutALoginTokenIsRefusedByTheServer(t *testing.T) {
	f := newFakeServer(t, http.StatusCreated)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveForward(ctx, ln, newClient(config{Server: f.URL, Token: "wrong"}), hostInfo{ID: "a_1", Name: "web-01"}, "127.0.0.1:22")
	defer ln.Close()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read with a bad token: %v, want io.EOF", err)
	}
	if n := f.sessions.Load(); n != 0 {
		t.Errorf("sessions opened = %d with a bad token, want 0", n)
	}
}
