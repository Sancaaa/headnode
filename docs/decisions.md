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

### D-16 — `agents` table has all M1 columns from the start; dev database is reset on schema changes · accepted · 2026-11-20
- **What:** `schema.sql` creates `agents` with every column M1 will use (`version`, `info_json`, `last_seen_at` included), even though enrollment fills only some of them. `CREATE TABLE IF NOT EXISTS` never alters an existing table, so during development a column added later means deleting `data/headnode.db`.
- **Why:** the first time the schema must change on a database that holds real data is the moment to add versioned migrations (D-05), not earlier. Until then the database only holds test hosts, so deleting it costs nothing. Putting the M1 columns in now avoids needing even that during M1.
- **Rejected:** writing a migration system now (YAGNI), or adding `ALTER TABLE` statements to `schema.sql` (they fail on the second run).
- **Consequence (groups):** `agents` has no `group_id` yet. Groups arrive in M2, and that is when the first real migration is needed if the database holds data worth keeping.

### D-17 — One control connection per agent: the newest wins · accepted · 2026-11-20
- **What:** if an agent connects while the server still holds a control connection for the same agent id, the server closes the old one and keeps the new one. The old connection's cleanup only removes the registry entry if it still points at that same connection.
- **Why:** after a network drop the agent reconnects within seconds, but the server may not notice the old connection is dead for up to 60 s (the heartbeat timeout). Refusing the new connection would leave the host offline for that whole minute. The newest connection is the one the agent is actually using.
- **Rejected:** keeping the old connection and refusing the new one (host looks offline until the timeout). Allowing several connections per agent (every later feature would have to decide which one to talk to).
- **Consequence:** two machines that share one `agent.json` (a cloned VM, for example) will keep kicking each other off. That is a real misconfiguration; the log line `agent online` repeating for one agent id is the symptom.

### D-18 — Agent system info is split into pure parsers, a Linux reader and a stub · accepted · 2026-11-20
- **What:** `agent/procfs.go` has functions that parse the *text* of `/proc` files and has no OS-specific code. `agent/sysinfo_linux.go` reads the files and calls `syscall.Statfs`. `agent/sysinfo_other.go` (build tag `!linux`) has stubs that return an error.
- **Why:** `syscall.Statfs` does not exist on Windows, so without the stub the agent package would not compile on the Windows laptop and `go vet ./...` / `go test ./...` would fail there. Keeping the parsers in a plain file means the logic that is easy to get wrong (CPU % from two samples, `/proc/meminfo` units) is tested on any OS.
- **Rejected:** `golang.org/x/sys` (a new dependency, and it does not remove the need for a Windows stub). Telling the owner to always set `GOOS=linux` when running `go vet` (easy to forget, and then the error message is confusing).
- **Relation to D-12:** this is the first OS-specific file split, forced by the development machine rather than by a Windows agent. A real Windows agent would replace `sysinfo_other.go`.

### D-19 — UI: one JS file, DOM built with text nodes, polling every 2 s, strict CSP · accepted · 2026-11-20
- **What:**
  1. `web/app.js` is the whole UI. The page is chosen from the URL hash (`#/` = Hosts, `#/hosts/<id>` = detail), and the file is read top to bottom.
  2. Elements are built with a small `h(tag, attrs, ...children)` helper. Strings become text nodes. Agent-supplied text (hostname, kernel, platform) is **never** put into `innerHTML`.
  3. The page asks the server for fresh data every 2 seconds and redraws only if something changed.
  4. The server sends `Content-Security-Policy: default-src 'self'; frame-ancestors 'none'` with the UI files, so web/ has no inline scripts or `style=""` attributes.
- **Why:** (2) A host's hostname is chosen by whoever controls that machine, so a hostname containing HTML must not be able to run script in an admin's browser. Building with text nodes makes this safe by construction instead of relying on remembering to escape. (3) 2 s polling meets the "host shows up within 2 seconds" goal with no server push code, and the load is tiny for an admin tool. (4) The CSP is a second wall behind (2). It is cheap now and painful to add after inline scripts exist.
- **Rejected:** a framework or a template library (D-06). Server push (WebSocket/SSE) for host status: more code, and not needed for the current goal. Hash routing is also the reason no server route is needed for the detail page.
- **Consequence:** xterm.js (M3) must be vendored as a file, which D-06 already requires. The 2 s poll is replaced only if it proves too slow or too heavy.

