package server

import (
	"database/sql"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"headnode/protocol"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
)

// audit records one action in the audit log. userID 0 means "nobody was logged
// in", agentID "" means "no host involved"; both are stored as NULL.
//
// detail is free text for the admin who reads the log. Never put secrets or
// anything a person typed into a field in it (a password can end up in a
// username box by mistake).
//
// A failed write is logged and then ignored, so a full disk cannot stop an
// admin from working. The slog line is the fallback record (D-24).
func (s *Server) audit(userID int64, agentID, action, detail string) {
	var uid, aid any
	if userID != 0 {
		uid = userID
	}
	if agentID != "" {
		aid = agentID
	}
	_, err := s.db.Exec(
		`INSERT INTO audit_log (at, user_id, agent_id, action, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), uid, aid, action, detail,
	)
	if err != nil {
		slog.Error("write audit log", "action", action, "err", err)
	}
}

// maxAuditError caps the error text an agent can put in an audit line. The
// agent's words are not trusted to be short.
const maxAuditError = 200

// auditActions maps the operation a files session reports to its audit action.
var auditActions = map[string]string{
	protocol.OpMkdir:  "file_mkdir",
	protocol.OpRename: "file_rename",
	protocol.OpDelete: "file_delete",
	protocol.OpCopy:   "file_copy",
	protocol.OpChmod:  "file_chmod",
	protocol.OpWrite:  "file_write",
}

// recordFileChange writes the audit line for one change an agent reported from
// a files session (D-37). The agent only says WHAT happened: the person comes
// from the session record, and the report is dropped unless that session was
// opened on the very agent that is reporting. So an agent can only add lines
// about its own host, under people who really had a files session on it.
//
// The log trusts what the agent says about the change itself. That is accepted:
// an agent that lies is already in control of its own machine.
func (s *Server) recordFileChange(agentID string, c protocol.FileChange) {
	userID, ownerAgent, ok := s.sessions.filesOwner(c.SessionID)
	if !ok || ownerAgent != agentID {
		// Same answer for "no such session" and "somebody else's": no hints.
		slog.Warn("dropped a file change report for an unknown session", "agent_id", agentID, "session", c.SessionID)
		return
	}
	action, known := auditActions[c.Op]
	if !known {
		slog.Warn("dropped a file change report with an unknown operation", "agent_id", agentID, "op", c.Op)
		return
	}

	detail := "path=" + auditText(c.Path)
	switch c.Op {
	case protocol.OpRename, protocol.OpCopy:
		detail += " -> " + auditText(c.To)
	case protocol.OpDelete:
		if c.Recursive {
			detail += " recursive=true"
		}
	case protocol.OpChmod:
		detail += " mode=" + clip(auditText(c.Mode), 10) // a mode is four characters: more is an agent making things up
	case protocol.OpWrite:
		// How much was written, never what: a saved file can hold a password.
		detail += " size=" + strconv.FormatInt(c.Size, 10)
	}
	if !c.OK {
		detail += " FAILED: " + clip(auditText(c.Error), maxAuditError)
	}
	s.audit(userID, agentID, action, detail)
}

// clip shortens s to at most max characters, without cutting one in half.
func clip(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// auditEntry is one line of the audit log as the Audit page shows it.
type auditEntry struct {
	ID int64     `json:"id"`
	At time.Time `json:"at"`
	// The log keeps ids, because it must outlive the people and hosts it
	// mentions. The names are looked up now, and are null when that user or
	// host no longer exists (or never did, for a failed login or an enroll).
	UserID   *int64  `json:"user_id"`
	Username *string `json:"username"`
	AgentID  *string `json:"agent_id"`
	HostName *string `json:"host_name"`
	Action   string  `json:"action"`
	Detail   string  `json:"detail"`
}

// handleListAudit returns the log, newest first. limit is how many entries
// (default 50, at most 200). before is an entry id: only older entries are
// returned, so a client pages backwards by passing the id of the last entry it
// has. Ids only grow, so new entries never shift the pages.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := int64(defaultAuditLimit)
	if text := r.URL.Query().Get("limit"); text != "" {
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		limit = min(n, maxAuditLimit)
	}
	before := int64(math.MaxInt64) // "no cursor": everything is older than this
	if text := r.URL.Query().Get("before"); text != "" {
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "before must be an entry id")
			return
		}
		before = n
	}

	rows, err := s.db.Query(
		`SELECT a.id, a.at, a.user_id, u.username, a.agent_id, g.name, a.action, a.detail
		 FROM audit_log a
		 LEFT JOIN users u ON u.id = a.user_id
		 LEFT JOIN agents g ON g.id = a.agent_id
		 WHERE a.id < ?
		 ORDER BY a.id DESC
		 LIMIT ?`, before, limit)
	if err != nil {
		internalError(w, "list audit log", err)
		return
	}
	defer rows.Close()

	entries := []auditEntry{} // not nil, so an empty log is [] and not null
	for rows.Next() {
		var e auditEntry
		var at int64
		var userID sql.NullInt64
		var username, agentID, hostName sql.NullString
		if err := rows.Scan(&e.ID, &at, &userID, &username, &agentID, &hostName, &e.Action, &e.Detail); err != nil {
			internalError(w, "scan audit entry", err)
			return
		}
		e.At = time.Unix(at, 0).UTC()
		if userID.Valid {
			e.UserID = &userID.Int64
		}
		if username.Valid {
			e.Username = &username.String
		}
		if agentID.Valid {
			e.AgentID = &agentID.String
		}
		if hostName.Valid {
			e.HostName = &hostName.String
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		internalError(w, "read audit rows", err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}
