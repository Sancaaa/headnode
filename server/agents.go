package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

const (
	// The agent must introduce itself quickly, or it is not one of ours.
	helloTimeout = 10 * time.Second
	// protocol.md §3: no message at all for this long means the agent is dead.
	// It is the only liveness check, so there is no separate ping/pong.
	deadAfter = 60 * time.Second
)

// onlineAgent is one live control connection.
type onlineAgent struct {
	conn      *websocket.Conn
	heartbeat *protocol.Heartbeat // nil until the first heartbeat arrives
}

// registry is the in-memory "who is online" map (architecture.md §3.1).
// Online status is deliberately not in the database: it is simply whether an
// entry exists here. A Mutex + map is all we need (principles.md §2).
type registry struct {
	mu     sync.Mutex
	online map[string]*onlineAgent
}

func newRegistry() *registry {
	return &registry{online: make(map[string]*onlineAgent)}
}

// add registers a connection and returns the one it replaced, if any (D-17).
func (r *registry) add(id string, a *onlineAgent) (replaced *onlineAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replaced = r.online[id]
	r.online[id] = a
	return replaced
}

// remove deletes the entry only if it still belongs to a. After a replace,
// the old connection's cleanup runs late and must not remove the new entry.
func (r *registry) remove(id string, a *onlineAgent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.online[id] == a {
		delete(r.online, id)
	}
}

var errAgentOffline = errors.New("agent is offline")

// send writes one control message to an online agent. Writes on a WebSocket
// connection are safe from several goroutines, so a request handler can use the
// connection that handleAgentConnect is reading from.
func (r *registry) send(id, msgType string, data any) error {
	r.mu.Lock()
	a := r.online[id]
	r.mu.Unlock()
	if a == nil {
		return errAgentOffline
	}

	msg, err := protocol.NewMessage(msgType, data)
	if err != nil {
		return err
	}
	// If this takes longer than a few seconds the connection is dead. The
	// library closes it when the deadline passes, and the agent reconnects.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.conn.Write(ctx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("send %s to agent: %w", msgType, err)
	}
	return nil
}

// disconnect closes an agent's control connection, if it has one, and forgets
// it. Used when a host is deleted: the agent must drop off at once instead of
// staying "online" until it next fails to authenticate.
func (r *registry) disconnect(id string) {
	r.mu.Lock()
	a := r.online[id]
	delete(r.online, id)
	r.mu.Unlock()

	if a != nil {
		a.conn.CloseNow() // outside the lock: closing can take a moment
	}
}

func (r *registry) setHeartbeat(a *onlineAgent, hb protocol.Heartbeat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a.heartbeat = &hb
}

// status reports whether an agent is online and its latest heartbeat.
func (r *registry) status(id string) (online bool, hb *protocol.Heartbeat) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.online[id]
	if !ok {
		return false, nil
	}
	if a.heartbeat != nil {
		copied := *a.heartbeat // hand out a copy so callers cannot race with setHeartbeat
		// The VNC password never leaves through here: this heartbeat goes into the
		// host list, which viewers see too (D-64). vncPassword is the one way to it.
		copied.VNCPassword = ""
		hb = &copied
	}
	return true, hb
}

// vncPassword is the password of the host's VNC server, from its latest heartbeat,
// or "" if it has none or is offline. Only handleOpenSession uses it, after checking
// that the user may open a tunnel to the host (D-64).
func (r *registry) vncPassword(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.online[id]
	if !ok || a.heartbeat == nil {
		return ""
	}
	return a.heartbeat.VNCPassword
}

// authenticateAgent checks "Authorization: Bearer <agent_id>.<agent_secret>"
// and returns the agent id.
func (s *Server) authenticateAgent(r *http.Request) (agentID string, ok bool) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		return "", false
	}
	id, secret, found := strings.Cut(token, ".")
	if !found {
		return "", false
	}

	var storedHash string
	err := s.db.QueryRow(`SELECT secret_hash FROM agents WHERE id = ?`, id).Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		slog.Error("look up agent", "err", err)
		return "", false
	}

	// ConstantTimeCompare takes the same time wherever the first difference is,
	// so response timing leaks nothing about the stored hash (D-09).
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(storedHash)) != 1 {
		return "", false
	}
	return id, true
}

