package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// Version is the agent's own version, sent in hello.
const Version = "0.1.0"

const (
	heartbeatInterval = 20 * time.Second
	writeTimeout      = 10 * time.Second

	// How long one dial + handshake may take. Without our own limit, a server
	// whose packets are silently dropped (a firewall, a dead route) would hold
	// us in the operating system's connect timeout, about 30 s, and the retry
	// rhythm would be unpredictable.
	connectTimeout = 10 * time.Second

	// A connection that stayed up this long counts as healthy, so the next
	// retry starts again from 1 s. One that dies within seconds (the server
	// rejects us, for example) keeps the delay growing.
	stableAfter = 30 * time.Second
)

// Run keeps the agent connected until ctx is cancelled: whenever the control
// connection ends for any reason, it waits a little and connects again
// (architecture.md §3.1). It only returns on ctx cancellation or an error
// that retrying cannot fix.
func Run(ctx context.Context, cfg Config) error {
	// Fail fast: if this host cannot be inspected at all (an unsupported OS),
	// retrying forever would only fill the log.
	if _, err := readHostInfo(); err != nil {
		return fmt.Errorf("read host info: %w", err)
	}

	attempt := 0
	for {
		slog.Info("connecting", "server", cfg.Server)

		// upFor is how long we were actually connected. It stays 0 when we never
		// got connected: the time spent dialing must not count as "healthy",
		// or a dial that hangs for 30 s would reset the backoff.
		var upFor time.Duration
		conn, err := connect(ctx, cfg)
		if err == nil {
			slog.Info("connected", "agent_id", cfg.AgentID)
			connectedAt := time.Now()
			err = heartbeatLoop(ctx, conn)
			upFor = time.Since(connectedAt)
		}
		if ctx.Err() != nil {
			return ctx.Err() // we were asked to stop (Ctrl+C)
		}

		attempt = nextAttempt(attempt, upFor)
		delay := backoffDelay(attempt)
		attempt++
		slog.Warn("connection lost, will retry", "err", err, "retry_in", delay.Round(10*time.Millisecond))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// nextAttempt decides where the backoff sequence continues: a connection that
// stayed up for stableAfter or longer starts it over, anything shorter
// (including a failed dial, upFor = 0) keeps it growing.
func nextAttempt(attempt int, upFor time.Duration) int {
	if upFor >= stableAfter {
		return 0
	}
	return attempt
}

// backoffDelay is how long to wait before retry number attempt (0, 1, 2, ...):
// 1 s, 2 s, 4 s, ... capped at 60 s, plus up to 50% random jitter. The jitter
// stops hundreds of agents from reconnecting at the same moment after a
// server restart.
func backoffDelay(attempt int) time.Duration {
	delay := time.Minute
	if attempt < 6 { // 1<<6 s = 64 s, already above the cap
		delay = time.Second << attempt
	}
	// math/rand is fine here: jitter only has to spread agents out, nobody
	// gains anything by predicting it (unlike secrets, see server/enroll.go).
	return delay + time.Duration(rand.Int64N(int64(delay/2)))
}

// connect dials the server and sends hello. On success the caller owns the
// open connection and must close it.
func connect(ctx context.Context, cfg Config) (*websocket.Conn, error) {
	hello, err := readHostInfo() // read again each time: the IP addresses may have changed
	if err != nil {
		return nil, fmt.Errorf("read host info: %w", err)
	}

	// The WebSocket library wants ws:// or wss://. Trimming "http" turns
	// http://x into ws://x and https://x into wss://x.
	url := "ws" + strings.TrimPrefix(cfg.Server, "http") + "/agent/connect"

	// This deadline only covers dialing and the handshake, not the connection
	// that comes out of it.
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.AgentID + "." + cfg.AgentSecret}},
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", url, err)
	}

	if err := send(ctx, conn, protocol.TypeHello, hello); err != nil {
		conn.CloseNow()
		return nil, err
	}
	return conn, nil
}

// heartbeatLoop sends a heartbeat every 20 seconds until the connection ends,
// then closes it.
func heartbeatLoop(ctx context.Context, conn *websocket.Conn) error {
	defer conn.CloseNow()

	// A WebSocket connection must be read, or the library never sees the
	// server's close frame. Nothing the server says matters yet, so CloseRead
	// reads and discards it. It also cancels the returned context when the
	// connection closes, which is how we notice a dropped connection. M3 will
	// replace this with a real read loop (the server's `open` messages).
	connCtx := conn.CloseRead(ctx)

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	// The zero value makes the first heartbeat report the CPU average since boot.
	var prevCPU cpuTimes
	for {
		hb, cur, err := readHeartbeat(prevCPU)
		if err != nil {
			return fmt.Errorf("read system info: %w", err)
		}
		prevCPU = cur
		if err := send(connCtx, conn, protocol.TypeHeartbeat, hb); err != nil {
			return err
		}

		select {
		case <-connCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("server closed the connection")
		case <-ticker.C:
		}
	}
}

// send encodes one message and writes it as a text frame, with a deadline so
// a stuck network cannot block the agent forever.
func send(ctx context.Context, conn *websocket.Conn, msgType string, data any) error {
	msg, err := protocol.NewMessage(msgType, data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("send %s: %w", msgType, err)
	}
	return nil
}
