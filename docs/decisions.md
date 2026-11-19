# Decision Log

Each entry answers three questions: **what** was chosen, **why**, and **what was rejected**. Entries are never deleted. When a decision changes, add a new entry that replaces the old one (write "supersedes D-xx").

Format: `D-xx — title` · status · date

---

### D-01 — The agent opens the connection (reverse connection) · accepted · 2026-11-19
- **What:** the agent dials out to the server over WSS and keeps the connection open.
- **Why:** hosts sit behind NAT, so the server cannot reach them. Outbound connections are almost always allowed by firewalls. Over port 443 with WebSocket, the connection looks like normal web traffic.
- **Rejected:** a mesh VPN (Tailscale/WireGuard), because it needs another component and doesn't serve the learning goal as well. Opening ports on the router, because it isn't always possible and isn't safe.

### D-02 — One control WS per agent + one new WS per session · accepted · 2026-11-19
- **What:** the control connection carries only small JSON messages. Every shell, file view or tunnel opens a new WebSocket, and the server joins the two WebSockets together.
- **Why:** the server's relay is just "copy bytes both ways" (`io.Copy`). No home-made framing or flow-control protocol. A stuck session doesn't affect other sessions. Easy to debug, because one session is one connection. MeshCentral uses the same approach.
- **Rejected:** multiplexing over one connection (yamux/smux). It saves connections but adds concepts (stream IDs, windows, backpressure) and a dependency we don't need yet.
- **Consequence:** every session costs one extra TLS handshake, a few hundred milliseconds. That is acceptable.

### D-03 — Uploads/bulk sends: the server stores the file, agents download it · accepted · 2026-11-19
- **What:** a file is uploaded to the server once. The server then sends a `fetch` message to each agent, and each agent downloads it over HTTP.
- **Why:** sending to 1 host and to 50 hosts uses the same code. HTTP downloads can be retried on failure. The server doesn't have to fan one stream out to many connections.
- **Rejected:** streaming the upload from the browser straight into agent sessions. That is complicated for many targets and fails entirely when one agent is slow.
- **Consequence:** transfer status lives in memory. If the server restarts mid-transfer, the status is lost, but the final results are still in the audit log.

### D-04 — HTTP server uses stdlib `net/http`, no framework · accepted · 2026-11-19
- **Why:** Go 1.22+ supports `mux.HandleFunc("GET /api/hosts/{id}", …)`. Learning `net/http` means learning the foundation every framework is built on.
- **Rejected:** Gin, Echo, Fiber, Chi.

### D-05 — SQLite (`modernc.org/sqlite`) + hand-written SQL · accepted · 2026-11-19
- **Why:** one database file, no separate DB server. A pure-Go driver (no CGO) keeps cross-compiling easy. Writing SQL by hand makes it obvious what happens.
- **Rejected:** ORMs (GORM, ent), Postgres/MySQL (not needed yet), `mattn/go-sqlite3` (needs CGO).
- **Migrations:** at first, a single `schema.sql` with `CREATE TABLE IF NOT EXISTS`. Versioned migrations are added the first time a schema change would break existing data.

### D-06 — Frontend: HTML + vanilla JS + CSS, embedded, no build step · accepted · 2026-11-19
- **Why:** only one toolchain (Go). Web files are served from `embed.FS`, so the server stays a single binary. The MVP has few, simple pages.
- **Rejected:** React/Vue/Svelte + Vite (a second toolchain with many new concepts), Tailwind (needs a build step unless you use the play CDN), htmx (worth reconsidering if hand-written JS starts repeating).
- **Allowed third-party library:** xterm.js (+ fit addon), vendored into `web/vendor/` with a pinned version, not loaded from a CDN, so the dashboard works without internet access.

### D-07 — TLS is handled by Caddy, not by the headnode server · accepted · 2026-11-19
- **Why:** Caddy handles Let's Encrypt automatically with about 3 lines of config. The server code needs to know nothing about certificates. In development, the server simply runs plain HTTP.
- **Rejected:** `autocert` inside Go (more code and state in the server), nginx (longer config).

### D-08 — WebSocket library: `github.com/coder/websocket` · accepted · 2026-11-19
- **Why:** small API, `context` support for timeouts and cancellation, and `websocket.NetConn`, which lets a tunnel session be treated like a `net.Conn`.
- **Rejected:** `gorilla/websocket` (also good, but lower-level, and you must handle concurrent writes yourself).

### D-09 — Agent secret: random 256-bit token, stored as SHA-256 · accepted · 2026-11-19
- **Why:** the secret is machine-generated with high entropy, so a fast hash is already safe. Bcrypt is only needed for guessable human passwords. Comparison uses `subtle.ConstantTimeCompare`.
- **Also applies to:** enroll tokens and login session tokens (cookies). User passwords still use bcrypt.

### D-10 — No architecture prepared for features that haven't been chosen · accepted · 2026-11-19
- **What:** remote desktop, CCTV, plugins, multiple servers, auto-update and 2FA get no "hooks" or abstractions now.
- **Why:** YAGNI. Guesses about future needs are usually wrong, and a wrong abstraction costs more than a refactor later.

### D-11 — Linux system info is read directly from `/proc` · accepted · 2026-11-19
- **Why:** the learning goal (seeing exactly where CPU% and RAM numbers come from), and no dependency. We only need `/proc/stat`, `/proc/meminfo`, `/proc/uptime`, `/etc/os-release` and `syscall.Statfs`.
- **Rejected (for now):** `gopsutil`. **Revisit** when the Windows agent is built, because reading system info on Windows through syscalls is much more work.

### D-12 — Linux agent first, Windows later · accepted · 2026-11-19
- **Why:** a PTY on Linux is simple (`creack/pty`), while Windows needs ConPTY and a different service manager. Prove the foundation (connections, relay, files) on one OS first.
- **Consequence:** OS-specific code is split per file with build tags (`shell_linux.go`, `shell_windows.go`), but only once a Windows file is actually written.

### D-13 — Name: `headnode`; binaries `headnode`, `headnode-agent`, `hnctl` · accepted · 2026-11-19
- **What:** Go module `headnode`. The server binary is just `headnode`, the agent is `headnode-agent`, the CLI client is `hnctl`. The login cookie is `hn_session`.
- **Why:** the server is the "head" that all hosts connect to. The agent name says what it belongs to. `hnctl` follows the common `*ctl` convention (`kubectl`, `systemctl`) and is short to type.

### D-14 — Language: English everywhere · accepted · 2026-11-19
- **What:** code, identifiers, comments, docs and commit messages are all in English.
- **Why:** technical detail is easier to follow in English, and it matches the stdlib, libraries and most reference material.

### D-15 — UI style modeled on the Tailscale admin console, light + dark · accepted · 2026-11-20
- **What:** warm-gray palette, left sidebar (org name on top, user at the bottom), tables without card boxes, one muted blue accent. Both light and dark themes, switched by `prefers-color-scheme`. Details in [design.md](design.md).
- **Why:** the owner prefers this look. It is quiet and readable for long admin sessions. Because every color is a CSS variable, the dark theme is just one extra block of values, not a second stylesheet.
- **Rejected:** the earlier Cloudflare-dashboard style (white cards on light gray, light only). A manual theme toggle is also left out for now (YAGNI); following the OS setting is enough.
- **Note:** layout inspiration only. No Tailscale logo, name, icons or other brand assets.
