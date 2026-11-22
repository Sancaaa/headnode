package server

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func schemaVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestNewDatabaseGetsEveryMigration(t *testing.T) {
	db := openTestDB(t)

	if got := schemaVersion(t, db); got != len(migrations) {
		t.Errorf("schema version = %d, want %d", got, len(migrations))
	}
	// The column added by migration 1 exists. No rows are needed: SQLite
	// rejects an UPDATE of a column that is not there.
	if _, err := db.Exec(`UPDATE agents SET wan_addr = ''`); err != nil {
		t.Errorf("wan_addr column missing: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", got)
	}
	for _, idx := range []string{
		"idx_web_sessions_user_id",
		"idx_web_sessions_expires_at",
		"idx_agents_group_id",
		"idx_group_members_group_id",
		"idx_files_sha256",
		"idx_audit_log_at",
	} {
		var name string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&name)
		if err != nil {
			t.Errorf("index %s missing: %v", idx, err)
		}
	}
}

// The case this exists for: a database from before migrations (made from
// schema.sql alone, with data in it) must be upgraded, not thrown away.
func TestOldDatabaseIsUpgradedWithoutLosingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")

	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schema); err != nil {
		t.Fatal(err)
	}
	mustExec(t, old, `INSERT INTO agents (id, name, secret_hash, hostname, os, arch, created_at)
		VALUES ('a_old', 'old-host', 'hash', 'old-host', 'linux', 'amd64', 1)`)
	if got := schemaVersion(t, old); got != 0 {
		t.Fatalf("a baseline-only database has version %d, want 0", got)
	}
	old.Close()

	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := schemaVersion(t, db); got != len(migrations) {
		t.Errorf("schema version after upgrade = %d, want %d", got, len(migrations))
	}
	var name, wan string
	if err := db.QueryRow(`SELECT name, wan_addr FROM agents WHERE id = 'a_old'`).Scan(&name, &wan); err != nil {
		t.Fatal(err)
	}
	if name != "old-host" || wan != "" {
		t.Errorf("after upgrade: name=%q wan_addr=%q, want the old row kept and an empty wan_addr", name, wan)
	}
	db.Close()

	// Opening it again must be harmless: a migration never runs twice.
	db, err = OpenDB(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	db.Close()
}

func TestDatabaseFromANewerVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations)+5))
	db.Close()

	if db, err := OpenDB(path); err == nil {
		db.Close()
		t.Error("opened a database that is newer than this program knows")
	}
}
