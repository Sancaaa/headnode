package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// Version is the agent's own version, sent in hello and shown on the host's
// page. 0.2.0 added file transfers (`fetch`), 0.3.0 the file change reports
// (`file_change`), 0.4.0 the file manager's copy, chmod and props, and 0.5.0 the
// editor's read and write and folder downloads (zip), and 0.6.0 the unpacking of
// a folder sent to the host (`fetch` with extract), and 0.7.0 the Windows agent
// (system info and the Windows service), and 0.8.0 the "vnc" flag in the
// heartbeat (D-50) and starting and stopping the VNC server (D-51): an older agent ignores the command, stays silent or answers
// "unknown operation", so the version is how a person can tell whether a host is
// up to date.
const Version = "0.8.0"

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
			err = heartbeatLoop(ctx, cfg, conn)
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

// wsURL turns the server address from agent.json plus a path into a WebSocket
// URL. The WebSocket library wants ws:// or wss://. Trimming "http" turns
// http://x into ws://x and https://x into wss://x.
func wsURL(server, path string) string {
	return "ws" + strings.TrimPrefix(server, "http") + path
}

// authHeader is how the agent proves who it is on every request after enrolling
// (protocol.md §1): "Authorization: Bearer <agent_id>.<agent_secret>".
func authHeader(cfg Config) http.Header {
	return http.Header{"Authorization": {"Bearer " + cfg.AgentID + "." + cfg.AgentSecret}}
}

// connect dials the server and sends hello. On success the caller owns the
// open connection and must close it.
func connect(ctx context.Context, cfg Config) (*websocket.Conn, error) {
	hello, err := readHostInfo() // read again each time: the IP addresses may have changed
	if err != nil {
		return nil, fmt.Errorf("read host info: %w", err)
	}

	url := wsURL(cfg.Server, "/agent/connect")

	// This deadline only covers dialing and the handshake, not the connection
	// that comes out of it.
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, url, &websocket.DialOptions{HTTPHeader: authHeader(cfg)})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", url, err)
	}

	if err := send(ctx, conn, protocol.TypeHello, hello); err != nil {
		conn.CloseNow()
		return nil, err
	}
	return conn, nil
}

// heartbeatLoop sends a heartbeat every 20 seconds and listens for the server's
// commands, until the connection ends. Then it closes the connection.
//
// ctx is the agent's own lifetime. Sessions use it rather than the
// connection's context: a shell has its own WebSocket, so it should not die
// just because the control connection dropped for a moment.
func heartbeatLoop(ctx context.Context, cfg Config, conn *websocket.Conn) error {
	defer conn.CloseNow()

	// A WebSocket connection must be read, or the library never sees the
	// server's close frame, and the server's commands arrive on it. The reader
	// runs on its own goroutine and cancels connCtx when the connection ends,
	// which is how this loop notices a dropped connection.
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	readFailed := make(chan error, 1)
	// A command that changes something a heartbeat reports (the VNC server) nudges
	// this channel, and the loop below sends a heartbeat at once instead of in up
	// to 20 seconds. One slot is enough: a nudge that finds the slot full is
	// already as good as delivered.
	kick := make(chan struct{}, 1)
	nudge := func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	go func() {
		readFailed <- readCommands(connCtx, ctx, cfg, conn, nudge)
		cancel()
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	// The zero value makes the first heartbeat report the CPU average since boot.
	var prevCPU cpuTimes
	var vnc vncProbe
	for {
		hb, cur, err := readHeartbeat(prevCPU)
		if err != nil {
			return fmt.Errorf("read system info: %w", err)
		}
		prevCPU = cur
		hb.VNC = vnc.listening()
		hb.VNCControl = canControlVNC()
		if err := send(connCtx, conn, protocol.TypeHeartbeat, hb); err != nil {
			return err
		}

		select {
		case <-connCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("server closed the connection: %w", <-readFailed)
		case <-ticker.C:
		case <-kick:
			vnc.at = time.Time{} // look again now, the whole point is to see what just changed
		}
	}
}

// readCommands handles what the server sends on the control connection: `open`
// (start a session), `fetch` (download a file) and `vnc_set`. It returns when
// the connection ends. nudge asks the heartbeat loop for an immediate heartbeat.
func readCommands(connCtx, agentCtx context.Context, cfg Config, conn *websocket.Conn, nudge func()) error {
	for {
		_, data, err := conn.Read(connCtx)
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			slog.Warn("bad message from the server", "err", err)
			continue
		}
		switch env.Type {
		case protocol.TypeOpen:
			var open protocol.Open
			if err := json.Unmarshal(env.Data, &open); err != nil {
				slog.Warn("bad open message", "err", err)
				continue
			}
			// Each session runs on its own goroutine with its own WebSocket,
			// so a slow or stuck shell never holds up the control connection.
			go runSession(agentCtx, cfg, conn, open)
		case protocol.TypeFetch:
			var fetch protocol.Fetch
			if err := json.Unmarshal(env.Data, &fetch); err != nil {
				slog.Warn("bad fetch message", "err", err)
				continue
			}
			// A transfer can take minutes, so it gets its own goroutine like a
			// session does. It reports on this connection when it is done.
			go runFetch(agentCtx, cfg, conn, fetch)
			case protocol.TypeVNCSet:
				var set protocol.VNCSet
				if err := json.Unmarshal(env.Data, &set); err != nil {
					slog.Warn("bad vnc_set message", "err", err)
					continue
				}
				// Stopping a service can take seconds: not on the reading goroutine.
				go runVNCSet(set, nudge)
		default:
			slog.Warn("unknown message from the server", "type", env.Type)
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
