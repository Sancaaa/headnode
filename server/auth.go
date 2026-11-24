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
	ErrUserNotFound    = errors.New("user not found")

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
	// What the sidebar shows instead of the username when it is set (D-69). The
	// username itself never changes: the audit log shows it.
	DisplayName string `json:"display_name"`
	TOTPEnabled bool   `json:"totp_enabled"`
	// Unexported so secrets and internal flags are never serialized into API replies.
	disabled   bool
	totpSecret string
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

// ResetUserTOTP clears a user's authenticator secret and deletes all of their
// stored sessions. Intended for emergency recovery via `headnode user totp-reset`
// when an administrator loses access to their authenticator app (D-71).
func ResetUserTOTP(db *sql.DB, username string) error {
	username = normalizeUsername(username)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin totp reset: %w", err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("look up user: %w", err)
	}
	if _, err := tx.Exec(`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("clear totp: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM web_sessions WHERE user_id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	ended, _ := res.RowsAffected()
	if _, err := tx.Exec(
		`INSERT INTO audit_log (at, user_id, agent_id, action, detail) VALUES (?, NULL, NULL, 'user_totp_reset', ?)`,
		time.Now().Unix(), fmt.Sprintf("user=%s via=cli sessions_ended=%d", username, ended),
	); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return tx.Commit()
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
	var admin, disabled, totpEnabled int
	err := s.db.QueryRow(
		`SELECT id, username, password_hash, is_admin, display_name, disabled, totp_secret, totp_enabled
		 FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &hash, &admin, &u.DisplayName, &disabled, &u.totpSecret, &totpEnabled)
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
	u.TOTPEnabled = totpEnabled == 1
	// A disabled user cannot log in (D-69). The password is checked first, so the
	// answer takes as long as any other, and the caller can tell the audit log that it
	// was a disabled account, while the person at the keyboard gets the same message.
	if disabled == 1 {
		u.disabled = true
		return u, false, nil
	}
	return u, true, nil
}

