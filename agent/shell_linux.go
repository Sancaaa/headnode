package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"headnode/protocol"
)

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// runShell runs a shell inside a pseudo-terminal and connects it to the
// session's WebSocket (protocol.md §4).
//
// Why a PTY and not plain pipes: programs like vim and htop need a terminal.
// They ask it for its size, switch it to raw mode, and draw with cursor
// movements. A pipe cannot do any of that. The PTY is a pair of connected
// files: the shell reads and writes one end as if it were a screen and a
// keyboard, and we hold the other end.
func runShell(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage) error {
	var params protocol.ShellParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			conn.Close(websocket.StatusInternalError, "bad session parameters")
			return fmt.Errorf("decode shell parameters: %w", err)
		}
	}
	// The server already clamped these, but the agent does not trust anyone's numbers.
	cols, rows := protocol.ClampSize(params.Cols, params.Rows)

	// The library refuses messages over 32 KB by default, and pasting a long
	// text into the terminal arrives as one message. Same limit as the relay.
	conn.SetReadLimit(1 << 20)

	shell := pickShell([]string{os.Getenv("SHELL"), "/bin/bash", "/bin/sh"}, isExecutable)
	if shell == "" {
		conn.Close(websocket.StatusInternalError, "no shell found on this host")
		return errors.New("no shell found")
	}

	// The shell is started by itself with no arguments: never "sh -c <string>"
	// (CLAUDE.md). What the user types goes to the shell's input, nowhere else.
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color") // what xterm.js speaks
	if home := os.Getenv("HOME"); home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			cmd.Dir = home
		}
	}

	// StartWithSize also gives the shell its own session and controlling
	// terminal (Setsid, Setctty). That is what makes closing the PTY below hang
	// up everything started from the shell.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		conn.Close(websocket.StatusInternalError, closeReason("cannot start a shell: "+err.Error()))
		return fmt.Errorf("start %s: %w", shell, err)
	}
	slog.Info("shell started", "shell", shell, "pid", cmd.Process.Pid, "cols", cols, "rows", rows)

	// Only this goroutine waits for the shell, so the process is reaped as soon
	// as it exits. The exit status is not needed.
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	// Terminal output to the browser. When the shell exits, reading the PTY
	// fails: everything it printed has been sent by then, so this is the moment
	// to say goodbye. Closing here, with the read loop below still running, lets
	// the close handshake finish.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return // the connection is gone, the loop below ends too
				}
			}
			if err != nil {
				conn.Close(websocket.StatusNormalClosure, "shell exited")
				return
			}
		}
	}()

	// The browser's keystrokes (binary) and resizes (text) to the terminal.
	// This loop ends when the connection does: the tab was closed, or the
	// output goroutine above closed it after the shell exited.
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		switch kind {
		case websocket.MessageBinary:
			// A write error means the shell is gone, and the output goroutine
			// reports that, so it is not handled here.
			ptmx.Write(data)
		case websocket.MessageText:
			resizeFromText(ptmx, data)
		}
	}

	stopShell(cmd, ptmx, exited)
	<-outputDone
	return nil
}

// resizeFromText applies {"type":"resize","cols":..,"rows":..}. Any other text
// frame is ignored: unknown messages must not break a session.
func resizeFromText(ptmx *os.File, data []byte) {
	var msg protocol.Resize
	if err := json.Unmarshal(data, &msg); err != nil || msg.Type != protocol.TypeResize {
		return
	}
	cols, rows := protocol.ClampSize(msg.Cols, msg.Rows)
	// The kernel tells the shell and whatever runs in it (SIGWINCH), which is
	// why vim and htop redraw to the new size.
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		slog.Warn("resize the terminal", "err", err)
	}
}

// stopShell makes sure nothing started from the session keeps running after
// the browser left (roadmap M3: "closing the tab kills the shell process").
func stopShell(cmd *exec.Cmd, ptmx *os.File, exited <-chan struct{}) {
	// Closing our end of the PTY hangs up the terminal: the kernel sends SIGHUP
	// to what runs in it, and an interactive shell exits on that.
	ptmx.Close()
	if waitFor(exited, 500*time.Millisecond) {
		return
	}

	// Something ignored the hangup. The shell has its own process group, whose
	// id is the shell's pid, and a negative pid means "the whole group": the
	// shell and everything it started in the foreground, vim and htop included.
	pgid := cmd.Process.Pid
	syscall.Kill(-pgid, syscall.SIGHUP)
	if waitFor(exited, 2*time.Second) {
		return
	}
	slog.Warn("shell ignored the hangup, killing it", "pid", pgid)
	syscall.Kill(-pgid, syscall.SIGKILL)
	if !waitFor(exited, 5*time.Second) {
		slog.Error("shell did not exit after SIGKILL", "pid", pgid)
	}
}

// waitFor reports whether done was closed within the time.
func waitFor(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
