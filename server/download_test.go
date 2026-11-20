package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// ------------------------------------------------------------- helpers

// getResult is what a download request in the background comes back with.
type getResult struct {
	resp *http.Response
	err  error
}

// startGet asks for a download in the background: the request blocks until the
// agent answers, and the test is the agent.
func startGet(client *http.Client, srvURL, hostID, path string) <-chan getResult {
	done := make(chan getResult, 1)
	go func() {
		resp, err := client.Get(srvURL + "/api/hosts/" + hostID + "/download?path=" + url.QueryEscape(path))
		done <- getResult{resp, err}
	}()
	return done
}

func waitResult(t *testing.T, ch <-chan getResult) *http.Response {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("download request failed: %v", res.err)
		}
		t.Cleanup(func() { res.resp.Body.Close() })
		return res.resp
	case <-time.After(5 * time.Second):
		t.Fatal("the download request never answered")
		return nil
	}
}

// answerAsAgent does what the agent does for a download: read `open`, dial the
// session, and return the connection for the test to write the file into.
func answerAsAgent(t *testing.T, control *websocket.Conn, srvURL, agentID, secret string, wantPath string) *websocket.Conn {
	t.Helper()
	open := readOpen(t, control)
	var params protocol.DownloadParams
	if err := json.Unmarshal(open.Params, &params); err != nil {
		t.Fatal(err)
	}
	if open.Kind != protocol.KindDownload || params.Path != wantPath {
		t.Fatalf("the agent was asked for %+v with %+v, want a download of %q", open, params, wantPath)
	}
	return dialAgentOf(t, srvURL, agentID, secret, open.SessionID)
}

func dialAgentOf(t *testing.T, srvURL, agentID, secret, sessionID string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srvURL, "http")+"/agent/sessions/"+sessionID, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + agentID + "." + secret}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func sendHeader(t *testing.T, agent *websocket.Conn, name string, size int64) {
	t.Helper()
	data, err := json.Marshal(protocol.DownloadHeader{Name: name, Size: size})
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, agent, websocket.MessageText, string(data))
}

// ------------------------------------------------------------ the tests

func TestDownloadStreamsTheFile(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	content := bytes.Repeat([]byte("abcdefgh"), 50000) // 400 KB, sent in three frames
	pending := startGet(admin, srv.URL, id, "/var/log/data.bin")
	agent := answerAsAgent(t, control, srv.URL, id, secret, "/var/log/data.bin")

	sendHeader(t, agent, "data.bin", int64(len(content)))
	for start := 0; start < len(content); start += 150000 {
		writeFrame(t, agent, websocket.MessageBinary, string(content[start:min(start+150000, len(content))]))
	}
	agent.Close(websocket.StatusNormalClosure, "")

	resp := waitResult(t, pending)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(content)) {
		t.Errorf("Content-Length = %q, want %d", got, len(content))
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") || !strings.Contains(got, "data.bin") {
		t.Errorf("Content-Disposition = %q, want an attachment named data.bin", got)
	}
	// Whatever the file really is, the browser must save it, never render it.
	if resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("Content-Type = %q, nosniff = %q, want a download the browser cannot render",
			resp.Header.Get("Content-Type"), resp.Header.Get("X-Content-Type-Options"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(body, content) {
		t.Errorf("body: %d bytes, err %v, want the %d bytes the agent sent", len(body), err, len(content))
	}

	// The start of the transfer is audited, with the path and size.
	var found bool
	for _, r := range auditRows(t, s.db) {
		if r.Action == "file_download" && r.AgentID.String == id && r.Detail == "path=/var/log/data.bin size=400000" {
			found = true
		}
	}
	if !found {
		t.Errorf("no file_download entry with the path and size, got %+v", auditRows(t, s.db))
	}

	// The session is cleaned up.
	waitFor(t, "the download session to be gone", func() bool {
		s.sessions.mu.Lock()
		defer s.sessions.mu.Unlock()
		return len(s.sessions.byID) == 0
	})
}

func TestDownloadErrorsFromTheAgentBecomeHTTPErrors(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	cases := []struct {
		reason     string
		wantStatus int
	}{
		{"no such file or directory", http.StatusNotFound},
		{"permission denied", http.StatusForbidden},
		{"is a directory", http.StatusBadRequest},
		{"path must be absolute", http.StatusBadRequest},
	}
	for _, c := range cases {
		pending := startGet(admin, srv.URL, id, "/some/path")
		agent := answerAsAgent(t, control, srv.URL, id, secret, "/some/path")
		agent.Close(websocket.StatusInternalError, c.reason) // no header: the file could not be read

		resp := waitResult(t, pending)
		var reply struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&reply)
		if resp.StatusCode != c.wantStatus || reply.Error != c.reason {
			t.Errorf("agent said %q: status = %d, error = %q, want %d and the same words", c.reason, resp.StatusCode, reply.Error, c.wantStatus)
		}
	}
	// Nothing was sent, so nothing is audited as a download.
	for _, r := range auditRows(t, s.db) {
		if r.Action == "file_download" {
			t.Errorf("a failed download was audited as a transfer: %+v", r)
		}
	}
}

