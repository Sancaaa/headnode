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

	"golang.org/x/crypto/bcrypt"
)

// Accounts (M11, D-69): what a person may do to their own account (Profile), and what an
// administrator may do to anyone's (Users). Everything here is audited, and everything
// that makes an old login unsafe (a new password, a disabled account) also ends it.

const maxDisplayNameLen = 64

// validDisplayName checks the optional name shown in the sidebar: up to 64
// characters and no control characters, because it is shown to other people and in the
// audit log, where a newline in it could fake a line. An empty name is allowed: it means
// "show the username".
func validDisplayName(name string) bool {
	if utf8.RuneCountInString(name) > maxDisplayNameLen {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// deleteSessionsOf ends a user's logins (browsers and hnctl tokens), all of them or all
// but one. The tokens are only hashes, so deleting the rows is all it takes: the next
// request with one of them is "not logged in". It returns how many it ended.
func (s *Server) deleteSessionsOf(userID int64, exceptTokenHash string) (int64, error) {
	result, err := s.db.Exec(`DELETE FROM web_sessions WHERE user_id = ? AND token_hash != ?`, userID, exceptTokenHash)
	if err != nil {
		return 0, fmt.Errorf("delete sessions: %w", err)
	}
	return result.RowsAffected()
}

// currentTokenHash is the hash of the token this very request logged in with, so that
// "everywhere else" can leave it alone.
func currentTokenHash(r *http.Request) string {
	token, _, _ := requestToken(r)
	return hashSecret(token)
}

// hashPassword makes the stored form of a password, after checking its length.
func hashPassword(password string) (string, error) {
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return "", ErrInvalidPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// ---------------------------------------------------------------- the Profile page

// handlePatchMe is PATCH /api/me: the person's own display name. The username never
// changes (the audit log shows it).
func (s *Server) handlePatchMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		DisplayName *string `json:"display_name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.DisplayName == nil {
		writeError(w, http.StatusBadRequest, "display_name is required (an empty text clears it)")
		return
	}
	name := strings.TrimSpace(*req.DisplayName)
	if !validDisplayName(name) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the name may be at most %d characters, with no control characters", maxDisplayNameLen))
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, u.ID); err != nil {
		internalError(w, "set display name", err)
		return
	}
	s.audit(u.ID, "", "display_name_set", "name="+name)
	writeJSON(w, http.StatusOK, map[string]string{"display_name": name})
}

// checkOwnPassword asks for the current password before something that must not be done
// with a stolen login alone (a new password). It answers the request itself when the
// password is wrong, and returns false then. The guesses are limited like a login's
// (a stolen session could otherwise be used to guess the password), and the bcrypt work
// shares the logins' small pool (D-61).
func (s *Server) checkOwnPassword(w http.ResponseWriter, r *http.Request, u user, password string) bool {
	key := "password:" + strconv.FormatInt(u.ID, 10)
	if wait := s.loginLimit.begin(key); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many wrong passwords, try again in %d seconds", seconds))
		return false
	}
	select {
	case s.passwordChecks <- struct{}{}:
	case <-r.Context().Done():
		return false
	case <-time.After(10 * time.Second):
		writeError(w, http.StatusServiceUnavailable, "the server is busy, try again in a moment")
		return false
	}
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, u.ID).Scan(&hash)
	ok := err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	<-s.passwordChecks
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		internalError(w, "look up password", err)
		return false
	}
	if !ok {
		// Not 401: the page takes that to mean "logged out". The attempt was counted by begin.
		writeError(w, http.StatusForbidden, "the current password is wrong")
		return false
	}
	s.loginLimit.reset(key)
	return true
}

// handleChangePassword is POST /api/me/password. It needs the current password, and
// afterwards every OTHER login of this user is over, hnctl tokens included: a password
// changed because it leaked is useless if the logins made with it stay open.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.New) < minPasswordLen || len(req.New) > maxPasswordLen {
		writeError(w, http.StatusBadRequest, ErrInvalidPassword.Error())
		return
	}
	if req.New == req.Current {
		writeError(w, http.StatusBadRequest, "the new password must be different from the current one")
		return
	}
	if !s.checkOwnPassword(w, r, u, req.Current) {
		return
	}

	hash, err := hashPassword(req.New)
	if err != nil {
		internalError(w, "hash new password", err)
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, u.ID); err != nil {
		internalError(w, "store new password", err)
		return
	}
	ended, err := s.deleteSessionsOf(u.ID, currentTokenHash(r))
	if err != nil {
		internalError(w, "end other sessions", err)
		return
	}
	s.audit(u.ID, "", "password_change", fmt.Sprintf("other_sessions_ended=%d", ended))
	w.WriteHeader(http.StatusNoContent)
}

// sessionInfo is one login, as the Profile page lists it. The id is the row's number,
// not the token or its hash: it is only a handle for "sign out this one".
type sessionInfo struct {
	ID         int64     `json:"id"`
	Client     string    `json:"client"` // "browser", or "cli" for hnctl
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"` // the one making this request
}

// handleListMySessions is GET /api/me/sessions: the person's own logins that have not
// expired, newest first.
func (s *Server) handleListMySessions(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rows, err := s.db.Query(
		`SELECT rowid, token_hash, client, ip, created_at, last_used_at, expires_at
		 FROM web_sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC, rowid DESC`,
		u.ID, time.Now().Unix())
	if err != nil {
		internalError(w, "list sessions", err)
		return
	}
	defer rows.Close()

	mine := currentTokenHash(r)
	list := []sessionInfo{}
	for rows.Next() {
		var info sessionInfo
		var tokenHash string
		var created, lastUsed, expires int64
		if err := rows.Scan(&info.ID, &tokenHash, &info.Client, &info.IP, &created, &lastUsed, &expires); err != nil {
			internalError(w, "scan session", err)
			return
		}
		if lastUsed == 0 {
			lastUsed = created // from before it was recorded: the login is the last known use
		}
		info.CreatedAt = time.Unix(created, 0).UTC()
		info.LastUsedAt = time.Unix(lastUsed, 0).UTC()
		info.ExpiresAt = time.Unix(expires, 0).UTC()
		info.Current = tokenHash == mine
		list = append(list, info)
	}
	if err := rows.Err(); err != nil {
		internalError(w, "read sessions", err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleRevokeMySession is DELETE /api/me/sessions/{id}: sign out one other login of
// the person's own. Somebody else's id is "not found", like an unknown one.
func (s *Server) handleRevokeMySession(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	var tokenHash, client string
	err := s.db.QueryRow(`SELECT token_hash, client FROM web_sessions WHERE rowid = ? AND user_id = ?`, id, u.ID).Scan(&tokenHash, &client)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		internalError(w, "look up session", err)
		return
	}
	if tokenHash == currentTokenHash(r) {
		writeError(w, http.StatusBadRequest, "this is the session you are using: log out instead")
		return
	}
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE rowid = ? AND user_id = ?`, id, u.ID); err != nil {
		internalError(w, "delete session", err)
		return
	}
	s.audit(u.ID, "", "session_revoke", "client="+client)
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeOtherSessions is POST /api/me/sessions/revoke-others: "sign out
// everywhere else". The login making the request stays.
func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	ended, err := s.deleteSessionsOf(u.ID, currentTokenHash(r))
	if err != nil {
		internalError(w, "end other sessions", err)
		return
	}
	s.audit(u.ID, "", "sessions_revoke_others", fmt.Sprintf("ended=%d", ended))
	writeJSON(w, http.StatusOK, map[string]int64{"ended": ended})
}

// ------------------------------------------------------------- the Users page (admin)

// handleResetPassword is POST /api/users/{id}/password: an administrator gives a user a
// new password, and all of that user's logins end. Not for the administrator's own
// account: that needs the current password, on the Profile page, so a stolen admin
// session cannot be turned into a new admin password.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	actor := currentUser(r)
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if id == actor.ID {
		writeError(w, http.StatusConflict, "to change your own password use your Profile page: it asks for the current one")
		return
	}
	var username string
	err := s.db.QueryRow(`SELECT username FROM users WHERE id = ?`, id).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		internalError(w, "look up user", err)
		return
	}
	hash, err := hashPassword(req.Password)
	if errors.Is(err, ErrInvalidPassword) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		internalError(w, "hash password", err)
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		internalError(w, "store password", err)
		return
	}
	ended, err := s.deleteSessionsOf(id, "")
	if err != nil {
		internalError(w, "end sessions", err)
		return
	}
	s.sessions.endUser(id) // and what they have open: a shell must not outlive the password it was opened with
	s.audit(actor.ID, "", "password_reset", fmt.Sprintf("user=%s sessions_ended=%d", username, ended))
	w.WriteHeader(http.StatusNoContent)
}

// errLastAdmin and the other refusals of handlePatchUser.
var (
	errSelfDisable = errors.New("you cannot disable your own account")
	errSelfDemote  = errors.New("you cannot remove your own administrator rights")
	errLastAdmin   = errors.New("the last enabled administrator cannot be disabled or demoted")
)

// handlePatchUser is PATCH /api/users/{id}: disable or enable a user (instead of
// deleting them, so the audit log keeps pointing at a real person), and give or take away
// administrator rights. Both fields are optional, at least one is needed.
//
// Nobody can do either harmful thing to themselves, and the last enabled administrator
// can be neither disabled nor demoted: the only way back from a dashboard with no
// administrator is the server's command line. The checks and the change are one
// transaction, so two administrators demoting each other at the same moment cannot both pass.
func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	actor := currentUser(r)
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	var req struct {
		Disabled *bool `json:"disabled"`
		IsAdmin  *bool `json:"is_admin"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Disabled == nil && req.IsAdmin == nil {
		writeError(w, http.StatusBadRequest, "give disabled and/or is_admin")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		internalError(w, "begin", err)
		return
	}
	defer tx.Rollback() // does nothing after a commit

	var username string
	var admin, disabled int
	err = tx.QueryRow(`SELECT username, is_admin, disabled FROM users WHERE id = ?`, id).Scan(&username, &admin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		internalError(w, "look up user", err)
		return
	}
	wasAdmin, wasDisabled := admin == 1, disabled == 1
	nowAdmin, nowDisabled := wasAdmin, wasDisabled
	if req.IsAdmin != nil {
		nowAdmin = *req.IsAdmin
	}
	if req.Disabled != nil {
		nowDisabled = *req.Disabled
	}

	disabling := nowDisabled && !wasDisabled
	demoting := wasAdmin && !nowAdmin
	if id == actor.ID && disabling {
		writeError(w, http.StatusConflict, errSelfDisable.Error())
		return
	}
	if id == actor.ID && demoting {
		writeError(w, http.StatusConflict, errSelfDemote.Error())
		return
	}
	if wasAdmin && !wasDisabled && (disabling || demoting) {
		others, err := otherEnabledAdmins(tx, id)
		if err != nil {
			internalError(w, "count administrators", err)
			return
		}
		if others == 0 {
			writeError(w, http.StatusConflict, errLastAdmin.Error())
			return
		}
	}

	if nowAdmin != wasAdmin || nowDisabled != wasDisabled {
		if _, err := tx.Exec(`UPDATE users SET is_admin = ?, disabled = ? WHERE id = ?`, boolInt(nowAdmin), boolInt(nowDisabled), id); err != nil {
			internalError(w, "update user", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internalError(w, "commit", err)
		return
	}

	// Only real changes are audited and acted on.
	if disabling {
		// Logged out and cut off at once: their logins and tokens, and every shell, file
		// session and tunnel they have open (like a deleted host, D-25).
		ended, err := s.deleteSessionsOf(id, "")
		if err != nil {
			slog.Error("end sessions of a disabled user", "user", username, "err", err)
		}
		open := s.sessions.endUser(id)
		s.audit(actor.ID, "", "user_disable", fmt.Sprintf("user=%s sessions_ended=%d open_sessions_ended=%d", username, ended, open))
	}
	if wasDisabled && !nowDisabled {
		s.audit(actor.ID, "", "user_enable", "user="+username)
	}
	if !wasAdmin && nowAdmin {
		s.audit(actor.ID, "", "admin_grant", "user="+username)
	}
	if demoting {
		s.audit(actor.ID, "", "admin_revoke", "user="+username)
	}

	users, err := s.loadUsers(" WHERE id = ?", id)
	if err != nil || len(users) != 1 {
		internalError(w, "load user", err)
		return
	}
	writeJSON(w, http.StatusOK, users[0])
}

// otherEnabledAdmins counts the administrators who are not disabled, leaving out one
// user: if it is 0, that user is the last one and must stay. q is a transaction or the
// database. Over HTTP the caller is always an enabled administrator of their own, so this
// can only come out 0 when two administrators act at the same moment, which is what the
// transaction around it is for.
func otherEnabledAdmins(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, excludeID int64) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND disabled = 0 AND id != ?`, excludeID).Scan(&n)
	return n, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
