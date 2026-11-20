package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"headnode/protocol"
)

func skipWithoutUnixModes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// ------------------------------------------------------------------- copy

func TestCopyAFile(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.txt"), filepath.Join(dir, "a (1).txt")
	write(t, from, "hello")

	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: from, To: to}); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	if got, _ := os.ReadFile(to); string(got) != "hello" {
		t.Errorf("copy holds %q, want hello", got)
	}
	if !exists(from) {
		t.Error("the original is gone: a copy must not move it")
	}
}

func TestCopyKeepsTheMode(t *testing.T) {
	skipWithoutUnixModes(t)
	dir := t.TempDir()
	from, to := filepath.Join(dir, "run.sh"), filepath.Join(dir, "run-copy.sh")
	write(t, from, "#!/bin/sh")
	if err := os.Chmod(from, 0o751); err != nil {
		t.Fatal(err)
	}
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: from, To: to}); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	if got := modeOf(t, to); got != 0o751 {
		t.Errorf("mode of the copy = %o, want 751", got)
	}
}

func TestCopyAFolderWithEverythingInIt(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "inner", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "top.txt"), "top")
	write(t, filepath.Join(tree, "inner", "deeper", "deep.txt"), "deep")
	if err := os.Mkdir(filepath.Join(tree, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	copied := filepath.Join(dir, "tree (1)")
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: tree, To: copied}); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	for path, want := range map[string]string{"top.txt": "top", "inner/deeper/deep.txt": "deep"} {
		if got, _ := os.ReadFile(filepath.Join(copied, filepath.FromSlash(path))); string(got) != want {
			t.Errorf("%s in the copy = %q, want %q", path, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(copied, "empty")); err != nil || !info.IsDir() {
		t.Error("an empty folder was not copied")
	}
	if !exists(filepath.Join(tree, "top.txt")) {
		t.Error("the original lost its files")
	}
}

// A read-only folder must still be copied completely, with its mode at the end.
func TestCopyOfAReadOnlyFolderIsComplete(t *testing.T) {
	skipWithoutUnixModes(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so this proves nothing")
	}
	dir := t.TempDir()
	tree := filepath.Join(dir, "locked")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "f"), "x")
	if err := os.Chmod(tree, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(tree, 0o755); os.Chmod(filepath.Join(dir, "locked-copy"), 0o755) })

	copied := filepath.Join(dir, "locked-copy")
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: tree, To: copied}); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	if got := modeOf(t, copied); got != 0o555 {
		t.Errorf("mode of the copied folder = %o, want 555", got)
	}
	if !exists(filepath.Join(copied, "f")) {
		t.Error("the file inside was not copied")
	}
}

func TestCopyNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, from, "A")
	write(t, to, "B")

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: from, To: to})
	if r.OK || r.Error != "destination already exists" {
		t.Errorf("copy onto an existing file: %+v, want 'destination already exists'", r)
	}
	if got, _ := os.ReadFile(to); string(got) != "B" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
}

func TestCopyOfAFolderIntoItselfIsRefused(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "f"), "x")

	for _, to := range []string{filepath.Join(tree, "sub"), filepath.Join(tree, "sub", "deeper")} {
		r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: tree, To: to})
		if r.OK || r.Error != "cannot copy a folder into itself" {
			t.Errorf("copy into %s: %+v, want it refused", to, r)
		}
	}
	if exists(filepath.Join(tree, "sub")) {
		t.Error("a refused copy left something behind")
	}
	// A folder next to a folder with a similar name is not "inside" it.
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpCopy, From: tree, To: tree + "-backup"}); !r.OK {
		t.Errorf("copy to a sibling with a similar name: %+v, want it to work", r)
	}
}

func TestCopyOfTooManyEntriesIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	old := walkLimit
	walkLimit = 3
	t.Cleanup(func() { walkLimit = old })

	dir := t.TempDir()
	tree := filepath.Join(dir, "big")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		write(t, filepath.Join(tree, name), "")
	}

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: tree, To: filepath.Join(dir, "big-copy")})
	if r.OK || r.Error == "" {
		t.Fatalf("copy of too many entries: %+v, want it refused", r)
	}
	if exists(filepath.Join(dir, "big-copy")) {
		t.Error("a refused copy created the destination")
	}
}

