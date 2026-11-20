package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"headnode/protocol"
)

func readOf(t *testing.T, readOnly bool, path string) protocol.FileResponse {
	t.Helper()
	return do(t, readOnly, protocol.FileRequest{ID: 1, Op: protocol.OpRead, Path: path})
}

func TestReadTextReturnsTheTextAndAVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.conf")
	write(t, file, "port = 8080\nname = \"ü\"\n")

	r := readOf(t, false, file)
	if !r.OK || r.Text != "port = 8080\nname = \"ü\"\n" || r.Version == "" {
		t.Fatalf("read: %+v", r)
	}
	// Reading again without a change gives the same version.
	if again := readOf(t, false, file); again.Version != r.Version {
		t.Errorf("the version moved without a change: %q then %q", r.Version, again.Version)
	}
}

func TestReadTextRefusesWhatIsNotAnEditableTextFile(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "bin")
	if err := os.WriteFile(binary, []byte("abc\x00def"), 0o644); err != nil {
		t.Fatal(err)
	}
	notUTF8 := filepath.Join(dir, "latin1")
	if err := os.WriteFile(notUTF8, []byte{'c', 'a', 'f', 0xe9}, 0o644); err != nil { // "café" in Latin-1
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", protocol.MaxEditBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		binary:                       "this is not a text file (it has binary data in it)",
		notUTF8:                      "this is not a text file (it has binary data in it)",
		big:                          errTooBigEdit.Error(),
		dir:                          "is a directory",
	} {
		if r := readOf(t, false, path); r.OK || r.Error != want {
			t.Errorf("read %s: %+v, want the error %q", filepath.Base(path), r, want)
		}
	}
	// The wording for a missing file is the operating system's own.
	if r := readOf(t, false, filepath.Join(dir, "nothing")); r.OK || r.Error == "" {
		t.Errorf("read nothing: %+v, want a failure with a message", r)
	}
	if r := readOf(t, false, "relative"); r.OK || r.Error != "path must be absolute" {
		t.Errorf("relative path: %+v", r)
	}

	// Exactly at the limit is fine.
	atLimit := filepath.Join(dir, "limit")
	if err := os.WriteFile(atLimit, []byte(strings.Repeat("x", protocol.MaxEditBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := readOf(t, false, atLimit); !r.OK {
		t.Errorf("a file of exactly the limit: %+v, want it to open", r)
	}
}

func writeOf(t *testing.T, readOnly bool, path, content, version string) protocol.FileResponse {
	t.Helper()
	return do(t, readOnly, protocol.FileRequest{ID: 2, Op: protocol.OpWrite, Path: path, Content: content, Version: version})
}

func TestWriteTextReplacesTheContent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.conf")
	write(t, file, "old")
	v1 := readOf(t, false, file).Version

	r := writeOf(t, false, file, "new text\n", v1)
	if !r.OK || r.Version == "" {
		t.Fatalf("write: %+v", r)
	}
	if got, _ := os.ReadFile(file); string(got) != "new text\n" {
		t.Errorf("the file holds %q, want the new text", got)
	}
	// The version it returns is the file's current one: a second save can follow.
	if again := writeOf(t, false, file, "newer", r.Version); !again.OK {
		t.Errorf("a second save with the returned version: %+v", again)
	}
	// An empty file is a file too.
	cur := readOf(t, false, file).Version
	if r := writeOf(t, false, file, "", cur); !r.OK {
		t.Errorf("saving an empty text: %+v", r)
	}
	if got, _ := os.ReadFile(file); len(got) != 0 {
		t.Errorf("the file holds %q, want it empty", got)
	}
}

// The editor must never throw away a change somebody else made on the host.
func TestWriteTextRefusesAFileThatChangedSinceItWasRead(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.conf")
	write(t, file, "one")
	version := readOf(t, false, file).Version

	write(t, file, "changed by someone else, with another length")
	r := writeOf(t, false, file, "my edit", version)
	if r.OK || r.Error != errChanged.Error() {
		t.Fatalf("write over a changed file: %+v, want %q", r, errChanged)
	}
	if got, _ := os.ReadFile(file); string(got) != "changed by someone else, with another length" {
		t.Errorf("the other person's change was overwritten: %q", got)
	}
}

func TestWriteTextRefusals(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	write(t, file, "x")
	version := readOf(t, false, file).Version

	cases := []struct {
		name, path, content, version, want string
	}{
		{"no version", file, "y", "", errNoVersion.Error()},
		{"too big", file, strings.Repeat("y", protocol.MaxEditBytes+1), version, errTooBigEdit.Error()},
		{"a folder", dir, "y", "1-1", "not a regular file"},
		{"relative", "f", "y", version, "path must be absolute"},
	}
	for _, c := range cases {
		if r := writeOf(t, false, c.path, c.content, c.version); r.OK || r.Error != c.want {
			t.Errorf("%s: %+v, want %q", c.name, r, c.want)
		}
	}
	if got, _ := os.ReadFile(file); string(got) != "x" {
		t.Errorf("a refused write changed the file: %q", got)
	}

	// It edits files, it does not create them.
	missing := filepath.Join(dir, "missing")
	if r := writeOf(t, false, missing, "y", "1-1"); r.OK {
		t.Error("a write created a file that was not there")
	}
	if exists(missing) {
		t.Error("the missing file exists now")
	}
}

func TestWriteTextKeepsTheModeAndLineEnds(t *testing.T) {
	skipWithoutUnixModes(t)
	file := filepath.Join(t.TempDir(), "run.sh")
	write(t, file, "#!/bin/sh\r\necho hi\r\n")
	if err := os.Chmod(file, 0o750); err != nil {
		t.Fatal(err)
	}
	r := readOf(t, false, file)
	if !strings.Contains(r.Text, "\r\n") {
		t.Fatalf("read lost the CRLF: %q", r.Text)
	}
	if w := writeOf(t, false, file, r.Text+"echo bye\r\n", r.Version); !w.OK {
		t.Fatalf("write: %+v", w)
	}
	if got, _ := os.ReadFile(file); string(got) != "#!/bin/sh\r\necho hi\r\necho bye\r\n" {
		t.Errorf("the file holds %q", got)
	}
	if got := modeOf(t, file); got != 0o750 {
		t.Errorf("mode after the write = %o, want 750 (the file itself is written, not replaced)", got)
	}
}

func TestReadOnlySessionMayReadButNotWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "f")
	write(t, file, "secret-ish")
	r := readOf(t, true, file)
	if !r.OK || r.Text != "secret-ish" {
		t.Fatalf("read in a read-only session: %+v, want it to work (viewing is reading)", r)
	}

	resp, reports := doReporting(t, true, protocol.FileRequest{ID: 2, Op: protocol.OpWrite, Path: file, Content: "x", Version: r.Version})
	if resp.OK || resp.Error != "this session is read-only" || len(reports) != 0 {
		t.Errorf("write in a read-only session: %+v with reports %+v", resp, reports)
	}
	if got, _ := os.ReadFile(file); string(got) != "secret-ish" {
		t.Errorf("a read-only session changed the file: %q", got)
	}
}

// The audit gets the size of a save, never what was saved: a file can hold a
// password.
func TestWriteIsReportedWithItsSizeAndNeverItsText(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.conf")
	write(t, file, "old")
	version := readOf(t, false, file).Version

	_, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpWrite, Path: file, Content: "password=hunter2", Version: version})
	want := protocol.FileChange{Op: protocol.OpWrite, Path: file, Size: 16, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("reports = %+v, want exactly %+v", reports, want)
	}

	// A refused save is reported too, with its error.
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpWrite, Path: file, Content: "x", Version: "stale"})
	if len(reports) != 1 || reports[0].OK || reports[0].Error != errChanged.Error() {
		t.Errorf("refused write reports = %+v", reports)
	}

	// Reading is not reported.
	if _, reports := doReporting(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpRead, Path: file}); len(reports) != 0 {
		t.Errorf("read reports = %+v, want none", reports)
	}
}