### D-20 — User passwords: bcrypt from `golang.org/x/crypto`; the CLI reads the password from stdin · accepted · 2026-11-20
- **What:** passwords are hashed with `bcrypt` (`golang.org/x/crypto/bcrypt`, default cost). Passwords must be 8 to 72 bytes: bcrypt reads only the first 72 bytes, so a longer password would be silently cut, and the code refuses it instead. Usernames are lowercased and limited to `a-z 0-9 . _ -`, 1 to 32 characters. `headnode user create` reads the password from stdin (prompted twice on a terminal, never a flag).
- **Why:** architecture.md §7 and D-09 already name bcrypt for human passwords. It is slow on purpose and salts each hash itself, which is what makes a stolen database hard to crack. `x/crypto` is maintained by the Go team and is the standard home of bcrypt. This is the project's third dependency, after `modernc.org/sqlite` and `coder/websocket`, and was approved by the owner on 2026-11-20.
- **Rejected:** PBKDF2 from the standard library (`crypto/pbkdf2`, no dependency, but much weaker against GPU cracking). Passing the password as a flag (visible in the process list and shell history).
- **Consequence:** the standard library cannot switch off terminal echo, so a password typed at the `user create` prompt was visible on screen. Superseded in part by D-21.

### D-21 — `golang.org/x/term` hides the password typed at `user create` · accepted · 2026-11-20 · supersedes the echo consequence of D-20
- **What:** `headnode user create` uses `term.ReadPassword`, so nothing is shown while the password is typed. When stdin is not a terminal (a pipe), it reads one plain line instead, so scripts keep working. A Ctrl+C handler restores the terminal before exiting.
- **Why:** the owner asked for it right after seeing the visible prompt. A password on screen ends up in scrollback and over-the-shoulder. `x/term` is maintained by the Go team, small, and works on Windows and Linux. This is the project's fourth dependency.
- **Rejected:** hand-written echo switching with `syscall` (different code for Windows and Unix, build tags, easy to get wrong). Leaving it visible.
- **Consequence:** `term.ReadPassword` leaves echo off if the process is killed in the middle of a prompt, so the Ctrl+C handler in `cmd/headnode/user.go` restores the terminal state. A `kill -9` cannot be caught; on Linux `reset` fixes the terminal.

### D-22 — API is default-deny, and the Origin check refuses a missing Origin · accepted · 2026-11-20
- **What:** all authenticated routes are registered on an inner mux, and the whole inner mux is wrapped by `requireLogin`. The outer mux has exactly four public things: the web page files, the agent endpoints (own credentials), `POST /api/login`, and nothing else under `/api/`. `POST`/`PATCH`/`PUT`/`DELETE` on the guarded routes, and login itself, also require an `Origin` header whose host equals the request's `Host`. A request with no `Origin` is refused.
- **Why:** CLAUDE.md says every endpoint except login and enroll must check authentication. Checking per handler means one forgotten line makes a public hole. With the wrapper, a new route is protected unless someone deliberately registers it on the outer mux, and unknown `/api/*` paths answer 401 too instead of leaking that they do not exist. Browsers always send `Origin` on a POST, so a missing one means the caller is not a browser tab, and cookie authentication must not apply.
- **Rejected:** a `requireLogin(handler)` call on every route (easy to forget). Checking `Referer` (browsers may omit it). CSRF tokens in forms (more moving parts than the Origin check plus `SameSite=Lax`, which together cover the same attacks for a JSON API).
- **Consequence:** the static page files are public. They contain no data, and the login page has to load before anyone can log in. The mux needs one line per HTTP method for the guarded prefix, because a pattern without a method would conflict with `GET /`. Clients that use `Authorization: Bearer` (hnctl, M5) will skip the Origin check, as protocol.md already says.

