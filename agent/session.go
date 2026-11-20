package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"unicode/utf8"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// runSession serves one session the server asked for with `open`
// (architecture.md §3.2). It runs on its own goroutine and only logs: nobody
// waits for its result, the browser learns what happened from the WebSocket.
//
// control is the agent's control connection. Most sessions never touch it:
// their bytes go over the session's own WebSocket. A files session uses it to
// report the changes it makes (D-37).
func runSession(ctx context.Context, cfg Config, control *websocket.Conn, open protocol.Open) {
	log := slog.With("session", open.SessionID, "kind", open.Kind)
	log.Info("session requested")
	if err := serveSession(ctx, cfg, control, open); err != nil {
		log.Warn("session ended with an error", "err", err)
		return
	}
	log.Info("session ended")
}

// changeReporter returns the function a files session calls for every change.
// It adds the session id, so the server can find the person the session was
// opened for, and sends the report on the control connection, the same way
// runFetch sends its result. Writes on a WebSocket are safe from several
// goroutines, so this shares the connection with the heartbeats.
//
// A report that cannot be sent is logged and dropped: losing an audit line is
// bad, but breaking the person's file manager over it would be worse.
func changeReporter(ctx context.Context, control *websocket.Conn, sessionID string) func(protocol.FileChange) {
	return func(change protocol.FileChange) {
		change.SessionID = sessionID
		if err := send(ctx, control, protocol.TypeFileChange, change); err != nil {
			slog.Warn("could not report a file change", "session", sessionID, "op", change.Op, "err", err)
		}
	}
}

func serveSession(ctx context.Context, cfg Config, control *websocket.Conn, open protocol.Open) error {
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, wsURL(cfg.Server, "/agent/sessions/"+url.PathEscape(open.SessionID)),
		&websocket.DialOptions{HTTPHeader: authHeader(cfg)})
	if err != nil {
		return fmt.Errorf("connect the session: %w", err)
	}
	defer conn.CloseNow()

	switch open.Kind {
	case protocol.KindShell:
		return runShell(ctx, conn, open.Params)
	case protocol.KindFiles:
		return runFiles(ctx, conn, open.Params, changeReporter(ctx, control, open.SessionID))
	case protocol.KindDownload:
		return runDownload(ctx, conn, open.Params)
	case protocol.KindTunnel:
		return runTunnel(ctx, conn, open.Params)
	default:
		// Even a session that cannot be served still dials and then closes
		// with the reason, so the browser shows a clear message instead of
		// waiting for the 15-second timeout (protocol.md §3).
		conn.Close(websocket.StatusInternalError, "unsupported session kind")
		return fmt.Errorf("unsupported session kind %q", open.Kind)
	}
}

// pickShell returns the first candidate that is an absolute path and exists,
// or "" if none does. The order of the candidates is the order of preference
// (protocol.md §4: $SHELL, then /bin/bash, then /bin/sh). A relative or empty
// $SHELL is skipped on purpose: it would be looked up in the current
// directory, and the agent never wants that.
func pickShell(candidates []string, exists func(string) bool) string {
	for _, candidate := range candidates {
		// path, not path/filepath: these are Linux paths whatever machine
		// this code is compiled on.
		if path.IsAbs(candidate) && exists(candidate) {
			return candidate
		}
	}
	return ""
}

// closeReason fits an error message into the 123 bytes a WebSocket close frame
// can carry, without cutting a character in half. The browser shows it.
func closeReason(message string) string {
	for len(message) > 120 {
		_, size := utf8.DecodeLastRuneInString(message)
		message = message[:len(message)-size]
	}
	return message
}
