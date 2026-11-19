package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"headnode/protocol"
)

// host is what both host endpoints return, so the list and the detail page
// share one shape (protocol.md §1).
type host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Online   bool   `json:"online"`
	// nil (JSON null) means the host is in no group: only admins see it.
	GroupID   *int64  `json:"group_id"`
	GroupName *string `json:"group_name"`
	// nil (JSON null) means the agent enrolled but never connected.
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
	// The latest heartbeat. nil when the host is offline, so the page never
	// shows old numbers as if they were live.
	Heartbeat *protocol.Heartbeat `json:"heartbeat"`
	// The agent's last hello. nil until the first hello arrives.
	Info *protocol.Hello `json:"info"`
}

const hostQuery = `
	SELECT a.id, a.name, a.hostname, a.os, a.arch, a.version, a.last_seen_at, a.created_at,
	       a.info_json, a.group_id, g.name
	FROM agents a LEFT JOIN groups g ON g.id = a.group_id`

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)

	var where string
	var args []any
	if !u.IsAdmin {
		// A host is visible when the user has ANY role in its group. This is the
		// same rule as can(..., actionView), written as SQL so the database
		// filters the list instead of asking once per host. A test keeps the two
		// in agreement (TestHostVisibility).
		where = ` WHERE a.group_id IN (SELECT group_id FROM group_members WHERE user_id = ?)`
		args = []any{u.ID}
	}

	hosts, err := s.loadHosts(where, args...)
	if err != nil {
		slog.Error("list hosts", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	hosts, err := s.loadHosts(" WHERE a.id = ?", r.PathValue("id"))
	if err != nil {
		slog.Error("get host", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if len(hosts) == 0 {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}

	allowed, err := s.can(u, hosts[0].ID, actionView)
	if err != nil {
		slog.Error("check permission", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !allowed {
		// 404, not 403: "forbidden" would confirm that a host with this id
		// exists. To someone who may not see it, it does not exist.
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, hosts[0])
}

// groupLabel is how a group (or its absence) is written in the audit log.
func groupLabel(name *string) string {
	if name == nil {
		return "(none)"
	}
	return *name
}

// sameGroup tells whether a host's current group (nil = none) is the group the
// request asks for (not Valid = none). Only a real move is worth an audit entry.
func sameGroup(current *int64, requested sql.NullInt64) bool {
	if current == nil {
		return !requested.Valid
	}
	return requested.Valid && *current == requested.Int64
}

// handlePatchHost renames a host and/or moves it to another group. Admin only
// (adminOnly in http.go): moving a host changes who can see it.
func (s *Server) handlePatchHost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
		// Three cases, which an ordinary int field could not tell apart:
		// absent = leave the group alone, null = take the host out of its
		// group, a number = move it to that group.
		GroupID json.RawMessage `json:"group_id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == nil && req.GroupID == nil {
		writeError(w, http.StatusBadRequest, "nothing to change: send name and/or group_id")
		return
	}

	hosts, err := s.loadHosts(" WHERE a.id = ?", r.PathValue("id"))
	if err != nil {
		internalError(w, "load host", err)
		return
	}
	if len(hosts) == 0 {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	before := hosts[0]

	// Check everything first, change things after. A request with a good name
	// and a bad group must not rename the host and then fail.
	var newName string
	if req.Name != nil {
		newName = strings.TrimSpace(*req.Name)
		if !validName(newName, 255) {
			writeError(w, http.StatusBadRequest, "name must be 1-255 characters, without control characters")
			return
		}
	}
	var newGroup sql.NullInt64 // not Valid = no group
	var newGroupName *string
	if req.GroupID != nil && string(req.GroupID) != "null" {
		var id int64
		if err := json.Unmarshal(req.GroupID, &id); err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "group_id must be a group id or null")
			return
		}
		name, err := s.nameOf(groupNameQuery, id)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if err != nil {
			internalError(w, "look up group", err)
			return
		}
		newGroup = sql.NullInt64{Int64: id, Valid: true}
		newGroupName = &name
	}

	tx, err := s.db.Begin()
	if err != nil {
		internalError(w, "begin transaction", err)
		return
	}
	defer tx.Rollback() // does nothing once Commit has succeeded
	if req.Name != nil {
		if _, err := tx.Exec(`UPDATE agents SET name = ? WHERE id = ?`, newName, before.ID); err != nil {
			internalError(w, "rename host", err)
			return
		}
	}
	if req.GroupID != nil {
		if _, err := tx.Exec(`UPDATE agents SET group_id = ? WHERE id = ?`, newGroup, before.ID); err != nil {
			internalError(w, "move host", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit host change", err)
		return
	}

	actor := currentUser(r).ID
	if req.Name != nil && newName != before.Name {
		s.audit(actor, before.ID, "host_rename", "name="+before.Name+" -> "+newName)
	}
	if req.GroupID != nil && !sameGroup(before.GroupID, newGroup) {
		s.audit(actor, before.ID, "host_move", "group="+groupLabel(before.GroupName)+" -> "+groupLabel(newGroupName))
	}

	after, err := s.loadHosts(" WHERE a.id = ?", before.ID)
	if err != nil || len(after) != 1 {
		internalError(w, "reload host", err)
		return
	}
	writeJSON(w, http.StatusOK, after[0])
}

// handleDeleteHost removes a host. Its secret stops working at once, because
// the row the secret is checked against is gone, and a live connection is cut.
func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.loadHosts(" WHERE a.id = ?", r.PathValue("id"))
	if err != nil {
		internalError(w, "load host", err)
		return
	}
	if len(hosts) == 0 {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	h := hosts[0]

	if _, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, h.ID); err != nil {
		internalError(w, "delete host", err)
		return
	}
	// Delete first, disconnect second: if it were the other way round, the
	// agent could reconnect in between, since its credentials would still be valid.
	s.agents.disconnect(h.ID)

	s.audit(currentUser(r).ID, h.ID, "host_delete", "name="+h.Name)
	w.WriteHeader(http.StatusNoContent)
}

// loadHosts reads hosts from the database and adds the live state from the
// in-memory registry. where is a fixed SQL fragment written in this file,
// never user input; user values always go through args.
func (s *Server) loadHosts(where string, args ...any) ([]host, error) {
	rows, err := s.db.Query(hostQuery+where+` ORDER BY a.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	defer rows.Close()

	hosts := []host{} // not nil, so an empty list is [] and not null
	for rows.Next() {
		var h host
		var lastSeen, createdAt int64
		var infoJSON string
		var groupID sql.NullInt64
		var groupName sql.NullString
		err := rows.Scan(&h.ID, &h.Name, &h.Hostname, &h.OS, &h.Arch, &h.Version, &lastSeen, &createdAt,
			&infoJSON, &groupID, &groupName)
		if err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}

		h.CreatedAt = time.Unix(createdAt, 0).UTC()
		if lastSeen != 0 { // 0 = never connected
			t := time.Unix(lastSeen, 0).UTC()
			h.LastSeenAt = &t
		}
		if groupID.Valid {
			h.GroupID = &groupID.Int64
			h.GroupName = &groupName.String
		}
		if infoJSON != "" {
			var info protocol.Hello
			if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
				slog.Error("decode stored hello", "agent_id", h.ID, "err", err) // show the host without info rather than failing the whole list
			} else {
				h.Info = &info
			}
		}
		h.Online, h.Heartbeat = s.agents.status(h.ID)
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read host rows: %w", err)
	}
	return hosts, nil
}
