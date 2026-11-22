package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// protocol.md §4: if the other side of a session has not arrived this long
// after the session was created, the session is cancelled.
const sessionPairTimeout = 15 * time.Second

// browserTunnelPort is the one port a browser may tunnel to on a host: the
// standard VNC port, for the dashboard's screen viewer (D-49).
const browserTunnelPort = "5900"

// session is one shell (later: file view, tunnel) that has been asked for but
// whose two WebSockets may not both have arrived yet.
//
// The browser's handler does the relaying. The agent's handler only hands over
// its connection and waits, so exactly one goroutine ever copies bytes.
type session struct {
	id           string
	userID       int64  // the one user who may attach a browser
	agentID      string // the one agent (host) that may attach
	kind         string
	need         action // permission required to attach and to keep the session open
	webTokenHash string // hash of the login token that created this session
	opened       time.Time

	agentConn chan *websocket.Conn // the agent's side, handed to the browser's handler
	done      chan struct{}        // closed when the session is over, whichever way
	endOnce   sync.Once

	mu          sync.Mutex
	browserSeen bool // each side may attach once: an id is single-use
	agentSeen   bool
	paired      bool // both sides arrived, the relay is running
	expired     bool // nobody paired it in time
}

// finishedKeepFor is how long the owner of an ended files or docker session is
// remembered. These sessions report their changes on the agent's control
// connection, a different TCP connection from the one the relay uses, so the
// last report can arrive a moment after the browser left and the session was
// removed. A few minutes is far more than that race needs, and short enough that
// the map stays tiny.
const finishedKeepFor = 5 * time.Minute

// reportsChanges says whether sessions of this kind report their changes to the
// server (file_change, docker_change), so the server must remember who they were
// opened for. No other kind reports anything.
func reportsChanges(kind string) bool {
	return kind == protocol.KindFiles || kind == protocol.KindDocker
}

// finishedSession is all that is kept of an ended files or docker session: who it
// was for, which host it was on and what kind it was, so a late change report is
// still attributed.
type finishedSession struct {
	userID  int64
	agentID string
	kind    string
	at      time.Time // when it ended
}

// sessionTable is every session that is waiting or running.
type sessionTable struct {
	mu          sync.Mutex
	byID        map[string]*session
	finished    map[string]finishedSession // files and docker sessions only, for finishedKeepFor
	pairTimeout time.Duration              // a field so tests can use a short one
	keepFor     time.Duration              // a field so tests can use a short one
}

func newSessionTable(pairTimeout time.Duration) *sessionTable {
	return &sessionTable{
		byID:        make(map[string]*session),
		finished:    make(map[string]finishedSession),
		pairTimeout: pairTimeout,
		keepFor:     finishedKeepFor,
	}
}

// create makes a session and starts its countdown.
func (t *sessionTable) create(userID int64, agentID, kind string, need action, webTokenHash string) (*session, error) {
	// 128 random bits: nobody can guess a session id (architecture.md §3.2).
	suffix, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	sess := &session{
		id:           "s_" + suffix,
		userID:       userID,
		agentID:      agentID,
		kind:         kind,
		need:         need,
		webTokenHash: webTokenHash,
		opened:       time.Now(),
		agentConn:    make(chan *websocket.Conn, 1),
		done:         make(chan struct{}),
	}
	t.mu.Lock()
	t.byID[sess.id] = sess
	timeout := t.pairTimeout
	t.mu.Unlock()

	time.AfterFunc(timeout, func() {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		if !sess.paired {
			sess.expired = true
			t.end(sess)
		}
	})
	return sess, nil
}

// endWhere ends every waiting or running session matching match, and returns how many.
// Each running relay watches its session's done channel, so ending a session cuts off
// both sides immediately.
func (t *sessionTable) endWhere(match func(*session) bool) int {
	t.mu.Lock()
	var victims []*session
	for _, sess := range t.byID {
		if match(sess) {
			victims = append(victims, sess)
		}
	}
	t.mu.Unlock()
	for _, sess := range victims { // outside the lock: end takes it
		t.end(sess)
	}
	return len(victims)
}

// endUser ends every session a user has, waiting or running, and returns how many. It is
// what makes disabling a user, or resetting their password, cut off a shell that is open
// right now (D-69).
func (t *sessionTable) endUser(userID int64) int {
	return t.endWhere(func(sess *session) bool { return sess.userID == userID })
}

// endHost ends every session targeting a host (e.g. when the host is deleted).
func (t *sessionTable) endHost(hostID string) int {
	return t.endWhere(func(sess *session) bool { return sess.agentID == hostID })
}

// endWebSession ends every session opened by one specific login (cookie or bearer token).
func (t *sessionTable) endWebSession(webTokenHash string) int {
	if webTokenHash == "" {
		return 0
	}
	return t.endWhere(func(sess *session) bool { return sess.webTokenHash == webTokenHash })
}

