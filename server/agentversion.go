package server

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"headnode/protocol"
)

// Which agents are behind (M10, D-66). The build scripts write the version of the
// agents they build to VERSION, next to the programs. The server compares it with
// the version each agent sent in its hello, and marks the older ones, so the
// dashboard can say "update available". It only says it: the update is run on each
// machine (`headnode update`, the tray icon), and that command reads the same file
// from /download/VERSION (D-67).

// latestAgentVersion is the version in the agents folder's VERSION file, or "" if
// there is none (no agents folder, no file, or not a version): then nothing is
// marked outdated, which is not an error. It is read every time it is needed, not
// once at start, because the programs can be rebuilt without restarting the server
// (D-40) and this must follow them.
func (s *Server) latestAgentVersion() string {
	if s.agentDir == "" {
		return ""
	}
	file, err := os.Open(filepath.Join(s.agentDir, "VERSION"))
	if err != nil {
		return ""
	}
	defer file.Close()
	text, err := io.ReadAll(io.LimitReader(file, 64)) // a version is a few characters
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(text))
	if _, ok := protocol.ParseVersion(version); !ok {
		return ""
	}
	return version
}
