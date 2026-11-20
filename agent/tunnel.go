package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// runTunnel connects the session's WebSocket to a TCP port on this machine and
// copies bytes both ways, until either side hangs up (protocol.md §4, `tunnel`).
// One TCP connection on the laptop is one tunnel session, so this serves
// exactly one connection.
//
// What it carries is whatever the program on the laptop speaks: for `ssh` it is
// the SSH protocol, which is encrypted end to end between ssh and sshd. The
// tunnel, the server and the agent only move opaque bytes.
func runTunnel(ctx context.Context, conn *websocket.Conn, rawParams json.RawMessage) error {
	var params protocol.TunnelParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		conn.Close(websocket.StatusInternalError, "bad session parameters")
		return err
	}

	// The server checked this already, and the agent checks again: it is the one
	// that dials, so it is the one that must never be talked into reaching
	// another machine (D-35).
	target, err := protocol.ParseTunnelTarget(params.Target)
	if err != nil {
		conn.Close(websocket.StatusInternalError, closeReason(err.Error()))
		return err
	}

	dialer := net.Dialer{Timeout: 10 * time.Second}
	tcp, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		// "connection refused" is what a person needs to hear: nothing listens there.
		conn.Close(websocket.StatusInternalError, closeReason(describe(err)))
		return err
	}
	defer tcp.Close()
	conn.SetReadLimit(1 << 20)
	slog.Info("tunnel opened", "target", target)

	// NetConn makes the WebSocket look like a TCP connection: reading gives the
	// bytes of the binary frames, writing sends them as frames. Then the two ends
	// are two net.Conn, and copying between them is io.Copy.
	ws := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	defer ws.Close() // sends a normal close

	done := make(chan struct{}, 2)
	go func() { // laptop to target
		io.Copy(tcp, ws)
		done <- struct{}{}
	}()
	go func() { // target to laptop
		io.Copy(ws, tcp)
		done <- struct{}{}
	}()

	// The first side to end ends the session: the laptop closed (the ssh client
	// exited) or the target closed (sshd hung up). Closing both ends unblocks
	// the other copy.
	<-done
	tcp.Close()
	ws.Close()
	<-done
	slog.Info("tunnel closed", "target", target)
	return nil
}
