//go:build !linux

package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/coder/websocket"
)

// errUnsupported is what the stubs for a system that is not implemented return.
var errUnsupported = errors.New("the agent does not support this system for this feature")

// Shells are only implemented for Linux (D-12): on Windows the host is managed
// through a VNC server reached by a tunnel (D-44), not a shell. This stub keeps
// the agent package compiling there; a session on such an agent fails with a
// message the browser shows.
func runShell(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage) error {
	conn.Close(websocket.StatusInternalError, "shell sessions are only supported on Linux")
	return errUnsupported
}
