package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// Accounts (M11, D-69). All users of newPermWorld have the password "correct horse".

const newPassword = "a brand new password"

func statusOfGet(t *testing.T, client *http.Client, w *permWorld, path string) int {
	t.Helper()
	return sendJSON(t, client, w.srv, "GET", path, nil, nil)
}

func auditHas(t *testing.T, w *permWorld, action, detailPart string) bool {
	t.Helper()
	for _, row := range auditRows(t, w.s.db) {
		if row.Action == action && strings.Contains(row.Detail, detailPart) {
			return true
		}
	}
	return false
}

func TestChangePasswordEndsTheOtherSessions(t *testing.T) {
	w := newPermWorld(t)
	alice := w.clients["alice"]
	otherBrowser := loginExisting(t, w.srv, "alice")
	_, token, _ := cliLogin(t, w.srv, "alice", "correct horse")

	change := func(current, next string) int {
		return sendJSON(t, alice, w.srv, "POST", "/api/me/password", map[string]string{"current": current, "new": next}, nil)
	}
	if code := change("not it", newPassword); code != http.StatusForbidden {
		t.Errorf("a wrong current password: status %d, want 403 (not 401: that would log the page out)", code)
	}
	if code := change("correct horse", "short"); code != http.StatusBadRequest {
		t.Errorf("a short new password: status %d, want 400", code)
	}
	if code := change("correct horse", "correct horse"); code != http.StatusBadRequest {
		t.Errorf("the same password: status %d, want 400", code)
	}
	// Nothing changed yet: the other sessions still work.
	if code := statusOfGet(t, otherBrowser, w, "/api/me"); code != http.StatusOK {
		t.Fatalf("refused changes ended a session: status %d", code)
	}

	if code := change("correct horse", newPassword); code != http.StatusNoContent {
		t.Fatalf("change: status %d, want 204", code)
	}
	// This session stays; every other one, hnctl tokens included, is over.
	if code := statusOfGet(t, alice, w, "/api/me"); code != http.StatusOK {
		t.Errorf("the session that changed the password: status %d, want 200", code)
	}
	if code := statusOfGet(t, otherBrowser, w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("another browser after the change: status %d, want 401", code)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("an hnctl token after the change: status %d, want 401", code)
	}
	// The new password logs in, the old one does not.
	if resp := postLogin(t, &http.Client{}, w.srv, "alice", "correct horse"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("login with the old password: status %d, want 401", resp.StatusCode)
	}
	if resp := postLogin(t, &http.Client{}, w.srv, "alice", newPassword); resp.StatusCode != http.StatusOK {
		t.Errorf("login with the new password: status %d, want 200", resp.StatusCode)
	}
	if !auditHas(t, w, "password_change", "other_sessions_ended=2") {
		t.Error("no password_change entry that says two sessions ended")
	}
	for _, row := range auditRows(t, w.s.db) {
		if strings.Contains(row.Detail, newPassword) || strings.Contains(row.Detail, "correct horse") {
			t.Errorf("a password is in the audit log: %q", row.Detail)
		}
	}
}

