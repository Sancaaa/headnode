package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// startTunnel serves runTunnel for one target on a WebSocket and returns the
// other end, which is where the server (and the laptop behind it) would be.
func startTunnel(t *testing.T, target string) (*websocket.Conn, context.Context) {
	t.Helper()
	params, err := json.Marshal(protocol.TunnelParams{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		runTunnel(r.Context(), conn, params)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn, ctx
}

// listen starts a TCP server on a free loopback port and hands each accepted
// connection to handle.
func listen(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return ln.Addr().String()
}

func TestTunnelCarriesBytesBothWays(t *testing.T) {
	// The target is an echo server: whatever arrives is sent back.
	closed := make(chan struct{})
	target := listen(t, func(c net.Conn) {
		defer close(closed)
		defer c.Close()
		io.Copy(c, c)
	})

	conn, ctx := startTunnel(t, target)
	laptop := websocket.NetConn(ctx, conn, websocket.MessageBinary)

	// Several messages, one bigger than a single frame would be, to check that
	// the bytes arrive complete and in order.
	big := strings.Repeat("0123456789", 20000) // 200000 bytes
	for _, message := range []string{"hello", "second message", big} {
		go laptop.Write([]byte(message)) // a writer apart from the reader: the echo may fill the pipe
		got := make([]byte, len(message))
		if _, err := io.ReadFull(laptop, got); err != nil {
			t.Fatalf("reading the echo of a %d byte message: %v", len(message), err)
		}
		if string(got) != message {
			t.Fatalf("the echo of a %d byte message differs from what was sent", len(message))
		}
	}

	// The laptop hangs up: the agent closes its connection to the target.
	laptop.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Error("the connection to the target was not closed after the laptop hung up")
	}
}

func TestTunnelEndsWhenTheTargetHangsUp(t *testing.T) {
	// The target says goodbye and closes, like sshd after the user typed exit.
	target := listen(t, func(c net.Conn) {
		defer c.Close()
		io.WriteString(c, "bye")
	})

	conn, ctx := startTunnel(t, target)
	laptop := websocket.NetConn(ctx, conn, websocket.MessageBinary)

	// The last words arrive, then the end of the connection (a normal close
	// reads as EOF), not an error and not a hang.
	got, err := io.ReadAll(laptop)
	if err != nil || string(got) != "bye" {
		t.Errorf("read %q, %v, want bye and a clean end", got, err)
	}
}

// Anything that is not a port on this machine is refused before a single
// connection is made, with the reason as the close message.
func TestTunnelRefusesOtherMachines(t *testing.T) {
	for _, target := range []string{"10.0.0.5:22", "192.168.1.1:80", "example.com:443", "169.254.169.254:80", "127.0.0.1", "127.0.0.1:0"} {
		conn, ctx := startTunnel(t, target)
		_, _, err := conn.Read(ctx)
		var closed websocket.CloseError
		if !errors.As(err, &closed) || closed.Code != websocket.StatusInternalError || closed.Reason == "" {
			t.Errorf("target %q: got %v, want a close with code 1011 and a reason", target, err)
		}
	}
}

func TestTunnelToAPortNobodyListensOn(t *testing.T) {
	// A port that was free a moment ago, so nothing listens there now.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	target := ln.Addr().String()
	ln.Close()

	conn, ctx := startTunnel(t, target)
	_, _, err = conn.Read(ctx)
	var closed websocket.CloseError
	if !errors.As(err, &closed) || closed.Code != websocket.StatusInternalError || closed.Reason == "" {
		t.Errorf("got %v, want a close with code 1011 and the reason the connection failed", err)
	}
}

func TestDialTunnelTargetUsesUnixSocketForVNC(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wayvnc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix domain sockets unavailable here: %v", err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.WriteString(c, "RFB 003.008\n")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialTunnelTarget(ctx, vncAddress, sock)
	if err != nil {
		t.Fatalf("dialTunnelTarget(vncAddress, sock): %v", err)
	}
	defer conn.Close()
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "RFB 003.008\n" {
		t.Errorf("read %q (%v), want RFB banner from Unix socket", got, err)
	}

	// Non-VNC targets must NOT be routed to the VNC Unix socket.
	tcpTarget := listen(t, func(c net.Conn) {
		defer c.Close()
		io.WriteString(c, "SSH-2.0\n")
	})
	tcpConn, err := dialTunnelTarget(ctx, tcpTarget, sock)
	if err != nil {
		t.Fatalf("dialTunnelTarget(tcpTarget, sock): %v", err)
	}
	defer tcpConn.Close()
	got, err = io.ReadAll(tcpConn)
	if err != nil || string(got) != "SSH-2.0\n" {
		t.Errorf("non-VNC target read %q (%v), want TCP banner", got, err)
	}
}
