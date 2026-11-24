package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"headnode/protocol"
)

// ------------------------------------------------------------- helpers

// sendJSON sends a request as a logged-in client, with the Origin header the
// server demands for changes, and decodes a successful JSON reply into into.
func sendJSON(t *testing.T, client *http.Client, srv *httptest.Server, method, path string, body, into any) int {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if into != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// loginExisting logs in a user that already exists and returns their client.
func loginExisting(t *testing.T, s *Server, srv *httptest.Server, username string) *http.Client {
	t.Helper()
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

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func addGroup(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO groups (name) VALUES (?)`, name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addHost enrolls a host the real way and puts it in a group (0 = no group).
func addHost(t *testing.T, db *sql.DB, name string, groupID int64) string {
	t.Helper()
	id, _ := addHostWithSecret(t, db, name, groupID)
	return id
}

// addHostWithSecret is addHost for tests that need the host to connect: it also
// returns the agent's secret.
func addHostWithSecret(t *testing.T, db *sql.DB, name string, groupID int64) (id, secret string) {
	t.Helper()
	token, _, err := CreateEnrollToken(db)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := EnrollAgent(db, protocol.EnrollRequest{Token: token, Hostname: name, OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if groupID != 0 {
		mustExec(t, db, `UPDATE agents SET group_id = ? WHERE id = ?`, groupID, resp.AgentID)
	}
	return resp.AgentID, resp.AgentSecret
}

func userIDOf(t *testing.T, db *sql.DB, username string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func addMember(t *testing.T, db *sql.DB, username string, groupID int64, role string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO group_members (user_id, group_id, role) VALUES (?, ?, ?)`,
		userIDOf(t, db, username), groupID, role)
}

// permWorld is a small company: two groups, three hosts, five people.
//
//	Office group: web-01   operator alice, viewer bob
//	Lab group:    lab-01   viewer carol
//	no group:     orphan
//	root is an admin, dave belongs to no group.
type permWorld struct {
	s       *Server
	srv     *httptest.Server
	clients map[string]*http.Client
	hosts   map[string]string // host name -> agent id
	secrets map[string]string // host name -> agent secret, for tests that connect the agent
	groups  map[string]int64  // group name -> id
}

func newPermWorld(t *testing.T) *permWorld {
	t.Helper()
	db := openTestDB(t)
	s := New(db)
	if err := s.UseFilesDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	office := addGroup(t, db, "Office")
	lab := addGroup(t, db, "Lab")
	w := &permWorld{
		s: s, srv: srv, clients: map[string]*http.Client{},
		hosts: map[string]string{}, secrets: map[string]string{},
		groups: map[string]int64{"Office": office, "Lab": lab},
	}
	for name, group := range map[string]int64{"web-01": office, "lab-01": lab, "orphan": 0} {
		w.hosts[name], w.secrets[name] = addHostWithSecret(t, db, name, group)
	}

	for name, admin := range map[string]bool{"root": true, "alice": false, "bob": false, "carol": false, "dave": false} {
		w.clients[name] = loginAs(t, s, srv, name, admin)
	}
	addMember(t, db, "alice", office, roleOperator)
	addMember(t, db, "bob", office, roleViewer)
	addMember(t, db, "carol", lab, roleViewer)
	return w
}

func (w *permWorld) user(t *testing.T, name string) user {
	t.Helper()
	var u user
	var admin int
	err := w.s.db.QueryRow(`SELECT id, username, is_admin FROM users WHERE username = ?`, name).Scan(&u.ID, &u.Username, &admin)
	if err != nil {
		t.Fatal(err)
	}
	u.IsAdmin = admin == 1
	return u
}

// ---------------------------------------------------------- permissions

func TestRoleAllows(t *testing.T) {
	// The table from architecture.md §6.
	cases := []struct {
		role   string
		action action
		want   bool
	}{
		{roleOperator, actionView, true},
		{roleOperator, actionShell, true},
		{roleOperator, actionFilesRead, true},
		{roleOperator, actionFilesWrite, true},
		{roleViewer, actionView, true},
		{roleViewer, actionShell, false},
		{roleViewer, actionFilesRead, true},
		{roleViewer, actionFilesWrite, false},
		// No role, an unknown role, or an unknown action gets nothing.
		{"", actionView, false},
		{"owner", actionView, false},
		{roleViewer, "delete-all", false},
	}
	for _, c := range cases {
		if got := roleAllows(c.role, c.action); got != c.want {
			t.Errorf("roleAllows(%q, %q) = %v, want %v", c.role, c.action, got, c.want)
		}
	}
}

func TestCan(t *testing.T) {
	w := newPermWorld(t)
	web, lab, orphan := w.hosts["web-01"], w.hosts["lab-01"], w.hosts["orphan"]

	cases := []struct {
		who    string
		host   string
		action action
		want   bool
	}{
		// An admin may do everything, everywhere, even to a host with no group.
		{"root", web, actionShell, true},
		{"root", orphan, actionFilesWrite, true},
		// alice is an operator in Office, and has nothing in Lab.
		{"alice", web, actionShell, true},
		{"alice", web, actionFilesWrite, true},
		{"alice", lab, actionView, false},
		// bob is a viewer in Office: he may look and read, not use a shell or change files.
		{"bob", web, actionView, true},
		{"bob", web, actionFilesRead, true},
		{"bob", web, actionShell, false},
		{"bob", web, actionFilesWrite, false},
		{"carol", lab, actionView, true},
		{"carol", web, actionView, false},
		// dave is in no group at all.
		{"dave", web, actionView, false},
		{"dave", orphan, actionView, false},
		// A host with no group is admin-only, even for an operator elsewhere.
		{"alice", orphan, actionView, false},
	}
	for _, c := range cases {
		got, err := w.s.can(w.user(t, c.who), c.host, c.action)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("can(%s, host %s, %s) = %v, want %v", c.who, c.host, c.action, got, c.want)
		}
	}
}

