package server

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// postLogin sends a login request the way the browser page does, with a
// matching Origin header.
func postLogin(t *testing.T, client *http.Client, srv *httptest.Server, username, password string, headers ...[2]string) *http.Response {
	t.Helper()
	return postLoginCode(t, client, srv, username, password, "", headers...)
}

func postLoginCode(t *testing.T, client *http.Client, srv *httptest.Server, username, password, code string, headers ...[2]string) *http.Response {
	t.Helper()
	payload := map[string]string{"username": username, "password": password}
	if code != "" {
		payload["code"] = code
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", srv.URL+"/api/login", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// totpCodeForUser returns a fresh valid 6-digit code for username, moving the
// server's TOTP clock forward one step if that step was already used in this test.
func totpCodeForUser(t *testing.T, s *Server, username string) string {
	t.Helper()
	var secret string
	var lastStep int64
	err := s.db.QueryRow(
		`SELECT totp_secret, totp_last_step FROM users WHERE username = ?`,
		normalizeUsername(username),
	).Scan(&secret, &lastStep)
	if err != nil {
		t.Fatalf("read totp for %s: %v", username, err)
	}
	now := s.now()
	if lastStep >= now.Unix()/totpPeriod {
		next := time.Unix((lastStep+1)*totpPeriod, 0)
		s.now = func() time.Time { return next }
		now = next
	}
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("generate totp for %s: %v", username, err)
	}
	return code
}

// completeLogin runs the full two-step login (password, then TOTP setup or code)
// and returns the final HTTP response.
func completeLogin(t *testing.T, s *Server, client *http.Client, srv *httptest.Server, username, password string, headers ...[2]string) *http.Response {
	t.Helper()
	resp := postLogin(t, client, srv, username, password, headers...)
	if resp.StatusCode != http.StatusOK {
		return resp
	}
	var step1 struct {
		TOTPRequired bool   `json:"totp_required"`
		TOTPSetup    bool   `json:"totp_setup"`
		Secret       string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&step1); err != nil {
		t.Fatalf("decode login step 1: %v", err)
	}
	var code string
	switch {
	case step1.TOTPSetup:
		var err error
		code, err = TOTPCode(step1.Secret, s.now())
		if err != nil {
			t.Fatalf("generate setup totp: %v", err)
		}
	case step1.TOTPRequired:
		code = totpCodeForUser(t, s, username)
	default:
		t.Fatalf("login step 1 without OTP succeeded unexpectedly")
	}
	return postLoginCode(t, client, srv, username, password, code, headers...)
}

// loginAs creates a user and returns an HTTP client holding a valid session
// cookie for them, like a browser after logging in.
func loginAs(t *testing.T, s *Server, srv *httptest.Server, username string, isAdmin bool) *http.Client {
	t.Helper()
	if err := CreateUser(s.db, username, "correct horse", isAdmin); err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	if resp := completeLogin(t, s, client, srv, username, "correct horse"); resp.StatusCode != http.StatusOK {
		t.Fatalf("login as %s: status = %d, want 200", username, resp.StatusCode)
	}
	return client
}

// loggedInClient is a client logged in as an administrator.
func loggedInClient(t *testing.T, s *Server, srv *httptest.Server) *http.Client {
	t.Helper()
	return loginAs(t, s, srv, "tester", true)
}

// do sends a request with an optional Origin header. Pass "" to send none.
func do(t *testing.T, client *http.Client, method, url, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAPIRequiresLogin(t *testing.T) {
	_, srv, _, _ := newTestServer(t)

	// The roadmap's done criterion: without login, every /api/* returns 401.
	// That includes paths that do not exist, so a forgotten route cannot leak.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/hosts"},
		{"GET", "/api/hosts/a_whatever"},
		{"GET", "/api/me"},
		{"POST", "/api/logout"},
		{"GET", "/api/does-not-exist"},
		{"DELETE", "/api/hosts/a_whatever"},
		{"PATCH", "/api/hosts/a_whatever"},
	} {
		resp := do(t, http.DefaultClient, c.method, srv.URL+c.path, srv.URL)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s without login: status = %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestLoginAndLogout(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", true); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Wrong password and unknown user get the same answer, and no cookie.
	for _, c := range [][2]string{{"sanca", "wrong password"}, {"nobody", "correct horse"}} {
		resp := postLogin(t, client, srv, c[0], c[1])
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("login %v: status = %d, want 401", c, resp.StatusCode)
		}
		if len(resp.Cookies()) != 0 {
			t.Errorf("login %v: server set a cookie on a failed login", c)
		}
	}

	resp := completeLogin(t, s, client, srv, "SANCA", "correct horse") // the name is case-insensitive
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "hn_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no hn_session cookie after login")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Secure {
		t.Errorf("cookie HttpOnly=%v SameSite=%v Secure=%v, want true / Lax / false (plain HTTP)",
			session.HttpOnly, session.SameSite, session.Secure)
	}

	// The database holds the hash of the token, never the token.
	if n := count(t, s.db, `SELECT COUNT(*) FROM web_sessions WHERE token_hash = ?`, session.Value); n != 0 {
		t.Error("plain-text session token found in the database")
	}
	if n := count(t, s.db, `SELECT COUNT(*) FROM web_sessions WHERE token_hash = ?`, hashSecret(session.Value)); n != 1 {
		t.Errorf("sessions with the token's hash = %d, want 1", n)
	}

	// /api/me works while logged in.
	if resp := do(t, client, "GET", srv.URL+"/api/me", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/me status = %d, want 200", resp.StatusCode)
	}

	// Logout ends the session on the server too. Replaying the old cookie by
	// hand proves it: clearing the browser's copy alone would not.
	if resp := do(t, client, "POST", srv.URL+"/api/logout", srv.URL); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/me", nil)
	req.AddCookie(session)
	replay, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Errorf("old cookie after logout: status = %d, want 401", replay.StatusCode)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	client := loggedInClient(t, s, srv)

	// Make every session look a minute old.
	if _, err := s.db.Exec(`UPDATE web_sessions SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if resp := do(t, client, "GET", srv.URL+"/api/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired session: status = %d, want 401", resp.StatusCode)
	}
}

func TestOriginIsChecked(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	client := loggedInClient(t, s, srv)

	// Reading needs no Origin: a plain GET cannot change anything.
	if resp := do(t, client, "GET", srv.URL+"/api/me", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("GET without Origin: status = %d, want 200", resp.StatusCode)
	}

	// A change from another site, or with no Origin at all, is refused.
	for _, origin := range []string{"", "http://evil.example", "null"} {
		resp := do(t, client, "POST", srv.URL+"/api/logout", origin)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST with Origin %q: status = %d, want 403", origin, resp.StatusCode)
		}
	}
	// ...and the session must still be alive, since none of those went through.
	if resp := do(t, client, "GET", srv.URL+"/api/me", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("after refused logouts: status = %d, want 200", resp.StatusCode)
	}

	if resp := do(t, client, "POST", srv.URL+"/api/logout", srv.URL); resp.StatusCode != http.StatusNoContent {
		t.Errorf("POST with the right Origin: status = %d, want 204", resp.StatusCode)
	}
}

func TestLoginChecksOrigin(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}

	// Even with the right password: another site must not be able to log the
	// visitor in (login CSRF).
	resp := postLogin(t, http.DefaultClient, srv, "sanca", "correct horse", [2]string{"Origin", "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}

	// Three wrong passwords are free, and so is the fourth attempt (it is the
	// failure that starts the penalty).
	for i := 1; i <= 4; i++ {
		resp := postLogin(t, http.DefaultClient, srv, "sanca", "wrong password")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, resp.StatusCode)
		}
	}

	// The fifth comes right away: refused, even with the correct password.
	resp := postLogin(t, http.DefaultClient, srv, "sanca", "correct horse")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempt 5: status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("429 response has no Retry-After header")
	}
}

func TestLoginRateLimitUsesForwardedAddressFromProxy(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}

	// Behind Caddy every request comes from 127.0.0.1. One client's failures
	// must not lock out another client.
	attacker := [2]string{"X-Forwarded-For", "203.0.113.66"}
	for i := 0; i < 4; i++ {
		postLogin(t, http.DefaultClient, srv, "sanca", "wrong password", attacker)
	}
	if resp := postLogin(t, http.DefaultClient, srv, "sanca", "correct horse", attacker); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("attacker address: status = %d, want 429", resp.StatusCode)
	}
	other := [2]string{"X-Forwarded-For", "198.51.100.7"}
	if resp := postLogin(t, http.DefaultClient, srv, "sanca", "correct horse", other); resp.StatusCode != http.StatusOK {
		t.Errorf("another client behind the same proxy: status = %d, want 200", resp.StatusCode)
	}
}

func TestSessionCookieIsSecureBehindHTTPSProxy(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}

	httpsOrigin := strings.Replace(srv.URL, "http://", "https://", 1)
	resp := completeLogin(t, s, http.DefaultClient, srv, "sanca", "correct horse",
		[2]string{"X-Forwarded-Proto", "https"},
		[2]string{"Origin", httpsOrigin})
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("cookies = %+v, want one cookie with Secure set", cookies)
	}
}

func TestSameOriginChecksScheme(t *testing.T) {
	cases := []struct {
		name      string
		origin    string
		proto     string
		directTLS bool
		remote    string
		want      bool
	}{
		{"plain HTTP matches http origin", "http://headnode.example.com", "", false, "203.0.113.9:1234", true},
		{"plain HTTP rejects https origin", "https://headnode.example.com", "", false, "203.0.113.9:1234", false},
		{"HTTPS via proxy matches https origin", "https://headnode.example.com", "https", false, "127.0.0.1:1234", true},
		{"HTTPS via proxy rejects http origin", "http://headnode.example.com", "https", false, "127.0.0.1:1234", false},
		{"direct TLS matches https origin", "https://headnode.example.com", "", true, "203.0.113.9:1234", true},
		{"direct TLS rejects http origin", "http://headnode.example.com", "", true, "203.0.113.9:1234", false},
		{"non-loopback X-Forwarded-Proto is ignored", "http://headnode.example.com", "https", false, "203.0.113.9:1234", true},
		{"missing origin is rejected", "", "", false, "127.0.0.1:1234", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", "/api/logout", nil)
		r.Host = "headnode.example.com"
		r.RemoteAddr = c.remote
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.directTLS {
			r.TLS = &tls.ConnectionState{}
		}
		if got := sameOrigin(r); got != c.want {
			t.Errorf("%s: sameOrigin = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCreateUser(t *testing.T) {
	db := openTestDB(t)

	// Mixed case and spaces are normalized.
	if err := CreateUser(db, "  Admin ", "correct horse", true); err != nil {
		t.Fatal(err)
	}

	var username, hash string
	var isAdmin int
	err := db.QueryRow(`SELECT username, password_hash, is_admin FROM users`).Scan(&username, &hash, &isAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if username != "admin" || isAdmin != 1 {
		t.Errorf("stored username=%q is_admin=%d, want admin / 1", username, isAdmin)
	}

	// The database must hold a hash that accepts the right password only.
	if strings.Contains(hash, "correct horse") {
		t.Error("plain-text password found in the database")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse")) != nil {
		t.Error("stored hash does not match the password")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("wrong password")) == nil {
		t.Error("stored hash accepts a wrong password")
	}
}

func TestCreateUserDuplicateIsCaseInsensitive(t *testing.T) {
	db := openTestDB(t)
	if err := CreateUser(db, "sanca", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(db, "SANCA", "another password", false); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("err = %v, want ErrUsernameTaken", err)
	}
}

func TestCreateUserRejectsBadInput(t *testing.T) {
	db := openTestDB(t)

	for _, name := range []string{"", "   ", "has space", "a/b", "ünïcode", strings.Repeat("a", 33)} {
		if err := CreateUser(db, name, "correct horse", false); !errors.Is(err, ErrInvalidUsername) {
			t.Errorf("username %q: err = %v, want ErrInvalidUsername", name, err)
		}
	}

	if err := CreateUser(db, "short", "1234567", false); err == nil {
		t.Error("7-byte password accepted")
	}
	// 73 bytes would be silently cut by bcrypt, so it must be refused.
	if err := CreateUser(db, "long", strings.Repeat("x", 73), false); err == nil {
		t.Error("73-byte password accepted")
	}
	if err := CreateUser(db, "limit", strings.Repeat("x", 72), false); err != nil {
		t.Errorf("72-byte password rejected: %v", err)
	}
}