// createSession stores a new login session and returns the token for the
// cookie. Only the hash is stored (D-09), so a leaked database cannot be used
// to take over a logged-in session.
//
// client is "browser" or "cli" (hnctl) and ip where the login came from: the Profile
// page lists them (D-69), so a person can tell their sessions apart.
func (s *Server) createSession(userID int64, client, ip string) (string, error) {
	token, err := newSecret()
	if err != nil {
		return "", err
	}
	now := time.Now()
	_, err = s.db.Exec(
		`INSERT INTO web_sessions (token_hash, user_id, expires_at, created_at, last_used_at, client, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashSecret(token), userID, now.Add(sessionLifetime).Unix(), now.Unix(), now.Unix(), client, ip,
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

// bearerKey is the context key that says how the request proved who it is.
type bearerKey struct{}

// viaBearer reports whether the request was authenticated with an
// "Authorization: Bearer" token (hnctl) and not with the browser's cookie.
func viaBearer(r *http.Request) bool {
	bearer, _ := r.Context().Value(bearerKey{}).(bool)
	return bearer
}

// requestToken returns the session token a request carries: a bearer token if
// there is an Authorization header, otherwise the cookie. bearer says which.
//
// An Authorization header that is not "Bearer <token>" counts as a bearer
// request with no valid token. It never falls back to the cookie: a request
// that tries explicit credentials is judged on them alone.
func requestToken(r *http.Request) (token string, bearer, valid bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		token, found := strings.CutPrefix(header, "Bearer ")
		return token, true, found && token != ""
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value, false, cookie.Value != ""
	}
	return "", false, false
}

// sessionUser finds the user behind the request's credentials: the session
// cookie from a browser, or the bearer token from hnctl. Both are the same kind
// of thing, a random token whose hash is in web_sessions (D-09, D-36).
func (s *Server) sessionUser(r *http.Request) (u user, bearer, ok bool, err error) {
	token, bearer, valid := requestToken(r)
	if !valid {
		return user{}, bearer, false, nil
	}

	var admin, totpEnabled int
	var lastUsed int64
	now := time.Now()
	// u.disabled = 0: a disabled user's sessions are deleted when they are disabled, and
	// this makes sure none that slipped through still works (D-69).
	err = s.db.QueryRow(
		`SELECT u.id, u.username, u.is_admin, u.display_name, u.totp_enabled, s.last_used_at
		 FROM web_sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ? AND u.disabled = 0`,
		hashSecret(token), now.Unix(),
	).Scan(&u.ID, &u.Username, &admin, &u.DisplayName, &totpEnabled, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return user{}, bearer, false, nil
	}
	if err != nil {
		return user{}, bearer, false, fmt.Errorf("look up session: %w", err)
	}
	u.IsAdmin = admin == 1
	u.TOTPEnabled = totpEnabled == 1
	// "Last used" for the Profile page. Written at most once a minute: the dashboard asks
	// every two seconds, and a database write for each would be a lot for a timestamp.
	if now.Unix()-lastUsed >= 60 {
		if _, err := s.db.Exec(`UPDATE web_sessions SET last_used_at = ? WHERE token_hash = ?`, now.Unix(), hashSecret(token)); err != nil {
			slog.Warn("record session use", "err", err)
		}
	}
	return u, bearer, true, nil
}

// requireLogin wraps every API route except login (D-22): no valid session
// means 401, and a change request from another site means 403.
func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, bearer, ok, err := s.sessionUser(r)
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
		// credentials are valid, and a caller with no login learns nothing else.
		//
		// A bearer token is not checked for Origin, because the check exists for
		// credentials the browser adds by itself. A cookie is sent with every
		// request to this site, including ones another website triggers. A bearer
		// token is only ever sent by code that was given it, so another website
		// cannot make a browser use it (D-36).
		if !bearer && !isSafeMethod(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "bad origin")
			return
		}
		ctx := context.WithValue(r.Context(), userKey{}, u)
		ctx = context.WithValue(ctx, bearerKey{}, bearer)
		next.ServeHTTP(w, r.WithContext(ctx))
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
	// Login responses can hold a TOTP setup secret or a CLI bearer token; never cache them.
	w.Header().Set("Cache-Control", "no-store")

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		// "cli" asks for a bearer token in the reply instead of a cookie.
		Client string `json:"client"`
		// 6-digit RFC 6238 one-time code for step 2 (or first-time setup confirmation).
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	cli := req.Client == "cli"

	// Login is outside requireLogin, so it checks Origin itself. Without this,
	// another website could log the visitor in as an attacker's account: it
	// makes the browser send a login form, and the browser stores the cookie.
	// A command-line login has no cookie to store and its answer cannot be read
	// by another site, so there is nothing to forge, and a program has no
	// Origin to send (D-36).
	if !cli && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad origin")
		return
	}

	// Check the limiter before doing any work: a blocked attempt must be cheap,
	// and it must be refused even if the password happens to be right. begin also
	// counts this attempt as a failure until it succeeds (D-61), so attempts sent
	// all at once cannot slip past together.
	ip := clientIP(r)
	if wait := s.loginLimit.begin(ip); wait > 0 {
		seconds := int((wait + time.Second - 1) / time.Second) // round up
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("too many failed attempts, try again in %d seconds", seconds))
		return
	}

	// bcrypt is slow on purpose, so a flood of logins from many addresses (each with
	// its free attempts) could keep every CPU busy. Only a few checks run at a time;
	// the others wait their turn, and give up if it takes too long (D-61).
	select {
	case s.passwordChecks <- struct{}{}:
	case <-r.Context().Done():
		s.loginLimit.undo(ip)
		return
	case <-time.After(10 * time.Second):
		s.loginLimit.undo(ip)
		writeError(w, http.StatusServiceUnavailable, "the server is busy, try again in a moment")
		return
	}
	u, ok, err := s.checkPassword(req.Username, req.Password)
	<-s.passwordChecks
	if err != nil {
		s.loginLimit.undo(ip)
		slog.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		// Already counted by begin.
		// The username is neither logged nor audited: people sometimes type
		// their password into that field. The address is enough to spot an attack.
		slog.Warn("login failed", "ip", ip)
		if u.disabled {
			// The right password for a disabled account: say so in the audit log (which
			// only administrators read), and tell the caller what they are told about any
			// wrong login.
			s.audit(u.ID, "", "login_failed", "ip="+ip+" account disabled")
		} else {
			s.audit(0, "", "login_failed", "ip="+ip)
		}
		// One message for "no such user" and "wrong password", so the answer
		// does not reveal which usernames exist or whether TOTP is enrolled.
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	// Mandatory TOTP second factor (D-71). Checked only after the password is verified
	// so wrong credentials never reveal whether an account exists or has TOTP set up.
	code := strings.TrimSpace(req.Code)
	if !u.TOTPEnabled {
		if u.totpSecret == "" {
			secret, err := GenerateTOTPSecret()
			if err != nil {
				s.loginLimit.undo(ip)
				slog.Error("generate totp secret", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			res, err := s.db.Exec(
				`UPDATE users SET totp_secret = ? WHERE id = ? AND totp_enabled = 0 AND totp_secret = ''`,
				secret, u.ID,
			)
			if err != nil {
				s.loginLimit.undo(ip)
				slog.Error("store totp secret", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if n, _ := res.RowsAffected(); n == 1 {
				u.totpSecret = secret
			} else {
				var enabled int
				if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled FROM users WHERE id = ?`, u.ID).Scan(&u.totpSecret, &enabled); err != nil {
					s.loginLimit.undo(ip)
					slog.Error("reload totp secret", "err", err)
					writeError(w, http.StatusInternalServerError, "internal error")
					return
				}
				u.TOTPEnabled = enabled == 1
			}
		}
	}

	if u.TOTPEnabled {
		if code == "" {
			// Password-only first step: take back begin's penalty without clearing
			// any prior OTP failures.
			s.loginLimit.undo(ip)
			writeJSON(w, http.StatusOK, map[string]bool{"totp_required": true})
			return
		}
		step, valid := VerifyTOTP(u.totpSecret, code, s.now())
		if !valid {
			slog.Warn("login failed", "ip", ip, "reason", "totp")
			s.audit(u.ID, "", "login_failed", "ip="+ip+" invalid totp")
			writeError(w, http.StatusUnauthorized, "invalid one-time code")
			return
		}
		// Reject replaying a code from the same or an earlier 30-second step.
		res, err := s.db.Exec(
			`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_enabled = 1 AND totp_last_step < ?`,
			step, u.ID, step,
		)
		if err != nil {
			s.loginLimit.undo(ip)
			slog.Error("record totp step", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			slog.Warn("login failed", "ip", ip, "reason", "totp_replay")
			s.audit(u.ID, "", "login_failed", "ip="+ip+" totp replay")
			writeError(w, http.StatusUnauthorized, "invalid one-time code")
			return
		}
	} else {
		if code == "" {
			s.loginLimit.undo(ip)
			writeJSON(w, http.StatusOK, map[string]any{
				"totp_setup":  true,
				"secret":      u.totpSecret,
				"otpauth_uri": TOTPURI(totpIssuer, u.Username, u.totpSecret),
			})
			return
		}
		step, valid := VerifyTOTP(u.totpSecret, code, s.now())
		if !valid {
			slog.Warn("login failed", "ip", ip, "reason", "totp_setup")
			s.audit(u.ID, "", "login_failed", "ip="+ip+" invalid totp")
			writeError(w, http.StatusUnauthorized, "invalid one-time code")
			return
		}
		res, err := s.db.Exec(
			`UPDATE users SET totp_enabled = 1, totp_last_step = ?
			 WHERE id = ? AND totp_enabled = 0 AND totp_secret = ?`,
			step, u.ID, u.totpSecret,
		)
		if err != nil {
			s.loginLimit.undo(ip)
			slog.Error("enable totp", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			slog.Warn("login failed", "ip", ip, "reason", "totp_setup_race")
			s.audit(u.ID, "", "login_failed", "ip="+ip+" invalid totp")
			writeError(w, http.StatusUnauthorized, "invalid one-time code")
			return
		}
		u.TOTPEnabled = true
		s.audit(u.ID, "", "totp_enabled", "ip="+ip)
	}

	client := "browser"
	if cli {
		client = "cli"
	}
	token, err := s.createSession(u.ID, client, ip)
	if err != nil {
		slog.Error("create session", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.loginLimit.reset(ip)
	slog.Info("login", "user", u.Username, "ip", ip, "client", req.Client)

	if cli {
		// hnctl keeps the token in its own file and sends it as a bearer token.
		// No cookie is set, and the token is in this one reply only.
		s.audit(u.ID, "", "login", "ip="+ip+" client=cli")
		writeJSON(w, http.StatusOK, struct {
			user
			Token string `json:"token"`
		}{u, token})
		return
	}
	setSessionCookie(w, r, token, int(sessionLifetime.Seconds()))
	s.audit(u.ID, "", "login", "ip="+ip)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Delete the session on the server, not just the cookie in the browser: a
	// copied cookie or token must stop working too, and any open WebSocket
	// sessions tied to this login must end at once.
	if token, _, valid := requestToken(r); valid {
		tokenHash := hashSecret(token)
		if _, err := s.db.Exec(`DELETE FROM web_sessions WHERE token_hash = ?`, tokenHash); err != nil {
			slog.Error("delete session", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.sessions.endWebSession(tokenHash)
	}
	s.audit(currentUser(r).ID, "", "logout", "")
	if !viaBearer(r) {
		setSessionCookie(w, r, "", -1)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	canUpload, err := s.canUpload(u)
	if err != nil {
		internalError(w, "check permission", err)
		return
	}
	// can_upload tells the page whether to show Transfers: only people who may
	// send files to hosts have anything to do there.
	// The server's version and start time ride along because the page already
	// asks for this on every login. It sends the start time, not the uptime, so
	// the browser can keep counting without asking again.
	writeJSON(w, http.StatusOK, struct {
		user
		CanUpload       bool      `json:"can_upload"`
		ServerVersion   string    `json:"server_version"`
		ServerStartedAt time.Time `json:"server_started_at"`
	}{u, canUpload, Version, s.started.UTC()})
}
