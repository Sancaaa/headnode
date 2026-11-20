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

	sess, err := s.sessions.create(u.ID, hostID, protocol.KindDownload)
	if err != nil {
		internalError(w, "create session", err)
		return
	}
	defer s.sessions.end(sess)

	params, err := json.Marshal(protocol.DownloadParams{Path: path})
	if err != nil {
		internalError(w, "encode session parameters", err)
		return
	}
	err = s.agents.send(hostID, protocol.TypeOpen, protocol.Open{SessionID: sess.id, Kind: sess.kind, Params: params})
	if errors.Is(err, errAgentOffline) {
		writeError(w, http.StatusConflict, "host offline")
		return
	}
	if err != nil {
		slog.Error("ask agent to open a download", "agent_id", hostID, "err", err)
		writeError(w, http.StatusBadGateway, "could not reach the host")
		return
	}

	// No browser attaches to this session: its id never leaves the server. Mark
	// the browser's side taken anyway, so nothing could.
	sess.claimBrowser()

	var agentConn *websocket.Conn
	select {
	case agentConn = <-sess.agentConn:
	case <-sess.done: // the 15-second countdown ran out
		writeError(w, http.StatusGatewayTimeout, "the host did not answer in time")
		return
	case <-r.Context().Done():
		return
	}
	defer agentConn.CloseNow()
	if !sess.tryPair() {
		writeError(w, http.StatusGatewayTimeout, "the host did not answer in time")
		return
	}
	agentConn.SetReadLimit(relayReadLimit)

	// The first frame says what is coming. If the file cannot be read, the agent
	// sends no header and closes with the reason instead, and we can still
	// answer with a proper HTTP error because nothing has been sent yet.
	headerCtx, cancel := context.WithTimeout(r.Context(), sessionPairTimeout)
	kind, data, err := agentConn.Read(headerCtx)
	cancel()
	if err != nil {
		status, message := downloadFailure(err)
		writeError(w, status, message)
		return
	}
	var head protocol.DownloadHeader
	if kind != websocket.MessageText || json.Unmarshal(data, &head) != nil {
		writeError(w, http.StatusBadGateway, "the host sent an unexpected answer")
		return
	}

	// Recorded when the transfer starts, with the path as it was asked for.
	s.audit(u.ID, hostID, "file_download", "path="+auditText(path)+" size="+strconv.FormatInt(head.Size, 10))

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
