package server

import (
	"database/sql"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"
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
