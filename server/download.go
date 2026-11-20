package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"headnode/protocol"
)

// If the browser stops reading for this long in the middle of a download, the
// download is abandoned instead of holding a connection to the host forever.
const downloadStallTimeout = time.Minute

// requireHostAction answers 404 if the host does not exist or u may not see it
// (the same answer for both, D-24), and 403 with the given message if u may see
// it but may not do a. It returns true only when the caller may go on.
func (s *Server) requireHostAction(w http.ResponseWriter, u user, hostID string, a action, denied string) bool {
	exists, err := s.hostExists(hostID)
	if err != nil {
		internalError(w, "look up host", err)
		return false
	}
	canView, err := s.can(u, hostID, actionView)
	if err != nil {
		internalError(w, "check permission", err)
		return false
	}
	if !exists || !canView {
		writeError(w, http.StatusNotFound, "host not found")
		return false
	}
	allowed, err := s.can(u, hostID, a)
	if err != nil {
		internalError(w, "check permission", err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, denied)
		return false
	}
	return true
}

// handleDownload is GET /api/hosts/{id}/download?path=... (protocol.md §1).
//
// It asks the agent for a `download` session and then plays the browser's part
// itself: it reads the file's bytes from the agent's WebSocket and writes them
// to the HTTP response, one chunk at a time. Nothing is collected in memory or
// saved to disk, so a 1 GB file costs the server about one chunk of RAM, and the
// browser's download speed sets the pace (a slow browser slows the agent).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	hostID := r.PathValue("id")

	path := r.URL.Query().Get("path")
	if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	if !s.requireHostAction(w, u, hostID, actionFilesRead, "you do not have permission to download files from this host") {
		return
	}

	stream, status, message := s.openHostFile(r.Context(), u.ID, hostID, path)
	if stream == nil {
		if status != 0 { // 0: the browser went away while we waited
			writeError(w, status, message)
		}
		return
	}
	defer stream.close()
	agentConn, head := stream.conn, stream.head

	// Recorded when the transfer starts, with the path as it was asked for. A
	// folder is zipped as it is sent, so its size is not known yet.
	size := strconv.FormatInt(head.Size, 10)
	if head.Size < 0 {
		size = "unknown (a folder, sent as a zip)"
	}
	s.audit(u.ID, hostID, "file_download", "path="+auditText(path)+" size="+size)

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream") // never something the browser would render
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Disposition", contentDisposition(head.Name))
	if head.Size >= 0 {
		// With a length, the browser shows a progress bar and can tell a cut-off
		// download from a complete one.
		h.Set("Content-Length", strconv.FormatInt(head.Size, 10))
	}

	rc := http.NewResponseController(w)
	for {
		kind, data, err := agentConn.Read(r.Context())
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return // the whole file was sent
			}
			// The agent failed in the middle, after the status line went out. The
			// only honest signal left is to cut the connection, so the browser
			// reports a failed download instead of keeping a short file that
			// looks complete (it would, with chunked encoding and no length).
			slog.Warn("download interrupted", "agent_id", hostID, "err", err)
			panic(http.ErrAbortHandler)
		}
		if kind != websocket.MessageBinary {
			continue
		}
		// Not an error if the response type cannot do deadlines (a test recorder).
		rc.SetWriteDeadline(time.Now().Add(downloadStallTimeout))
		if _, err := w.Write(data); err != nil {
			return // the browser went away; the deferred closes stop the agent
		}
	}
}

// hostFile is a `download` session that is open and has sent its header: the
// bytes of the file (or of a zipped folder) are next on conn.
type hostFile struct {
	conn *websocket.Conn
	head protocol.DownloadHeader
	end  func()
}

// close ends the session and the connection to the host. Safe to call twice.
func (f *hostFile) close() {
	f.conn.CloseNow()
	f.end()
}