### D-23 — Login sessions: random token, hash stored, 7 days; `Secure` cookie only over HTTPS; per-address failure delay · accepted · 2026-11-20
- **What:** a login creates a random 256-bit token. The cookie `hn_session` holds it, the `web_sessions` table holds only its SHA-256 (D-09). It expires 7 days after login, with no renewal on use. The cookie is `HttpOnly` and `SameSite=Lax`, and `Secure` when the request arrived over HTTPS (directly, or through a proxy on this machine sending `X-Forwarded-Proto: https`). A wrong username still costs one bcrypt comparison, against a dummy hash. Failed logins are counted per client address in memory: 3 free, then a growing wait (1 s, 2 s, 4 s … 15 min).
- **Why:** stored hashes mean a leaked database cannot be used to hijack a logged-in browser. `HttpOnly` keeps an injected script from reading the cookie. A fixed lifetime is the easiest rule to explain and to test. `Secure` must not be forced, because development runs on plain `http://` and the browser would silently drop the cookie, but it must be on in production behind Caddy, hence the proxy check. The dummy comparison keeps "no such user" from answering faster than "wrong password", which would reveal which usernames exist. The growing wait turns online guessing from thousands per minute into about 15 per hour per address, and a person who mistypes twice never notices it.
- **Rejected:** sliding expiry (harder to reason about, a stolen cookie could live forever). JWTs (cannot be revoked on logout without a server-side list, which is a sessions table anyway). A fixed lockout after N failures (lets an attacker lock a real user out; a growing wait does not lock anyone out for long). Trusting `X-Forwarded-For` from anyone (a client can write anything there, so it is only believed from a proxy on this machine, and only its last entry).
- **Consequence:** the limiter is in memory, so a server restart forgets it. Sessions survive a restart because they are in the database. The limiter tracks addresses, not usernames, so someone with many addresses can try a few passwords from each. That is accepted for now. The audit log records failed logins by address (D-24).

### D-24 — Permissions: one `can()`, hidden hosts answer 404, ungrouped hosts are admin-only, audit writes never block, dev database reset · accepted · 2026-11-20
- **What:**
  1. `can(user, host, action)` in `server/permissions.go` is the only place that decides whether a user may act on a host. The role table from architecture.md §6 is data (`rolePermissions`), and unknown roles or actions get `false`. Admins pass everything. Everyone else needs a role in the host's group.
  2. The host list is filtered with the equivalent SQL (`group_id IN (SELECT group_id FROM group_members WHERE user_id = ?)`), because asking `can()` once per host would be one query per host. A test checks that the two always agree.
  3. A host the user may not see answers `404`, exactly like a host that does not exist.
  4. A host in no group (`agents.group_id` is NULL) is visible to admins only. Hosts enrolled from the command line start this way.
  5. `audit()` records one row per action. If the write fails it logs through `slog` and the action still goes through.
  6. `groups`, `group_members`, `audit_log`, `agents.group_id` and `enroll_tokens.group_id` are all in `schema.sql` now, and the development database must be deleted once (the D-16 consequence).
- **Why:** (1) "All checks go through one function" is the only way to audit permissions by reading code. Putting the table in data makes it readable next to architecture.md and easy to test completely. (3) A `403` would confirm that the host exists. (4) Without it we would need a rule for "no group", and "only admins" is the safest one that needs no extra concept. (5) An admin should not be locked out of fixing something because the log disk is full, and the `slog` line is still a record. The cost is that a crashed audit write can leave an action unrecorded, which is accepted for a self-hosted tool at this size. (6) Putting every M2 column in at once means one reset instead of one per slice.
- **Rejected:** checking roles inline in each handler (nobody can audit that). Failing the action when the audit write fails (safer on paper, but makes the audit log a single point of failure for the whole dashboard). `403` for hidden hosts. Writing a migration system now: the database only holds test hosts, and migrations are added the first time real data would be lost (D-05).
- **Consequence:** deleting `data/headnode.db*` means re-creating the admin user and re-enrolling hosts once. After this, a schema change to an existing table needs a real migration. The groups feature is only usable together with the move-host action in the next slice, since new hosts start ungrouped.

