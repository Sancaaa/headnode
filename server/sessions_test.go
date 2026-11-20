package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// ------------------------------------------------------------- helpers

// connectAgent connects a host the way the real agent does (control
// connection and hello) and waits until the server counts it as online.
func connectAgent(t *testing.T, s *Server, srv *httptest.Server, id, secret string) *websocket.Conn {
	t.Helper()
	conn, _, err := dialAgent(srv, id, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	sendMessage(t, conn, protocol.TypeHello, validHello())
	waitFor(t, "agent to be online", func() bool { online, _ := s.agents.status(id); return online })
	return conn
}

// readOpen reads the next control message the server sends to the agent, which
// must be an `open`.
func readOpen(t *testing.T, control *websocket.Conn) protocol.Open {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := control.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeOpen {
		t.Fatalf("control message = %s, want an open message (err %v)", data, err)
	}
	var open protocol.Open
	if err := json.Unmarshal(env.Data, &open); err != nil {
		t.Fatal(err)
	}
	return open
}

func shellRequest(cols, rows int) map[string]any {
	return map[string]any{"kind": "shell", "params": map[string]any{"cols": cols, "rows": rows}}
}

// openSession asks for a shell on a host and returns the status and session id.
func openSession(t *testing.T, client *http.Client, srv *httptest.Server, hostID string, body any) (int, string) {
	t.Helper()
	var reply struct {
		ID string `json:"id"`
	}
	code := sendJSON(t, client, srv, "POST", "/api/hosts/"+hostID+"/sessions", body, &reply)
	return code, reply.ID
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

// dialAgentSession is the agent's side of a session.
func dialAgentSession(srv *httptest.Server, agentID, secret, sessionID string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL(srv, "/agent/sessions/"+sessionID), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + agentID + "." + secret}},
	})
}

// dialBrowserSession is the browser's side: the login cookie from the client's
// jar, and an Origin header (pass "" to send none).
func dialBrowserSession(srv *httptest.Server, client *http.Client, sessionID, origin string) (*websocket.Conn, *http.Response, error) {
	u, err := url.Parse(srv.URL)
	if err != nil {
		return nil, nil, err
	}
	var cookies []string
	for _, c := range client.Jar.Cookies(u) {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	header := http.Header{"Cookie": {strings.Join(cookies, "; ")}}
	if origin != "" {
		header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, wsURL(srv, "/ui/sessions/"+sessionID), &websocket.DialOptions{HTTPHeader: header})
}

func readFrame(t *testing.T, conn *websocket.Conn) (websocket.MessageType, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return kind, string(data)
}

func writeFrame(t *testing.T, conn *websocket.Conn, kind websocket.MessageType, data string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, kind, []byte(data)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// readError reads until the connection ends and returns the error.
func readError(conn *websocket.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return err
		}
	}
}

// emptyJar is a cookie jar with no cookies: a browser that is not logged in.
func emptyJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func hasAudit(t *testing.T, s *Server, action string) bool {
	t.Helper()
	for _, r := range auditRows(t, s.db) {
		if r.Action == action {
			return true
		}
	}
	return false
}

// ------------------------------------------------------- opening a session

