package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// startPull asks the server to copy a file from a host into its uploads, in the
// background: the request blocks until the agent has sent the file, and the
// test is the agent.
func startPull(client *http.Client, srvURL, hostID, path, origin string) <-chan getResult {
	done := make(chan getResult, 1)
	go func() {
		body, _ := json.Marshal(map[string]string{"path": path})
		req, err := http.NewRequest("POST", srvURL+"/api/hosts/"+hostID+"/pull", bytes.NewReader(body))
		if err != nil {
			done <- getResult{nil, err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := client.Do(req)
		done <- getResult{resp, err}
	}()
	return done
}

func leftoverUploads(t *testing.T, s *Server) []string {
	t.Helper()
	entries, err := os.ReadDir(s.filesDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".upload-") {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestPullStoresTheFileAsAnUpload(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)
	alice := w.user(t, "alice")

	content := bytes.Repeat([]byte("config line\n"), 40000) // about 480 KB, in several frames
	pending := startPull(w.clients["alice"], w.srv.URL, web, "/etc/app/app.conf", w.srv.URL)
	agent := answerAsAgent(t, control, w.srv.URL, web, secret, "/etc/app/app.conf")
	sendHeader(t, agent, "app.conf", int64(len(content)))
	for start := 0; start < len(content); start += 100000 {
		writeFrame(t, agent, websocket.MessageBinary, string(content[start:min(start+100000, len(content))]))
	}
	agent.Close(websocket.StatusNormalClosure, "")

	resp := waitResult(t, pending)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var stored uploadReply
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Name != "app.conf" || stored.Size != int64(len(content)) || stored.SHA256 != sha256Hex(string(content)) {
		t.Fatalf("reply = %+v, want the file's name, size and hash", stored)
	}

	// It is an ordinary upload now: the bytes are on disk under their hash, the
	// admin can list it, and it belongs to alice.
	if got, err := os.ReadFile(filepath.Join(w.s.filesDir, stored.SHA256)); err != nil || !bytes.Equal(got, content) {
		t.Errorf("the stored blob differs from what the host sent (err %v)", err)
	}
	files := listFiles(t, w)
	if len(files) != 1 || files[0].ID != stored.ID || files[0].UploadedBy == nil || *files[0].UploadedBy != "alice" {
		t.Errorf("uploads = %+v, want the pulled file, uploaded by alice", files)
	}
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "file_upload" && r.UserID.Int64 == alice.ID && r.AgentID.String == web &&
			strings.Contains(r.Detail, "file=app.conf") && strings.Contains(r.Detail, "from=/etc/app/app.conf") {
			found = true
		}
	}
	if !found {
		t.Errorf("no file_upload audit entry that says where the file came from: %+v", auditRows(t, w.s.db))
	}
	if left := leftoverUploads(t, w.s); len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}

	// And it can be sent on, like any upload.
	body := map[string]any{"file_id": stored.ID, "agent_ids": []string{web}, "dest_path": "/tmp/app.conf", "mode": "0644"}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, nil); code != http.StatusCreated {
		t.Errorf("sending the pulled file to a host: status = %d, want 201", code)
	}
}

func TestPullNeedsReadAccessAndUploadRights(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	connectAgent(t, w.s, w.srv, web, secret)

	cases := []struct {
		who  string
		want int
		why  string
	}{
		{"bob", http.StatusForbidden, "a viewer can read the file but may not upload"},
		{"carol", http.StatusNotFound, "a user who cannot see the host gets 'not found'"},
		{"dave", http.StatusNotFound, "a user with no group"},
	}
	for _, c := range cases {
		res := <-startPull(w.clients[c.who], w.srv.URL, web, "/etc/hostname", w.srv.URL)
		if res.err != nil {
			t.Fatal(res.err)
		}
		res.resp.Body.Close()
		if res.resp.StatusCode != c.want {
			t.Errorf("%s (%s): status = %d, want %d", c.who, c.why, res.resp.StatusCode, c.want)
		}
	}
	if n := len(listFiles(t, w)); n != 0 {
		t.Errorf("%d uploads exist, want none: every attempt was refused", n)
	}
}

