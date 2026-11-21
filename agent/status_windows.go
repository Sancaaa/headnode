//go:build windows

package agent

import (
	"os"
	"path/filepath"
	"time"
)

// statusPath is the one line that tells the tray icon whether the agent is connected
// (tray_logic.go). It is in its own folder under ProgramData, not in the agent's, because
// the agent's folder is private to the system and administrators (it holds the
// credentials) and this line, which holds nothing secret, must be readable by the person
// at the machine. A new folder in ProgramData is readable by every user by default.
func statusPath() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "headnode-status", "agent.status")
}

// writeStatus records whether the agent is connected. Failing to write it only costs
// the icon its colour, so errors are not worth stopping the agent for.
func writeStatus(connected bool) {
	path := statusPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	os.WriteFile(path, []byte(statusLine(connected, time.Now())), 0o644)
}