### D-25 — Host admin actions: admin-only, PATCH with three-way `group_id`, delete cuts the connection, token reply shows the manual enroll command · accepted · 2026-11-20
- **What:** renaming, moving and deleting a host, and creating group enroll tokens, are admin-only. `PATCH /api/hosts/{id}` takes `name` and/or `group_id`, and `group_id` can be absent (keep), `null` (no group) or an id (move there). The whole request is validated before any change and applied in one transaction. `DELETE` removes the row first and then closes the live connection. A real move or rename is audited, a no-op is not. The token reply's `install_command` is the manual enroll command until the installer exists (M6).
- **Why:** moving a host changes who can see it, and architecture.md §6 gives user, group and token management to admins only, so the host actions follow the same rule instead of inventing a middle role. A plain integer cannot tell "leave the group alone" from "remove the group", and a wrong guess there would silently hide a host, so the field is read as raw JSON. Validate-then-apply keeps a half-good request from leaving the host half-changed. Deleting the row first closes the window in which a still-valid agent could reconnect. Not auditing no-ops keeps the log readable. The UI needs a stable field name for the command, so the field keeps its documented name and only its content changes in M6.
- **Rejected:** letting operators rename hosts in their own group (a second kind of permission to explain, for little gain). A separate `/api/hosts/{id}/group` endpoint (more routes for the same thing). Closing the connection first, then deleting.
- **Consequence:** deleting a host is final: the agent's `agent.json` becomes useless and the agent keeps retrying with 401 until it is re-enrolled. The audit log keeps the host's id and name after the delete.

- **Update (same day):** the token route is `POST /api/enroll-tokens` with an optional `group_id`, replacing `POST /api/groups/{id}/enroll-tokens`. The Hosts page lets the admin choose "No group" (the default choice), and the group page's "Add host" skips the choice and creates the token for that group at once. One route with a nullable field is simpler than two routes for the same action, and an ungrouped token is the safer default: a host that lands in the wrong group is visible to people who should not see it, an ungrouped one only to admins.

### D-27 — Sessions: opaque relay run by the browser's handler, single-use user-bound ids, pings, close pass-through · accepted · 2026-11-20
- **What:** `relay()` copies WebSocket messages between two connections without reading them. The browser's handler runs it. The agent's handler hands over its connection and waits until the session is over, so exactly one goroutine copies bytes. A session id is 128 random bits, valid for one browser and one agent attach, and bound to the user who created it (and the agent it was opened on): anyone else gets 404, admins included. A 15 s countdown from creation cancels a session nobody paired. The relay pings both sides every 30 s. When one side ends, the other gets the same close code and reason, and the relay closes the surviving side first (a close frame), cancels afterwards.
- **Why:** an opaque relay is the whole reason shell, files and tunnel can share one mechanism (D-02), and it keeps the server from ever parsing terminal bytes. One relaying goroutine avoids two copiers racing on the same connections. Binding the id to the user means a leaked id is useless to another logged-in person. Pings are how a vanished peer (a laptop that slept, a pulled network cable) is detected: without them the session, and the shell on the host, would live forever. Passing the close code and reason through lets the browser show "cannot start a shell: …" instead of a bare disconnect. Cancelling the context before sending the close frame would make the library drop the connection without a goodbye, hence the order.
- **Rejected:** a session id any logged-in user can attach (guessing is hard, but a leak would then be fatal). Letting both handlers copy (two goroutines per direction, racy shutdown). Detecting dead peers only through TCP (can take hours).
- **Consequence:** there is no per-host or per-user limit on concurrent sessions yet, so one operator can start many shells. A fixed limit is a small addition when it matters. Closing the browser tab ends the shell because the close propagates: browser, relay, agent, PTY hangup. Background jobs that a shell started in their own process groups may outlive it.

### D-28 — Terminal dependencies: `creack/pty` for the agent, xterm.js vendored, and `style-src 'unsafe-inline'` in the CSP · accepted · 2026-11-20
- **What:** the agent uses `github.com/creack/pty` to start the shell in a pseudo-terminal (fifth dependency, already named in architecture.md and D-12). The browser uses xterm.js 5.5.0 and its fit addon 0.10.0, copied into `web/vendor/` (D-06, with a README that pins versions and gives the download commands). The Content-Security-Policy changed from `default-src 'self'` to `default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'`.
- **Why:** a PTY is what vim and htop need (size, raw mode, cursor control), and `creack/pty` is the standard, small way to get one on Linux without hand-writing ioctls. xterm.js builds `<style>` elements at run time to size and color the terminal, and the strict policy blocks that, so styles must allow inline. Scripts stay locked to our own files, which is where the protection against injected code lives (D-19). The files are vendored so the dashboard needs no internet and a CDN cannot change what runs in the page.
- **Rejected:** writing the PTY handling with raw syscalls (more code, easy to get wrong, and the dependency is small). Loading xterm.js from a CDN. A style nonce (xterm.js does not support passing one). Keeping the strict style policy and a broken terminal.
- **Consequence:** the page now accepts inline styles. An attacker who could inject markup could restyle the page, but still not run script. Updating xterm.js means editing `web/vendor/README.md` and downloading the files again.

