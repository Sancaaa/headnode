package server

import (
	"database/sql"
	"fmt"
)

// migrations change the schema after the baseline in schema.sql. Each one runs
// once, in order, on every database: an old one catches up, a new one gets all
// of them right after the baseline. The database file remembers how many have
// run in SQLite's user_version, so a migration is never run twice.
//
// Never edit or reorder an entry that has shipped: add a new one at the end.
var migrations = []string{
	// M3: the address the server saw the agent connect from (D-29).
	`ALTER TABLE agents ADD COLUMN wan_addr TEXT NOT NULL DEFAULT ''`,

	// M4: files uploaded to the server, to be sent to hosts. The bytes are on
	// disk under data/files/<sha256>; this table is the list of names. The id is
	// what a transfer refers to. No foreign key on uploaded_by: the record of who
	// uploaded something must outlive that user.
	`CREATE TABLE files (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		size        INTEGER NOT NULL,
		sha256      TEXT NOT NULL,
		uploaded_by INTEGER NOT NULL,
		created_at  INTEGER NOT NULL
	)`,

	// M11: accounts (D-69). A name for the sidebar (the username, which the audit
	// log shows, never changes), and "disabled" instead of deleting a user, so every
	// audit line still points at a real row.
	`ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0`,
	// What the Profile page shows about each login: when it was last used, which
	// kind of client it is ('browser' or 'cli', that is hnctl), and where it came
	// from. 0 and '' are what a session from before this change has.
	`ALTER TABLE web_sessions ADD COLUMN last_used_at INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE web_sessions ADD COLUMN client TEXT NOT NULL DEFAULT 'browser'`,
	`ALTER TABLE web_sessions ADD COLUMN ip TEXT NOT NULL DEFAULT ''`,
}

// migrate brings the database up to date. It runs every time the server (or a
// command like `token create`) opens the database, and does nothing when there
// is nothing new.
func migrate(db *sql.DB) error {
	var applied int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&applied); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if applied > len(migrations) {
		// Going back would run old code against columns it does not know.
		return fmt.Errorf("the database is at schema version %d, but this headnode only knows %d: it was made by a newer version", applied, len(migrations))
	}

	for i := applied; i < len(migrations); i++ {
		// One transaction per migration: the change and the new version number
		// land together, or neither does.
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		// PRAGMA takes no ? parameters. The number comes from our own loop,
		// not from input.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}
