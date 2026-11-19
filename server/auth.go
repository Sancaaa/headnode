package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	minPasswordLen = 8
	// bcrypt only looks at the first 72 bytes. Accepting a longer password
	// would silently ignore the rest, so we refuse it instead.
	maxPasswordLen = 72

	sessionCookie = "hn_session" // D-13
	// Absolute lifetime: 7 days after login the user logs in again. There is
	// no sliding renewal, which keeps the rule easy to explain (D-23).
	sessionLifetime = 7 * 24 * time.Hour
)

var (
	ErrUsernameTaken   = errors.New("username already exists")
	ErrInvalidUsername = errors.New("username must be 1-32 characters: lowercase letters, digits, dot, dash or underscore")
	ErrInvalidPassword = fmt.Errorf("password must be %d to %d bytes long", minPasswordLen, maxPasswordLen)

	usernameRe = regexp.MustCompile(`^[a-z0-9._-]{1,32}$`)
)

// normalizeUsername makes "Admin " and "admin" the same person.
func normalizeUsername(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// user is a logged-in person, as handlers see them.
type user struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

// userKey is the context key for the logged-in user. It is its own unexported
// type so no other package can accidentally use the same key.
type userKey struct{}

// currentUser returns the user that requireLogin put on the request. Handlers
// behind requireLogin can rely on it. Calling it anywhere else panics, which
// is the right outcome: it means a route skipped the login check.
func currentUser(r *http.Request) user {
	return r.Context().Value(userKey{}).(user)
}

// CreateUser adds a dashboard user. The username is lowercased and trimmed
// first, so "Admin " and "admin" are the same person.
func CreateUser(db *sql.DB, username, password string, isAdmin bool) error {
	username = normalizeUsername(username)
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	if len(password) < minPasswordLen || len(password) > maxPasswordLen {
		return ErrInvalidPassword
	}

	// bcrypt is deliberately slow (about 50 ms at the default cost), so
	// guessing passwords from a stolen database is slow too. It also salts
	// every hash itself, so the same password gives a different hash each time.
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	admin := 0
	if isAdmin {
		admin = 1
	}
	_, err = db.Exec(
		`INSERT INTO users (username, password_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`,
		username, string(hash), admin, time.Now().Unix(),
	)
	if isUniqueViolation(err) {
		return ErrUsernameTaken
	}
	if err != nil {
		return fmt.Errorf("store user: %w", err)
	}
	return nil
}

// dummyHash is a valid bcrypt hash of a password nobody has. It is made on
// first use, not at startup, so tests and CLI commands do not pay for it.
var dummyHash = sync.OnceValue(func() []byte {
	// Cannot fail: the input is short and the cost is the default.
	hash, _ := bcrypt.GenerateFromPassword([]byte("not a real password"), bcrypt.DefaultCost)
	return hash
})

// checkPassword returns the user if the username and password are right.
func (s *Server) checkPassword(username, password string) (user, bool, error) {
	username = normalizeUsername(username)

	var u user
	var hash string
	var admin int
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, is_admin FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &hash, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		// Compare against a dummy hash anyway. Otherwise "no such user" would
		// answer in 1 ms and "wrong password" in 50 ms, and the difference
		// would tell an attacker which usernames exist.
		bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return user{}, false, nil
	}
	if err != nil {
		return user{}, false, fmt.Errorf("look up user: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return user{}, false, nil
	}
	u.IsAdmin = admin == 1
	return u, true, nil
}

// createSession stores a new login session and returns the token for the
// cookie. Only the hash is stored (D-09), so a leaked database cannot be used
// to take over a logged-in session.
func (s *Server) createSession(userID int64) (string, error) {
	token, err := newSecret()
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.db.Exec(
		`INSERT INTO web_sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		hashSecret(token), userID, now.Add(sessionLifetime).Unix(), now.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}

	// Housekeeping: expired sessions are useless, so remove them now and then
	// instead of letting the table grow forever. Failure is harmless.
	if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE expires_at < ?`, now.Unix()); err != nil {
		slog.Warn("delete expired sessions", "err", err)
	}
	return token, nil
}

// sessionUser finds the user behind the request's session cookie.
func (s *Server) sessionUser(r *http.Request) (user, bool, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return user{}, false, nil // no cookie: not logged in
	}

	var u user
	var admin int
	err = s.db.QueryRow(
		`SELECT u.id, u.username, u.is_admin
		 FROM web_sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`,
		hashSecret(cookie.Value), time.Now().Unix(),
	).Scan(&u.ID, &u.Username, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return user{}, false, nil
	}
	if err != nil {
		return user{}, false, fmt.Errorf("look up session: %w", err)
	}
	u.IsAdmin = admin == 1
	return u, true, nil
}

// requireLogin wraps every API route except login (D-22): no valid session
// means 401, and a change request from another site means 403.
func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok, err := s.sessionUser(r)
		if err != nil {
			slog.Error("check session", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		// Login first, Origin second: the Origin check only matters once the
		// cookie is valid, and a caller with no login learns nothing else.
		if !isSafeMethod(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "bad origin")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:  sessionCookie,
		Value: token,
		Path:  "/",
		// MaxAge below 0 deletes the cookie in the browser (logout).
		MaxAge: maxAge,
		// JavaScript cannot read an HttpOnly cookie, so a script injected into
		// the page still cannot steal the session.
		HttpOnly: true,
		// Secure keeps the cookie off plain HTTP. It is only set when the
		// request came in over HTTPS, otherwise a development setup on
		// http:// would never get its cookie back (D-23).
		Secure: isHTTPS(r),
		// Lax: the cookie is not sent on cross-site POSTs. A second layer
		// behind the Origin check.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Login is outside requireLogin, so it checks Origin itself. Without this,
	// another website could log the visitor in as an attacker's account.
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad origin")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	// Check the limiter before doing any work: a blocked attempt must be cheap,
	// and it must be refused even if the password happens to be right.
	ip := clientIP(r)
	if wait := s.loginLimit.wait(ip); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second) // round up
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("too many failed attempts, try again in %d seconds", seconds))
		return
	}

	u, ok, err := s.checkPassword(req.Username, req.Password)
	if err != nil {
		slog.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		s.loginLimit.fail(ip)
		// The username is neither logged nor audited: people sometimes type
		// their password into that field. The address is enough to spot an attack.
		slog.Warn("login failed", "ip", ip)
		s.audit(0, "", "login_failed", "ip="+ip)
		// One message for "no such user" and "wrong password", so the answer
		// does not reveal which usernames exist.
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	token, err := s.createSession(u.ID)
	if err != nil {
		slog.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.loginLimit.reset(ip)
	setSessionCookie(w, r, token, int(sessionLifetime.Seconds()))
	slog.Info("login", "user", u.Username, "ip", ip)
	s.audit(u.ID, "", "login", "ip="+ip)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Delete the session on the server, not just the cookie in the browser: a
	// copied cookie must stop working too.
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, hashSecret(cookie.Value)); err != nil {
			slog.Error("delete session", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	s.audit(currentUser(r).ID, "", "logout", "")
	setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}
