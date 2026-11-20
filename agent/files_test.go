package agent

import (
	"context"
	"encoding/json"
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

// do sends one request through the same path a WebSocket frame takes.
func do(t *testing.T, readOnly bool, req protocol.FileRequest) protocol.FileResponse {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return handleFileRequest(data, readOnly, nil)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestListAndStat(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b.txt"), "hello")
	if err := os.Mkdir(filepath.Join(dir, "a-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	resp := do(t, false, protocol.FileRequest{ID: 7, Op: protocol.OpList, Path: dir})
	if !resp.OK || resp.ID != 7 {
		t.Fatalf("list: %+v, want ok with the request's id", resp)
	}
	if len(resp.Entries) != 2 || resp.Entries[0].Name != "a-dir" || resp.Entries[1].Name != "b.txt" {
		t.Fatalf("entries = %+v, want a-dir then b.txt (sorted)", resp.Entries)
	}
	if !resp.Entries[0].IsDir || resp.Entries[1].IsDir || resp.Entries[1].Size != 5 || resp.Entries[1].Mode == "" || resp.Entries[1].ModTime.IsZero() {
		t.Errorf("entry details look wrong: %+v", resp.Entries)
	}

	stat := do(t, false, protocol.FileRequest{ID: 8, Op: protocol.OpStat, Path: filepath.Join(dir, "b.txt")})
	if !stat.OK || stat.Entry == nil || stat.Entry.Name != "b.txt" || stat.Entry.Size != 5 {
		t.Errorf("stat = %+v", stat)
	}

	for _, bad := range []protocol.FileRequest{
		{ID: 9, Op: protocol.OpList, Path: filepath.Join(dir, "missing")},
		{ID: 10, Op: protocol.OpStat, Path: filepath.Join(dir, "missing")},
	} {
		if r := do(t, false, bad); r.OK || r.Error == "" || r.ID != bad.ID {
			t.Errorf("%s of a missing path: %+v, want a failure with a message and the request's id", bad.Op, r)
		}
	}
}

func TestPathsMustBeAbsolute(t *testing.T) {
	for _, op := range []string{protocol.OpList, protocol.OpStat, protocol.OpMkdir, protocol.OpDelete} {
		resp := do(t, false, protocol.FileRequest{ID: 1, Op: op, Path: "relative/dir"})
		if resp.OK || resp.Error != "path must be absolute" {
			t.Errorf("%s with a relative path: %+v, want 'path must be absolute'", op, resp)
		}
	}
	resp := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpRename, From: "a", To: "b"})
	if resp.OK || resp.Error != "path must be absolute" {
		t.Errorf("rename with relative paths: %+v", resp)
	}
	// An empty path is not absolute either.
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpList, Path: ""}); r.OK {
		t.Error("an empty path was accepted")
	}
}

func TestMkdir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new")

	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpMkdir, Path: target}); !r.OK {
		t.Fatalf("mkdir: %+v", r)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Error("the directory was not created")
	}
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpMkdir, Path: target}); r.OK {
		t.Error("creating a directory that exists succeeded")
	}
	// One level only: a missing parent is an error, not a surprise.
	if r := do(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpMkdir, Path: filepath.Join(dir, "no", "parent")}); r.OK {
		t.Error("mkdir created a whole chain")
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
	write(t, a, "A")
	write(t, c, "C")

	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpRename, From: a, To: b}); !r.OK {
		t.Fatalf("rename: %+v", r)
	}
	if exists(a) || !exists(b) {
		t.Error("the file was not moved")
	}

	// The destination exists: refused, and nothing is lost.
	r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpRename, From: b, To: c})
	if r.OK || r.Error != "destination already exists" {
		t.Errorf("rename onto an existing file: %+v, want 'destination already exists'", r)
	}
	if got, _ := os.ReadFile(c); string(got) != "C" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	tree := filepath.Join(dir, "tree")
	write(t, file, "x")
	if err := os.MkdirAll(filepath.Join(tree, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "inner", "deep"), "y")

	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpDelete, Path: file}); !r.OK || exists(file) {
		t.Errorf("deleting a file: %+v, still there: %v", r, exists(file))
	}

	// A directory with things in it needs recursive: true.
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpDelete, Path: tree}); r.OK || !exists(tree) {
		t.Errorf("deleting a non-empty directory without recursive: %+v, still there: %v", r, exists(tree))
	}
	if r := do(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpDelete, Path: tree, Recursive: true}); !r.OK || exists(tree) {
		t.Errorf("recursive delete: %+v, still there: %v", r, exists(tree))
	}

	// A path that is not there is an error, not a silent success.
	if r := do(t, false, protocol.FileRequest{ID: 4, Op: protocol.OpDelete, Path: file, Recursive: true}); r.OK {
		t.Error("deleting something that does not exist succeeded")
	}

	// The root is refused before anything else happens. The test asks without
	// "recursive" on purpose: if the check ever broke, the worst outcome would
	// be a harmless error from the operating system, not a wiped disk.
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	r := do(t, false, protocol.FileRequest{ID: 5, Op: protocol.OpDelete, Path: root})
	if r.OK || r.Error != "refusing to delete the root directory" {
		t.Errorf("deleting the root: %+v, want it refused", r)
	}
}

