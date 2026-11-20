package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// entry is one thing to put in a test archive.
type entry struct {
	name string
	body string
	mode fs.FileMode // 0 means a plain 0644 file
}

// buildZip makes an archive from entries, including ones a real packer would
// never write (a path that climbs out, a link).
func buildZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: time.Now()}
		if e.mode != 0 {
			header.SetMode(e.mode)
		} else if !strings.HasSuffix(e.name, "/") {
			header.SetMode(0o644)
		}
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// listing is every path under dir, relative, so a test can see exactly what is there.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(dir, path); rel != "." {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExtractUnpacksAFolderMadeByZipFolder(t *testing.T) {
	// The real round trip: the zip the agent makes for a download is what a send unpacks.
	root := makeTree(t)
	var buf bytes.Buffer
	if err := writeZip(context.Background(), &buf, root); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()

	if err := extractZip(zipFile(t, buf.Bytes()), dest); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	for path, want := range map[string]string{"project/top.txt": "top", "project/inner/deeper/deep.txt": "deep"} {
		if got, _ := os.ReadFile(filepath.Join(dest, filepath.FromSlash(path))); string(got) != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !exists(filepath.Join(dest, "project", "empty")) {
		t.Error("the empty folder was not unpacked")
	}
	// Only the folder: no staging folder and no archive left behind.
	if got := listing(t, dest); strings.Contains(strings.Join(got, "|"), ".headnode") {
		t.Errorf("leftovers in the destination: %v", got)
	}
}

func TestExtractKeepsTheModes(t *testing.T) {
	skipWithoutUnixModes(t)
	data := buildZip(t,
		entry{name: "app/", mode: fs.ModeDir | 0o750},
		entry{name: "app/run.sh", body: "#!/bin/sh", mode: 0o755},
		entry{name: "app/secret", body: "x", mode: 0o600},
		// Bits the packer must not be able to smuggle in.
		entry{name: "app/tool", body: "x", mode: 0o755 | fs.ModeSetuid},
	)
	dest := t.TempDir()
	if err := extractZip(zipFile(t, data), dest); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		"app": 0o750, "app/run.sh": 0o755, "app/secret": 0o600, "app/tool": 0o755,
	} {
		info, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
			t.Errorf("%s mode = %v, want %o with no setuid/setgid/sticky", path, info.Mode(), want)
		}
	}
}

func TestExtractRefusesPathsThatEscape(t *testing.T) {
	for _, name := range []string{"../evil.txt", "app/../../evil.txt", "/etc/evil.txt", `app\..\evil.txt`, "app/\x00x", "./../evil"} {
		parent := t.TempDir()
		dest := filepath.Join(parent, "dest")
		if err := os.Mkdir(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		data := buildZip(t, entry{name: "fine.txt", body: "ok"}, entry{name: name, body: "bad"})

		err := extractZip(zipFile(t, data), dest)
		if !errors.Is(err, errUnsafeArchive) {
			t.Errorf("entry %q: error = %v, want the unsafe-archive error", name, err)
		}
		// Nothing at all was written: not even the harmless first entry, and
		// nothing next to the destination either.
		if got := listing(t, parent); len(got) != 1 || got[0] != "dest" {
			t.Errorf("entry %q left files behind: %v", name, got)
		}
	}
}

func TestExtractRefusesLinksAndSpecialFiles(t *testing.T) {
	for name, mode := range map[string]fs.FileMode{"link": fs.ModeSymlink | 0o777, "pipe": fs.ModeNamedPipe | 0o644, "device": fs.ModeDevice | 0o644} {
		dest := t.TempDir()
		data := buildZip(t, entry{name: "app/" + name, body: "/etc/passwd", mode: mode})
		if err := extractZip(zipFile(t, data), dest); !errors.Is(err, errUnsafeArchive) {
			t.Errorf("%s entry: error = %v, want the unsafe-archive error", name, err)
		}
		if got := listing(t, dest); len(got) != 0 {
			t.Errorf("%s entry left files behind: %v", name, got)
		}
	}
}

// A folder of the same name is already there: nothing is touched.
func TestExtractRefusesToReplaceAnExistingFolder(t *testing.T) {
	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, "project", "mine.txt"), "mine")
	write(t, filepath.Join(dest, "other.txt"), "other")

	data := buildZip(t,
		entry{name: "new/", mode: fs.ModeDir | 0o755},
		entry{name: "new/a.txt", body: "a"},
		entry{name: "project/", mode: fs.ModeDir | 0o755},
		entry{name: "project/theirs.txt", body: "theirs"},
	)
	err := extractZip(zipFile(t, data), dest)
	if err == nil || !strings.Contains(err.Error(), `"project" already exists`) {
		t.Fatalf("error = %v, want 'project already exists'", err)
	}
	want := []string{"other.txt", "project", "project/mine.txt"}
	if got := listing(t, dest); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("destination holds %v, want it exactly as it was: %v (not even the harmless folder moved in)", got, want)
	}
}

