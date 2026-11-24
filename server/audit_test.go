package server

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func TestAuditAPI(t *testing.T) {
	s, srv, agentID, _ := newTestServer(t)
	admin := loggedInClient(t, s, srv) // writes a "login" entry

	for _, name := range []string{"A", "B", "C"} {
		if code := sendJSON(t, admin, srv, "POST", "/api/groups", map[string]any{"name": name}, nil); code != http.StatusCreated {
			t.Fatalf("create group %s: status = %d", name, code)
		}
	}

	// Newest first, with the names looked up.
	var all []auditEntry
	if code := sendJSON(t, admin, srv, "GET", "/api/audit", nil, &all); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	var actions []string
	for _, e := range all {
		actions = append(actions, e.Action+":"+e.Detail)
	}
	want := []string{"group_create:group=C", "group_create:group=B", "group_create:group=A", "login:ip=127.0.0.1", "totp_enabled:ip=127.0.0.1"}
	if !slices.Equal(actions, want) {
		t.Fatalf("entries = %v\nwant      %v", actions, want)
	}
	if all[0].Username == nil || *all[0].Username != "tester" || all[0].UserID == nil {
		t.Errorf("entry user = %v / %v, want tester and an id", all[0].Username, all[0].UserID)
	}
	if all[0].At.IsZero() {
		t.Error("entry has no time")
	}

	// Paging: limit, then "before" the last entry we have.
	var first []auditEntry
	sendJSON(t, admin, srv, "GET", "/api/audit?limit=2", nil, &first)
	if len(first) != 2 || first[0].ID != all[0].ID || first[1].ID != all[1].ID {
		t.Fatalf("first page = %+v, want the two newest entries", first)
	}
	var second []auditEntry
	sendJSON(t, admin, srv, "GET", fmt.Sprintf("/api/audit?limit=2&before=%d", first[1].ID), nil, &second)
	if len(second) != 2 || second[0].ID != all[2].ID || second[1].ID != all[3].ID {
		t.Fatalf("second page = %+v, want the two older entries", second)
	}
	var past []auditEntry
	sendJSON(t, admin, srv, "GET", fmt.Sprintf("/api/audit?before=%d", all[len(all)-1].ID), nil, &past)
	if past == nil || len(past) != 0 {
		t.Errorf("page past the oldest entry = %v, want an empty list", past)
	}

	for _, bad := range []string{"limit=0", "limit=abc", "limit=-5", "before=abc", "before=0"} {
		if code := sendJSON(t, admin, srv, "GET", "/api/audit?"+bad, nil, nil); code != http.StatusBadRequest {
			t.Errorf("?%s: status = %d, want 400", bad, code)
		}
	}
	// A huge limit is capped, not refused.
	if code := sendJSON(t, admin, srv, "GET", "/api/audit?limit=100000", nil, nil); code != http.StatusOK {
		t.Errorf("limit=100000: status = %d, want 200", code)
	}

	// A host's name is looked up while the host exists. After it is deleted the
	// entry keeps the id, and the name is gone.
	sendJSON(t, admin, srv, "PATCH", "/api/hosts/"+agentID, map[string]any{"name": "web-final"}, nil)
	var latest []auditEntry
	sendJSON(t, admin, srv, "GET", "/api/audit?limit=1", nil, &latest)
	if len(latest) != 1 || latest[0].Action != "host_rename" || latest[0].AgentID == nil || *latest[0].AgentID != agentID ||
		latest[0].HostName == nil || *latest[0].HostName != "web-final" {
		t.Fatalf("rename entry = %+v, want host_rename naming web-final", latest)
	}

	sendJSON(t, admin, srv, "DELETE", "/api/hosts/"+agentID, nil, nil)
	sendJSON(t, admin, srv, "GET", "/api/audit?limit=2", nil, &latest)
	for _, e := range latest {
		if e.AgentID == nil || *e.AgentID != agentID {
			t.Errorf("entry %s lost the host id: %+v", e.Action, e)
		}
		if e.HostName != nil {
			t.Errorf("entry %s still names the deleted host: %q", e.Action, *e.HostName)
		}
	}
}

func TestAuditAPIEmptyLogIsAList(t *testing.T) {
	// Logging in writes an entry, so empty the log after logging in to get a
	// logged-in admin and an empty log at the same time.
	s, srv, _, _ := newTestServer(t)
	admin := loggedInClient(t, s, srv)
	mustExec(t, s.db, `DELETE FROM audit_log`)

	var entries []auditEntry
	if code := sendJSON(t, admin, srv, "GET", "/api/audit", nil, &entries); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if entries == nil || len(entries) != 0 {
		t.Errorf("entries = %v, want an empty list (the page does entries.length)", entries)
	}
}
