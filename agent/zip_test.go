package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// zipOf zips a folder and opens the result, the way the person's unzip would.
func zipOf(t *testing.T, root string) *zip.Reader {
	t.Helper()
	var buf bytes.Buffer
	if err := writeZip(context.Background(), &buf, root); err != nil {
		t.Fatalf("writeZip: %v", err)
	}
	return openZip(t, buf.Bytes())
}

func openZip(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the archive is not a valid zip: %v", err)
	}
	return zr
}

func zipNames(zr *zip.Reader) []string {
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func zipText(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("%s is not in the archive (it has %v)", name, zipNames(zr))
	return ""
}

func makeTree(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(root, "inner", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "top.txt"), "top")
	write(t, filepath.Join(root, "inner", "deeper", "deep.txt"), "deep")
	return root
}

func TestZipHoldsTheWholeFolderInsideOneFolder(t *testing.T) {
	zr := zipOf(t, makeTree(t))

	want := []string{
		"project/", "project/empty/", "project/inner/", "project/inner/deeper/",
		"project/inner/deeper/deep.txt", "project/top.txt",
	}
	if got := zipNames(zr); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("archive holds %v, want %v", got, want)
	}
	if got := zipText(t, zr, "project/inner/deeper/deep.txt"); got != "deep" {
		t.Errorf("deep.txt = %q", got)
	}
	if got := zipText(t, zr, "project/top.txt"); got != "top" {
		t.Errorf("top.txt = %q", got)
	}
}

func TestZipOfALargeFileIsIntact(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("0123456789abcdef", 100000) // 1.6 MB: many chunks
	write(t, filepath.Join(root, "big.bin"), content)

	zr := zipOf(t, root)
	if got := zipText(t, zr, folderName(root)+"/big.bin"); got != content {
		t.Errorf("the file in the archive differs: %d bytes, want %d", len(got), len(content))
	}
}

func TestZipLeavesOutLinksAndSaysSo(t *testing.T) {
	root := makeTree(t)
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	zr := zipOf(t, root)
	for _, name := range zipNames(zr) {
		if name == "project/link" {
			t.Error("a link was put in the archive: it must not be followed or stored")
		}
	}
	note := zipText(t, zr, "project/SKIPPED.txt")
	if !strings.Contains(note, "link (not a regular file)") {
		t.Errorf("SKIPPED.txt = %q, want it to name the link", note)
	}
}

func TestZipSkipsWhatCannotBeReadAndSaysSo(t *testing.T) {
	skipWithoutUnixModes(t)
	if os.Geteuid() == 0 {
		t.Skip("root can read everything, so nothing is skipped")
	}
	root := makeTree(t)
	secret := filepath.Join(root, "secret.txt")
	write(t, secret, "hidden")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	zr := zipOf(t, root)
	note := zipText(t, zr, "project/SKIPPED.txt")
	if !strings.Contains(note, "secret.txt (permission denied)") || !strings.Contains(note, "locked (permission denied)") {
		t.Errorf("SKIPPED.txt = %q, want both unreadable items named", note)
	}
	if got := zipText(t, zr, "project/top.txt"); got != "top" {
		t.Errorf("the readable files must still be there: top.txt = %q", got)
	}
}

func TestZipOfANormalFolderHasNoSkippedNote(t *testing.T) {
	for _, name := range zipNames(zipOf(t, makeTree(t))) {
		if strings.HasSuffix(name, "SKIPPED.txt") {
			t.Error("a folder where everything was readable got a SKIPPED.txt")
		}
	}
}

func TestZipRefusesAFolderWithTooManyEntries(t *testing.T) {
	old := walkLimit
	walkLimit = 3
	t.Cleanup(func() { walkLimit = old })

	var buf bytes.Buffer
	err := writeZip(context.Background(), &buf, makeTree(t)) // 6 entries
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("writeZip = %v, want the too-many-entries error", err)
	}
}

func TestZipStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	if err := writeZip(ctx, &buf, makeTree(t)); err == nil {
		t.Error("writeZip went on after its context ended")
	}
}

func TestZipName(t *testing.T) {
	for in, want := range map[string]string{
		"/home/sanca/project": "project.zip",
		"/var/log":            "log.zip",
		"/":                   "root.zip",
	} {
		if got := zipName(in); got != want {
			t.Errorf("zipName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The whole path a folder download takes: a `download` session whose path is a
// folder. The header has no size (-1) and the frames add up to a valid archive.
func TestDownloadOfAFolderOverAWebSocket(t *testing.T) {
	root := makeTree(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		params, _ := json.Marshal(protocol.DownloadParams{Path: root})
		runDownload(r.Context(), conn, params)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // like the server's: the default is smaller than a 64 KB chunk

	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("first frame: kind %v, err %v, want the text header", kind, err)
	}
	var head protocol.DownloadHeader
	if err := json.Unmarshal(data, &head); err != nil || head.Name != "project.zip" || head.Size != -1 {
		t.Fatalf("header = %+v (%v), want project.zip with an unknown size (-1)", head, err)
	}

	var archive bytes.Buffer
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("the session ended with %v, want a normal close", err)
			}
			break
		}
		if kind != websocket.MessageBinary {
			t.Fatalf("unexpected text frame %q after the header", data)
		}
		archive.Write(data)
	}
	zr := openZip(t, archive.Bytes())
	if got := zipText(t, zr, "project/inner/deeper/deep.txt"); got != "deep" {
		t.Errorf("deep.txt in the downloaded archive = %q", got)
	}
}

// A folder that is too big is a failed download, never a short archive that
// looks complete: the close is not a normal one.
func TestDownloadOfATooBigFolderEndsWithAnError(t *testing.T) {
	old := walkLimit
	walkLimit = 2
	t.Cleanup(func() { walkLimit = old })

	root := makeTree(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		params, _ := json.Marshal(protocol.DownloadParams{Path: root})
		runDownload(r.Context(), conn, params)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				t.Fatal("a failed zip ended with a normal close: it would look like a complete download")
			}
			if !strings.Contains(err.Error(), "too many") {
				t.Errorf("close = %v, want the reason to say there are too many entries", err)
			}
			return
		}
	}
}