func TestPullIsOriginCheckedAndNeedsAPath(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	connectAgent(t, w.s, w.srv, web, secret)

	for _, origin := range []string{"", "http://evil.example"} {
		res := <-startPull(w.clients["alice"], w.srv.URL, web, "/etc/hostname", origin)
		if res.err != nil {
			t.Fatal(res.err)
		}
		res.resp.Body.Close()
		if res.resp.StatusCode != http.StatusForbidden {
			t.Errorf("Origin %q: status = %d, want 403", origin, res.resp.StatusCode)
		}
	}
	res := <-startPull(w.clients["alice"], w.srv.URL, web, "", w.srv.URL)
	if res.err != nil {
		t.Fatal(res.err)
	}
	res.resp.Body.Close()
	if res.resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty path: status = %d, want 400", res.resp.StatusCode)
	}
}

// A folder reaches the server as a zip of unknown size (-1 in the header). It is
// stored like a file, named after the folder, and the reply says it is an archive
// so the page sends it with `extract`.
func TestPullOfAFolderStoresItsZip(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)

	archive := "PK-pretend-archive-" + strings.Repeat("z", 2000)
	pending := startPull(w.clients["alice"], w.srv.URL, web, "/etc/app", w.srv.URL)
	agent := answerAsAgent(t, control, w.srv.URL, web, secret, "/etc/app")
	sendHeader(t, agent, "app.zip", -1)
	writeFrame(t, agent, websocket.MessageBinary, archive)
	agent.Close(websocket.StatusNormalClosure, "")

	resp := waitResult(t, pending)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var stored struct {
		uploadReply
		Archive bool `json:"archive"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Name != "app.zip" || stored.Size != int64(len(archive)) || !stored.Archive {
		t.Errorf("reply = %+v, want app.zip of %d bytes, marked as an archive", stored, len(archive))
	}
	if got, err := os.ReadFile(filepath.Join(w.s.filesDir, stored.SHA256)); err != nil || string(got) != archive {
		t.Errorf("the stored archive differs (err %v)", err)
	}
	var noted bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "file_upload" && strings.Contains(r.Detail, "from=/etc/app (a folder, zipped)") {
			noted = true
		}
	}
	if !noted {
		t.Error("the audit entry does not say a folder was zipped")
	}

	// An ordinary file is not marked as an archive.
	pending = startPull(w.clients["alice"], w.srv.URL, web, "/etc/hostname", w.srv.URL)
	agent = answerAsAgent(t, control, w.srv.URL, web, secret, "/etc/hostname")
	sendHeader(t, agent, "hostname", 4)
	writeFrame(t, agent, websocket.MessageBinary, "web1")
	agent.Close(websocket.StatusNormalClosure, "")
	resp = waitResult(t, pending)
	var plain struct {
		Archive bool `json:"archive"`
	}
	json.NewDecoder(resp.Body).Decode(&plain)
	if resp.StatusCode != http.StatusCreated || plain.Archive {
		t.Errorf("a plain file: status = %d, archive = %v, want 201 and false", resp.StatusCode, plain.Archive)
	}
}

func TestPullThatBreaksOffOrChangesStoresNothing(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)

	// The host fails in the middle.
	pending := startPull(w.clients["alice"], w.srv.URL, web, "/var/log/big", w.srv.URL)
	agent := answerAsAgent(t, control, w.srv.URL, web, secret, "/var/log/big")
	sendHeader(t, agent, "big", 1000)
	writeFrame(t, agent, websocket.MessageBinary, strings.Repeat("x", 500))
	agent.Close(websocket.StatusInternalError, "read error: input/output error")
	if resp := waitResult(t, pending); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("host failed half way: status = %d, want 502", resp.StatusCode)
	}

	// The file ends at another size than the header said (a log that was written to).
	pending = startPull(w.clients["alice"], w.srv.URL, web, "/var/log/live", w.srv.URL)
	agent = answerAsAgent(t, control, w.srv.URL, web, secret, "/var/log/live")
	sendHeader(t, agent, "live", 1000)
	writeFrame(t, agent, websocket.MessageBinary, strings.Repeat("x", 900))
	agent.Close(websocket.StatusNormalClosure, "")
	if resp := waitResult(t, pending); resp.StatusCode != http.StatusConflict {
		t.Errorf("file changed while copying: status = %d, want 409", resp.StatusCode)
	}

	// The host cannot read it.
	pending = startPull(w.clients["alice"], w.srv.URL, web, "/etc/shadow", w.srv.URL)
	agent = answerAsAgent(t, control, w.srv.URL, web, secret, "/etc/shadow")
	agent.Close(websocket.StatusInternalError, "permission denied")
	if resp := waitResult(t, pending); resp.StatusCode != http.StatusForbidden {
		t.Errorf("unreadable file: status = %d, want 403", resp.StatusCode)
	}

	if n := len(listFiles(t, w)); n != 0 {
		t.Errorf("%d uploads exist, want none", n)
	}
	waitFor(t, "temporary upload files to be removed", func() bool { return len(leftoverUploads(t, w.s)) == 0 })
}

// Sending an archive with `extract` tells the host to unpack it into a folder.
func TestTransferWithExtractUnpacksOnTheHost(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)

	archive := uploadAs(t, w, "alice", "app.zip", "PK-pretend-archive")
	var started transferView
	body := map[string]any{"file_id": archive.ID, "agent_ids": []string{web}, "dest_path": "/opt", "extract": true}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started); code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", code)
	}
	if !started.Extract || started.DestPath != "/opt" || started.Mode != "" {
		t.Errorf("transfer = %+v, want an unpacking transfer into /opt with no mode", started)
	}
	fetch := readFetch(t, control)
	if !fetch.Extract || fetch.DestPath != "/opt" || fetch.FileID != archive.ID {
		t.Errorf("the host was told %+v, want a fetch with extract into /opt", fetch)
	}
	var noted bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "transfer_start" && strings.HasSuffix(r.Detail, " unpack=yes") {
			noted = true
		}
	}
	if !noted {
		t.Error("the transfer_start audit entry does not say the archive is unpacked")
	}

	// A mode that would be wrong for a plain file is not looked at: an unpacked
	// folder keeps the modes inside it.
	body["mode"] = "4755"
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, nil); code != http.StatusCreated {
		t.Errorf("extract with a mode: status = %d, want 201 (the mode is ignored)", code)
	}
	readFetch(t, control)

	// Only an archive can be unpacked, and the same permissions apply as for any send.
	plain := uploadAs(t, w, "alice", "notes.txt", "hello")
	body = map[string]any{"file_id": plain.ID, "agent_ids": []string{web}, "dest_path": "/opt", "extract": true}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, nil); code != http.StatusBadRequest {
		t.Errorf("extract of a .txt: status = %d, want 400", code)
	}
	body = map[string]any{"file_id": archive.ID, "agent_ids": []string{web}, "dest_path": "/opt", "extract": true}
	if code := sendJSON(t, w.clients["bob"], w.srv, "POST", "/api/transfers", body, nil); code != http.StatusForbidden {
		t.Errorf("a viewer unpacking an archive on the host: status = %d, want 403", code)
	}
}

// A folder download has no length (its zip is built while it is sent) and is
// audited as such.
func TestDownloadOfAFolderIsChunkedAndAudited(t *testing.T) {
	s, srv, id, secret := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	control := connectAgent(t, s, srv, id, secret)

	pending := startGet(admin, srv.URL, id, "/var/www")
	agent := answerAsAgent(t, control, srv.URL, id, secret, "/var/www")
	sendHeader(t, agent, "www.zip", -1)
	// More than the server's output buffer, or Go would send the whole small body
	// with a Content-Length of its own and nothing would be chunked.
	archive := strings.Repeat("PK-pretend-archive;", 20000)
	for start := 0; start < len(archive); start += 50000 {
		writeFrame(t, agent, websocket.MessageBinary, archive[start:min(start+50000, len(archive))])
	}
	agent.Close(websocket.StatusNormalClosure, "")

	resp := waitResult(t, pending)
	if resp.StatusCode != http.StatusOK || resp.ContentLength != -1 || resp.Header.Get("Content-Length") != "" {
		t.Fatalf("status = %d, Content-Length = %q (%d), want 200 with no length", resp.StatusCode, resp.Header.Get("Content-Length"), resp.ContentLength)
	}
	if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, "www.zip") {
		t.Errorf("Content-Disposition = %q, want www.zip", got)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if buf.String() != archive {
		t.Errorf("body: %d bytes, want the %d the agent sent", buf.Len(), len(archive))
	}
	var detail string
	for _, r := range auditRows(t, s.db) {
		if r.Action == "file_download" {
			detail = r.Detail
		}
	}
	if !strings.HasPrefix(detail, "path=/var/www size=unknown") {
		t.Errorf("audit detail = %q, want the path and an unknown size", detail)
	}
}