// endOtherWebSessions ends every session of userID except those belonging to exceptTokenHash.
func (t *sessionTable) endOtherWebSessions(userID int64, exceptTokenHash string) int {
	return t.endWhere(func(sess *session) bool {
		return sess.userID == userID && sess.webTokenHash != exceptTokenHash
	})
}

// snapshot returns all waiting or running sessions matching match, so callers can
// check database permissions outside t.mu.
func (t *sessionTable) snapshot(match func(*session) bool) []*session {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []*session
	for _, sess := range t.byID {
		if match(sess) {
			out = append(out, sess)
		}
	}
	return out
}

// endUnauthorizedSessions re-checks each candidate session against current database
// permissions and ends any that its owner is no longer allowed to hold.
func (s *Server) endUnauthorizedSessions(candidates []*session) int {
	ended := 0
	for _, sess := range candidates {
		if !s.userStillAllowed(sess.userID, sess.agentID, sess.need) {
			s.sessions.end(sess)
			ended++
		}
	}
	return ended
}

func (s *Server) userStillAllowed(userID int64, hostID string, a action) bool {
	var admin, disabled int
	err := s.db.QueryRow(`SELECT is_admin, disabled FROM users WHERE id = ?`, userID).Scan(&admin, &disabled)
	if err != nil || disabled == 1 {
		return false
	}
	allowed, err := s.can(user{ID: userID, IsAdmin: admin == 1}, hostID, a)
	return err == nil && allowed
}

func (s *Server) endUnauthorizedForUser(userID int64) int {
	return s.endUnauthorizedSessions(s.sessions.snapshot(func(sess *session) bool {
		return sess.userID == userID
	}))
}

func (s *Server) endUnauthorizedForHost(hostID string) int {
	return s.endUnauthorizedSessions(s.sessions.snapshot(func(sess *session) bool {
		return sess.agentID == hostID
	}))
}

func (t *sessionTable) get(id string) *session {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.byID[id]
}

// end removes a session and wakes everything waiting on it. Safe to call twice.
func (t *sessionTable) end(sess *session) {
	t.mu.Lock()
	delete(t.byID, sess.id)
	if reportsChanges(sess.kind) {
		t.rememberLocked(sess)
	}
	t.mu.Unlock()
	sess.endOnce.Do(func() { close(sess.done) })
}

// rememberLocked keeps the owner of an ended files or docker session (see
// finishedKeepFor), and drops the ones that have been kept long enough. The
// cleanup happens here, on insert, like a plain "sweep when adding": no timer
// and no background goroutine, and the map cannot grow without bound because
// every insert shrinks it back to the last few minutes. The caller holds t.mu.
func (t *sessionTable) rememberLocked(sess *session) {
	now := time.Now()
	for id, old := range t.finished {
		if now.Sub(old.at) > t.keepFor {
			delete(t.finished, id)
		}
	}
	t.finished[sess.id] = finishedSession{userID: sess.userID, agentID: sess.agentID, kind: sess.kind, at: now}
}

// changeOwner says who a session of the given kind (files or docker) was opened
// for and on which host, whether it is still running or ended a moment ago. A
// report that names a session of another kind is bogus: a files report about a
// docker session, or anything about a shell, is not answered with an owner.
func (t *sessionTable) changeOwner(id, kind string) (userID int64, agentID string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if sess := t.byID[id]; sess != nil {
		return sess.userID, sess.agentID, sess.kind == kind
	}
	if old, found := t.finished[id]; found && old.kind == kind && time.Since(old.at) <= t.keepFor {
		return old.userID, old.agentID, true
	}
	return 0, "", false
}

func (sess *session) claimBrowser() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.browserSeen {
		return false
	}
	sess.browserSeen = true
	return true
}

func (sess *session) claimAgent() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.agentSeen {
		return false
	}
	sess.agentSeen = true
	return true
}

// tryPair says "both sides are here, start". It fails if the countdown ran out
// a moment earlier, so a session is never both expired and running.
func (sess *session) tryPair() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.expired {
		return false
	}
	sess.paired = true
	return true
}

// ----------------------------------------------------------- opening one