func TestReadOnlySessionChangesNothing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep")
	write(t, file, "data")

	for _, req := range []protocol.FileRequest{
		{ID: 1, Op: protocol.OpMkdir, Path: filepath.Join(dir, "new")},
		{ID: 2, Op: protocol.OpRename, From: file, To: filepath.Join(dir, "renamed")},
		{ID: 3, Op: protocol.OpDelete, Path: file, Recursive: true},
	} {
		if r := do(t, true, req); r.OK || r.Error != "this session is read-only" {
			t.Errorf("%s in a read-only session: %+v, want 'this session is read-only'", req.Op, r)
		}
	}
	if !exists(file) || exists(filepath.Join(dir, "new")) || exists(filepath.Join(dir, "renamed")) {
		t.Error("a read-only session changed the disk")
	}

	// Reading is still fine.
	for _, req := range []protocol.FileRequest{
		{ID: 4, Op: protocol.OpList, Path: dir},
		{ID: 5, Op: protocol.OpStat, Path: file},
		{ID: 6, Op: protocol.OpHome},
	} {
		if r := do(t, true, req); !r.OK {
			t.Errorf("%s in a read-only session: %+v, want it to work", req.Op, r)
		}
	}
}

func TestHomeIsAnAbsolutePath(t *testing.T) {
	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpHome})
	if !r.OK || !filepath.IsAbs(r.Path) {
		t.Errorf("home = %+v, want ok and an absolute path", r)
	}
}

func TestUnknownOperationAndBadJSON(t *testing.T) {
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: "format-disk"}); r.OK || r.Error != "unknown operation" || r.ID != 1 {
		t.Errorf("unknown op: %+v", r)
	}
	if r := handleFileRequest([]byte("not json"), false, nil); r.OK || r.Error != "invalid request" {
		t.Errorf("bad JSON: %+v", r)
	}
}

func TestLongListingIsTruncated(t *testing.T) {
	old := listLimit
	listLimit = 3
	t.Cleanup(func() { listLimit = old })

	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		write(t, filepath.Join(dir, name), "")
	}
	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpList, Path: dir})
	if !r.OK || len(r.Entries) != 3 || !r.Truncated {
		t.Errorf("listing = %d entries, truncated = %v, want 3 and true", len(r.Entries), r.Truncated)
	}
}

func TestSymlinkToADirectoryOpensLikeOne(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err) // Windows without the privilege
	}

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpList, Path: dir})
	var link *protocol.FileEntry
	for i := range r.Entries {
		if r.Entries[i].Name == "link" {
			link = &r.Entries[i]
		}
	}
	if link == nil || !link.IsLink || !link.IsDir {
		t.Errorf("link entry = %+v, want a link that is shown as a directory", link)
	}
}

// The whole thing over a real WebSocket: framing, ids, and that a binary frame
// or a bad request does not end the session.
func TestFilesSessionOverWebSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		runFiles(r.Context(), conn, json.RawMessage(`{"readonly":true}`), nil)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	ask := func(kind websocket.MessageType, payload string) protocol.FileResponse {
		t.Helper()
		if err := conn.Write(ctx, kind, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var resp protocol.FileResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatalf("response is not JSON: %s", data)
		}
		return resp
	}

	if r := ask(websocket.MessageText, `{"id":7,"op":"home"}`); !r.OK || r.ID != 7 || r.Path == "" {
		t.Errorf("home: %+v", r)
	}
	// This session is read-only, because the server said so in `open`.
	if r := ask(websocket.MessageText, `{"id":8,"op":"mkdir","path":"/tmp/x"}`); r.OK || r.ID != 8 || r.Error != "this session is read-only" {
		t.Errorf("mkdir: %+v", r)
	}
	// A bad request gets an answer, and the session goes on.
	if r := ask(websocket.MessageText, `{broken`); r.OK || r.Error == "" {
		t.Errorf("bad request: %+v", r)
	}
	// A binary frame is ignored: no answer, and the next request still works.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if r := ask(websocket.MessageText, `{"id":9,"op":"home"}`); !r.OK || r.ID != 9 {
		t.Errorf("after a binary frame: %+v", r)
	}
}