// A stolen login must not be a way to guess the password: the guesses are limited.
func TestChangePasswordGuessesAreLimited(t *testing.T) {
	w := newPermWorld(t)
	var last int
	for i := 0; i < 6; i++ {
		last = sendJSON(t, w.clients["bob"], w.srv, "POST", "/api/me/password",
			map[string]string{"current": "wrong " + string(rune('a'+i)), "new": newPassword}, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after six wrong current passwords: status %d, want 429", last)
	}
}

func TestDisplayName(t *testing.T) {
	w := newPermWorld(t)
	alice := w.clients["alice"]
	set := func(body map[string]any) int {
		return sendJSON(t, alice, w.srv, "PATCH", "/api/me", body, nil)
	}
	if code := set(map[string]any{"display_name": "  Alice Liddell "}); code != http.StatusOK {
		t.Fatalf("set: status %d, want 200", code)
	}
	var me struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	sendJSON(t, alice, w.srv, "GET", "/api/me", nil, &me)
	if me.DisplayName != "Alice Liddell" || me.Username != "alice" {
		t.Errorf("/api/me = %+v: the name is trimmed, the username does not change", me)
	}
	for _, bad := range []map[string]any{
		{}, // the field is required
		{"display_name": strings.Repeat("x", 65)},
		{"display_name": "two\nlines"},
	} {
		if code := set(bad); code != http.StatusBadRequest {
			t.Errorf("PATCH /api/me %v: status %d, want 400", bad, code)
		}
	}
	if code := set(map[string]any{"display_name": ""}); code != http.StatusOK {
		t.Errorf("clearing the name: status %d, want 200", code)
	}
	if !auditHas(t, w, "display_name_set", "Alice Liddell") {
		t.Error("no display_name_set entry")
	}
}

func TestMySessions(t *testing.T) {
	w := newPermWorld(t)
	alice := w.clients["alice"]
	second := loginExisting(t, w.srv, "alice")
	_, token, _ := cliLogin(t, w.srv, "alice", "correct horse")

	var list []sessionInfo
	if code := sendJSON(t, alice, w.srv, "GET", "/api/me/sessions", nil, &list); code != http.StatusOK || len(list) != 3 {
		t.Fatalf("sessions: status %d, %d listed, want 200 and 3 (two browsers and an hnctl token)", code, len(list))
	}
	var current, cli int
	var otherBrowserID, currentID int64
	for _, info := range list {
		if info.Current {
			current++
			currentID = info.ID
		}
		if info.Client == "cli" {
			cli++
		} else if !info.Current {
			otherBrowserID = info.ID
		}
		if info.IP == "" || info.LastUsedAt.IsZero() {
			t.Errorf("a session without an address or a last use: %+v", info)
		}
	}
	if current != 1 || cli != 1 {
		t.Errorf("%d current and %d hnctl sessions, want 1 and 1", current, cli)
	}

	// Nobody else's sessions are visible or touchable: bob's id is "not found" for alice.
	var bobs []sessionInfo
	sendJSON(t, w.clients["bob"], w.srv, "GET", "/api/me/sessions", nil, &bobs)
	if len(bobs) != 1 {
		t.Fatalf("bob has %d sessions, want 1", len(bobs))
	}
	if code := sendJSON(t, alice, w.srv, "DELETE", "/api/me/sessions/"+itoa(bobs[0].ID), nil, nil); code != http.StatusNotFound {
		t.Errorf("signing out somebody else's session: status %d, want 404", code)
	}
	if code := statusOfGet(t, w.clients["bob"], w, "/api/me"); code != http.StatusOK {
		t.Error("alice ended bob's session")
	}

	// The one in use is Log out, not this.
	if code := sendJSON(t, alice, w.srv, "DELETE", "/api/me/sessions/"+itoa(currentID), nil, nil); code != http.StatusBadRequest {
		t.Errorf("signing out the session in use: status %d, want 400", code)
	}
	// Another browser, by its id.
	if code := sendJSON(t, alice, w.srv, "DELETE", "/api/me/sessions/"+itoa(otherBrowserID), nil, nil); code != http.StatusNoContent {
		t.Fatalf("signing out another browser: status %d, want 204", code)
	}
	if code := statusOfGet(t, second, w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("the browser that was signed out: status %d, want 401", code)
	}

	// Everywhere else: the hnctl token goes, this session stays.
	var reply struct {
		Ended int64 `json:"ended"`
	}
	if code := sendJSON(t, alice, w.srv, "POST", "/api/me/sessions/revoke-others", nil, &reply); code != http.StatusOK || reply.Ended != 1 {
		t.Errorf("sign out everywhere else: status %d, ended %d, want 200 and 1", code, reply.Ended)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("the hnctl token: status %d, want 401", code)
	}
	if code := statusOfGet(t, alice, w, "/api/me"); code != http.StatusOK {
		t.Errorf("this session after signing out the others: status %d, want 200", code)
	}
	if !auditHas(t, w, "sessions_revoke_others", "ended=1") || !auditHas(t, w, "session_revoke", "client=browser") {
		t.Error("the sign-outs are not in the audit log")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestAdminResetsAPassword(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]
	bobID := w.user(t, "bob").ID
	path := func(id int64) string { return "/api/users/" + itoa(id) + "/password" }

	_, token, _ := cliLogin(t, w.srv, "bob", "correct horse")
	if code := sendJSON(t, root, w.srv, "POST", path(bobID), map[string]string{"password": "short"}, nil); code != http.StatusBadRequest {
		t.Errorf("a short password: status %d, want 400", code)
	}
	if code := sendJSON(t, root, w.srv, "POST", path(999999), map[string]string{"password": newPassword}, nil); code != http.StatusNotFound {
		t.Errorf("an unknown user: status %d, want 404", code)
	}
	// An administrator's own password needs the current one: the Profile page.
	if code := sendJSON(t, root, w.srv, "POST", path(w.user(t, "root").ID), map[string]string{"password": newPassword}, nil); code != http.StatusConflict {
		t.Errorf("resetting one's own password: status %d, want 409", code)
	}

	if code := sendJSON(t, root, w.srv, "POST", path(bobID), map[string]string{"password": newPassword}, nil); code != http.StatusNoContent {
		t.Fatalf("reset: status %d, want 204", code)
	}
	if code := statusOfGet(t, w.clients["bob"], w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("bob's browser after the reset: status %d, want 401", code)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("bob's hnctl token after the reset: status %d, want 401", code)
	}
	if resp := postLogin(t, &http.Client{}, w.srv, "bob", newPassword); resp.StatusCode != http.StatusOK {
		t.Errorf("login with the new password: status %d, want 200", resp.StatusCode)
	}
	if !auditHas(t, w, "password_reset", "user=bob") {
		t.Error("no password_reset entry")
	}
	for _, row := range auditRows(t, w.s.db) {
		if strings.Contains(row.Detail, newPassword) {
			t.Error("the new password is in the audit log")
		}
	}
}

func TestDisableAndEnableAUser(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]
	carol := w.user(t, "carol")
	_, token, _ := cliLogin(t, w.srv, "carol", "correct horse")
	patch := func(id int64, body map[string]any) int {
		return sendJSON(t, root, w.srv, "PATCH", "/api/users/"+itoa(id), body, nil)
	}

	if code := patch(carol.ID, map[string]any{"disabled": true}); code != http.StatusOK {
		t.Fatalf("disable: status %d, want 200", code)
	}
	// Logged out everywhere, tokens included, and unable to log in again: with the
	// same answer as a wrong password.
	if code := statusOfGet(t, w.clients["carol"], w, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("carol's browser: status %d, want 401", code)
	}
	if code := withToken(t, "GET", w.srv.URL+"/api/me", token, nil, nil); code != http.StatusUnauthorized {
		t.Errorf("carol's token: status %d, want 401", code)
	}
	resp := postLogin(t, &http.Client{}, w.srv, "carol", "correct horse")
	var body struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || body.Error != "invalid username or password" {
		t.Errorf("login of a disabled user: status %d %q, want the usual 401", resp.StatusCode, body.Error)
	}
	if !auditHas(t, w, "user_disable", "user=carol") || !auditHas(t, w, "login_failed", "account disabled") {
		t.Error("the disable and the refused login are not in the audit log")
	}
	var users []userRow
	sendJSON(t, root, w.srv, "GET", "/api/users", nil, &users)
	for _, u := range users {
		if u.ID == carol.ID && !u.Disabled {
			t.Error("the user list does not say carol is disabled")
		}
	}

	// Enabled again: the same password works. Nothing was deleted.
	if code := patch(carol.ID, map[string]any{"disabled": false}); code != http.StatusOK {
		t.Fatalf("enable: status %d, want 200", code)
	}
	if resp := postLogin(t, &http.Client{}, w.srv, "carol", "correct horse"); resp.StatusCode != http.StatusOK {
		t.Errorf("login after enabling: status %d, want 200", resp.StatusCode)
	}
	if !auditHas(t, w, "user_enable", "user=carol") {
		t.Error("no user_enable entry")
	}
	// Doing what is already so changes nothing and is not audited again.
	before := len(auditRows(t, w.s.db))
	patch(carol.ID, map[string]any{"disabled": false})
	if len(auditRows(t, w.s.db)) != before {
		t.Error("a change that changed nothing was audited")
	}
}

// Disabling a user cuts off what they have open right now, like deleting a host.
func TestDisablingAUserEndsTheirOpenShell(t *testing.T) {
	w := newPermWorld(t)
	web := w.hosts["web-01"]
	control := connectAgent(t, w.s, w.srv, web, w.secrets["web-01"])

	code, sid := openSession(t, w.clients["alice"], w.srv, web, shellRequest(80, 24))
	if code != http.StatusCreated {
		t.Fatalf("open: status %d, want 201", code)
	}
	readOpen(t, control)
	agent, _, err := dialAgentSession(w.srv, web, w.secrets["web-01"], sid)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.CloseNow()
	browser, _, err := dialBrowserSession(w.srv, w.clients["alice"], sid, w.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer browser.CloseNow()
	writeFrame(t, browser, websocket.MessageBinary, "ls\n") // it works
	readFrame(t, agent)

	if code := sendJSON(t, w.clients["root"], w.srv, "PATCH", "/api/users/"+itoa(w.user(t, "alice").ID), map[string]any{"disabled": true}, nil); code != http.StatusOK {
		t.Fatalf("disable: status %d, want 200", code)
	}
	// Both ends of the shell are closed, so the agent can kill the process.
	if err := readError(browser); err == nil {
		t.Error("the browser side of the shell is still open")
	}
	if err := readError(agent); err == nil {
		t.Error("the agent side of the shell is still open")
	}
	if !auditHas(t, w, "user_disable", "open_sessions_ended=1") {
		t.Error("the audit entry does not say that an open session was ended")
	}
}

func TestAdminGuards(t *testing.T) {
	w := newPermWorld(t)
	root := w.clients["root"]
	rootID := w.user(t, "root").ID
	patch := func(client *http.Client, id int64, body map[string]any) int {
		return sendJSON(t, client, w.srv, "PATCH", "/api/users/"+itoa(id), body, nil)
	}
	// Nobody disables or demotes themselves.
	if code := patch(root, rootID, map[string]any{"disabled": true}); code != http.StatusConflict {
		t.Errorf("disabling yourself: status %d, want 409", code)
	}
	if code := patch(root, rootID, map[string]any{"is_admin": false}); code != http.StatusConflict {
		t.Errorf("demoting yourself: status %d, want 409", code)
	}
	if code := patch(root, rootID, map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("nothing to change: status %d, want 400", code)
	}
	if code := patch(root, 999999, map[string]any{"disabled": true}); code != http.StatusNotFound {
		t.Errorf("an unknown user: status %d, want 404", code)
	}

	// A second administrator: the first can demote and disable them, and they can
	// do the same to the first, but not both: the last enabled administrator stays.
	aliceID := w.user(t, "alice").ID
	if code := patch(root, aliceID, map[string]any{"is_admin": true}); code != http.StatusOK {
		t.Fatalf("make alice an administrator: status %d, want 200", code)
	}
	if !auditHas(t, w, "admin_grant", "user=alice") {
		t.Error("no admin_grant entry")
	}
	alice := w.clients["alice"]
	if code := sendJSON(t, alice, w.srv, "GET", "/api/users", nil, nil); code != http.StatusOK {
		t.Errorf("alice is an administrator now, but GET /api/users: status %d", code)
	}
	if code := patch(alice, rootID, map[string]any{"disabled": true}); code != http.StatusOK {
		t.Fatalf("alice disables root: status %d, want 200", code)
	}
	// root is disabled; alice is the last enabled administrator, and she cannot demote herself.
	if code := patch(alice, aliceID, map[string]any{"is_admin": false}); code != http.StatusConflict {
		t.Errorf("the last administrator demoting herself: status %d, want 409", code)
	}
	if code := patch(alice, rootID, map[string]any{"disabled": false, "is_admin": false}); code != http.StatusOK {
		t.Errorf("enabling root and removing their admin rights: status %d, want 200 (alice stays an administrator)", code)
	}
	if code := patch(alice, rootID, map[string]any{"is_admin": true}); code != http.StatusOK {
		t.Errorf("making root an administrator again: status %d, want 200", code)
	}
	if code := patch(alice, rootID, map[string]any{"is_admin": false}); code != http.StatusOK {
		t.Errorf("demoting root while alice is an enabled administrator: status %d, want 200", code)
	}
	if !auditHas(t, w, "admin_revoke", "user=root") || !auditHas(t, w, "user_disable", "user=root") {
		t.Error("the demotion and the disable are not in the audit log")
	}
}

// The last-administrator guard counts the OTHER enabled administrators. Over HTTP it can
// only come out 0 when two administrators act at once (the caller is always an enabled
// administrator who is not the target), so the count behind it is tested on its own.
func TestOtherEnabledAdmins(t *testing.T) {
	w := newPermWorld(t) // root is the only administrator
	rootID, aliceID := w.user(t, "root").ID, w.user(t, "alice").ID

	count := func(excluding int64) int {
		t.Helper()
		n, err := otherEnabledAdmins(w.s.db, excluding)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(rootID); n != 0 {
		t.Errorf("others besides the only administrator: %d, want 0 (so root must stay)", n)
	}
	if n := count(aliceID); n != 1 {
		t.Errorf("others besides a plain user: %d, want 1 (root)", n)
	}
	mustExec(t, w.s.db, `UPDATE users SET is_admin = 1 WHERE id = ?`, aliceID)
	if n := count(rootID); n != 1 {
		t.Errorf("with a second administrator: %d, want 1", n)
	}
	mustExec(t, w.s.db, `UPDATE users SET disabled = 1 WHERE id = ?`, aliceID)
	if n := count(rootID); n != 0 {
		t.Errorf("a disabled administrator counts: %d, want 0", n)
	}
}
