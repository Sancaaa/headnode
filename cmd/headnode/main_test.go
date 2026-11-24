package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"headnode/server"
)

// The data directory holds password hashes, agent secret hashes and uploaded
// files, so a directory the server creates is private to its owner. (An
// existing directory keeps its mode: MkdirAll never changes it.)
func TestNewDataDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "data")
	db := openDB(filepath.Join(dir, "headnode.db"))
	defer db.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("data directory mode = %o, want 700", got)
	}
}

func TestUserTOTPResetCommand(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "headnode.db")
	db := openDB(dbPath)
	if err := server.CreateUser(db, "admin", "correct horse", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET totp_secret = 'JBSWY3DPEHPK3PXP', totp_enabled = 1, totp_last_step = 42 WHERE username = 'admin'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO web_sessions (token_hash, user_id, expires_at, created_at) VALUES ('hash1', 1, 9999999999, 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	userTOTPResetCommand([]string{"-username", "admin", "-db", dbPath})

	db = openDB(dbPath)
	defer db.Close()
	var secret string
	var enabled, sessions int
	var lastStep int64
	if err := db.QueryRow(`SELECT totp_secret, totp_enabled, totp_last_step FROM users WHERE username = 'admin'`).Scan(&secret, &enabled, &lastStep); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM web_sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if secret != "" || enabled != 0 || lastStep != 0 || sessions != 0 {
		t.Errorf("after user totp-reset: secret=%q enabled=%d lastStep=%d sessions=%d, want all cleared", secret, enabled, lastStep, sessions)
	}
}