func TestHostVisibility(t *testing.T) {
	w := newPermWorld(t)

	visible := map[string][]string{
		"root":  {"lab-01", "orphan", "web-01"},
		"alice": {"web-01"},
		"bob":   {"web-01"},
		"carol": {"lab-01"},
		"dave":  {},
	}
	for who, want := range visible {
		client := w.clients[who]

		// The list shows exactly the hosts the user may see...
		var list []host
		if code := getJSON(t, client, w.srv, "/api/hosts", &list); code != http.StatusOK {
			t.Fatalf("%s: list status = %d", who, code)
		}
		var got []string
		for _, h := range list {
			got = append(got, h.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s sees %v, want %v", who, got, want)
		}

		// can_shell is what the Hosts page uses to decide who gets a Shell
		// button. It must say exactly what can() says, or the button would be
		// offered to people the server then refuses.
		for _, h := range list {
			allowed, err := w.s.can(w.user(t, who), w.hosts[h.Name], actionShell)
			if err != nil {
				t.Fatal(err)
			}
			if h.CanShell != allowed {
				t.Errorf("%s: can_shell for %s = %v, but can(shell) = %v", who, h.Name, h.CanShell, allowed)
			}
			// The same for sending files: the Send dialog lists hosts by this flag.
			canWrite, err := w.s.can(w.user(t, who), w.hosts[h.Name], actionFilesWrite)
			if err != nil {
				t.Fatal(err)
			}
			if h.CanFilesWrite != canWrite {
				t.Errorf("%s: can_files_write for %s = %v, but can(files_write) = %v", who, h.Name, h.CanFilesWrite, canWrite)
			}
		}

		// ...the detail page agrees, and a hidden host is "not found", not
		// "forbidden", so its existence is not revealed...
		for name, id := range w.hosts {
			code := getJSON(t, client, w.srv, "/api/hosts/"+id, nil)
			wantCode := http.StatusNotFound
			if slices.Contains(want, name) {
				wantCode = http.StatusOK
			}
			if code != wantCode {
				t.Errorf("%s GET host %s: status = %d, want %d", who, name, code, wantCode)
			}
		}

		// ...and the SQL filter of the list agrees with can(view), the single
		// source of truth.
		u := w.user(t, who)
		for name, id := range w.hosts {
			allowed, err := w.s.can(u, id, actionView)
			if err != nil {
				t.Fatal(err)
			}
			if allowed != slices.Contains(want, name) {
				t.Errorf("can(%s, %s, view) = %v disagrees with the list", who, name, allowed)
			}
		}
	}
}

// ------------------------------------------------------------ admin API

func TestAdminRoutesRequireAdmin(t *testing.T) {
	w := newPermWorld(t)
	usersBefore := count(t, w.s.db, `SELECT COUNT(*) FROM users`)

	// Every admin-only route. If a new one is added in http.go, add it here.
	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/users", nil},
		{"POST", "/api/users", map[string]any{"username": "mallory", "password": "correct horse", "is_admin": true}},
		{"GET", "/api/groups", nil},
		{"POST", "/api/groups", map[string]any{"name": "Evil"}},
		{"GET", "/api/groups/1", nil},
		{"PUT", "/api/groups/1/members/1", map[string]any{"role": "operator"}},
		{"DELETE", "/api/groups/1/members/1", nil},
		{"POST", "/api/enroll-tokens", map[string]any{"group_id": 1}},
		{"POST", "/api/enroll-tokens", map[string]any{}},
		{"GET", "/api/audit", nil},
		{"PATCH", "/api/hosts/a_nobody", map[string]any{"name": "x"}},
		{"DELETE", "/api/hosts/a_nobody", nil},
		{"GET", "/api/files", nil},
		{"DELETE", "/api/files/f_nobody", nil},
		{"POST", "/api/users/1/password", map[string]any{"password": "a new password"}},
		{"POST", "/api/users/1/totp-reset", nil},
		{"PATCH", "/api/users/1", map[string]any{"disabled": true}},
	}

	// alice is an operator, which is the most powerful role a non-admin has.
	for _, r := range routes {
		if code := sendJSON(t, w.clients["alice"], w.srv, r.method, r.path, r.body, nil); code != http.StatusForbidden {
			t.Errorf("alice %s %s: status = %d, want 403", r.method, r.path, code)
		}
	}
	// None of the refused requests may have changed anything.
	if n := count(t, w.s.db, `SELECT COUNT(*) FROM users`); n != usersBefore {
		t.Errorf("users: %d before, %d after refused requests", usersBefore, n)
	}
	if n := count(t, w.s.db, `SELECT COUNT(*) FROM groups WHERE name = 'Evil'`); n != 0 {
		t.Error("a non-admin created a group")
	}

	// And the admin is not refused (the requests may still fail for other reasons).
	for _, r := range routes {
		if code := sendJSON(t, w.clients["root"], w.srv, r.method, r.path, r.body, nil); code == http.StatusForbidden {
			t.Errorf("root %s %s: refused with 403", r.method, r.path)
		}
	}
}

