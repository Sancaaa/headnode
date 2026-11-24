package server

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B SHA-1 test vectors use the 20-byte ASCII seed
// "12345678901234567890" and specify 8-digit outputs; the 6-digit TOTP code is
// the last 6 digits of each 8-digit value (dynamic truncation mod 10^6).
func TestTOTPRFC6238SHA1Vectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	vectors := []struct {
		unix int64
		want string // last 6 digits of the RFC 6238 Appendix B 8-digit SHA-1 vector
	}{
		{59, "287082"},          // 94287082
		{1111111109, "081804"},  // 07081804
		{1111111111, "050471"},  // 14050471
		{1234567890, "005924"},  // 89005924
		{2000000000, "279037"},  // 69279037
		{20000000000, "353130"}, // 65353130
	}
	for _, v := range vectors {
		at := time.Unix(v.unix, 0).UTC()
		got, err := TOTPCode(secret, at)
		if err != nil {
			t.Fatalf("TOTPCode(%d): %v", v.unix, err)
		}
		if got != v.want {
			t.Errorf("TOTPCode(%d) = %q, want %q", v.unix, got, v.want)
		}
		step, ok := VerifyTOTP(secret, v.want, at)
		if !ok || step != v.unix/totpPeriod {
			t.Errorf("VerifyTOTP(%d, %q) = (%d, %v), want (%d, true)", v.unix, v.want, step, ok, v.unix/totpPeriod)
		}
	}
}

