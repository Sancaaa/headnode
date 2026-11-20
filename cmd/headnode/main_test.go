package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The data directory holds password hashes, agent secret hashes and uploaded
// files, so a directory the server creates is private to its owner. (An
// existing directory keeps its mode: MkdirAll never changes it.)
func TestNewDataDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "data")
	db := openDB(filepath.Join(dir, "headnode.db"))
	defer db.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("data directory mode = %o, want 700", got)
	}
}