func TestOpenSessionChecksPermissions(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"]) // lab-01 and orphan stay offline

	// alice is an operator in Office: allowed, and the agent is told.
	code, id := openSession(t, w.clients["alice"], w.srv, web, shellRequest(120, 32))
	if code != http.StatusCreated || !strings.HasPrefix(id, "s_") {
		t.Fatalf("alice: status = %d, id = %q, want 201 and an s_ id", code, id)
	}
	open := readOpen(t, control)
	var params protocol.ShellParams
	if err := json.Unmarshal(open.Params, &params); err != nil {
		t.Fatal(err)
	}
	if open.SessionID != id || open.Kind != "shell" || params.Cols != 120 || params.Rows != 32 {
		t.Errorf("agent got %+v with %+v, want session %s, a shell, 120x32", open, params, id)
	}

	// Silly sizes are clamped before they reach the agent.
	if code, _ := openSession(t, w.clients["alice"], w.srv, web, shellRequest(99999, -1)); code != http.StatusCreated {
		t.Fatalf("clamped size: status = %d, want 201", code)
	}
	open = readOpen(t, control)
	if err := json.Unmarshal(open.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Cols != 500 || params.Rows != 24 {
		t.Errorf("clamped size = %dx%d, want 500x24", params.Cols, params.Rows)
	}

	// bob is a viewer: he can see the host, but not use a shell on it.
	if code, _ := openSession(t, w.clients["bob"], w.srv, web, shellRequest(80, 24)); code != http.StatusForbidden {
		t.Errorf("viewer: status = %d, want 403", code)
	}
	// carol and dave cannot see the host at all: it does not exist for them.
	for _, who := range []string{"carol", "dave"} {
		if code, _ := openSession(t, w.clients[who], w.srv, web, shellRequest(80, 24)); code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", who, code)
		}
	}
	if code, _ := openSession(t, w.clients["alice"], w.srv, w.hosts["lab-01"], shellRequest(80, 24)); code != http.StatusNotFound {
		t.Errorf("alice on a host in another group: status = %d, want 404", code)
	}
	if code, _ := openSession(t, w.clients["root"], w.srv, "a_nobody", shellRequest(80, 24)); code != http.StatusNotFound {
		t.Errorf("unknown host: status = %d, want 404", code)
	}

	// An admin may use any host, but an offline one cannot open a session.
	if code, _ := openSession(t, w.clients["root"], w.srv, w.hosts["lab-01"], shellRequest(80, 24)); code != http.StatusConflict {
		t.Errorf("offline host: status = %d, want 409", code)
	}
	// Shells and file managers exist so far. A tunnel (M5) does not.
	if code, _ := openSession(t, w.clients["root"], w.srv, web, map[string]any{"kind": "tunnel"}); code != http.StatusBadRequest {
		t.Errorf("kind tunnel: status = %d, want 400", code)
	}

	// Opening is audited: who, which host.
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "session_open" && r.AgentID.String == web && r.UserID.Int64 == w.user(t, "alice").ID {
			found = true
		}
	}
	if !found {
		t.Error("no session_open entry for alice on web-01 in the audit log")
	}
}

// A files session is allowed with the files_read permission, and the agent is
// told whether the user may also change files. A viewer gets a read-only one.
func TestOpenFilesSession(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])

	type filesReply struct {
		ID       string `json:"id"`
		Readonly bool   `json:"readonly"`
	}
	// The browser may send params, but they must not matter: the server decides.
	body := map[string]any{"kind": "files", "params": map[string]any{"readonly": false}}

	cases := []struct {
		who          string
		wantStatus   int
		wantReadonly bool
	}{
		{"alice", http.StatusCreated, false}, // operator: read and write
		{"root", http.StatusCreated, false},  // admin
		{"bob", http.StatusCreated, true},    // viewer: read only, whatever the browser asked for
		{"carol", http.StatusNotFound, false},
		{"dave", http.StatusNotFound, false},
	}
	for _, c := range cases {
		var reply filesReply
		code := sendJSON(t, w.clients[c.who], w.srv, "POST", "/api/hosts/"+web+"/sessions", body, &reply)
		if code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d", c.who, code, c.wantStatus)
			continue
		}
		if code != http.StatusCreated {
			continue
		}
		if reply.Readonly != c.wantReadonly {
			t.Errorf("%s: readonly in the reply = %v, want %v", c.who, reply.Readonly, c.wantReadonly)
		}

		// What the agent is told must say the same.
		open := readOpen(t, control)
		var params protocol.FilesParams
		if err := json.Unmarshal(open.Params, &params); err != nil {
			t.Fatal(err)
		}
		if open.Kind != protocol.KindFiles || open.SessionID != reply.ID || params.ReadOnly != c.wantReadonly {
			t.Errorf("%s: the agent got %+v with %+v, want a files session with readonly=%v", c.who, open, params, c.wantReadonly)
		}
	}

	// The viewer can attach his own files session: files_read is enough for that.
	var reply filesReply
	if code := sendJSON(t, w.clients["bob"], w.srv, "POST", "/api/hosts/"+web+"/sessions", map[string]any{"kind": "files"}, &reply); code != http.StatusCreated {
		t.Fatalf("bob opening a files session: status = %d", code)
	}
	readOpen(t, control)
	browser, _, err := dialBrowserSession(w.srv, w.clients["bob"], reply.ID, w.srv.URL)
	if err != nil {
		t.Fatalf("a viewer could not attach a files session: %v", err)
	}
	browser.CloseNow()

	// The audit entry says what kind of session and whether it can write.
	var details []string
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "session_open" {
			details = append(details, r.Detail)
		}
	}
	want := []string{"kind=files readonly=false", "kind=files readonly=false", "kind=files readonly=true", "kind=files readonly=true"}
	if strings.Join(details, "|") != strings.Join(want, "|") {
		t.Errorf("session_open details = %v, want %v", details, want)
	}
}

