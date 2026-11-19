# Architecture

This document explains **how the system works**. The reasons behind each choice are in [decisions.md](decisions.md) (referenced as `D-xx`). Message formats are in [protocol.md](protocol.md).

## 1. The problem

The hosts we want to manage usually sit behind NAT: an office or home router, a shared public IP, no port forwarding. From the internet, those hosts **cannot be reached**. They **can**, however, reach the internet.

So the agent opens an outbound connection to a public server, and that connection is then used as the "way in" from the other direction (`D-01`). It is the same idea as `ssh -R`.

## 2. Components

```
┌───────────┐        HTTPS / WS        ┌──────────────────────────┐        WSS (dials out)          ┌────────────────┐
│  Browser  │ ───────────────────────► │  Caddy (TLS, :443)       │ ◄────────────────────────────── │ headnode-agent │
└───────────┘                          │      │ reverse proxy     │                                 │    (host A)    │
┌───────────┐   WS (tunnel, M5)        │      ▼                   │ ◄────────────────────────────── ├────────────────┤
│  hnctl    │ ───────────────────────► │  headnode (:8080 local)  │                                 │ headnode-agent │
└───────────┘                          │   ├─ HTTP: UI + API      │                                 │    (host B)    │
                                       │   ├─ agent registry      │                                 └────────────────┘
                                       │   ├─ session relay       │
                                       │   └─ SQLite + file store │
                                       └──────────────────────────┘
```