// handleOpenSession is POST /api/hosts/{id}/sessions. It checks that the user
// may do this, asks the agent to dial back, and returns the session id the
// browser then connects to.
func (s *Server) handleOpenSession(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	hostID := r.PathValue("id")

	var req struct {
		Kind   string `json:"kind"`
		Params struct {
			Cols   int    `json:"cols"`
			Rows   int    `json:"rows"`
			Target string `json:"target"`
		} `json:"params"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Kind != protocol.KindShell && req.Kind != protocol.KindFiles && req.Kind != protocol.KindTunnel && req.Kind != protocol.KindDocker {
		writeError(w, http.StatusBadRequest, "unsupported session kind")
		return
	}

	osName, exists, err := s.hostOS(hostID)
	if err != nil {
		internalError(w, "look up host", err)
		return
	}
	// A host the user may not see is "not found", like everywhere else (D-24).
	canView, err := s.can(u, hostID, actionView)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !exists || !canView {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	// What the session may do depends on its kind and on the user's role. The
	// agent gets these parameters from the server, never from the browser, so
	// nobody can give themselves more than their role allows. The relay does not
	// look inside a session (D-27), which is why a files session carries its
	// "read-only" flag to the agent instead of the server policing each request.
	var kindParams any
	readOnly := false
	tunnelTarget := ""
	need := actionShell
	switch req.Kind {
	case protocol.KindTunnel:
		// A tunnel reaches anything the host can reach on its own loopback: its
		// database, its admin panels, its SSH. That is as powerful as a shell, so
		// it needs the same permission (architecture.md §6).
		allowed, err := s.can(u, hostID, actionShell)
		if err != nil {
			internalError(w, "check permission", err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "you do not have permission to open a tunnel to this host")
			return
		}
		target, err := protocol.ParseTunnelTarget(req.Params.Target)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// A browser (a cookie login) may only open a tunnel to a VNC server: that is
		// all the dashboard's screen viewer needs, and a page that can reach any
		// port of a host's loopback would turn a bug in the page, or a stolen
		// login in a browser tab, into a way into the host's databases and admin
		// panels. hnctl, which logs in with a token it was given on purpose, keeps
		// every loopback port (D-35, D-36, D-49).
		if !viaBearer(r) {
			if _, port, _ := net.SplitHostPort(target); port != browserTunnelPort {
				writeError(w, http.StatusForbidden, "the browser may only open a tunnel to a VNC server (port "+browserTunnelPort+"): use hnctl forward for other ports")
				return
			}
		}
		tunnelTarget = target
		kindParams = protocol.TunnelParams{Target: target}
	case protocol.KindShell:
		allowed, err := s.can(u, hostID, actionShell)
		if err != nil {
			internalError(w, "check permission", err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "you do not have permission to open a terminal on this host")
			return
		}
		cols, rows := protocol.ClampSize(req.Params.Cols, req.Params.Rows)
		kindParams = protocol.ShellParams{Cols: cols, Rows: rows}
	case protocol.KindDocker:
		// The Docker socket is root on the host: a container can mount "/", and even
		// listing containers shows their environment. So this is as strong as a shell
		// and needs the same role, and there is no read-only variant for a viewer (D-70).
		allowed, err := s.can(u, hostID, actionShell)
		if err != nil {
			internalError(w, "check permission", err)
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "you do not have permission to use Docker on this host")
			return
		}
		kindParams = struct{}{} // the agent needs nothing from the browser
	case protocol.KindFiles:
		canRead, err := s.can(u, hostID, actionFilesRead)
		if err != nil {
			internalError(w, "check permission", err)
			return
		}
		if !canRead {
			writeError(w, http.StatusForbidden, "you do not have permission to browse files on this host")
			return
		}
		// Windows hosts do not offer a file manager (D-47), and the Windows agent
		// runs as LocalSystem: enforce this here so a direct API call cannot bypass
		// the hidden UI button.
		if osName == "windows" {
			writeError(w, http.StatusBadRequest, "file manager sessions are not supported on Windows")
			return
		}
		canWrite, err := s.can(u, hostID, actionFilesWrite)
		if err != nil {
			internalError(w, "check permission", err)
			return
		}
		readOnly = !canWrite
		if readOnly {
			need = actionFilesRead
		} else {
			need = actionFilesWrite
		}
		kindParams = protocol.FilesParams{ReadOnly: readOnly}
	}
	params, err := json.Marshal(kindParams)
	if err != nil {
		internalError(w, "encode session parameters", err)
		return
	}

	sess, err := s.sessions.create(u.ID, hostID, req.Kind, need, currentTokenHash(r))
	if err != nil {
		internalError(w, "create session", err)
		return
	}
	err = s.agents.send(hostID, protocol.TypeOpen, protocol.Open{SessionID: sess.id, Kind: sess.kind, Params: params})
	if errors.Is(err, errAgentOffline) {
		s.sessions.end(sess)
		writeError(w, http.StatusConflict, "host offline")
		return
	}
	if err != nil {
		s.sessions.end(sess)
		slog.Error("ask agent to open a session", "agent_id", hostID, "err", err)
		writeError(w, http.StatusBadGateway, "could not reach the host")
		return
	}

	detail := "kind=" + sess.kind
	reply := map[string]any{"id": sess.id}
	if sess.kind == protocol.KindFiles {
		detail += fmt.Sprintf(" readonly=%t", readOnly)
		reply["readonly"] = readOnly // so the page can hide the buttons that would be refused
	}
	if sess.kind == protocol.KindTunnel {
		detail += " target=" + tunnelTarget // never the VNC password below
		// A screen tunnel comes with the VNC server's password, which the agent made
		// when it started the server (D-64), so the viewer logs in by itself. The
		// person was allowed this tunnel above, so they may see the screen anyway.
		if _, port, _ := net.SplitHostPort(tunnelTarget); port == browserTunnelPort {
			if password := s.agents.vncPassword(hostID); password != "" {
				reply["vnc_password"] = password
			}
		}
	}
	s.audit(u.ID, hostID, "session_open", detail)
	w.Header().Set("Cache-Control", "no-store") // it can hold a VNC password
	writeJSON(w, http.StatusCreated, reply)
}

func (s *Server) hostOS(id string) (osName string, exists bool, err error) {
	err = s.db.QueryRow(`SELECT os FROM agents WHERE id = ?`, id).Scan(&osName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up host: %w", err)
	}
	return osName, true, nil
}

func (s *Server) hostExists(id string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("look up host: %w", err)
	}
	return n > 0, nil
}

// ------------------------------------------------------- the two sides

// handleUISession is the browser's side: GET /ui/sessions/{id}, a WebSocket.
// It sits behind requireLogin, which proves who is asking.
func (s *Server) handleUISession(w http.ResponseWriter, r *http.Request) {
	// requireLogin only checks Origin on changes, and a WebSocket handshake is a
	// GET. But a page on another site could open this WebSocket with the
	// visitor's cookie (cross-site WebSocket hijacking), so check it here. A
	// bearer token (hnctl) is not ambient, so it needs no Origin (D-36).
	if !viaBearer(r) && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad origin")
		return
	}
	u := currentUser(r)

	// The id belongs to the user and login session that asked for it. Anyone
	// else, and any unknown id, gets the same answer.
	sess := s.sessions.get(r.PathValue("id"))
	if sess == nil || sess.userID != u.ID || (sess.webTokenHash != "" && sess.webTokenHash != currentTokenHash(r)) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	// Permissions can change in the seconds between the two requests.
	allowed, err := s.can(u, sess.agentID, sess.need)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	if !allowed || !sess.claimBrowser() {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("session websocket handshake failed", "err", err) // Accept already replied
		s.sessions.end(sess)
		return
	}
	defer conn.CloseNow()

	// Wait for the agent's side to arrive. The countdown that started when the
	// session was created closes sess.done if it does not.
	var agentConn *websocket.Conn
	select {
	case agentConn = <-sess.agentConn:
	case <-sess.done:
		conn.Close(closeNoPeer, "the host did not answer in time")
		return
	case <-r.Context().Done():
		s.sessions.end(sess)
		return
	}
	if !sess.tryPair() {
		agentConn.CloseNow()
		conn.Close(closeNoPeer, "the host did not answer in time")
		return
	}

	slog.Info("session started", "session", sess.id, "agent_id", sess.agentID, "user", u.Username)
	started := time.Now()
	// The relay also stops when the session is ended from outside (a user who is disabled
	// or whose password is reset, D-69): sess.done closes, and that cancels the relay.
	relayCtx, stopRelay := context.WithCancel(r.Context())
	go func() {
		select {
		case <-sess.done:
			stopRelay()
		case <-relayCtx.Done():
		}
	}()
	relay(relayCtx, conn, agentConn)
	stopRelay()
	s.sessions.end(sess)

	duration := time.Since(started).Round(time.Second)
	slog.Info("session ended", "session", sess.id, "duration", duration)
	s.audit(u.ID, sess.agentID, "session_close", fmt.Sprintf("kind=%s duration=%s", sess.kind, duration))
}

// handleAgentSession is the agent's side: GET /agent/sessions/{id}, a
// WebSocket. The agent dials this after it received `open` on its control
// connection.
func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.authenticateAgent(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid agent credentials")
		return
	}
	// Only the agent the session was opened on may attach to it.
	sess := s.sessions.get(r.PathValue("id"))
	if sess == nil || sess.agentID != agentID || !sess.claimAgent() {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Warn("agent session handshake failed", "agent_id", agentID, "err", err)
		s.sessions.end(sess)
		return
	}
	defer conn.CloseNow()

	// Hand the connection to the browser's handler (the channel has room for
	// it, so this never blocks), then wait. The handler must not return while
	// the relay uses the connection.
	sess.agentConn <- conn
	select {
	case <-sess.done:
	case <-r.Context().Done():
	}
	// Normally the relay has closed the connection by now and this does
	// nothing. If the session expired before the browser came, this tells the
	// agent why, so it can stop its shell.
	conn.Close(closeNoPeer, "the browser did not connect in time")
}
