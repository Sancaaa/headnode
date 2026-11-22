package server

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, registers itself under the name "sqlite" (D-05)
)

// isUniqueViolation reports whether err came from inserting a duplicate into a
// UNIQUE column. The driver has no exported "duplicate" error value, so we
// match on the message SQLite documents for it.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// The schema is embedded so the binary does not need schema.sql next to it.
//
//go:embed schema.sql
var schema string

// OpenDB opens (and creates, if missing) the SQLite database at path
// and makes sure all tables exist.
func OpenDB(path string) (*sql.DB, error) {
	// WAL lets readers and the writer work at the same time. busy_timeout makes
	// a second writer wait up to 5 s instead of failing at once with
	// "database is locked", which would happen as soon as heartbeats and
	// HTTP requests write concurrently. foreign_keys is off by default in
	// SQLite, and without it "REFERENCES ... ON DELETE CASCADE" in schema.sql
	// would be ignored. Each pragma runs on every new connection.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	// SQLite allows one writer at a time; capping the pool at one connection
	// serialises writes in-process and avoids SQLITE_BUSY contention.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create tables: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
