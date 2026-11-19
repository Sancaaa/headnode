//go:build !linux

package agent

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
)

// Like the system info (sysinfo_other.go), shells are only implemented for
// Linux (D-12). This stub keeps the agent package compiling on the Windows
// laptop; a session on such an agent fails with a message the browser shows.
func runShell(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage) error {
	conn.Close(websocket.StatusInternalError, "shell sessions are only supported on Linux")
	return errUnsupported
}
