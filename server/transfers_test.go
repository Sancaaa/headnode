package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// ------------------------------------------------------------- helpers

type uploadReply struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// upload sends a multipart upload like the browser does, with an Origin header.
func upload(t *testing.T, client *http.Client, srv *httptest.Server, field, filename, content string) (int, uploadReply) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, content)
	form.Close()

	req, err := http.NewRequest("POST", srv.URL+"/api/files", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", srv.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var reply uploadReply
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, reply
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// readFetch reads the next control message the server sends to the agent, which
// must be a `fetch`.
func readFetch(t *testing.T, control *websocket.Conn) protocol.Fetch {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := control.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != protocol.TypeFetch {
		t.Fatalf("control message = %s, want a fetch message (err %v)", data, err)
	}
	var fetch protocol.Fetch
	if err := json.Unmarshal(env.Data, &fetch); err != nil {
		t.Fatal(err)
	}
	return fetch
}

// agentFetches does what the agent does with the file: ask the server for it
// with its own credentials. It returns the status and the body.
func agentFetches(t *testing.T, srv *httptest.Server, agentID, secret, fileID string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+"/agent/files/"+fileID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if agentID != "" {
		req.Header.Set("Authorization", "Bearer "+agentID+"."+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func reportResult(t *testing.T, control *websocket.Conn, transferID string, ok bool, message string) {
	t.Helper()
	sendMessage(t, control, protocol.TypeFetchResult, protocol.FetchResult{TransferID: transferID, OK: ok, Error: message})
}

func getTransfer(t *testing.T, client *http.Client, srv *httptest.Server, id string) (int, transferView) {
	t.Helper()
	var view transferView
	code := getJSON(t, client, srv, "/api/transfers/"+id, &view)
	return code, view
}

// ------------------------------------------------------------- uploading

func TestUploadStoresTheFileByItsHash(t *testing.T) {
	w := newPermWorld(t)
	content := "#!/bin/sh\necho hello\n"

	code, reply := upload(t, w.clients["alice"], w.srv, "file", "tool.sh", content)
	if code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", code)
	}
	if !strings.HasPrefix(reply.ID, "f_") || reply.Name != "tool.sh" || reply.Size != int64(len(content)) || reply.SHA256 != sha256Hex(content) {
		t.Errorf("reply = %+v", reply)
	}

	// The bytes are on disk under their hash, readable only by the owner, and
	// the temporary file is gone.
	stored := filepath.Join(w.s.filesDir, reply.SHA256)
	if got, err := os.ReadFile(stored); err != nil || string(got) != content {
		t.Fatalf("stored file: %q, %v", got, err)
	}
	if info, err := os.Stat(stored); err == nil && info.Mode().Perm()&0o077 != 0 && filepath.Separator == '/' {
		t.Errorf("stored file mode = %v, want it private to the server's user", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(w.s.filesDir)
	if len(entries) != 1 {
		t.Errorf("files directory holds %d entries, want exactly the stored file", len(entries))
	}
	if n := count(t, w.s.db, `SELECT COUNT(*) FROM files WHERE id = ? AND sha256 = ?`, reply.ID, reply.SHA256); n != 1 {
		t.Error("no row for the upload in the files table")
	}

	// The same bytes again: a new file id, but the bytes are stored once.
	_, again := upload(t, w.clients["root"], w.srv, "file", "copy.sh", content)
	if again.ID == reply.ID || again.SHA256 != reply.SHA256 {
		t.Errorf("second upload = %+v, want a new id and the same hash", again)
	}
	entries, _ = os.ReadDir(w.s.filesDir)
	if len(entries) != 1 {
		t.Errorf("files directory holds %d entries after uploading the same bytes twice, want 1", len(entries))
	}

	// The upload is audited with the name, size and the start of the hash.
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "file_upload" && strings.Contains(r.Detail, "file=tool.sh") && strings.Contains(r.Detail, reply.SHA256[:12]) {
			found = true
		}
	}
	if !found {
		t.Error("no file_upload entry for tool.sh in the audit log")
	}
}

func TestUploadPermissionsAndBadInput(t *testing.T) {
	w := newPermWorld(t)

	// An operator and an admin may upload. A viewer, and someone in no group, may not.
	for who, want := range map[string]int{"alice": http.StatusCreated, "root": http.StatusCreated, "bob": http.StatusForbidden, "dave": http.StatusForbidden} {
		if code, _ := upload(t, w.clients[who], w.srv, "file", "x", "data"); code != want {
			t.Errorf("%s uploading: status = %d, want %d", who, code, want)
		}
	}

	// Without a login there is no upload.
	if code, _ := upload(t, &http.Client{Jar: emptyJar(t)}, w.srv, "file", "x", "data"); code != http.StatusUnauthorized {
		t.Errorf("no login: status = %d, want 401", code)
	}
	// A part that is not called "file" is not the upload.
	if code, _ := upload(t, w.clients["alice"], w.srv, "other", "x", "data"); code != http.StatusBadRequest {
		t.Errorf("wrong field name: status = %d, want 400", code)
	}
	// A body that is not multipart at all.
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/files", map[string]any{"a": 1}, nil); code != http.StatusBadRequest {
		t.Errorf("JSON instead of multipart: status = %d, want 400", code)
	}
	// An upload from another site is refused even with a valid cookie.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("file", "x")
	io.WriteString(part, "data")
	form.Close()
	req, _ := http.NewRequest("POST", w.srv.URL+"/api/files", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Origin", "http://evil.example")
	resp, err := w.clients["alice"].Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: status = %d, want 403", resp.StatusCode)
	}
}

func TestCleanFileName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\me\tool.exe`, "tool.exe"},
		{"line\nbreak.txt", "linebreak.txt"},
		{"  spaced.txt  ", "spaced.txt"},
		{"", "upload"},
		{"..", "upload"},
		{"/", "upload"},
		{strings.Repeat("a", 300), strings.Repeat("a", 255)},
	}
	for _, c := range cases {
		if got := cleanFileName(c.in); got != c.want {
			t.Errorf("cleanFileName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ------------------------------------------------------ sending a file

func TestTransferEndToEnd(t *testing.T) {
	w := newPermWorld(t)
	office := w.groups["Office"]
	web1, web1Secret := w.hosts["web-01"], w.secrets["web-01"]
	web2, web2Secret := addHostWithSecret(t, w.s.db, "web-02", office)
	lab, labSecret := w.hosts["lab-01"], w.secrets["lab-01"]

	control1 := connectAgent(t, w.s, w.srv, web1, web1Secret)
	control2 := connectAgent(t, w.s, w.srv, web2, web2Secret)
	connectAgent(t, w.s, w.srv, lab, labSecret) // online, but not part of the transfer

	content := "pretend this is a 15 MB program"
	code, file := upload(t, w.clients["alice"], w.srv, "file", "k3s-manager", content)
	if code != http.StatusCreated {
		t.Fatalf("upload: status = %d", code)
	}

	// alice sends it to both Office hosts, with the mode of a program.
	var started transferView
	body := map[string]any{"file_id": file.ID, "agent_ids": []string{web1, web2}, "dest_path": "/usr/local/bin/k3s-manager", "mode": "0755"}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started); code != http.StatusCreated {
		t.Fatalf("start transfer: status = %d, want 201", code)
	}
	if !strings.HasPrefix(started.ID, "t_") || len(started.Hosts) != 2 || started.FileName != "k3s-manager" || started.Mode != "0755" {
		t.Fatalf("started = %+v", started)
	}
	for _, h := range started.Hosts {
		if h.Status != "pending" || h.HostName == nil {
			t.Errorf("host %s right after starting: %+v, want pending with a name", h.AgentID, h)
		}
	}

	// Each host is told what to fetch, and what to check it against.
	for _, control := range []*websocket.Conn{control1, control2} {
		fetch := readFetch(t, control)
		if fetch.TransferID != started.ID || fetch.FileID != file.ID || fetch.DestPath != "/usr/local/bin/k3s-manager" ||
			fetch.Mode != "0755" || fetch.SHA256 != sha256Hex(content) || fetch.Size != int64(len(content)) {
			t.Errorf("fetch = %+v, want the transfer's details and the file's checksum and size", fetch)
		}
	}

	// The two hosts may download it. Nobody else may: not an unauthenticated
	// caller, not another enrolled host that was not told to fetch it.
	for _, h := range []struct{ id, secret string }{{web1, web1Secret}, {web2, web2Secret}} {
		if code, got := agentFetches(t, w.srv, h.id, h.secret, file.ID); code != http.StatusOK || got != content {
			t.Errorf("agent %s fetching: status = %d, body %q, want 200 and the file", h.id, code, got)
		}
	}
	if code, _ := agentFetches(t, w.srv, lab, labSecret, file.ID); code != http.StatusNotFound {
		t.Errorf("an agent outside the transfer: status = %d, want 404", code)
	}
	if code, _ := agentFetches(t, w.srv, "", "", file.ID); code != http.StatusUnauthorized {
		t.Errorf("no credentials: status = %d, want 401", code)
	}
	if code, _ := agentFetches(t, w.srv, web1, web1Secret, "f_nothing"); code != http.StatusNotFound {
		t.Errorf("unknown file: status = %d, want 404", code)
	}

	// One host succeeds, one fails: the status is per host.
	reportResult(t, control1, started.ID, true, "")
	reportResult(t, control2, started.ID, false, "permission denied")
	waitFor(t, "both results", func() bool {
		_, view := getTransfer(t, w.clients["alice"], w.srv, started.ID)
		for _, h := range view.Hosts {
			if h.Status == "pending" {
				return false
			}
		}
		return true
	})
	_, view := getTransfer(t, w.clients["alice"], w.srv, started.ID)
	got := map[string]transferHostView{}
	for _, h := range view.Hosts {
		got[h.AgentID] = h
	}
	if got[web1].Status != "ok" || got[web2].Status != "error" || got[web2].Error != "permission denied" {
		t.Errorf("final status = %+v, want web-01 ok and web-02 error: permission denied", view.Hosts)
	}

	// A finished transfer no longer lets its host download the file.
	if code, _ := agentFetches(t, w.srv, web1, web1Secret, file.ID); code != http.StatusNotFound {
		t.Errorf("after the result: status = %d, want 404", code)
	}

	// Only the person who started it (or an admin) can look at it.
	for who, want := range map[string]int{"alice": 200, "root": 200, "bob": 404, "carol": 404} {
		if code, _ := getTransfer(t, w.clients[who], w.srv, started.ID); code != want {
			t.Errorf("%s reading the transfer: status = %d, want %d", who, code, want)
		}
	}

	// The audit log has the start, and one result per host.
	var actions []string
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "transfer_start" || r.Action == "transfer_result" {
			actions = append(actions, r.Action+": "+r.Detail)
		}
	}
	if len(actions) != 3 || !strings.Contains(actions[0], "file=k3s-manager") || !strings.Contains(actions[0], "hosts=2") {
		t.Fatalf("transfer audit entries = %v", actions)
	}
	joined := strings.Join(actions[1:], "\n")
	if !strings.Contains(joined, "dest=/usr/local/bin/k3s-manager ok") || !strings.Contains(joined, "failed: permission denied") {
		t.Errorf("result entries = %q, want one ok and one failed", joined)
	}
}

func TestTransferRefusesWhatTheUserMayNotDo(t *testing.T) {
	w := newPermWorld(t)
	office := w.groups["Office"]
	web := w.hosts["web-01"]
	web2, _ := addHostWithSecret(t, w.s.db, "web-02", office)
	connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])

	_, file := upload(t, w.clients["alice"], w.srv, "file", "f", "data")
	good := func() map[string]any {
		return map[string]any{"file_id": file.ID, "agent_ids": []string{web}, "dest_path": "/tmp/f", "mode": "0644"}
	}
	start := func(who string, body map[string]any) int {
		return sendJSON(t, w.clients[who], w.srv, "POST", "/api/transfers", body, nil)
	}
	change := func(key string, value any) map[string]any {
		body := good()
		body[key] = value
		return body
	}

	// Input checks. 201 different ids, because the same id twice counts once.
	var tooMany []string
	for i := 0; i <= maxTransferHosts; i++ {
		tooMany = append(tooMany, fmt.Sprintf("a_%d", i))
	}
	badInput := []struct {
		name string
		body map[string]any
	}{
		{"no hosts", change("agent_ids", []string{})},
		{"no destination", change("dest_path", "")},
		{"NUL in the path", change("dest_path", "/tmp/a\x00b")},
		{"setuid mode", change("mode", "4755")},
		{"not octal", change("mode", "999")},
		{"letters as mode", change("mode", "rwx")},
		{"too many hosts", change("agent_ids", tooMany)},
	}
	for _, c := range badInput {
		if code := start("alice", c.body); code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", c.name, code)
		}
	}
	// No mode given means 0644.
	body := good()
	delete(body, "mode")
	var started transferView
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started); code != http.StatusCreated || started.Mode != "0644" {
		t.Errorf("default mode: status = %d, mode = %q, want 201 and 0644", code, started.Mode)
	}

	// Files: an unknown one, and one that belongs to somebody else.
	if code := start("alice", change("file_id", "f_nothing")); code != http.StatusNotFound {
		t.Errorf("unknown file: status = %d, want 404", code)
	}
	_, rootsFile := upload(t, w.clients["root"], w.srv, "file", "rootsecret", "classified")
	if code := start("alice", change("file_id", rootsFile.ID)); code != http.StatusNotFound {
		t.Errorf("someone else's file: status = %d, want 404", code)
	}
	// An admin may send any upload.
	if code := start("root", change("file_id", file.ID)); code != http.StatusCreated {
		t.Errorf("admin sending alice's file: status = %d, want 201", code)
	}

	// Hosts: a viewer cannot write, a hidden host does not exist, and one bad
	// host in the list refuses the whole request (nothing is sent to the others).
	if code := start("bob", good()); code != http.StatusForbidden {
		t.Errorf("a viewer sending: status = %d, want 403", code)
	}
	if code := start("alice", change("agent_ids", []string{web, w.hosts["lab-01"]})); code != http.StatusNotFound {
		t.Errorf("a host alice cannot see in the list: status = %d, want 404", code)
	}
	before := len(auditRows(t, w.s.db))
	if code := start("alice", change("agent_ids", []string{web, web2, "a_nobody"})); code != http.StatusNotFound {
		t.Errorf("an unknown host in the list: status = %d, want 404", code)
	}
	for _, r := range auditRows(t, w.s.db)[before:] {
		if r.Action == "transfer_start" {
			t.Error("a refused request still started a transfer")
		}
	}

	// A host that is offline fails at once, and does not stop the others.
	var mixed transferView
	body = change("agent_ids", []string{web, web2}) // web2 was never connected
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &mixed); code != http.StatusCreated {
		t.Fatalf("mixed online and offline: status = %d, want 201", code)
	}
	status := map[string]string{}
	for _, h := range mixed.Hosts {
		status[h.AgentID] = h.Status + ":" + h.Error
	}
	if status[web] != "pending:" || status[web2] != "error:host offline" {
		t.Errorf("host states = %v, want web pending and web2 failed with 'host offline'", status)
	}

	// The same host listed twice is one transfer to that host.
	var once transferView
	sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", change("agent_ids", []string{web, web}), &once)
	if len(once.Hosts) != 1 {
		t.Errorf("a host listed twice: %d entries, want 1", len(once.Hosts))
	}
}

func TestListTransfers(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"] // offline: a transfer to it is created, and its host fails at once

	_, aliceFile := upload(t, w.clients["alice"], w.srv, "file", "alice.bin", "alice's bytes")
	_, rootFile := upload(t, w.clients["root"], w.srv, "file", "root.bin", "root's bytes!!")
	start := func(who, fileID, dest string) transferView {
		var view transferView
		body := map[string]any{"file_id": fileID, "agent_ids": []string{web}, "dest_path": dest}
		if code := sendJSON(t, w.clients[who], w.srv, "POST", "/api/transfers", body, &view); code != http.StatusCreated {
			t.Fatalf("%s starting a transfer: status = %d", who, code)
		}
		return view
	}
	first := start("alice", aliceFile.ID, "/tmp/one")
	time.Sleep(10 * time.Millisecond) // so "newest first" is not a tie
	second := start("alice", aliceFile.ID, "/tmp/two")
	time.Sleep(10 * time.Millisecond)
	third := start("root", rootFile.ID, "/tmp/three")

	ids := func(who string) []string {
		var list []transferView
		if code := getJSON(t, w.clients[who], w.srv, "/api/transfers", &list); code != http.StatusOK {
			t.Fatalf("%s listing: status = %d", who, code)
		}
		var out []string
		for _, v := range list {
			out = append(out, v.ID)
		}
		return out
	}
	join := func(ids []string) string { return strings.Join(ids, ",") }

	// A person sees their own, newest first. An admin sees everyone's.
	if got, want := join(ids("alice")), join([]string{second.ID, first.ID}); got != want {
		t.Errorf("alice sees %s, want %s", got, want)
	}
	if got, want := join(ids("root")), join([]string{third.ID, second.ID, first.ID}); got != want {
		t.Errorf("root sees %s, want %s", got, want)
	}
	// Someone who started nothing sees an empty list, not null.
	var raw []byte
	resp, err := w.clients["bob"].Get(w.srv.URL + "/api/transfers")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("bob's list = %s, want []", raw)
	}

	// The list carries what the page shows: who, how big, and each host's state.
	var list []transferView
	getJSON(t, w.clients["root"], w.srv, "/api/transfers", &list)
	if list[0].StartedBy != "root" || list[0].FileName != "root.bin" || list[0].FileSize != int64(len("root's bytes!!")) {
		t.Errorf("newest entry = %+v", list[0])
	}
	if len(list[0].Hosts) != 1 || list[0].Hosts[0].Status != "error" || list[0].Hosts[0].Error != "host offline" {
		t.Errorf("host state = %+v, want the offline host failed", list[0].Hosts)
	}
}

func TestMeSaysWhoMayUpload(t *testing.T) {
	w := newPermWorld(t)
	for who, want := range map[string]bool{"root": true, "alice": true, "bob": false, "carol": false, "dave": false} {
		var me struct {
			Username  string `json:"username"`
			CanUpload bool   `json:"can_upload"`
		}
		if code := getJSON(t, w.clients[who], w.srv, "/api/me", &me); code != http.StatusOK {
			t.Fatalf("%s: /api/me status = %d", who, code)
		}
		if me.Username != who || me.CanUpload != want {
			t.Errorf("%s: /api/me = %+v, want can_upload = %v", who, me, want)
		}
	}
}

func TestTransferFailsWhenTheHostDisconnects(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	_, file := upload(t, w.clients["alice"], w.srv, "file", "f", "data")

	var started transferView
	body := map[string]any{"file_id": file.ID, "agent_ids": []string{web}, "dest_path": "/tmp/f"}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started); code != http.StatusCreated {
		t.Fatalf("status = %d", code)
	}
	readFetch(t, control)

	// The host drops off before it can say how it went.
	control.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "the transfer to fail", func() bool {
		_, view := getTransfer(t, w.clients["alice"], w.srv, started.ID)
		return len(view.Hosts) == 1 && view.Hosts[0].Status == "error"
	})
	_, view := getTransfer(t, w.clients["alice"], w.srv, started.ID)
	if !strings.Contains(view.Hosts[0].Error, "disconnected") {
		t.Errorf("error = %q, want it to say the host disconnected", view.Hosts[0].Error)
	}
}

// A host that reports on a transfer that is not its own changes nothing.
func TestAHostCannotReportOnSomeoneElsesTransfer(t *testing.T) {
	w := newPermWorld(t)
	office := w.groups["Office"]
	web1, web1Secret := w.hosts["web-01"], w.secrets["web-01"]
	web2, web2Secret := addHostWithSecret(t, w.s.db, "web-02", office)
	connectAgent(t, w.s, w.srv, web1, web1Secret)
	control2 := connectAgent(t, w.s, w.srv, web2, web2Secret)
	_, file := upload(t, w.clients["alice"], w.srv, "file", "f", "data")

	var started transferView
	body := map[string]any{"file_id": file.ID, "agent_ids": []string{web1}, "dest_path": "/tmp/f"}
	sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started)

	// web-02 claims web-01's transfer succeeded.
	reportResult(t, control2, started.ID, true, "")
	time.Sleep(200 * time.Millisecond)
	_, view := getTransfer(t, w.clients["alice"], w.srv, started.ID)
	if view.Hosts[0].Status != "pending" {
		t.Errorf("status = %q after a stranger's report, want it still pending", view.Hosts[0].Status)
	}
}