// ------------------------------------------------------------ the relay

func TestSessionRelay(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	code, sid := openSession(t, admin, srv, id, shellRequest(120, 32))
	if code != http.StatusCreated {
		t.Fatalf("open: status = %d, want 201", code)
	}
	readOpen(t, control)

	agent, _, err := dialAgentSession(srv, id, secret, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.CloseNow()
	browser, _, err := dialBrowserSession(srv, admin, sid, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()

	// Browser to agent: keystrokes are binary and stay binary.
	writeFrame(t, browser, websocket.MessageBinary, "ls\n")
	if kind, data := readFrame(t, agent); kind != websocket.MessageBinary || data != "ls\n" {
		t.Errorf("agent got %v %q, want binary \"ls\\n\"", kind, data)
	}
	// Agent to browser: terminal output.
	writeFrame(t, agent, websocket.MessageBinary, "file-a\n")
	if kind, data := readFrame(t, browser); kind != websocket.MessageBinary || data != "file-a\n" {
		t.Errorf("browser got %v %q, want binary \"file-a\\n\"", kind, data)
	}
	// A resize is a text frame and must arrive as one, unchanged: the relay
	// does not look inside, but it must not turn text into binary.
	resize := `{"type":"resize","cols":100,"rows":30}`
	writeFrame(t, browser, websocket.MessageText, resize)
	if kind, data := readFrame(t, agent); kind != websocket.MessageText || data != resize {
		t.Errorf("agent got %v %q, want the resize as a text frame", kind, data)
	}

	// The browser tab closes: the agent side is closed too, so the agent can
	// kill the shell (roadmap M3: "closing the tab kills the shell").
	browser.Close(websocket.StatusNormalClosure, "")
	if err := readError(agent); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("agent saw %v, want a normal close", err)
	}

	waitFor(t, "session_close in the audit log", func() bool { return hasAudit(t, s, "session_close") })
	if s.sessions.get(sid) != nil {
		t.Error("the finished session is still in the table")
	}
}

func TestSessionClosedByTheAgentTellsTheBrowserWhy(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	_, sid := openSession(t, admin, srv, id, shellRequest(80, 24))
	readOpen(t, control)
	agent, _, err := dialAgentSession(srv, id, secret, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.CloseNow()
	browser, _, err := dialBrowserSession(srv, admin, sid, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()

	// An agent that cannot start a shell still dials, then closes with the
	// error as the reason (protocol.md §3), so the browser can show it.
	agent.Close(websocket.StatusInternalError, "cannot start a shell: no such file")

	err = readError(browser)
	var closed websocket.CloseError
	if !errors.As(err, &closed) || closed.Code != websocket.StatusInternalError || closed.Reason != "cannot start a shell: no such file" {
		t.Errorf("browser saw %v, want close 1011 with the agent's reason", err)
	}
}

func TestSessionAccessRules(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	secret := w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)

	_, sid := openSession(t, w.clients["alice"], w.srv, web, shellRequest(80, 24))
	readOpen(t, control)

	// Only the user who asked for the session may attach a browser: not another
	// operator, not even an admin.
	for _, who := range []string{"bob", "root"} {
		if _, resp, err := dialBrowserSession(w.srv, w.clients[who], sid, w.srv.URL); err == nil || statusOf(resp) != http.StatusNotFound {
			t.Errorf("%s attaching alice's session: status = %d, err = %v, want 404", who, statusOf(resp), err)
		}
	}
	// A WebSocket from another site, or with no Origin at all, is refused even
	// with alice's valid cookie.
	for _, origin := range []string{"", "http://evil.example"} {
		if _, resp, err := dialBrowserSession(w.srv, w.clients["alice"], sid, origin); err == nil || statusOf(resp) != http.StatusForbidden {
			t.Errorf("Origin %q: status = %d, err = %v, want 403", origin, statusOf(resp), err)
		}
	}
	// Another agent cannot take the agent's side, and bad credentials are refused.
	if _, resp, err := dialAgentSession(w.srv, w.hosts["lab-01"], w.secrets["lab-01"], sid); err == nil || statusOf(resp) != http.StatusNotFound {
		t.Errorf("another agent: status = %d, err = %v, want 404", statusOf(resp), err)
	}
	if _, resp, err := dialAgentSession(w.srv, web, "wrong-secret", sid); err == nil || statusOf(resp) != http.StatusUnauthorized {
		t.Errorf("wrong secret: status = %d, err = %v, want 401", statusOf(resp), err)
	}
	// None of the refused attempts used up the session: alice and the right
	// agent can still attach.
	agent, _, err := dialAgentSession(w.srv, web, secret, sid)
	if err != nil {
		t.Fatalf("the agent could not attach after refused attempts: %v", err)
	}
	defer agent.CloseNow()
	browser, _, err := dialBrowserSession(w.srv, w.clients["alice"], sid, w.srv.URL)
	if err != nil {
		t.Fatalf("alice could not attach after refused attempts: %v", err)
	}
	defer browser.CloseNow()

	// An id is single-use for each side.
	if _, resp, err := dialBrowserSession(w.srv, w.clients["alice"], sid, w.srv.URL); err == nil || statusOf(resp) != http.StatusNotFound {
		t.Errorf("second browser: status = %d, err = %v, want 404", statusOf(resp), err)
	}
	if _, resp, err := dialAgentSession(w.srv, web, secret, sid); err == nil || statusOf(resp) != http.StatusNotFound {
		t.Errorf("second agent: status = %d, err = %v, want 404", statusOf(resp), err)
	}

	// And the pair still works.
	writeFrame(t, browser, websocket.MessageBinary, "x")
	if _, data := readFrame(t, agent); data != "x" {
		t.Errorf("agent got %q, want x", data)
	}

	// Without a login there is no session endpoint at all.
	if _, resp, err := dialBrowserSession(w.srv, &http.Client{Jar: emptyJar(t)}, sid, w.srv.URL); err == nil || statusOf(resp) != http.StatusUnauthorized {
		t.Errorf("no login: status = %d, err = %v, want 401", statusOf(resp), err)
	}
}

func TestSessionExpiresWhenTheOtherSideNeverComes(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)
	s.sessions.mu.Lock()
	s.sessions.pairTimeout = 500 * time.Millisecond // the real one is 15 s
	s.sessions.mu.Unlock()

	// The browser comes, the agent does not.
	_, sid := openSession(t, admin, srv, id, shellRequest(80, 24))
	readOpen(t, control)
	browser, _, err := dialBrowserSession(srv, admin, sid, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	if got := websocket.CloseStatus(readError(browser)); got != 4000 {
		t.Errorf("browser close status = %d, want 4000", got)
	}

	// The agent comes, the browser does not.
	_, sid = openSession(t, admin, srv, id, shellRequest(80, 24))
	readOpen(t, control)
	agent, _, err := dialAgentSession(srv, id, secret, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.CloseNow()
	if got := websocket.CloseStatus(readError(agent)); got != 4000 {
		t.Errorf("agent close status = %d, want 4000", got)
	}

	// An expired id is gone.
	if s.sessions.get(sid) != nil {
		t.Error("the expired session is still in the table")
	}
}
