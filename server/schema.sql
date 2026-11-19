-- Only the tables the current milestone needs. New tables are added as
-- milestones reach them. CREATE TABLE IF NOT EXISTS makes this file safe to
-- run on every start (D-05).
--
-- NOTE: IF NOT EXISTS never changes a table that already exists. If a column
-- is added to an existing table while there is no versioned migration yet,
-- delete data/headnode.db (and its -wal and -shm files) during development
-- (see D-16 and D-24).

-- Times are unix seconds (INTEGER): no timezone parsing, easy to compare.

-- People who can log in to the dashboard.
CREATE TABLE IF NOT EXISTS users (
    -- AUTOINCREMENT: ids are never reused after a delete, so an old audit log
    -- entry can never point at a different person.
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    -- COLLATE NOCASE: "Admin" and "admin" are the same user. The code also
    -- lowercases names, this is the safety net underneath it.
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,             -- bcrypt, never the password (D-20)
    is_admin      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);

-- One row per logged-in browser. The cookie holds a random token, the table
-- holds only its hash (D-09), so a leaked database cannot be used to take over
-- a session. Rows are deleted on logout and cleaned up after they expire.
CREATE TABLE IF NOT EXISTS web_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- Groups decide who may see and use which hosts (architecture.md §6).
CREATE TABLE IF NOT EXISTS groups (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

-- A user can be in many groups, with a different role in each. Admins do not
-- need rows here: they see everything.
CREATE TABLE IF NOT EXISTS group_members (
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    role     TEXT NOT NULL CHECK (role IN ('operator', 'viewer')),
    PRIMARY KEY (user_id, group_id)
);

-- Enroll tokens let a new agent register itself (architecture.md §5).
-- Only the SHA-256 hash is stored, so a leaked database file cannot be used
-- to enroll fake agents (D-09).
CREATE TABLE IF NOT EXISTS enroll_tokens (
    token_hash TEXT PRIMARY KEY,
    -- The group a host joins when it enrolls with this token. NULL for tokens
    -- made on the command line: those hosts start ungrouped (admin-only).
    group_id   INTEGER REFERENCES groups(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

-- One row per enrolled host. The columns after created_at are filled in by the
-- agent's hello and heartbeat messages; they live here from the start so a
-- later milestone needs no schema change.
CREATE TABLE IF NOT EXISTS agents (
    id           TEXT PRIMARY KEY,
    -- NULL = ungrouped. Only admins can see an ungrouped host. If the group is
    -- deleted the host is kept and becomes ungrouped.
    group_id     INTEGER REFERENCES groups(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,              -- display name, starts as the hostname
    secret_hash  TEXT NOT NULL,              -- SHA-256 of the agent secret (D-09)
    hostname     TEXT NOT NULL,
    os           TEXT NOT NULL,
    arch         TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    version      TEXT NOT NULL DEFAULT '',   -- from hello
    info_json    TEXT NOT NULL DEFAULT '',   -- full hello payload, shown on the host detail page
    last_seen_at INTEGER NOT NULL DEFAULT 0  -- 0 = never connected
);

-- Who did what. Deliberately NO foreign keys: the history must survive when a
-- user or host is deleted. user_id is NULL for things nobody was logged in for
-- (a failed login, an agent enrolling); agent_id is NULL when no host is involved.
CREATE TABLE IF NOT EXISTS audit_log (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    at       INTEGER NOT NULL,
    user_id  INTEGER,
    agent_id TEXT,
    action   TEXT NOT NULL,                  -- "login", "group_create", ...
    detail   TEXT NOT NULL DEFAULT ''
);