func TestTOTPWindowAndFormatting(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || strings.Contains(secret, "=") {
		t.Errorf("GenerateTOTPSecret() = %q (len %d), want 32-char unpadded Base32", secret, len(secret))
	}

	base := time.Unix(1_700_000_010, 0).UTC()
	baseStep := base.Unix() / totpPeriod

	// -1, 0, +1 steps are accepted and report their exact matched step.
	for _, delta := range []int64{-1, 0, 1} {
		code, err := TOTPCode(secret, base.Add(time.Duration(delta*totpPeriod)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		step, ok := VerifyTOTP(secret, code, base)
		if !ok || step != baseStep+delta {
			t.Errorf("delta %d: VerifyTOTP = (%d, %v), want (%d, true)", delta, step, ok, baseStep+delta)
		}
	}

	// Spaces and dashes are stripped before comparison.
	cur, _ := TOTPCode(secret, base)
	for _, formatted := range []string{cur[:3] + " " + cur[3:], cur[:3] + "-" + cur[3:], " " + cur + " "} {
		if step, ok := VerifyTOTP(secret, formatted, base); !ok || step != baseStep {
			t.Errorf("VerifyTOTP(%q) = (%d, %v), want (%d, true)", formatted, step, ok, baseStep)
		}
	}

	// -2 and +2 steps are outside the ±1 tolerance window.
	for _, delta := range []int64{-2, 2} {
		code, _ := TOTPCode(secret, base.Add(time.Duration(delta*totpPeriod)*time.Second))
		if _, ok := VerifyTOTP(secret, code, base); ok {
			t.Errorf("delta %d was accepted, want rejection outside ±1 step", delta)
		}
	}

	// Invalid codes and invalid secrets are always rejected.
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12345a", "0000000"} {
		if _, ok := VerifyTOTP(secret, bad, base); ok {
			t.Errorf("VerifyTOTP accepted bad code %q", bad)
		}
	}
	if _, ok := VerifyTOTP("", cur, base); ok {
		t.Error("VerifyTOTP accepted empty secret")
	}
	if _, ok := VerifyTOTP("not!base32!", cur, base); ok {
		t.Error("VerifyTOTP accepted invalid base32 secret")
	}
	if _, err := TOTPCode("not!base32!", base); err == nil {
		t.Error("TOTPCode accepted invalid base32 secret")
	}
}

func TestTOTPURI(t *testing.T) {
	got := TOTPURI("headnode", "alice.smith", "JBSWY3DPEHPK3PXP")
	want := "otpauth://totp/headnode:alice.smith?secret=JBSWY3DPEHPK3PXP&issuer=headnode"
	if got != want {
		t.Errorf("TOTPURI = %q, want %q", got, want)
	}
	escaped := TOTPURI("My App", "a b", "SECRET")
	wantEsc := "otpauth://totp/My%20App:a%20b?secret=SECRET&issuer=My+App"
	if escaped != wantEsc {
		t.Errorf("TOTPURI escaped = %q, want %q", escaped, wantEsc)
	}
}

func TestMandatoryTOTPFirstTimeSetupAndLoginFlow(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	s.now = func() time.Time { return now }

	if err := CreateUser(s.db, "sanca", "correct horse", true); err != nil {
		t.Fatal(err)
	}

	// Wrong password must NOT leak totp_setup or totp_required.
	wrongResp := postLogin(t, http.DefaultClient, srv, "sanca", "wrong password")
	if wrongResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status = %d, want 401", wrongResp.StatusCode)
	}
	wrongBody, _ := io.ReadAll(wrongResp.Body)
	if strings.Contains(string(wrongBody), "totp") || strings.Contains(string(wrongBody), "secret") {
		t.Errorf("wrong password leaked TOTP info: %s", wrongBody)
	}
	if wrongResp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", wrongResp.Header.Get("Cache-Control"))
	}

	// Step 1 for a new user returns totp_setup with secret + otpauth_uri, and NO cookie.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp := postLogin(t, client, srv, "sanca", "correct horse")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 1 setup: status = %d, want 200", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("step 1 setup set a session cookie")
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("step 1 setup Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var setup struct {
		TOTPSetup  bool   `json:"totp_setup"`
		Secret     string `json:"secret"`
		OTPAuthURI string `json:"otpauth_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&setup); err != nil {
		t.Fatal(err)
	}
	if !setup.TOTPSetup || len(setup.Secret) != 32 || !strings.HasPrefix(setup.OTPAuthURI, "otpauth://totp/headnode:sanca?secret="+setup.Secret) {
		t.Fatalf("unexpected setup reply: %+v", setup)
	}

	// Repeating step 1 before completion returns the same secret and does not rate-limit.
	for i := 0; i < 6; i++ {
		again := postLogin(t, client, srv, "sanca", "correct horse")
		if again.StatusCode != http.StatusOK {
			t.Fatalf("repeat step 1 #%d: status = %d, want 200", i, again.StatusCode)
		}
		var s2 struct {
			Secret string `json:"secret"`
		}
		json.NewDecoder(again.Body).Decode(&s2)
		if s2.Secret != setup.Secret {
			t.Fatalf("repeat step 1 returned different secret %q, want %q", s2.Secret, setup.Secret)
		}
	}

	// Invalid code during setup is rejected with 401 and does not enable TOTP.
	badCode := postLoginCode(t, client, srv, "sanca", "correct horse", "000000")
	if badCode.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid setup code: status = %d, want 401", badCode.StatusCode)
	}
	if len(badCode.Cookies()) != 0 {
		t.Error("invalid setup code set a cookie")
	}

	// Valid code completes enrollment and sets the session cookie.
	code1, err := TOTPCode(setup.Secret, now)
	if err != nil {
		t.Fatal(err)
	}
	okResp := postLoginCode(t, client, srv, "sanca", "correct horse", code1)
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("valid setup code: status = %d, want 200", okResp.StatusCode)
	}
	if len(okResp.Cookies()) == 0 {
		t.Fatal("valid setup code did not set session cookie")
	}

	// Neither /api/me nor /api/users leaks totp_secret, and both report totp_enabled=true.
	for _, path := range []string{"/api/me", "/api/users"} {
		r := do(t, client, "GET", srv.URL+path, "")
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), setup.Secret) || strings.Contains(string(raw), "totp_secret") {
			t.Errorf("%s leaked totp_secret: %s", path, raw)
		}
		if !strings.Contains(string(raw), `"totp_enabled":true`) {
			t.Errorf("%s did not include totp_enabled:true: %s", path, raw)
		}
	}

	// Subsequent login: step 1 returns totp_required (not totp_setup) and NO cookie.
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2}
	step1 := postLogin(t, client2, srv, "sanca", "correct horse")
	if step1.StatusCode != http.StatusOK || len(step1.Cookies()) != 0 {
		t.Fatalf("normal step 1: status = %d, cookies = %d, want 200 and 0", step1.StatusCode, len(step1.Cookies()))
	}
	var req2 struct {
		TOTPRequired bool   `json:"totp_required"`
		TOTPSetup    bool   `json:"totp_setup"`
		Secret       string `json:"secret"`
	}
	json.NewDecoder(step1.Body).Decode(&req2)
	if !req2.TOTPRequired || req2.TOTPSetup || req2.Secret != "" {
		t.Fatalf("normal step 1 = %+v, want totp_required only", req2)
	}

	// Replaying the same 30-second step's code is rejected.
	replay := postLoginCode(t, client2, srv, "sanca", "correct horse", code1)
	if replay.StatusCode != http.StatusUnauthorized || len(replay.Cookies()) != 0 {
		t.Fatalf("replayed code: status = %d, cookies = %d, want 401 and 0", replay.StatusCode, len(replay.Cookies()))
	}

	// Next 30-second step succeeds.
	now = now.Add(30 * time.Second)
	code2, _ := TOTPCode(setup.Secret, now)
	ok2 := postLoginCode(t, client2, srv, "sanca", "correct horse", code2)
	if ok2.StatusCode != http.StatusOK || len(ok2.Cookies()) == 0 {
		t.Fatalf("next-step login: status = %d, cookies = %d, want 200 and cookie", ok2.StatusCode, len(ok2.Cookies()))
	}

	// Neither code nor secret ever appears in the audit log.
	for _, row := range auditRows(t, s.db) {
		if strings.Contains(row.Detail, setup.Secret) || strings.Contains(row.Detail, code1) || strings.Contains(row.Detail, code2) {
			t.Errorf("audit log leaked TOTP secret or code: %q", row.Detail)
		}
	}
}

func TestTOTPWrongCodesAreRateLimitedAndStep1DoesNotResetLimiter(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	loginAs(t, s, srv, "sanca", false)

	// 3 wrong codes + step 1 in between must NOT reset the counter; 4th wrong code trips backoff.
	for i := 0; i < 3; i++ {
		resp := postLoginCode(t, http.DefaultClient, srv, "sanca", "correct horse", "000000")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: status = %d, want 401", i+1, resp.StatusCode)
		}
		// Sending password-only step 1 must neither penalize nor erase prior failures.
		if s1 := postLogin(t, http.DefaultClient, srv, "sanca", "correct horse"); s1.StatusCode != http.StatusOK {
			t.Fatalf("step 1 after %d failures: status = %d, want 200", i+1, s1.StatusCode)
		}
	}
	if resp := postLoginCode(t, http.DefaultClient, srv, "sanca", "correct horse", "000000"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("4th wrong code: status = %d, want 401", resp.StatusCode)
	}
	// 5th attempt is rate-limited even with a valid code.
	code := totpCodeForUser(t, s, "sanca")
	if resp := postLoginCode(t, http.DefaultClient, srv, "sanca", "correct horse", code); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("5th attempt after 4 wrong codes: status = %d, want 429", resp.StatusCode)
	}
}

func TestAdminAndSelfTOTPReset(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])
	root := w.clients["root"]
	aliceID := w.user(t, "alice").ID
	rootID := w.user(t, "root").ID

	// Admin cannot reset their own TOTP via the admin endpoint (must use Profile).
	if code := sendJSON(t, root, w.srv, "POST", "/api/users/"+itoa(rootID)+"/totp-reset", nil, nil); code != http.StatusConflict {
		t.Errorf("admin self totp-reset: status = %d, want 409", code)
	}
	if code := sendJSON(t, root, w.srv, "POST", "/api/users/999999/totp-reset", nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown user totp-reset: status = %d, want 404", code)
	}

	// Open an active shell and a CLI token for alice, then have root reset alice's TOTP.
	_, aliceToken, _ := cliLogin(t, w.s, w.srv, "alice", "correct horse")
	b1, a1 := startLiveSession(t, w.srv, w.clients["alice"], control, web, w.secrets["web-01"], shellRequest(80, 24))

	if code := sendJSON(t, root, w.srv, "POST", "/api/users/"+itoa(aliceID)+"/totp-reset", nil, nil); code != http.StatusNoContent {
		t.Fatalf("admin totp-reset for alice: status = %d, want 204", code)
	}
	assertSessionClosed(t, "alice shell after admin totp-reset", b1, a1)
	if code := statusOfGet(t, w.clients["alice"], w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("alice browser after admin totp-reset: status = %d, want 401", code)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", aliceToken, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("alice token after admin totp-reset: status = %d, want 401", code)
	}
	if !auditHas(t, w, "user_totp_reset", "user=alice") {
		t.Error("no user_totp_reset audit entry")
	}

	// Alice's next login requires fresh TOTP setup.
	resp := postLogin(t, &http.Client{}, w.srv, "alice", "correct horse")
	var setup struct {
		TOTPSetup bool   `json:"totp_setup"`
		Secret    string `json:"secret"`
	}
	json.NewDecoder(resp.Body).Decode(&setup)
	if resp.StatusCode != http.StatusOK || !setup.TOTPSetup || setup.Secret == "" {
		t.Fatalf("alice login after reset: status = %d, body = %+v, want totp_setup", resp.StatusCode, setup)
	}

	// Self-service reset for bob: requires current password + valid non-replayed code.
	bob := w.clients["bob"]
	_, bobToken, _ := cliLogin(t, w.s, w.srv, "bob", "correct horse")

	// Wrong password -> 403 (not 401).
	if code := sendJSON(t, bob, w.srv, "POST", "/api/me/totp/reset",
		map[string]string{"password": "wrong password", "code": totpCodeForUser(t, w.s, "bob")}, nil); code != http.StatusForbidden {
		t.Errorf("self reset with wrong password: status = %d, want 403", code)
	}
	// Wrong code -> 403.
	if code := sendJSON(t, bob, w.srv, "POST", "/api/me/totp/reset",
		map[string]string{"password": "correct horse", "code": "000000"}, nil); code != http.StatusForbidden {
		t.Errorf("self reset with wrong code: status = %d, want 403", code)
	}
	// Replaying the code bob just used to log in -> 403.
	var bobSecret string
	var bobLastStep int64
	if err := w.s.db.QueryRow(`SELECT totp_secret, totp_last_step FROM users WHERE username = 'bob'`).Scan(&bobSecret, &bobLastStep); err != nil {
		t.Fatal(err)
	}
	reusedCode, _ := TOTPCode(bobSecret, time.Unix(bobLastStep*totpPeriod, 0))
	w.s.now = func() time.Time { return time.Unix(bobLastStep*totpPeriod, 0) }
	if code := sendJSON(t, bob, w.srv, "POST", "/api/me/totp/reset",
		map[string]string{"password": "correct horse", "code": reusedCode}, nil); code != http.StatusForbidden {
		t.Errorf("self reset with replayed code: status = %d, want 403", code)
	}

	// Valid password + fresh code resets TOTP and ends all sessions.
	freshCode := totpCodeForUser(t, w.s, "bob")
	if code := sendJSON(t, bob, w.srv, "POST", "/api/me/totp/reset",
		map[string]string{"password": "correct horse", "code": freshCode}, nil); code != http.StatusNoContent {
		t.Fatalf("self reset with valid credentials: status = %d, want 204", code)
	}
	if code := statusOfGet(t, bob, w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("bob browser after self reset: status = %d, want 401", code)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", bobToken, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("bob token after self reset: status = %d, want 401", code)
	}
	if !auditHas(t, w, "totp_reset", "sessions_ended=2") {
		t.Error("no totp_reset audit entry")
	}

	// Bob must re-enroll on next login.
	bobNext := postLogin(t, &http.Client{}, w.srv, "bob", "correct horse")
	var bobSetup struct {
		TOTPSetup bool `json:"totp_setup"`
	}
	json.NewDecoder(bobNext.Body).Decode(&bobSetup)
	if bobNext.StatusCode != http.StatusOK || !bobSetup.TOTPSetup {
		t.Errorf("bob next login after self reset: status = %d, totp_setup = %v, want 200 & true", bobNext.StatusCode, bobSetup.TOTPSetup)
	}
}

func TestResetUserTOTPCLIHelper(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	client := loginAs(t, s, srv, "admin", true)

	if err := ResetUserTOTP(s.db, "nobody"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user: err = %v, want ErrUserNotFound", err)
	}
	if err := ResetUserTOTP(s.db, "ADMIN"); err != nil {
		t.Fatalf("ResetUserTOTP: %v", err)
	}
	if resp := do(t, client, "GET", srv.URL+"/api/me", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("session after CLI totp-reset: status = %d, want 401", resp.StatusCode)
	}
	var secret string
	var enabled int
	var lastStep int64
	if err := s.db.QueryRow(`SELECT totp_secret, totp_enabled, totp_last_step FROM users WHERE username = 'admin'`).Scan(&secret, &enabled, &lastStep); err != nil {
		t.Fatal(err)
	}
	if secret != "" || enabled != 0 || lastStep != 0 {
		t.Errorf("after ResetUserTOTP: secret=%q enabled=%d lastStep=%d, want all cleared", secret, enabled, lastStep)
	}
}