// handleAgentConnect is the control connection (protocol.md §3):
// authenticate, read hello, then read heartbeats until the agent goes away.
func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.authenticateAgent(r)
	if !ok {
		// Rejected before the upgrade, so this is a plain HTTP 401.
		writeError(w, http.StatusUnauthorized, "invalid agent credentials")
		return
	}

	// Accept refuses a browser-style cross-origin request (the Origin check the
	// project requires). Agents are not browsers and send no Origin header.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("agent websocket handshake failed", "agent_id", agentID, "err", err) // Accept already replied
		return
	}
	defer conn.CloseNow()

	helloCtx, cancel := context.WithTimeout(r.Context(), helloTimeout)
	_, data, err := conn.Read(helloCtx)
	cancel()
	if err != nil {
		slog.Warn("agent sent no hello", "agent_id", agentID, "err", err)
		return
	}
	hello, err := parseHello(data)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "first message must be a valid hello")
		return
	}
	if hello.Protocol != protocol.Version {
		conn.Close(websocket.StatusPolicyViolation, "protocol mismatch")
		return
	}

	// Store our own re-encoding of the decoded struct, not the raw bytes, so
	// only known fields end up in the database.
	infoJSON, err := json.Marshal(hello)
	if err != nil {
		slog.Error("encode hello", "agent_id", agentID, "err", err)
		conn.Close(websocket.StatusInternalError, "server error")
		return
	}
	// wan_addr is how the agent looks from outside. The agent cannot know that
	// (it may be behind NAT), and nothing it says about itself could be trusted
	// for it, so it is the server's own view of the connection. clientIP is the
	// same function login uses, so X-Forwarded-For is believed only when the
	// request really came from our proxy (D-29).
	_, err = s.db.Exec(
		`UPDATE agents SET hostname = ?, os = ?, arch = ?, version = ?, info_json = ?, wan_addr = ?, last_seen_at = ? WHERE id = ?`,
		hello.Hostname, hello.OS, hello.Arch, hello.AgentVersion, string(infoJSON), clientIP(r), time.Now().Unix(), agentID,
	)
	if err != nil {
		slog.Error("store hello", "agent_id", agentID, "err", err)
		conn.Close(websocket.StatusInternalError, "server error")
		return
	}

	me := &onlineAgent{conn: conn}
	if old := s.agents.add(agentID, me); old != nil {
		// The agent reconnected before we noticed its old connection was dead.
		// CloseNow, not Close: the old connection is probably half-dead and
		// Close would wait for a reply that never comes.
		old.conn.CloseNow()
	}
	slog.Info("agent online", "agent_id", agentID, "hostname", hello.Hostname)
	defer func() {
		s.agents.remove(agentID, me)
		s.touch(agentID) // last_seen_at = the moment it went offline
		slog.Info("agent offline", "agent_id", agentID)

		// A host that is gone cannot report how its file transfers went. But if
		// it only reconnected (this connection was replaced, D-17), it is still
		// online, and its transfers may well finish.
		if online, _ := s.agents.status(agentID); !online {
			s.transfers.failPending(agentID, "the host disconnected before it reported a result")
		}
	}()

	for {
		readCtx, cancel := context.WithTimeout(r.Context(), deadAfter)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			slog.Info("agent connection ended", "agent_id", agentID, "reason", err)
			return
		}
		s.handleControlMessage(agentID, me, data)
	}
}

func parseHello(data []byte) (protocol.Hello, error) {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return protocol.Hello{}, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type != protocol.TypeHello {
		return protocol.Hello{}, fmt.Errorf("first message has type %q, want hello", env.Type)
	}
	var hello protocol.Hello
	if err := json.Unmarshal(env.Data, &hello); err != nil {
		return protocol.Hello{}, fmt.Errorf("decode hello: %w", err)
	}
	return hello, nil
}

func (s *Server) handleControlMessage(agentID string, me *onlineAgent, data []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		slog.Warn("bad control message", "agent_id", agentID, "err", err)
		return
	}
	switch env.Type {
	case protocol.TypeHeartbeat:
		var hb protocol.Heartbeat
		if err := json.Unmarshal(env.Data, &hb); err != nil {
			slog.Warn("bad heartbeat", "agent_id", agentID, "err", err)
			return
		}
		s.agents.setHeartbeat(me, hb)
		s.touch(agentID)
	case protocol.TypeFetchResult:
		var res protocol.FetchResult
		if err := json.Unmarshal(env.Data, &res); err != nil {
			slog.Warn("bad fetch result", "agent_id", agentID, "err", err)
			return
		}
		s.finishTransfer(agentID, res)
	case protocol.TypeFileChange:
		var change protocol.FileChange
		if err := json.Unmarshal(env.Data, &change); err != nil {
			slog.Warn("bad file change report", "agent_id", agentID, "err", err)
			return
		}
		s.recordFileChange(agentID, change)
	case protocol.TypeDockerChange:
		var change protocol.DockerChange
		if err := json.Unmarshal(env.Data, &change); err != nil {
			slog.Warn("bad docker change report", "agent_id", agentID, "err", err)
			return
		}
		s.recordDockerChange(agentID, change)
	default:
		slog.Warn("unknown control message", "agent_id", agentID, "type", env.Type)
	}
}

// touch records "seen just now", so the dashboard can show "last online 3 hours ago".
func (s *Server) touch(agentID string) {
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at = ? WHERE id = ?`, time.Now().Unix(), agentID); err != nil {
		slog.Error("update last_seen_at", "agent_id", agentID, "err", err)
	}
}
