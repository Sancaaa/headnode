package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// ------------------------------------------------------------- helpers

// cliLogin logs in the way hnctl does: no Origin, and "client":"cli" in the
// body. It returns the status and the token (empty if the login failed).
func cliLogin(t *testing.T, srv *httptest.Server, username, password string) (int, string, *http.Response) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password, "client": "cli"})
	resp, err := http.Post(srv.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply struct {
		Token string `json:"token"`
	}
	json.NewDecoder(resp.Body).Decode(&reply)
	return resp.StatusCode, reply.Token, resp
}

// withToken sends a request with a bearer token and no Origin and no cookie,
// like hnctl. A nil body sends none.
func withToken(t *testing.T, method, url, token string, body, into any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode/100 == 2 {
		json.NewDecoder(resp.Body).Decode(into)
	}
	return resp.StatusCode
}

// ----------------------------------------------------------- bearer login

func TestCLILoginGivesATokenAndNoCookie(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", true); err != nil {
		t.Fatal(err)
	}

	code, token, resp := cliLogin(t, srv, "sanca", "correct horse")
	if code != http.StatusOK || len(token) != 64 {
		t.Fatalf("status = %d, token length = %d, want 200 and 64 hex characters", code, len(token))
	}
	if len(resp.Cookies()) != 0 {
		t.Error("a command-line login set a cookie")
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the reply holds a token and must not be cached")
	}
	// The database holds the hash, never the token (D-09).
	if n := count(t, s.db, `SELECT COUNT(*) FROM web_sessions WHERE token_hash = ?`, token); n != 0 {
		t.Error("the plain token is in the database")
	}

	// The token works, with no cookie and no Origin.
	var me struct {
		Username string `json:"username"`
	}
	if code := withToken(t, "GET", srv.URL+"/api/me", token, nil, &me); code != http.StatusOK || me.Username != "sanca" {
		t.Errorf("/api/me with the token: status = %d, user = %q", code, me.Username)
	}
	// A change request needs no Origin either: the Origin check protects
	// credentials a browser adds by itself, and a bearer token is not one (D-36).
	if code := withToken(t, "POST", srv.URL+"/api/groups", token, map[string]any{"name": "FromCLI"}, nil); code != http.StatusCreated {
		t.Errorf("POST with a bearer token and no Origin: status = %d, want 201", code)
	}

	// Audited as a command-line login.
	var found bool
	for _, r := range auditRows(t, s.db) {
		if r.Action == "login" && strings.Contains(r.Detail, "client=cli") {
			found = true
		}
	}
	if !found {
		t.Error("no login entry marked client=cli in the audit log")
	}
}

func TestCLILoginIsStillRateLimitedAndRejectsBadPasswords(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		if code, token, _ := cliLogin(t, srv, "sanca", "wrong password"); code != http.StatusUnauthorized || token != "" {
			t.Fatalf("attempt %d: status = %d, token %q, want 401 and no token", i, code, token)
		}
	}
	// The fifth comes at once and is refused even with the right password: the
	// command line gets no way around the limit.
	if code, _, _ := cliLogin(t, srv, "sanca", "correct horse"); code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", code)
	}
}

// Only "client":"cli" skips the Origin check at login. Without it, a login
// from a browser still needs one: that is the protection against another site
// logging a visitor in as someone else.
func TestBrowserLoginStillNeedsAnOrigin(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"", "web", "CLI", "cli "} {
		body, _ := json.Marshal(map[string]string{"username": "sanca", "password": "correct horse", "client": client})
		resp, err := http.Post(srv.URL+"/api/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("client %q without an Origin: status = %d, want 403", client, resp.StatusCode)
		}
	}
}

func TestBadBearerTokensAreRefused(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	admin := loggedInClient(t, s, srv) // has a valid cookie, which must not rescue a bad token

	headers := []struct{ name, value string }{
		{"an unknown token", "Bearer " + strings.Repeat("0", 64)},
		{"an empty token", "Bearer "},
		{"another scheme", "Basic dGVzdDp0ZXN0"},
		{"no scheme at all", "sometoken"},
		{"the wrong case", "bearer sometoken"},
	}
	for _, c := range headers {
		req, _ := http.NewRequest("GET", srv.URL+"/api/me", nil)
		req.Header.Set("Authorization", c.value)
		resp, err := admin.Do(req) // the client also sends the valid cookie
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401: a request that offers explicit credentials is judged on them", c.name, resp.StatusCode)
		}
	}
	// And no credentials at all.
	if code := withToken(t, "GET", srv.URL+"/api/me", "", nil, nil); code != http.StatusUnauthorized {
		t.Errorf("no credentials: status = %d, want 401", code)
	}
}

func TestLogoutEndsABearerSession(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	_, token, _ := cliLogin(t, srv, "sanca", "correct horse")

	if code := withToken(t, "POST", srv.URL+"/api/logout", token, nil, nil); code != http.StatusNoContent {
		t.Fatalf("logout: status = %d, want 204", code)
	}
	if code := withToken(t, "GET", srv.URL+"/api/me", token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("the token after logout: status = %d, want 401", code)
	}
}

// ---------------------------------------------------------- tunnel sessions

