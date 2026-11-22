package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// adminOnly narrows a route to administrators. It runs inside requireLogin, so
// currentUser is always set.
func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin {
			writeError(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r)
	}
}

// pathID reads a numeric id from the URL, like the 3 in /api/groups/3.
func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func internalError(w http.ResponseWriter, what string, err error) {
	// Details go to the log; the caller only learns that something broke.
	slog.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// ---------------------------------------------------------------- users

type userRow struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	IsAdmin     bool      `json:"is_admin"`
	Disabled    bool      `json:"disabled"` // cannot log in (D-69)
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) loadUsers(where string, args ...any) ([]userRow, error) {
	rows, err := s.db.Query(
		`SELECT id, username, display_name, is_admin, disabled, created_at FROM users`+where+` ORDER BY username`, args...)
	if err != nil {
		return nil, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()

	users := []userRow{}
	for rows.Next() {
		var u userRow
		var admin, disabled int
		var createdAt int64
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &disabled, &createdAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.IsAdmin = admin == 1
		u.Disabled = disabled == 1
		u.CreatedAt = time.Unix(createdAt, 0).UTC()
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read user rows: %w", err)
	}
	return users, nil
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.loadUsers("")
	if err != nil {
		internalError(w, "list users", err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	err := CreateUser(s.db, req.Username, req.Password, req.IsAdmin)
	if errors.Is(err, ErrInvalidUsername) || errors.Is(err, ErrInvalidPassword) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if errors.Is(err, ErrUsernameTaken) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		internalError(w, "create user", err)
		return
	}

	created, err := s.loadUsers(" WHERE username = ?", normalizeUsername(req.Username))
	if err != nil || len(created) != 1 {
		internalError(w, "load new user", err)
		return
	}
	s.audit(currentUser(r).ID, "", "user_create",
		fmt.Sprintf("username=%s admin=%t", created[0].Username, created[0].IsAdmin))
	writeJSON(w, http.StatusCreated, created[0])
}

// --------------------------------------------------------------- groups

type groupSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
	HostCount   int    `json:"host_count"`
}

type member struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type groupDetail struct {
	groupSummary
	Members []member `json:"members"`
	// The hosts in this group, as full host objects, so the group page can show
	// the same table as the Hosts page. Admin-only route, so no visibility filter.
	Hosts []host `json:"hosts"`
}

const groupQuery = `
	SELECT g.id, g.name,
	       (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id),
	       (SELECT COUNT(*) FROM agents a WHERE a.group_id = g.id)
	FROM groups g`

func (s *Server) loadGroups(where string, args ...any) ([]groupSummary, error) {
	rows, err := s.db.Query(groupQuery+where+` ORDER BY g.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, fmt.Errorf("query groups: %w", err)
	}
	defer rows.Close()

	groups := []groupSummary{}
	for rows.Next() {
		var g groupSummary
		if err := rows.Scan(&g.ID, &g.Name, &g.MemberCount, &g.HostCount); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read group rows: %w", err)
	}
	return groups, nil
}

// validName checks a display name for a group or a host: 1 to max characters,
// and no control characters. Names are shown in tables and the audit log, so
// a newline in one could fake a line of the log.
func validName(name string, max int) bool {
	if n := utf8.RuneCountInString(name); n < 1 || n > max {
		return false
	}
	return !strings.ContainsFunc(name, unicode.IsControl)
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.loadGroups("")
	if err != nil {
		internalError(w, "list groups", err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validName(name, 64) {
		writeError(w, http.StatusBadRequest, "group name must be 1-64 characters, without control characters")
		return
	}

	res, err := s.db.Exec(`INSERT INTO groups (name) VALUES (?)`, name)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "a group with this name already exists")
		return
	}
	if err != nil {
		internalError(w, "create group", err)
		return
	}
	id, err := res.LastInsertId()
	if err != nil {
		internalError(w, "read new group id", err)
		return
	}

	s.audit(currentUser(r).ID, "", "group_create", "group="+name)
	writeJSON(w, http.StatusCreated, groupSummary{ID: id, Name: name})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	groups, err := s.loadGroups(" WHERE g.id = ?", id)
	if err != nil {
		internalError(w, "get group", err)
		return
	}
	if len(groups) == 0 {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}

	rows, err := s.db.Query(
		`SELECT m.user_id, u.username, m.role
		 FROM group_members m JOIN users u ON u.id = m.user_id
		 WHERE m.group_id = ? ORDER BY u.username`, id)
	if err != nil {
		internalError(w, "list members", err)
		return
	}
	defer rows.Close()

	detail := groupDetail{groupSummary: groups[0], Members: []member{}}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			internalError(w, "scan member", err)
			return
		}
		detail.Members = append(detail.Members, m)
	}
	if err := rows.Err(); err != nil {
		internalError(w, "read member rows", err)
		return
	}

	detail.Hosts, err = s.loadHosts(currentUser(r), " WHERE a.group_id = ?", id)
	if err != nil {
		internalError(w, "list group hosts", err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// nameOf returns the name behind an id, or sql.ErrNoRows if there is none.
// query is one of the fixed SELECTs below, never user input.
func (s *Server) nameOf(query string, id int64) (string, error) {
	var name string
	err := s.db.QueryRow(query, id).Scan(&name)
	return name, err
}

const (
	groupNameQuery = `SELECT name FROM groups WHERE id = ?`
	userNameQuery  = `SELECT username FROM users WHERE id = ?`
)

// memberTarget reads {id} and {user_id} from the URL and checks that both the
// group and the user exist. On failure it has already answered, and ok is false.
func (s *Server) memberTarget(w http.ResponseWriter, r *http.Request) (groupID, userID int64, groupName, username string, ok bool) {
	groupID, ok1 := pathID(r, "id")
	userID, ok2 := pathID(r, "user_id")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "not found")
		return 0, 0, "", "", false
	}

	groupName, err := s.nameOf(groupNameQuery, groupID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "group not found")
		return 0, 0, "", "", false
	}
	if err != nil {
		internalError(w, "look up group", err)
		return 0, 0, "", "", false
	}
	username, err = s.nameOf(userNameQuery, userID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return 0, 0, "", "", false
	}
	if err != nil {
		internalError(w, "look up user", err)
		return 0, 0, "", "", false
	}
	return groupID, userID, groupName, username, true
}

// handleSetMember adds a user to a group, or changes their role if they are
// already in it.
func (s *Server) handleSetMember(w http.ResponseWriter, r *http.Request) {
	groupID, userID, groupName, username, ok := s.memberTarget(w, r)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if _, valid := rolePermissions[req.Role]; !valid {
		writeError(w, http.StatusBadRequest, "role must be operator or viewer")
		return
	}

	_, err := s.db.Exec(
		`INSERT INTO group_members (user_id, group_id, role) VALUES (?, ?, ?)
		 ON CONFLICT (user_id, group_id) DO UPDATE SET role = excluded.role`,
		userID, groupID, req.Role)
	if err != nil {
		internalError(w, "set member", err)
		return
	}
	// If the role was lowered (e.g. operator -> viewer), cut off any open
	// sessions that need rights this user no longer has (shells, tunnels, docker,
	// or read-write file sessions).
	s.endUnauthorizedForUser(userID)
	s.audit(currentUser(r).ID, "", "member_set",
		fmt.Sprintf("group=%s user=%s role=%s", groupName, username, req.Role))
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateEnrollToken makes an enroll token. A host that enrolls with it
// joins the token's group, or has no group when group_id is absent or null:
// then only admins see it until it is moved. The plain token is in this one
// reply and nowhere else, like at the command line.
func (s *Server) handleCreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID *int64 `json:"group_id"` // a pointer: absent and null both mean "no group"
	}
	if !readJSON(w, r, &req) {
		return
	}

	var group sql.NullInt64 // not Valid = no group
	var groupName *string
	if req.GroupID != nil {
		name, err := s.nameOf(groupNameQuery, *req.GroupID)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		if err != nil {
			internalError(w, "look up group", err)
			return
		}
		group = sql.NullInt64{Int64: *req.GroupID, Valid: true}
		groupName = &name
	}

	token, expiresAt, err := createEnrollToken(s.db, group)
	if err != nil {
		internalError(w, "create enroll token", err)
		return
	}
	s.audit(currentUser(r).ID, "", "token_create", "group="+groupLabel(groupName))

	// One command, run on the new host: it downloads the installer from this
	// server, which downloads the agent, enrolls with this token and installs a
	// service (M6). The token is on the command line, so it is visible to other
	// users of that host while the command runs; it expires in 24 hours.
	command := fmt.Sprintf("curl -fsSL %s/install.sh | sudo sh -s -- --token %s", serverURL(r), token)
	// The same for a Windows host, in PowerShell opened as administrator (D-47).
	windowsCommand := fmt.Sprintf("& ([scriptblock]::Create((irm %s/install.ps1))) -Token %s", serverURL(r), token)

	w.Header().Set("Cache-Control", "no-store") // the reply holds a secret
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":                   token,
		"expires_at":              expiresAt.UTC(),
		"group_id":                req.GroupID,
		"group_name":              groupName,
		"install_command":         command,
		"install_command_windows": windowsCommand,
	})
}

func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID, userID, groupName, username, ok := s.memberTarget(w, r)
	if !ok {
		return
	}
	res, err := s.db.Exec(`DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, groupID, userID)
	if err != nil {
		internalError(w, "remove member", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "user is not a member of this group")
		return
	}
	// Cut off any active sessions the removed member holds on hosts in this group.
	s.endUnauthorizedForUser(userID)
	s.audit(currentUser(r).ID, "", "member_remove",
		fmt.Sprintf("group=%s user=%s", groupName, username))
	w.WriteHeader(http.StatusNoContent)
}