| Component | Responsible for | Not responsible for |
|---|---|---|
| **headnode** (server) | User login, storing data, knowing which agents are online, sending commands to agents, connecting sessions | TLS (Caddy's job, `D-07`), executing anything on a host |
| **headnode-agent** | Reporting host info; running shell, file and tunnel sessions **only** when the server asks | Storing user data, authorization decisions |
| **hnctl** | Opening a local port on the laptop and forwarding it through the server to a target host | — |
| **Caddy** | Let's Encrypt certificates, HTTPS | Application logic |

Authorization is **always** decided by the server. The agent trusts the server because its connection to the server URL uses verified TLS.

## 3. Two kinds of agent connection

This is the core of the architecture (`D-02`).

### 3.1 Control connection (one per agent, long-lived)

```
agent ──WS /agent/connect──► server
  → hello      (once at the start: hostname, OS, arch, version)
  → heartbeat  (every 20 s: CPU, RAM, disk, uptime)
  ← open       (server asks the agent to open a session)
  ← fetch      (server asks the agent to download a file)
```

- While this connection is open, the agent counts as **online**. The server keeps an in-memory map `agentID → connection` (file `server/agents.go`).
- When the connection drops, the agent reconnects with a growing delay (1s, 2s, 4s, … up to 60s) plus some random jitter. The jitter stops hundreds of agents from hitting the server at the same moment after a server restart.

### 3.2 Session connection (one per activity, short-lived)

Every time someone opens a shell, a file view or a tunnel, a **new WebSocket** is created just for that.

```
 Browser                      Server                                Agent
    │  POST /api/hosts/A/sessions {kind:"shell"}                       │
    │ ──────────────────────────►│ check login + permission,           │
    │                            │ create sessionID=s1, write audit log│
    │                            │ ── control: {type:"open",id:s1} ──► │
    │ ◄── {id:s1} ───────────────│                                     │
    │  WS /ui/sessions/s1        │                                     │
    │ ──────────────────────────►│ (waits for the other side)          │
    │                            │ ◄──── WS /agent/sessions/s1 ─────── │ agent dials, starts a PTY
    │                            │ pair them: copy(a→b) & copy(b→a)    │
    │ ◄═════════ terminal bytes flow both ways ═══════════════════════►│
```

The server **does not understand** what flows through a session. It only copies bytes from one side to the other (file `server/relay.go`). Shell, file manager and tunnel all use the same mechanism. The only differences are the `kind` and what the agent does with it.

Relay rules:
- If the other side does not arrive within 15 seconds, the session is cancelled.
- If one side closes, the other side is closed too.
- `sessionID` is a random 128-bit value, single-use, and bound to the user who requested it.

## 4. Features per session kind

| `kind` | Browser/client side | What the agent does | What flows |
|---|---|---|---|
| `shell` | xterm.js | Runs a shell in a PTY (`creack/pty` on Linux) | Binary: terminal bytes. Text: JSON `resize` |
| `files` | File manager page | Simple file operations | JSON request/response (`list`, `stat`, `mkdir`, `rename`, `delete`) |
| `download` | The server itself (forwarded to the browser as an HTTP response) | Reads a file and sends its bytes | Binary until EOF |
| `tunnel` | `hnctl` | `net.Dial` to the target `host:port` | Raw binary, both ways |

**Uploads and bulk sends** deliberately do **not** go through sessions (`D-03`):
1. The browser uploads the file to the server (`POST /api/files`). The server stores it at `data/files/<sha256>`.
2. The user picks target hosts and a path. The server sends `fetch` to each agent over its control connection.
3. Each agent downloads `GET /agent/files/<id>` with its own credentials, saves it to the target path, and reports the result.

So uploading to 1 host and to 50 hosts uses the **same** code path.

## 5. Agent enrollment

```
1. An admin creates an "enroll token" for a device group in the dashboard (valid 24 h, reusable).
2. The dashboard shows the install command:
     curl -fsSL https://headnode.example.com/install.sh | sh -s -- --token <TOKEN>
3. The agent calls POST /agent/enroll {token, hostname, os}
   → the server replies {agent_id, agent_secret} (the secret is sent ONCE)
4. The agent saves both to agent.json (permission 0600) and installs itself as a service.
5. Every later connection sends the header  Authorization: Bearer <agent_id>.<agent_secret>
```

The server stores only a **hash** of the secret (`D-09`). If a host is stolen or decommissioned, the admin deletes the agent in the dashboard and its secret stops working.

## 6. Data model (SQLite)

Only the tables the MVP needs. The exact schema will live in `server/schema.sql`.

```
users          id, username, password_hash, is_admin, created_at
groups         id, name
group_members  user_id, group_id, role            -- role: 'operator' | 'viewer'
agents         id, group_id, name, secret_hash, hostname, os, arch, version,
               info_json, wan_addr, last_seen_at, created_at
enroll_tokens  token_hash, group_id, expires_at, created_by
web_sessions   token_hash, user_id, expires_at     -- login cookie
files          id, name, size, sha256, uploaded_by, created_at
audit_log      id, at, user_id, agent_id, action, detail
```

**Online** status is not stored in the database. It exists only in memory: "is there an open control connection?". `last_seen_at` is written on heartbeat so the dashboard can show "last online 3 hours ago".

### Permissions (simple RBAC)

| Role | See hosts | Shell / tunnel | Files (read) | Files (write/send) | Manage users, groups, tokens |
|---|---|---|---|---|---|
| `is_admin` | all | ✓ | ✓ | ✓ | ✓ |
| `operator` (per group) | its groups | ✓ | ✓ | ✓ | — |
| `viewer` (per group) | its groups | — | ✓ | — | — |

All checks go through **one function**, `can(user, agent, action)`, so they are easy to audit.

## 7. Security

This system is effectively a "master key" to every host, so the items below are required from day one, not "later":

| Threat | Countermeasure |
|---|---|
| Someone else opens the dashboard | Login required; passwords hashed with bcrypt; cookie `HttpOnly; Secure; SameSite=Lax` |
| Another site triggers actions as the user (CSRF / CSWSH) | Check the `Origin` header on every POST and every browser WebSocket |
| A fake agent pretends to be another host | A secret per agent, stored only as a hash |
| Eavesdropping | Everything over TLS (Caddy); the agent rejects servers without a valid certificate (except with an `--insecure` flag for dev) |
| Command injection | No `sh -c` with concatenated strings; arguments are always separate |
| Abuse by a legitimate user | Audit log: login, enroll, session open, file operations, file sends |
| Login brute force | Growing delay after repeated failures per IP (simple, in memory) |

Limitation **accepted** for now: the agent usually runs as root/SYSTEM. Anyone allowed to open a shell on a host is treated as allowed to do anything on that host.

## 8. Code layout (planned)

```
cmd/headnode/main.go        flags + start the server
cmd/headnode-agent/main.go  subcommands: run | enroll | service install/uninstall
cmd/hnctl/main.go           (M5) forward
server/                     one package, split by FILE:
  http.go                   routing + login/origin middleware
  auth.go                   login, cookies, can()
  ratelimit.go              login attempt limiter
  permissions.go            roles, can()
  admin.go                  admin API: users, groups, members
  audit.go                  audit log writer
  agents.go                 online agent registry, control connections
  sessions.go               session table, open/attach handlers
  relay.go                  copies messages between two WebSockets
  files.go                  upload / download / fetch
  db.go, schema.sql         SQLite access
  enroll.go                 enroll tokens, agent enrollment
  hosts.go                  host list / detail API
  audit.go
agent/                      conn.go (control + reconnect), shell.go, files.go, tunnel.go, sysinfo.go
protocol/                   JSON message structs shared by server and agent
web/                        index.html, *.js, style.css (embedded into the server binary)
```

## 9. Deliberately NOT included yet

Remote desktop (KVM/VNC/RDP), CCTV, agent auto-update, 2FA, SSO, multiple servers, plugins, notifications. Any of these may be added later, but the architecture above is **not** prepared for them in advance (`D-10`). When the time comes, we change the architecture.