func TestOpenTunnelSession(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	request := func(target any) map[string]any {
		return map[string]any{"kind": "tunnel", "params": map[string]any{"target": target}}
	}

	// alice is an operator: the agent is told which port to reach.
	code, id := openSession(t, w.clients["alice"], w.srv, web, request("127.0.0.1:22"))
	if code != http.StatusCreated || !strings.HasPrefix(id, "s_") {
		t.Fatalf("alice: status = %d, id = %q", code, id)
	}
	open := readOpen(t, control)
	var params protocol.TunnelParams
	if err := json.Unmarshal(open.Params, &params); err != nil {
		t.Fatal(err)
	}
	if open.Kind != protocol.KindTunnel || open.SessionID != id || params.Target != "127.0.0.1:22" {
		t.Errorf("the agent got %+v with %+v, want a tunnel to 127.0.0.1:22", open, params)
	}

	// A tunnel is as powerful as a shell, so it needs the same role. A viewer
	// may not, and a host that cannot be seen does not exist.
	if code, _ := openSession(t, w.clients["bob"], w.srv, web, request("127.0.0.1:22")); code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", code)
	}
	for _, who := range []string{"carol", "dave"} {
		if code, _ := openSession(t, w.clients[who], w.srv, web, request("127.0.0.1:22")); code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", who, code)
		}
	}

	// Only the host itself may be a target (D-35), even for an admin. Nothing
	// reaches the agent for a refused request.
	for _, bad := range []any{"10.0.0.5:22", "192.168.1.1:80", "example.com:80", "127.0.0.1", "127.0.0.1:0", "169.254.169.254:80", "", nil} {
		if code, _ := openSession(t, w.clients["root"], w.srv, web, request(bad)); code != http.StatusBadRequest {
			t.Errorf("target %v: status = %d, want 400", bad, code)
		}
	}
	if code, _ := openSession(t, w.clients["root"], w.srv, web, map[string]any{"kind": "tunnel"}); code != http.StatusBadRequest {
		t.Errorf("no params at all: status = %d, want 400", code)
	}

	// Opening is audited with the target.
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "session_open" && r.Detail == "kind=tunnel target=127.0.0.1:22" {
			found = true
		}
	}
	if !found {
		t.Error("no session_open entry with kind=tunnel and the target")
	}
}

// The path hnctl takes: log in with a token, open a tunnel session, attach to
// it with the token alone (no cookie, no Origin), and carry bytes.
func TestTunnelSessionWithABearerToken(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", true); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(s.db, "mallory", "correct horse", true); err != nil {
		t.Fatal(err)
	}
	_, token, _ := cliLogin(t, srv, "sanca", "correct horse")
	_, otherToken, _ := cliLogin(t, srv, "mallory", "correct horse")
	control := connectAgent(t, s, srv, id, secret)

	var reply struct {
		ID string `json:"id"`
	}
	body := map[string]any{"kind": "tunnel", "params": map[string]any{"target": "127.0.0.1:22"}}
	if code := withToken(t, "POST", srv.URL+"/api/hosts/"+id+"/sessions", token, body, &reply); code != http.StatusCreated {
		t.Fatalf("open with a token: status = %d, want 201", code)
	}
	readOpen(t, control)
	agent := dialAgentOf(t, srv.URL, id, secret, reply.ID)

	dial := func(token string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		header := http.Header{}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		return websocket.Dial(ctx, wsURL(srv, "/ui/sessions/"+reply.ID), &websocket.DialOptions{HTTPHeader: header})
	}

	// Someone else's token, or none, cannot take the session.
	if _, resp, err := dial(otherToken); err == nil || statusOf(resp) != http.StatusNotFound {
		t.Errorf("another user's token: status = %d, err = %v, want 404", statusOf(resp), err)
	}
	if _, resp, err := dial(""); err == nil || statusOf(resp) != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, err = %v, want 401", statusOf(resp), err)
	}

	laptop, _, err := dial(token)
	if err != nil {
		t.Fatalf("the owner's token could not attach: %v", err)
	}
	defer laptop.CloseNow()

	writeFrame(t, laptop, websocket.MessageBinary, "SSH-2.0-test\r\n")
	if kind, data := readFrame(t, agent); kind != websocket.MessageBinary || data != "SSH-2.0-test\r\n" {
		t.Errorf("agent got %v %q, want the bytes unchanged", kind, data)
	}
	writeFrame(t, agent, websocket.MessageBinary, "SSH-2.0-OpenSSH\r\n")
	if kind, data := readFrame(t, laptop); kind != websocket.MessageBinary || data != "SSH-2.0-OpenSSH\r\n" {
		t.Errorf("laptop got %v %q, want the bytes unchanged", kind, data)
	}
}

// A cookie-authenticated WebSocket still needs its Origin: the bearer exception
// must not have loosened the browser's protection.
func TestBrowserWebSocketStillNeedsAnOrigin(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	_, sid := openSession(t, w.clients["alice"], w.srv, web, shellRequest(80, 24))
	readOpen(t, control)

	if _, resp, err := dialBrowserSession(w.srv, w.clients["alice"], sid, ""); err == nil || statusOf(resp) != http.StatusForbidden {
		t.Errorf("a cookie and no Origin: status = %d, err = %v, want 403", statusOf(resp), err)
	}
}
