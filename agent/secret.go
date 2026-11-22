package agent

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// The agent's own credentials file (agent.json) is the one file on the host the
// file manager never reads, copies, edits, renames, deletes or chmods (D-62).
// Whoever has the agent's secret can connect as this host: an operator's
// terminal, file manager and file sends would then go to them. Blocking writes,
// copies, renames, deletes and permission changes as well stops a file manager
// session from exposing the secret indirectly (for instance by copying it to a
// new file, renaming its folder out from under secretFile, or loosening its
// permissions) or locking the host out.

// secretFile is the credentials file this agent was started with. Run sets it once,
// before any session can start, from Config.Path. Empty means unknown: then nothing
// is protected, which is only the case in tests.
var secretFile string

var errSecretFile = errors.New("this is headnode's own credentials file, which cannot be accessed")

// isSecretFile says whether info describes the credentials file. It compares the
// files themselves (os.SameFile: the same inode on Linux, the same file id on
// Windows), not their paths, so another name for the same file does not get past
// it: a link, a different case or a short name on Windows, a path through \\?\ or
// a network share of this machine.
func isSecretFile(info fs.FileInfo) bool {
	if secretFile == "" || info == nil {
		return false
	}
	secret, err := os.Stat(secretFile)
	return err == nil && os.SameFile(info, secret)
}

// isSecretOrAncestor reports whether info is the credentials file itself or a
// directory containing it at any depth. Renaming or recursively deleting such a
// directory would move or remove the credentials file along with it (and moving
// its parent directory would break os.Stat(secretFile) in isSecretFile).
func isSecretOrAncestor(info fs.FileInfo) bool {
	if secretFile == "" || info == nil {
		return false
	}
	if isSecretFile(info) {
		return true
	}
	if !info.IsDir() {
		return false
	}
	path := secretFile
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	for dir := filepath.Dir(path); ; {
		if anc, err := os.Stat(dir); err == nil && os.SameFile(info, anc) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