// If the agent fails after the file started arriving, the status line is gone.
// The only honest signal is a failed download in the browser, not a short file
// that looks complete.
func TestDownloadCutOffInTheMiddleIsAFailure(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	pending := startGet(admin, srv.URL, id, "/big")
	agent := answerAsAgent(t, control, srv.URL, id, secret, "/big")
	sendHeader(t, agent, "big", 1000000)
	writeFrame(t, agent, websocket.MessageBinary, strings.Repeat("x", 1000))
	agent.Close(websocket.StatusInternalError, "read error: input/output error")

	// The failure can show up in two places, both of which are a failed download:
	// the connection is cut before the browser has seen any response (the first
	// chunk was still in the server's small output buffer), or the body breaks
	// off with an error. What must never happen is a clean, short body.
	select {
	case res := <-pending:
		if res.err != nil {
			return
		}
		defer res.resp.Body.Close()
		if body, err := io.ReadAll(res.resp.Body); err == nil {
			t.Errorf("read %d bytes without an error, want the download to fail", len(body))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the download request never finished")
	}
}

func TestDownloadPermissionsAndInput(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])

	get := func(who, host, query string) int {
		resp, err := w.clients[who].Get(w.srv.URL + "/api/hosts/" + host + "/download" + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Without a path there is nothing to ask for.
	if code := get("root", web, ""); code != http.StatusBadRequest {
		t.Errorf("no path: status = %d, want 400", code)
	}
	if code := get("root", web, "?path="+url.QueryEscape("/a\x00b")); code != http.StatusBadRequest {
		t.Errorf("path with a NUL byte: status = %d, want 400", code)
	}
	// Hidden hosts do not exist; unknown ones do not either.
	for _, who := range []string{"carol", "dave"} {
		if code := get(who, web, "?path=/etc/hostname"); code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", who, code)
		}
	}
	if code := get("root", "a_nobody", "?path=/etc/hostname"); code != http.StatusNotFound {
		t.Errorf("unknown host: status = %d, want 404", code)
	}
	// An offline host cannot be asked.
	if code := get("root", w.hosts["lab-01"], "?path=/etc/hostname"); code != http.StatusConflict {
		t.Errorf("offline host: status = %d, want 409", code)
	}

	// A viewer has files_read, so a viewer may download. An operator too.
	for _, who := range []string{"bob", "alice"} {
		pending := startGet(w.clients[who], w.srv.URL, web, "/etc/hostname")
		agent := answerAsAgent(t, control, w.srv.URL, web, w.secrets["web-01"], "/etc/hostname")
		sendHeader(t, agent, "hostname", 3)
		writeFrame(t, agent, websocket.MessageBinary, "web")
		agent.Close(websocket.StatusNormalClosure, "")
		resp := waitResult(t, pending)
		if body, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusOK || string(body) != "web" {
			t.Errorf("%s: status = %d, body = %q, want 200 and web", who, resp.StatusCode, body)
		}
	}
}

func TestDownloadTimesOutIfTheAgentNeverAnswers(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	connectAgent(t, s, srv, id, secret) // online, but it will ignore the request
	s.sessions.mu.Lock()
	s.sessions.pairTimeout = 300 * time.Millisecond // the real one is 15 s
	s.sessions.mu.Unlock()

	resp, err := admin.Get(srv.URL + "/api/hosts/" + id + "/download?path=/etc/hostname")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", resp.StatusCode)
	}
}

func TestDownloadRequiresLogin(t *testing.T) {
	_, srv, id, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/hosts/" + id + "/download?path=/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// File names come from the host's disk, so they are not trusted: a name must
// not be able to add a header or to point at a folder.
func TestContentDispositionCleansTheName(t *testing.T) {
	cases := []struct{ name, mustContain string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "etcpasswd"}, // folders are gone, only letters are left
		{"evil\r\nSet-Cookie: x=1", "evilSet-Cookie: x=1"},
		{`quote".txt`, "quote.txt"},
		{"zażółć.txt", "filename*="}, // non-ASCII is encoded the standard way
		{"", "download"},
		{"\n\r", "download"},
	}
	for _, c := range cases {
		got := contentDisposition(c.name)
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("contentDisposition(%q) = %q, contains a line break", c.name, got)
		}
		if !strings.HasPrefix(got, "attachment") || !strings.Contains(got, c.mustContain) {
			t.Errorf("contentDisposition(%q) = %q, want an attachment containing %q", c.name, got, c.mustContain)
		}
	}
}

func TestAuditTextDropsControlCharacters(t *testing.T) {
	if got := auditText("/tmp/a\nfile_download path=/etc/shadow\r\x00"); got != "/tmp/afile_download path=/etc/shadow" {
		t.Errorf("auditText = %q", got)
	}
}