func TestExtractRefusesADuplicateEntry(t *testing.T) {
	dest := t.TempDir()
	data := buildZip(t, entry{name: "app/x.txt", body: "one"}, entry{name: "app/x.txt", body: "two"})
	if err := extractZip(zipFile(t, data), dest); err == nil {
		t.Error("an archive with two entries of the same name was unpacked")
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Errorf("a failed unpack left files behind: %v", got)
	}
}

func TestExtractLimits(t *testing.T) {
	oldEntries, oldBytes := walkLimit, maxUnpackBytes
	t.Cleanup(func() { walkLimit, maxUnpackBytes = oldEntries, oldBytes })

	data := buildZip(t, entry{name: "a/", mode: fs.ModeDir | 0o755}, entry{name: "a/1", body: "1234567890"}, entry{name: "a/2", body: "x"})

	walkLimit = 2
	if err := extractZip(zipFile(t, data), t.TempDir()); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("too many entries: error = %v", err)
	}
	walkLimit = oldEntries

	maxUnpackBytes = 5
	dest := t.TempDir()
	if err := extractZip(zipFile(t, data), dest); !errors.Is(err, errUnpackTooBig) {
		t.Errorf("too big: error = %v, want %v", err, errUnpackTooBig)
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Errorf("a refused archive left files behind: %v", got)
	}
}

func TestExtractOfSomethingThatIsNotAZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.zip")
	write(t, path, "this is plain text")
	err := extractZip(path, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not a valid zip") {
		t.Errorf("error = %v, want 'not a valid zip archive'", err)
	}
}

// -------------------------------------------------- through the whole fetch

func TestFetchWithExtractUnpacksTheArchive(t *testing.T) {
	var buf bytes.Buffer
	if err := writeZip(context.Background(), &buf, makeTree(t)); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	_, cfg := fileServer(t, body)
	dest := t.TempDir()

	cmd := command(dest, "", body)
	cmd.Extract = true
	if err := fetchFile(context.Background(), cfg, cmd); err != nil {
		t.Fatalf("fetchFile: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "project", "inner", "deeper", "deep.txt")); string(got) != "deep" {
		t.Errorf("the folder was not unpacked: deep.txt = %q", got)
	}
	// The archive itself is not kept, and no temporary file stays.
	if got := listing(t, dest); strings.Contains(strings.Join(got, "|"), ".zip") || strings.Contains(strings.Join(got, "|"), ".part") || strings.Contains(strings.Join(got, "|"), ".headnode") {
		t.Errorf("leftovers in the destination: %v", got)
	}
}

func TestFetchWithExtractNeedsAnExistingFolderAndAGoodChecksum(t *testing.T) {
	data := buildZip(t, entry{name: "app/a.txt", body: "a"})
	body := string(data)
	_, cfg := fileServer(t, body)

	cmd := command(filepath.Join(t.TempDir(), "missing"), "", body)
	cmd.Extract = true
	if err := fetchFile(context.Background(), cfg, cmd); err == nil {
		t.Error("unpacking into a folder that does not exist succeeded")
	}

	dest := t.TempDir()
	cmd = command(dest, "", body)
	cmd.Extract = true
	cmd.SHA256 = sum("something else")
	if err := fetchFile(context.Background(), cfg, cmd); !errors.Is(err, errBadChecksum) {
		t.Errorf("a wrong checksum: error = %v, want %v", err, errBadChecksum)
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Errorf("a failed fetch left files behind: %v", got)
	}
}
