package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// doReporting is do, but it also returns every report the request produced.
func doReporting(t *testing.T, readOnly bool, req protocol.FileRequest) (protocol.FileResponse, []protocol.FileChange) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var reports []protocol.FileChange
	resp := handleFileRequest(data, readOnly, func(c protocol.FileChange) { reports = append(reports, c) })
	return resp, reports
}

func TestMkdirIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new")
	resp, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpMkdir, Path: path})
	if !resp.OK {
		t.Fatalf("mkdir: %+v", resp)
	}
	want := protocol.FileChange{Op: protocol.OpMkdir, Path: path, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports = %+v, want exactly %+v", reports, want)
	}
}

func TestFailedMkdirIsReportedWithItsError(t *testing.T) {
	// The parent does not exist, and mkdir never creates a chain.
	path := filepath.Join(t.TempDir(), "missing", "new")
	resp, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpMkdir, Path: path})
	if resp.OK {
		t.Fatal("mkdir under a missing parent should fail")
	}
	if len(reports) != 1 || reports[0].OK || reports[0].Op != protocol.OpMkdir || reports[0].Path != path {
		t.Fatalf("reports = %+v, want one failed mkdir of %s", reports, path)
	}
	// The report carries the same short text the browser got, not the long form.
	if reports[0].Error == "" || reports[0].Error != resp.Error || strings.Contains(reports[0].Error, path) {
		t.Errorf("report error = %q, response error = %q", reports[0].Error, resp.Error)
	}
}

// The operating system says "file exists" whether the thing in the way is a
// file or a folder. The person should be told which.
func TestMkdirSaysWhetherAFileOrAFolderIsInTheWay(t *testing.T) {
	dir := t.TempDir()
	folder, file := filepath.Join(dir, "folder"), filepath.Join(dir, "file.txt")
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpMkdir, Path: folder}); !r.OK {
		t.Fatalf("setup: %+v", r)
	}
	write(t, file, "x")

	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpMkdir, Path: folder}); r.OK || r.Error != "a folder with that name already exists" {
		t.Errorf("mkdir over a folder: %+v", r)
	}
	if r := do(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpMkdir, Path: file}); r.OK || r.Error != "a file with that name already exists" {
		t.Errorf("mkdir over a file: %+v", r)
	}
}

func TestRenameIsReportedWithBothPaths(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	write(t, from, "x")

	resp, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpRename, From: from, To: to})
	if !resp.OK {
		t.Fatalf("rename: %+v", resp)
	}
	want := protocol.FileChange{Op: protocol.OpRename, Path: from, To: to, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports = %+v, want exactly %+v", reports, want)
	}

	// Renaming onto something that exists is refused, and that is reported too.
	write(t, from, "x")
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpRename, From: from, To: to})
	if len(reports) != 1 || reports[0].OK || reports[0].Error != "destination already exists" {
		t.Errorf("reports = %+v, want one failed rename: destination already exists", reports)
	}
}

func TestDeleteIsReported(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gone.txt")
	write(t, file, "x")

	resp, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpDelete, Path: file})
	if !resp.OK {
		t.Fatalf("delete: %+v", resp)
	}
	want := protocol.FileChange{Op: protocol.OpDelete, Path: file, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports = %+v, want exactly %+v", reports, want)
	}

	// A recursive delete says so.
	tree := filepath.Join(dir, "tree")
	write(t, filepath.Join(dir, "keep.txt"), "x")
	if _, r := doReporting(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpMkdir, Path: tree}); len(r) != 1 {
		t.Fatalf("setup mkdir reports = %+v", r)
	}
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpDelete, Path: tree, Recursive: true})
	want = protocol.FileChange{Op: protocol.OpDelete, Path: tree, Recursive: true, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports = %+v, want exactly %+v", reports, want)
	}

	// Deleting what is not there fails, and the failure is reported.
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 4, Op: protocol.OpDelete, Path: file})
	if len(reports) != 1 || reports[0].OK || reports[0].Error == "" {
		t.Errorf("reports = %+v, want one failed delete with an error", reports)
	}
}

// A read-only session refused the change: nothing was attempted on the disk, so
// there is nothing to audit (the session's own record says it was read-only).
func TestReadOnlySessionReportsNothing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep.txt")
	write(t, file, "x")

	for _, req := range []protocol.FileRequest{
		{ID: 1, Op: protocol.OpMkdir, Path: filepath.Join(dir, "new")},
		{ID: 2, Op: protocol.OpRename, From: file, To: filepath.Join(dir, "other")},
		{ID: 3, Op: protocol.OpDelete, Path: file},
	} {
		resp, reports := doReporting(t, true, req)
		if resp.OK || resp.Error != "this session is read-only" {
			t.Errorf("%s: %+v, want a read-only refusal", req.Op, resp)
		}
		if len(reports) != 0 {
			t.Errorf("%s: reports = %+v, want none", req.Op, reports)
		}
	}
	if !exists(file) {
		t.Error("the file must still be there")
	}
}

func TestReadsAndBadRequestsReportNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "x")

	for _, req := range []protocol.FileRequest{
		{ID: 1, Op: protocol.OpList, Path: dir},
		{ID: 2, Op: protocol.OpList, Path: filepath.Join(dir, "missing")}, // a failed read is still a read
		{ID: 3, Op: protocol.OpStat, Path: filepath.Join(dir, "a.txt")},
		{ID: 4, Op: protocol.OpHome},
		{ID: 5, Op: "format-disk"},
	} {
		if _, reports := doReporting(t, false, req); len(reports) != 0 {
			t.Errorf("%s: reports = %+v, want none", req.Op, reports)
		}
	}

	var reports []protocol.FileChange
	handleFileRequest([]byte("not json"), false, func(c protocol.FileChange) { reports = append(reports, c) })
	if len(reports) != 0 {
		t.Errorf("bad JSON: reports = %+v, want none", reports)
	}
}

// changeReporter must put the session id on the report and send it on the
// control connection in the usual {"type","data"} envelope.
func TestChangeReporterSendsAnEnvelopeWithTheSessionID(t *testing.T) {
	received := make(chan protocol.Envelope, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) == nil {
			received <- env
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	control, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.CloseNow()

	report := changeReporter(ctx, control, "s_abc")
	report(protocol.FileChange{Op: protocol.OpMkdir, Path: "/tmp/x", OK: true})

	select {
	case env := <-received:
		if env.Type != protocol.TypeFileChange {
			t.Fatalf("type = %q, want %q", env.Type, protocol.TypeFileChange)
		}
		var got protocol.FileChange
		if err := json.Unmarshal(env.Data, &got); err != nil {
			t.Fatal(err)
		}
		want := protocol.FileChange{SessionID: "s_abc", Op: protocol.OpMkdir, Path: "/tmp/x", OK: true}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("the control connection never received the report")
	}
}

// A dead control connection must not break the file manager: the report is
// dropped with a log line, and the reporter returns.
func TestChangeReporterSurvivesABrokenConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.CloseNow()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	control, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	control.CloseNow() // dead before the first report

	done := make(chan struct{})
	go func() {
		changeReporter(ctx, control, "s_abc")(protocol.FileChange{Op: protocol.OpDelete, Path: "/x"})
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the reporter hung on a broken connection")
	}
}