func TestCopyKeepsALinkAsALink(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(tree, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	copied := filepath.Join(dir, "copy")
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: tree, To: copied}); !r.OK {
		t.Fatalf("copy: %+v", r)
	}
	target, err := os.Readlink(filepath.Join(copied, "link"))
	if err != nil || filepath.ToSlash(target) != "/etc/hostname" { // Windows writes it with backslashes
		t.Errorf("the link in the copy = %q (%v), want it to still point at /etc/hostname", target, err)
	}
}

func TestCopyNeedsAbsolutePathsAndAnExistingSource(t *testing.T) {
	dir := t.TempDir()
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: "a", To: "b"}); r.OK || r.Error != "path must be absolute" {
		t.Errorf("relative paths: %+v", r)
	}
	r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpCopy, From: filepath.Join(dir, "nothing"), To: filepath.Join(dir, "x")})
	if r.OK || r.Error == "" {
		t.Errorf("a missing source: %+v, want a failure", r)
	}
}

// ------------------------------------------------------------------ chmod

func TestChmod(t *testing.T) {
	skipWithoutUnixModes(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "run.sh")
	write(t, file, "x")

	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpChmod, Path: file, Mode: "0755"}); !r.OK {
		t.Fatalf("chmod: %+v", r)
	}
	if got := modeOf(t, file); got != 0o755 {
		t.Errorf("mode = %o, want 755", got)
	}
	// Three digits work like four, as everywhere else.
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpChmod, Path: file, Mode: "600"}); !r.OK {
		t.Fatalf("chmod 600: %+v", r)
	}
	if got := modeOf(t, file); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestChmodRefusesWhatItShouldNot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	write(t, file, "x")
	before := modeOf(t, file)

	for _, mode := range []string{"", "4755", "2755", "1777", "0999", "rwx", "-1", "07777"} {
		r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpChmod, Path: file, Mode: mode})
		if r.OK || r.Error != "mode must be at most 0777" {
			t.Errorf("chmod %q: %+v, want it refused", mode, r)
		}
	}
	if modeOf(t, file) != before {
		t.Error("a refused chmod changed the mode")
	}
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpChmod, Path: "relative", Mode: "0644"}); r.OK || r.Error != "path must be absolute" {
		t.Errorf("relative path: %+v", r)
	}
	if r := do(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpChmod, Path: filepath.Join(dir, "missing"), Mode: "0644"}); r.OK {
		t.Error("chmod of a missing file succeeded")
	}
}

// ------------------------------------------------------------------ props

func TestPropsOfAFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	write(t, file, "hello")

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpProps, Path: file})
	if !r.OK || r.Props == nil {
		t.Fatalf("props: %+v", r)
	}
	p := r.Props
	if p.Name != "notes.txt" || p.IsDir || p.IsLink || p.Size != 5 || p.Mode == "" || p.Perm == "" || p.ModTime.IsZero() {
		t.Errorf("props = %+v", p)
	}
}

func TestPropsOfAFolderAddsUpWhatIsInside(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tree, "a"), "12345")        // 5
	write(t, filepath.Join(tree, "inner", "b"), "123") // 3
	write(t, filepath.Join(tree, "inner", "c"), "")    // 0

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpProps, Path: tree})
	if !r.OK || r.Props == nil {
		t.Fatalf("props: %+v", r)
	}
	p := r.Props
	if !p.IsDir || p.Size != 8 || p.Files != 3 || p.Dirs != 1 || p.Truncated {
		t.Errorf("props = %+v, want a folder of 8 bytes with 3 files and 1 folder", p)
	}
}