// openHostFile asks a host to send one file and waits for the header that says
// it can. Both the browser's download and "send this file to other hosts" start
// here: the server plays the browser's part (D-32) and the caller decides where
// the bytes go.
//
// On failure it returns nil with the HTTP status and message to answer with.
// Status 0 means the caller's own request ended while we waited: answer nothing.
func (s *Server) openHostFile(ctx context.Context, userID int64, hostID, path string) (*hostFile, int, string) {
	sess, err := s.sessions.create(userID, hostID, protocol.KindDownload)
	if err != nil {
		slog.Error("create session", "err", err)
		return nil, http.StatusInternalServerError, "internal error"
	}
	fail := func(status int, message string) (*hostFile, int, string) {
		s.sessions.end(sess)
		return nil, status, message
	}

	params, err := json.Marshal(protocol.DownloadParams{Path: path})
	if err != nil {
		slog.Error("encode session parameters", "err", err)
		return fail(http.StatusInternalServerError, "internal error")
	}
	err = s.agents.send(hostID, protocol.TypeOpen, protocol.Open{SessionID: sess.id, Kind: sess.kind, Params: params})
	if errors.Is(err, errAgentOffline) {
		return fail(http.StatusConflict, "host offline")
	}
	if err != nil {
		slog.Error("ask agent to open a download", "agent_id", hostID, "err", err)
		return fail(http.StatusBadGateway, "could not reach the host")
	}

	// No browser attaches to this session: its id never leaves the server. Mark
	// the browser's side taken anyway, so nothing could.
	sess.claimBrowser()

	var agentConn *websocket.Conn
	select {
	case agentConn = <-sess.agentConn:
	case <-sess.done: // the 15-second countdown ran out
		return fail(http.StatusGatewayTimeout, "the host did not answer in time")
	case <-ctx.Done():
		return fail(0, "")
	}
	if !sess.tryPair() {
		agentConn.CloseNow()
		return fail(http.StatusGatewayTimeout, "the host did not answer in time")
	}
	agentConn.SetReadLimit(relayReadLimit)

	// The first frame says what is coming. If the file cannot be read, the agent
	// sends no header and closes with the reason instead, and we can still
	// answer with a proper HTTP error because nothing has been sent yet.
	headerCtx, cancel := context.WithTimeout(ctx, sessionPairTimeout)
	kind, data, err := agentConn.Read(headerCtx)
	cancel()
	if err != nil {
		agentConn.CloseNow()
		status, message := downloadFailure(err)
		return fail(status, message)
	}
	var head protocol.DownloadHeader
	if kind != websocket.MessageText || json.Unmarshal(data, &head) != nil {
		agentConn.CloseNow()
		return fail(http.StatusBadGateway, "the host sent an unexpected answer")
	}
	return &hostFile{conn: agentConn, head: head, end: func() { s.sessions.end(sess) }}, 0, ""
}

// downloadFailure turns the agent's close (or silence) into an HTTP status and
// a message for the person. The agent's reasons are the operating system's own
// short wording (agent/files.go, describe).
func downloadFailure(err error) (int, string) {
	var closed websocket.CloseError
	if !errors.As(err, &closed) || closed.Reason == "" {
		return http.StatusBadGateway, "the host closed the connection"
	}
	reason := closed.Reason
	switch {
	case strings.Contains(reason, "no such file"):
		return http.StatusNotFound, reason
	case strings.Contains(reason, "permission denied"):
		return http.StatusForbidden, reason
	default:
		// "is a directory", "path must be absolute", "not a regular file", ...
		return http.StatusBadRequest, reason
	}
}

// contentDisposition builds the header that makes the browser save the file
// under its name. The name comes from the host's disk, so it is cleaned first:
// no folders, no quotes, no control characters (a newline in a header value
// would let a file name add headers). FormatMediaType then encodes whatever is
// left correctly, non-ASCII names included.
func contentDisposition(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == '"' {
			return -1
		}
		return r
	}, name)
	if name = strings.TrimSpace(name); name == "" {
		name = "download"
	}
	if value := mime.FormatMediaType("attachment", map[string]string{"filename": name}); value != "" {
		return value
	}
	return "attachment"
}

// auditText removes control characters from text that goes into the audit log,
// so a path with a newline in it cannot look like a second log line.
func auditText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