### D-26 — Admin UI: native `<dialog>`, dialogs outside the redrawn area, admin controls hidden for convenience only · accepted · 2026-11-20
- **What:** pop-up forms use the browser's `<dialog>` element (`showModal()`), created in `app.js` and attached to `<body>`, not inside `#view`. Every dangerous action (delete host, remove member) goes through a confirm dialog that names the object. Admin buttons and the Admin sidebar section are shown only when `/api/me` says `is_admin`, and nothing relies on that.
- **Why:** `<dialog>` gives focus trapping, Esc to close and the dimmed backdrop with no code and no library (D-06). The page redraws whenever data changes, so a form inside `#view` could be wiped while someone types. Hiding admin controls keeps the UI quiet for people who cannot use them, but the server is the security boundary: every admin route is `adminOnly` and tested (D-22, D-24).
- **Rejected:** a modal built from divs (focus handling and Esc would be our code). Putting forms inline in the page (wiped by the redraw). Treating the hidden buttons as protection.
- **Consequence:** below 1024 px the sidebar is hidden (design.md), so on a narrow screen there is no navigation to the Admin pages yet. A top bar for small screens is still to do.

### D-29 — Hosts table: no Version column, WAN address from the server's view of the connection, Shell button from `can_shell` · accepted · 2026-11-20
- **What:** the Version column left the Hosts table (the agent version and kernel stay on the host page). The Addresses cell shows the host's own addresses, then `WAN <ip>`. The WAN address is what the server sees when the agent's control connection arrives: it is stored on every connect (`agents.wan_addr`) and returned as `wan_addr`. A **Shell** button appears on a row when the host is online and the API says `can_shell` for the current user.
- **Why:** every host runs the same agent version until auto-update (A3) exists, so the column repeated one value on every row. The agent cannot know its public address (it may sit behind NAT), and one it reported about itself could not be trusted, so the only honest source is the other end of the connection. The server finds that address with `clientIP`, the function login already uses, so `X-Forwarded-For` is believed only when the request came from the proxy on this machine, and then only its last entry. Without that rule anyone could put any address in the header: an agent could claim to be somewhere else, and a login attacker could rotate fake addresses past the rate limit. `can_shell` is computed from the same role table as `can()` and a test keeps the two equal, so a button is never offered that the server would refuse.
- **Rejected:** having the agent look up its public IP on an outside service (a dependency on a third party, and the agent could lie). Keeping the Version column "for when it differs". Asking `can()` once per host for the button (one query per host on every 2-second poll).
- **Consequence:** `wan_addr` is the address of the last connect and stays when the host goes offline. On a local test it is `127.0.0.1`. If the server sits behind a proxy that is not on the same machine, the address is that proxy's. A refused terminal (for example a role change after the page loaded) is still answered by the server, since the button is only a convenience.

### D-30 — Versioned schema migrations with `PRAGMA user_version` · accepted · 2026-11-20
- **What:** `schema.sql` is now the frozen baseline. Later changes to a table are entries in `migrations` (`server/migrations.go`), applied in order by `OpenDB` to every database, new or old. SQLite's `user_version` records how many have run, each migration and its version number commit together, and a database newer than the program is refused. The first entry adds `agents.wan_addr`.
- **Why:** D-05 said migrations arrive the first time a schema change would break existing data, and D-24 said the next change to an existing table would need one. The database now holds users, groups and enrolled hosts, so deleting it for a new column is no longer a small thing. `user_version` is built into SQLite, so it needs no table and no dependency.
- **Rejected:** deleting the development database again (it holds real setup work now). Editing the `CREATE TABLE` in `schema.sql` (`IF NOT EXISTS` would silently skip it on existing databases). A migration library (a dependency for a handful of statements).
- **Consequence:** never edit or reorder a migration that has shipped: add a new one. Reading the full current schema means the baseline plus the migrations. Two processes that open a freshly upgraded database at the very same moment (the server and a command-line command) could collide once, and one of them reports an error that goes away on retry.
