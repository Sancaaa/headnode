package server

import (
	"database/sql"
	"errors"
	"fmt"
)

// action is something a user can try to do to a host.
type action string

const (
	actionView       action = "view"        // see the host and its details
	actionShell      action = "shell"       // terminal and tunnels (M3, M5)
	actionFilesRead  action = "files_read"  // browse and download (M4)
	actionFilesWrite action = "files_write" // change files and send files (M4)
)

// The two roles a user can have in a group.
const (
	roleOperator = "operator"
	roleViewer   = "viewer"
)

// rolePermissions is the table from architecture.md §6, as data. A role that
// is not listed (or an action that is not listed for a role) is not allowed:
// looking up a missing key in a Go map gives false.
var rolePermissions = map[string]map[action]bool{
	roleOperator: {actionView: true, actionShell: true, actionFilesRead: true, actionFilesWrite: true},
	roleViewer:   {actionView: true, actionFilesRead: true},
}

func roleAllows(role string, a action) bool {
	return rolePermissions[role][a]
}

// can is the ONE place that decides whether a user may do something to a host
// (architecture.md §6). Every feature that touches a host calls it, so the
// rules can be audited by reading this function.
//
// Admins may do everything. Everyone else needs a role in the host's group.
// A host with no group has no members, so only admins can reach it.
//
// can does not check that the host exists: for an admin it answers true either
// way. Callers load the host first.
func (s *Server) can(u user, hostID string, a action) (bool, error) {
	if u.IsAdmin {
		return true, nil
	}

	var role string
	err := s.db.QueryRow(
		`SELECT m.role
		 FROM agents a JOIN group_members m ON m.group_id = a.group_id
		 WHERE a.id = ? AND m.user_id = ?`,
		hostID, u.ID,
	).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // not a member of this host's group
	}
	if err != nil {
		return false, fmt.Errorf("look up role: %w", err)
	}
	return roleAllows(role, a), nil
}
