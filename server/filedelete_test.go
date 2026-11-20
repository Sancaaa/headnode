package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (s *Server) blobExists(sum string) bool {
	_, err := os.Stat(filepath.Join(s.filesDir, sum))
	return err == nil
}

func listFiles(t *testing.T, w *permWorld) []fileView {
	t.Helper()
	var files []fileView
	if code := getJSON(t, w.clients["root"], w.srv, "/api/files", &files); code != http.StatusOK {
		t.Fatalf("list files: status = %d", code)
	}
	return files
}

func deleteFile(t *testing.T, w *permWorld, who, id string) int {
	t.Helper()
	return sendJSON(t, w.clients[who], w.srv, "DELETE", "/api/files/"+id, nil, nil)
}

func uploadAs(t *testing.T, w *permWorld, who, name, content string) uploadReply {
	t.Helper()
	code, reply := upload(t, w.clients[who], w.srv, "file", name, content)
	if code != http.StatusCreated {
		t.Fatalf("%s uploading %s: status = %d", who, name, code)
	}
	return reply
}

func TestAdminDeletesAnUploadAndItsBlob(t *testing.T) {
	w := newPermWorld(t)
	file := uploadAs(t, w, "alice", "old-build.tar", "some bytes")

	files := listFiles(t, w)
	if len(files) != 1 || files[0].ID != file.ID || files[0].Name != "old-build.tar" || files[0].Size != 10 ||
		files[0].UploadedBy == nil || *files[0].UploadedBy != "alice" || files[0].InUse {
		t.Fatalf("list = %+v, want the one upload by alice, not in use", files)
	}
	if !w.s.blobExists(file.SHA256) {
		t.Fatal("the blob should be on disk after the upload")
	}

	// An operator may upload, but not look at everyone's uploads or delete any,
	// not even their own. Nothing changes.
	if code := deleteFile(t, w, "alice", file.ID); code != http.StatusForbidden {
		t.Errorf("alice deleting: status = %d, want 403", code)
	}
	if code := getJSON(t, w.clients["alice"], w.srv, "/api/files", nil); code != http.StatusForbidden {
		t.Errorf("alice listing: status = %d, want 403", code)
	}
	if len(listFiles(t, w)) != 1 || !w.s.blobExists(file.SHA256) {
		t.Fatal("a refused delete must not change anything")
	}

	if code := deleteFile(t, w, "root", file.ID); code != http.StatusNoContent {
		t.Fatalf("root deleting: status = %d, want 204", code)
	}
	if len(listFiles(t, w)) != 0 {
		t.Error("the upload is still listed")
	}
	if w.s.blobExists(file.SHA256) {
		t.Error("the blob is still on disk: nothing else uses it")
	}
	if code := deleteFile(t, w, "root", file.ID); code != http.StatusNotFound {
		t.Errorf("deleting twice: status = %d, want 404", code)
	}

	// The audit line says who, what, and what happened to the blob.
	root := w.user(t, "root")
	var found bool
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "upload_delete" {
			found = true
			want := "file=old-build.tar sha256=" + file.SHA256[:12] + " blob=removed"
			if r.Detail != want || !r.UserID.Valid || r.UserID.Int64 != root.ID {
				t.Errorf("audit row = %+v, want %q by root", r, want)
			}
		}
	}
	if !found {
		t.Error("no upload_delete entry in the audit log")
	}
}

// The same bytes uploaded twice are one blob with two rows. Deleting one row
// must leave the blob for the other.
func TestBlobSharedByTwoUploadsStaysUntilTheLastIsDeleted(t *testing.T) {
	w := newPermWorld(t)
	first := uploadAs(t, w, "alice", "tool", "identical content")
	second := uploadAs(t, w, "root", "tool-copy", "identical content")
	if first.ID == second.ID || first.SHA256 != second.SHA256 {
		t.Fatalf("setup: %+v and %+v should be two rows with one hash", first, second)
	}
	if n := len(listFiles(t, w)); n != 2 {
		t.Fatalf("%d uploads listed, want 2", n)
	}

	if code := deleteFile(t, w, "root", first.ID); code != http.StatusNoContent {
		t.Fatalf("deleting the first: status = %d", code)
	}
	if !w.s.blobExists(first.SHA256) {
		t.Fatal("the blob was removed while another upload still uses it")
	}
	if remaining := listFiles(t, w); len(remaining) != 1 || remaining[0].ID != second.ID {
		t.Fatalf("after the first delete: %+v, want only the second upload", remaining)
	}

	if code := deleteFile(t, w, "root", second.ID); code != http.StatusNoContent {
		t.Fatalf("deleting the second: status = %d", code)
	}
	if w.s.blobExists(first.SHA256) {
		t.Error("the blob should go with the last upload that used it")
	}

	var details []string
	for _, r := range auditRows(t, w.s.db) {
		if r.Action == "upload_delete" {
			details = append(details, r.Detail[strings.LastIndex(r.Detail, "blob="):])
		}
	}
	want := "blob=kept (another upload uses it)|blob=removed"
	if strings.Join(details, "|") != want {
		t.Errorf("audit blob notes = %v, want %q", details, want)
	}
}