func TestPropsOfABigFolderStopsAtTheLimit(t *testing.T) {
	old := walkLimit
	walkLimit = 3
	t.Cleanup(func() { walkLimit = old })

	dir := t.TempDir()
	tree := filepath.Join(dir, "big")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		write(t, filepath.Join(tree, name), "x")
	}

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpProps, Path: tree})
	if !r.OK || r.Props == nil || !r.Props.Truncated || r.Props.Files != 3 {
		t.Errorf("props = %+v, want a partial count of 3 files, marked truncated", r.Props)
	}

	// Exactly at the limit is complete, not truncated.
	walkLimit = 5
	r = do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpProps, Path: tree})
	if r.Props == nil || r.Props.Truncated || r.Props.Files != 5 {
		t.Errorf("props at the limit = %+v, want all 5 files, not truncated", r.Props)
	}
}

func TestPropsOfALinkDescribesTheLinkAndDoesNotWalkIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(target, "inside"), "x")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpProps, Path: link})
	if !r.OK || r.Props == nil || !r.Props.IsLink || r.Props.IsDir || r.Props.LinkTarget != target || r.Props.Files != 0 {
		t.Errorf("props of a link = %+v, want the link itself pointing at %s", r.Props, target)
	}
}

func TestPropsErrors(t *testing.T) {
	dir := t.TempDir()
	if r := do(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpProps, Path: filepath.Join(dir, "missing")}); r.OK || r.Error == "" {
		t.Errorf("props of a missing path: %+v", r)
	}
	if r := do(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpProps, Path: "relative"}); r.OK || r.Error != "path must be absolute" {
		t.Errorf("props of a relative path: %+v", r)
	}
}

// ------------------------------------------- read-only sessions and reports

func TestReadOnlySessionRefusesCopyAndChmodButAllowsProps(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep")
	write(t, file, "data")

	for _, req := range []protocol.FileRequest{
		{ID: 1, Op: protocol.OpCopy, From: file, To: filepath.Join(dir, "copy")},
		{ID: 2, Op: protocol.OpChmod, Path: file, Mode: "0777"},
	} {
		resp, reports := doReporting(t, true, req)
		if resp.OK || resp.Error != "this session is read-only" || len(reports) != 0 {
			t.Errorf("%s in a read-only session: %+v with reports %+v, want a refusal and no report", req.Op, resp, reports)
		}
	}
	if exists(filepath.Join(dir, "copy")) {
		t.Error("a read-only session copied a file")
	}
	if r := do(t, true, protocol.FileRequest{ID: 3, Op: protocol.OpProps, Path: file}); !r.OK {
		t.Errorf("props in a read-only session: %+v, want it to work", r)
	}
}

func TestCopyAndChmodAreReported(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "a (1)")
	write(t, from, "x")

	_, reports := doReporting(t, false, protocol.FileRequest{ID: 1, Op: protocol.OpCopy, From: from, To: to})
	want := protocol.FileChange{Op: protocol.OpCopy, Path: from, To: to, OK: true}
	if len(reports) != 1 || reports[0] != want {
		t.Errorf("copy reports = %+v, want exactly %+v", reports, want)
	}

	// A failed copy (the destination exists now) is reported with its error.
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 2, Op: protocol.OpCopy, From: from, To: to})
	if len(reports) != 1 || reports[0].OK || reports[0].Error != "destination already exists" {
		t.Errorf("failed copy reports = %+v", reports)
	}

	// A chmod report carries the mode that was asked for, ok or not.
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 3, Op: protocol.OpChmod, Path: from, Mode: "0600"})
	if len(reports) != 1 || reports[0].Op != protocol.OpChmod || reports[0].Path != from || reports[0].Mode != "0600" {
		t.Errorf("chmod reports = %+v", reports)
	}
	_, reports = doReporting(t, false, protocol.FileRequest{ID: 4, Op: protocol.OpChmod, Path: from, Mode: "4755"})
	if len(reports) != 1 || reports[0].OK || reports[0].Mode != "4755" || reports[0].Error == "" {
		t.Errorf("refused chmod reports = %+v, want a failure that says what was asked", reports)
	}

	// Props is a read: nothing is reported.
	if _, reports := doReporting(t, false, protocol.FileRequest{ID: 5, Op: protocol.OpProps, Path: from}); len(reports) != 0 {
		t.Errorf("props reports = %+v, want none", reports)
	}
}
