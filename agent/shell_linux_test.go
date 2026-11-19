package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// These tests need a real Linux with /bin/sh, so they only build there. On the
// Windows laptop they are skipped by the _linux file name. To run them, use
// WSL: `go test ./agent` inside the Linux distribution.

// startTestShell serves runShell on a WebSocket, like the agent does in a
// session, and returns the other end of it, like the relay would.
func startTestShell(t *testing.T) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		runShell(r.Context(), conn, json.RawMessage(`{"cols":80,"rows":24}`))
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func writeToShell(t *testing.T, conn *websocket.Conn, kind websocket.MessageType, data string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, kind, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestShellRunsCommandsAndResizes(t *testing.T) {
	conn := startTestShell(t)

	// A resize before the command: the shell must see the new size.
	writeToShell(t, conn, websocket.MessageText, `{"type":"resize","cols":100,"rows":30}`)
	// "42" only appears in the output: the typed command line has $((6*7)) in it.
	writeToShell(t, conn, websocket.MessageBinary, "stty size; echo $((6*7))\nexit\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var output strings.Builder
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// The shell exited, so the agent closes the session normally.
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("session ended with %v, want a normal close after the shell exited", err)
			}
			break
		}
		output.Write(data)
	}
	if !strings.Contains(output.String(), "30 100") {
		t.Errorf("stty size did not show the resized terminal (30 rows, 100 columns), output:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "42") {
		t.Errorf("the command's output is missing, output:\n%s", output.String())
	}
}

// The roadmap's criterion: closing the tab kills the shell process on the host.
func TestShellDiesWhenTheConnectionCloses(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	conn := startTestShell(t)

	// The shell writes its own pid, then runs something long, like a user's vim.
	writeToShell(t, conn, websocket.MessageBinary, "echo $$ > '"+pidFile+"'\nsleep 60\n")

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				pid = n
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the shell never wrote its pid")
	}

	// Close abruptly, like a closed browser tab, without a goodbye.
	conn.CloseNow()

	// Signal 0 only asks "does this process exist?". ESRCH means it is gone.
	for time.Now().Before(deadline.Add(5 * time.Second)) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the shell (pid %d) is still running after its connection closed", pid)
}
