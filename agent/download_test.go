package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// startDownload serves runDownload for one path on a WebSocket and returns the
// other end, which is where the server would be.
func startDownload(t *testing.T, path string) (*websocket.Conn, context.Context) {
	t.Helper()
	params, err := json.Marshal(protocol.DownloadParams{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		runDownload(r.Context(), conn, params)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	// The server reads agent frames with a big limit (relayReadLimit); the
	// library's default (32 KB) is smaller than one 64 KB chunk.
	conn.SetReadLimit(1 << 20)
	return conn, ctx
}

func TestDownloadSendsTheFile(t *testing.T) {
	// Bigger than one chunk, and not a multiple of it, so there are several
	// frames and the last one is short.
	content := bytes.Repeat([]byte("0123456789abcdef"), 20000) // 320000 bytes
	content = append(content, "tail"...)
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	conn, ctx := startDownload(t, path)

	kind, data, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("first frame: kind %v, err %v, want a text header", kind, err)
	}
	var head protocol.DownloadHeader
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatal(err)
	}
	if head.Name != "data.bin" || head.Size != int64(len(content)) {
		t.Errorf("header = %+v, want data.bin and %d bytes", head, len(content))
	}

	var got bytes.Buffer
	frames := 0
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("download ended with %v, want a normal close", err)
			}
			break
		}
		if kind != websocket.MessageBinary {
			t.Fatalf("got a %v frame, want only binary frames after the header", kind)
		}
		frames++
		got.Write(data)
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Errorf("received %d bytes that differ from the file's %d", got.Len(), len(content))
	}
	if frames < 5 {
		t.Errorf("the file arrived in %d frames, want several (chunks of %d bytes)", frames, downloadChunk)
	}
}

func TestDownloadOfAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	conn, ctx := startDownload(t, path)

	_, data, err := conn.Read(ctx)
	var head protocol.DownloadHeader
	if err != nil || json.Unmarshal(data, &head) != nil || head.Size != 0 || head.Name != "empty" {
		t.Fatalf("header = %s, err %v, want a header for an empty file", data, err)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("after the header: %v, want a normal close", err)
	}
}

// A file that cannot be sent gets no header: the agent closes with the reason,
// which is what lets the server answer the browser with a proper error.
func TestDownloadRefusalsCloseWithTheReason(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, path, reason string
	}{
		{"a missing file", filepath.Join(dir, "missing"), ""}, // the wording is the operating system's
		// (A directory is not refused any more: it is sent as a zip, see zip_test.go.)
		{"a relative path", "some/file", "path must be absolute"},
	}
	for _, c := range cases {
		conn, ctx := startDownload(t, c.path)
		_, _, err := conn.Read(ctx)
		var closed websocket.CloseError
		if !errors.As(err, &closed) || closed.Code != websocket.StatusInternalError || closed.Reason == "" {
			t.Errorf("%s: got %v, want a close with code 1011 and a reason", c.name, err)
			continue
		}
		if c.reason != "" && closed.Reason != c.reason {
			t.Errorf("%s: reason = %q, want %q", c.name, closed.Reason, c.reason)
		}
	}
}
