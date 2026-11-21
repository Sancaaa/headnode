package agent

import (
	"errors"
	"io/fs"
	"os"
)

// The agent's own credentials file (agent.json) is the one file on the host the
// file manager never hands out (D-62). Whoever has the agent's secret can connect
// as this host: an operator's terminal, file manager and file sends would then go
// to them. A viewer may read every other file the agent's account can read, but not
// this one, or read-only access could become that.

// secretFile is the credentials file this agent was started with. Run sets it once,
// before any session can start, from Config.Path. Empty means unknown: then nothing
// is protected, which is only the case in tests.
var secretFile string

var errSecretFile = errors.New("this is headnode's own credentials file, which is never sent")

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