func TestGroupsAPI(t *testing.T) {
	s, srv, agentID, _ := newTestServer(t) // the test server comes with one enrolled host
	admin := loggedInClient(t, s, srv)
	if err := CreateUser(s.db, "alice", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	aliceID := userIDOf(t, s.db, "alice")

	// Create.
	var created groupSummary
	if code := sendJSON(t, admin, srv, "POST", "/api/groups", map[string]any{"name": "  Office  "}, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201", code)
	}
	if created.Name != "Office" || created.ID == 0 {
		t.Errorf("created = %+v, want name Office (trimmed) and an id", created)
	}

	// Bad and duplicate names.
	for _, name := range []string{"", "   ", "line\nbreak"} {
		if code := sendJSON(t, admin, srv, "POST", "/api/groups", map[string]any{"name": name}, nil); code != http.StatusBadRequest {
			t.Errorf("name %q: status = %d, want 400", name, code)
		}
	}
	if code := sendJSON(t, admin, srv, "POST", "/api/groups", map[string]any{"name": "OFFICE"}, nil); code != http.StatusConflict {
		t.Errorf("duplicate name (other case): status = %d, want 409", code)
	}

	// Members: add, change the role, refuse bad input.
	path := fmt.Sprintf("/api/groups/%d/members/%d", created.ID, aliceID)
	if code := sendJSON(t, admin, srv, "PUT", path, map[string]any{"role": "viewer"}, nil); code != http.StatusNoContent {
		t.Fatalf("add member: status = %d, want 204", code)
	}
	if code := sendJSON(t, admin, srv, "PUT", path, map[string]any{"role": "operator"}, nil); code != http.StatusNoContent {
		t.Fatalf("change role: status = %d, want 204", code)
	}
	if code := sendJSON(t, admin, srv, "PUT", path, map[string]any{"role": "owner"}, nil); code != http.StatusBadRequest {
		t.Errorf("role owner: status = %d, want 400", code)
	}
	if code := sendJSON(t, admin, srv, "PUT", fmt.Sprintf("/api/groups/%d/members/9999", created.ID), map[string]any{"role": "viewer"}, nil); code != http.StatusNotFound {
		t.Errorf("unknown user: status = %d, want 404", code)
	}
	if code := sendJSON(t, admin, srv, "PUT", fmt.Sprintf("/api/groups/9999/members/%d", aliceID), map[string]any{"role": "viewer"}, nil); code != http.StatusNotFound {
		t.Errorf("unknown group: status = %d, want 404", code)
	}

	// Detail and list show the member once, with the changed role.
	var detail groupDetail
	if code := sendJSON(t, admin, srv, "GET", fmt.Sprintf("/api/groups/%d", created.ID), nil, &detail); code != http.StatusOK {
		t.Fatalf("detail: status = %d", code)
	}
	if len(detail.Members) != 1 || detail.Members[0].Username != "alice" || detail.Members[0].Role != "operator" {
		t.Errorf("members = %+v, want alice as operator", detail.Members)
	}
	var list []groupSummary
	sendJSON(t, admin, srv, "GET", "/api/groups", nil, &list)
	if len(list) != 1 || list[0].MemberCount != 1 || list[0].HostCount != 0 {
		t.Errorf("list = %+v, want one group with 1 member and 0 hosts", list)
	}
	if code := sendJSON(t, admin, srv, "GET", "/api/groups/9999", nil, nil); code != http.StatusNotFound {
		t.Errorf("unknown group detail: status = %d, want 404", code)
	}

	// Hosts: an empty group has an empty list. After a host is moved in, the
	// detail lists it as a full host object, and the counts agree.
	if detail.Hosts == nil || len(detail.Hosts) != 0 {
		t.Errorf("hosts of an empty group = %v, want an empty list", detail.Hosts)
	}
	if code := sendJSON(t, admin, srv, "PATCH", "/api/hosts/"+agentID, map[string]any{"group_id": created.ID}, nil); code != http.StatusOK {
		t.Fatalf("move host into the group: status = %d, want 200", code)
	}
	detail = groupDetail{}
	sendJSON(t, admin, srv, "GET", fmt.Sprintf("/api/groups/%d", created.ID), nil, &detail)
	if len(detail.Hosts) != 1 || detail.Hosts[0].ID != agentID || detail.HostCount != 1 {
		t.Errorf("hosts = %+v host_count = %d, want the moved host and a count of 1", detail.Hosts, detail.HostCount)
	}

	// Remove: once works, twice is "not found".
	if code := sendJSON(t, admin, srv, "DELETE", path, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove member: status = %d, want 204", code)
	}
	if code := sendJSON(t, admin, srv, "DELETE", path, nil, nil); code != http.StatusNotFound {
		t.Errorf("remove again: status = %d, want 404", code)
	}
}

func TestUsersAPI(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	admin := loggedInClient(t, s, srv)

	var created userRow
	body := map[string]any{"username": "  Bob ", "password": "correct horse", "is_admin": false}
	if code := sendJSON(t, admin, srv, "POST", "/api/users", body, &created); code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201", code)
	}
	if created.Username != "bob" || created.IsAdmin || created.ID == 0 {
		t.Errorf("created = %+v, want bob, not admin, with an id", created)
	}

	// The reply must never carry the password or its hash.
	var raw map[string]any
	body = map[string]any{"username": "carl", "password": "correct horse"}
	if code := sendJSON(t, admin, srv, "POST", "/api/users", body, &raw); code != http.StatusCreated {
		t.Fatalf("create carl: status = %d, want 201", code)
	}
	for key := range raw {
		if key == "password" || key == "password_hash" {
			t.Errorf("reply contains %q", key)
		}
	}

	if code := sendJSON(t, admin, srv, "POST", "/api/users", map[string]any{"username": "BOB", "password": "correct horse"}, nil); code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want 409", code)
	}
	if code := sendJSON(t, admin, srv, "POST", "/api/users", map[string]any{"username": "has space", "password": "correct horse"}, nil); code != http.StatusBadRequest {
		t.Errorf("bad username: status = %d, want 400", code)
	}
	if code := sendJSON(t, admin, srv, "POST", "/api/users", map[string]any{"username": "dora", "password": "short"}, nil); code != http.StatusBadRequest {
		t.Errorf("short password: status = %d, want 400", code)
	}

	var list []userRow
	sendJSON(t, admin, srv, "GET", "/api/users", nil, &list)
	var names []string
	for _, u := range list {
		names = append(names, u.Username)
	}
	if !slices.Equal(names, []string{"bob", "carl", "tester"}) {
		t.Errorf("users = %v, want [bob carl tester]", names)
	}

	// A user made through the API can log in.
	resp := postLogin(t, http.DefaultClient, srv, "bob", "correct horse")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login as the new user: status = %d, want 200", resp.StatusCode)
	}
}

