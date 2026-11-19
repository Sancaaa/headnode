package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"headnode/protocol"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateEnrollToken(t *testing.T) {
	db := openTestDB(t)

	token, expiresAt, err := CreateEnrollToken(db)
	if err != nil {
		t.Fatal(err)
	}

	if len(token) != 64 {
		t.Errorf("token length = %d, want 64 hex characters", len(token))
	}
	if time.Until(expiresAt) < 23*time.Hour {
		t.Errorf("expiresAt = %v, want about 24 h from now", expiresAt)
	}

	// The whole point of hashing: the database must hold the hash, never the token.
	if n := count(t, db, `SELECT COUNT(*) FROM enroll_tokens WHERE token_hash = ?`, token); n != 0 {
		t.Error("plain-text token found in the database")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM enroll_tokens WHERE token_hash = ?`, hashSecret(token)); n != 1 {
		t.Errorf("rows with the token's hash = %d, want 1", n)
	}
}

func TestNewSecretIsUnique(t *testing.T) {
	a, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two calls returned the same secret")
	}
}

// postEnroll sends a request through the real router, like an agent would.
func postEnroll(t *testing.T, srv *httptest.Server, req protocol.EnrollRequest) *http.Response {
	t.Helper()
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/agent/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestEnrollEndpoint(t *testing.T) {
	db := openTestDB(t)
	srv := httptest.NewServer(New(db).Handler())
	defer srv.Close()

	token, _, err := CreateEnrollToken(db)
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.EnrollRequest{Token: token, Hostname: "web-01", OS: "linux", Arch: "amd64"}

	t.Run("valid token enrolls and stores only the secret hash", func(t *testing.T) {
		resp := postEnroll(t, srv, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out protocol.EnrollResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out.AgentID == "" || len(out.AgentSecret) != 64 {
			t.Fatalf("unexpected response: %+v", out)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE id = ? AND secret_hash = ?`, out.AgentID, hashSecret(out.AgentSecret)); n != 1 {
			t.Error("agent row with the secret's hash not found")
		}
		if n := count(t, db, `SELECT COUNT(*) FROM agents WHERE secret_hash = ?`, out.AgentSecret); n != 0 {
			t.Error("plain-text secret found in the database")
		}
	})

	t.Run("token is reusable", func(t *testing.T) {
		if resp := postEnroll(t, srv, req); resp.StatusCode != http.StatusOK {
			t.Errorf("second enroll with the same token: status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("unknown token is rejected", func(t *testing.T) {
		bad := req
		bad.Token = "not-a-real-token"
		if resp := postEnroll(t, srv, bad); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("expired token is rejected", func(t *testing.T) {
		expired := "expired-token"
		_, err := db.Exec(`INSERT INTO enroll_tokens (token_hash, expires_at, created_at) VALUES (?, ?, ?)`,
			hashSecret(expired), time.Now().Add(-time.Hour).Unix(), time.Now().Add(-25*time.Hour).Unix())
		if err != nil {
			t.Fatal(err)
		}
		bad := req
		bad.Token = expired
		if resp := postEnroll(t, srv, bad); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("missing hostname is a bad request", func(t *testing.T) {
		bad := req
		bad.Hostname = ""
		if resp := postEnroll(t, srv, bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
}