// An agent that was told to fetch the file must be able to finish. Once every
// host has answered, the file can go.
func TestUploadCannotBeDeletedWhileATransferUsesIt(t *testing.T) {
	w := newPermWorld(t)
	web, secret := w.hosts["web-01"], w.secrets["web-01"]
	control := connectAgent(t, w.s, w.srv, web, secret)
	file := uploadAs(t, w, "alice", "k3s-manager", "the program")

	var started transferView
	body := map[string]any{"file_id": file.ID, "agent_ids": []string{web}, "dest_path": "/tmp/k3s-manager", "mode": "0755"}
	if code := sendJSON(t, w.clients["alice"], w.srv, "POST", "/api/transfers", body, &started); code != http.StatusCreated {
		t.Fatalf("start transfer: status = %d", code)
	}
	readFetch(t, control)

	if files := listFiles(t, w); len(files) != 1 || !files[0].InUse {
		t.Errorf("list = %+v, want the file marked in use", files)
	}
	resp := do(t, w.clients["root"], "DELETE", w.srv.URL+"/api/files/"+file.ID, w.srv.URL)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting a file in use: status = %d, want 409", resp.StatusCode)
	}
	if len(listFiles(t, w)) != 1 || !w.s.blobExists(file.SHA256) {
		t.Fatal("a refused delete must leave the upload and its blob alone")
	}
	// The agent's download still works: that is the point of refusing.
	if code, got := agentFetches(t, w.srv, web, secret, file.ID); code != http.StatusOK || got != "the program" {
		t.Errorf("agent fetching: status = %d, body %q", code, got)
	}

	reportResult(t, control, started.ID, true, "")
	waitFor(t, "the transfer to finish", func() bool { return !w.s.transfers.filesInUse()[file.ID] })
	if code := deleteFile(t, w, "root", file.ID); code != http.StatusNoContent {
		t.Errorf("deleting after the transfer finished: status = %d, want 204", code)
	}
}

// A host that never answers must not keep a file from being deleted for good.
func TestAnOldUnansweredTransferDoesNotBlockDeletion(t *testing.T) {
	table := newTransferTable()
	table.add(&transfer{id: "t_old", fileID: "f_old", created: time.Now().Add(-2 * transferStuckAfter),
		hosts: []*transferHost{{agentID: "a_x", status: transferPending}}})
	table.add(&transfer{id: "t_new", fileID: "f_new", created: time.Now(),
		hosts: []*transferHost{{agentID: "a_x", status: transferPending}}})
	table.add(&transfer{id: "t_done", fileID: "f_done", created: time.Now(),
		hosts: []*transferHost{{agentID: "a_x", status: transferOK}}})

	inUse := table.filesInUse()
	if inUse["f_old"] || inUse["f_done"] || !inUse["f_new"] {
		t.Errorf("in use = %v, want only f_new", inUse)
	}
}

// Deleting changes things, so it is Origin-checked like every other change.
func TestDeletingAnUploadNeedsTheOrigin(t *testing.T) {
	w := newPermWorld(t)
	file := uploadAs(t, w, "alice", "keep-me", "bytes")

	resp := do(t, w.clients["root"], "DELETE", w.srv.URL+"/api/files/"+file.ID, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no Origin: status = %d, want 403", resp.StatusCode)
	}
	resp = do(t, w.clients["root"], "DELETE", w.srv.URL+"/api/files/"+file.ID, "http://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: status = %d, want 403", resp.StatusCode)
	}
	if len(listFiles(t, w)) != 1 || !w.s.blobExists(file.SHA256) {
		t.Error("a refused request deleted something")
	}
}