// ------------------------------------------------------------ audit log

type auditRow struct {
	Action  string
	UserID  sql.NullInt64
	AgentID sql.NullString
	Detail  string
}

func auditRows(t *testing.T, db *sql.DB) []auditRow {
	t.Helper()
	rows, err := db.Query(`SELECT action, user_id, agent_id, detail FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.Action, &r.UserID, &r.AgentID, &r.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestActionsAreAudited(t *testing.T) {
	s, srv, _, _ := newTestServer(t)
	if err := CreateUser(s.db, "root", "correct horse", true); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(s.db, "alice", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	rootID := userIDOf(t, s.db, "root")
	aliceID := userIDOf(t, s.db, "alice")

	// A failed login, then a real one (which also enrolls TOTP on first login).
	postLogin(t, http.DefaultClient, srv, "root", "wrong password")
	admin := loginExisting(t, s, srv, "root")

	// Admin actions.
	sendJSON(t, admin, srv, "POST", "/api/users", map[string]any{"username": "bob", "password": "correct horse"}, nil)
	var g groupSummary
	sendJSON(t, admin, srv, "POST", "/api/groups", map[string]any{"name": "Office"}, &g)
	sendJSON(t, admin, srv, "PUT", fmt.Sprintf("/api/groups/%d/members/%d", g.ID, aliceID), map[string]any{"role": "viewer"}, nil)
	sendJSON(t, admin, srv, "DELETE", fmt.Sprintf("/api/groups/%d/members/%d", g.ID, aliceID), nil, nil)
	sendJSON(t, admin, srv, "POST", "/api/logout", nil, nil)

	// An agent enrolling, over HTTP like the real agent.
	token, _, err := CreateEnrollToken(s.db)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(protocol.EnrollRequest{Token: token, Hostname: "web-01", OS: "linux", Arch: "amd64"})
	resp, err := http.Post(srv.URL+"/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	rows := auditRows(t, s.db)
	var actions []string
	for _, r := range rows {
		actions = append(actions, r.Action)
	}
	want := []string{"login_failed", "totp_enabled", "login", "user_create", "group_create", "member_set", "member_remove", "logout", "agent_enroll"}
	if !slices.Equal(actions, want) {
		t.Fatalf("audit actions = %v\nwant           %v", actions, want)
	}

	// Who is recorded: nobody for the failed login and the enroll, root for the rest.
	if rows[0].UserID.Valid {
		t.Error("failed login has a user id, but nobody was logged in")
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7} {
		if !rows[i].UserID.Valid || rows[i].UserID.Int64 != rootID {
			t.Errorf("%s: user_id = %v, want root (%d)", rows[i].Action, rows[i].UserID, rootID)
		}
	}
	// The enroll names the host. The failed login does not name the username.
	if !rows[8].AgentID.Valid || rows[8].AgentID.String == "" {
		t.Error("agent_enroll has no agent id")
	}
	if got := rows[0].Detail; got == "" || strings.Contains(got, "root") || strings.Contains(got, "wrong password") {
		t.Errorf("failed login detail = %q, must name the address and not what was typed", got)
	}
	if got := rows[5].Detail; got != "group=Office user=alice role=viewer" {
		t.Errorf("member_set detail = %q", got)
	}
}
