# Protocol

The communication contract between the browser/`hnctl`, the `headnode` server, and `headnode-agent`. If the code and this document disagree, one of them is a bug. Change both in the same commit.

Protocol version: **1**

## 1. HTTP endpoints

All API responses are JSON. Errors use the shape `{"error": "message"}` with a matching HTTP status (400, 401, 403, 404, 409, 500).

### For the browser and hnctl (login required, except `/api/login`)

| Method | Path | Purpose | Milestone |
|---|---|---|---|
| `POST` | `/api/login` | `{username, password}` → sets the `hn_session` cookie | M2 |
| `POST` | `/api/logout` | Ends the login session | M2 |
| `GET` | `/api/me` | Current user and their roles | M2 |
| `GET` | `/api/hosts` | Agents the user may see, with online status | M1 |
| `GET` | `/api/hosts/{id}` | Details of one agent | M1 |
| `PATCH` | `/api/hosts/{id}` | Change `name` / `group_id` | M2 |
| `DELETE` | `/api/hosts/{id}` | Delete an agent; its secret stops working | M2 |
| `POST` | `/api/hosts/{id}/sessions` | `{kind, params}` → `{id}` (see §4) | M3 |
| `GET` | `/api/hosts/{id}/download?path=…` | Download a file from a host (streamed) | M4 |
| `POST` | `/api/files` | Multipart upload → `{id, name, size, sha256}` | M4 |
| `POST` | `/api/transfers` | `{file_id, agent_ids[], dest_path}` → `{id}` | M4 |
| `GET` | `/api/transfers/{id}` | Status per agent: `pending` / `ok` / `error` | M4 |
| `GET`, `POST` | `/api/groups` | List / create groups (admin) | M2 |
| `GET`, `POST` | `/api/users` | List / create users (admin) | M2 |
| `GET` | `/api/groups/{id}` | One group with its members (admin) | M2 |
| `PUT` | `/api/groups/{id}/members/{user_id}` | `{role}`: add a member or change their role (admin) | M2 |
| `DELETE` | `/api/groups/{id}/members/{user_id}` | Remove a member (admin) | M2 |
| `POST` | `/api/enroll-tokens` | `{group_id}` → `{token, expires_at, group_id, group_name, install_command}` (admin) | M2 |
| `GET` | `/api/audit?limit=&before=` | Audit log (admin) | M2 |

M1 has no login yet, so enroll tokens are created with a CLI command on the server machine (`headnode token create`), not over HTTP. That way there is never an unauthenticated endpoint that creates tokens.

Since M2 every `/api/*` route except `POST /api/login` needs a login. The group filter (a user sees only hosts in their groups) arrives with groups, later in M2.

#### Host objects (M1)

`GET /api/hosts` → a JSON array (`[]` when empty) of:
```json
{"id":"a_4411ec05776b5fa5","name":"web-01","hostname":"web-01","os":"linux","arch":"amd64",
 "version":"0.1.0","online":true,"group_id":1,"group_name":"Office",
 "last_seen_at":"2026-11-20T04:19:22Z","created_at":"2026-11-20T04:18:06Z",
 "heartbeat":{"cpu_percent":2.4,"mem_used":7516192768,"mem_total":16523436032,
              "disk_used":41000000000,"disk_total":250000000000,"uptime_sec":67240},
 "info":{"protocol":1,"agent_version":"0.1.0","hostname":"web-01","os":"linux","arch":"amd64",
         "platform":"Debian GNU/Linux 12 (bookworm)","kernel":"6.1.0-18-amd64",
         "cpu_model":"Intel(R) Core(TM) i5-12400","cpu_cores":12,
         "mem_total":16523436032,"lan_ips":["192.168.10.2"]}}
```
- `last_seen_at` is `null` if the agent never connected.
- `heartbeat` is `null` when the host is offline (so old numbers are never shown as live) or before its first heartbeat.
- `online` is true exactly while the agent's control connection is open.

- `info` is the agent's last `hello` data (§3), or `null` before the first hello. It is in the list too, because the Hosts page shows addresses and platform.
- `group_id` and `group_name` are `null` for a host that is in no group.

**Who sees which hosts:** an admin sees every host. Anyone else sees the hosts in groups they belong to, with any role. A host in no group is visible to admins only. `GET /api/hosts/{id}` for a host the user may not see answers `404`, the same as for an id that does not exist, so it does not reveal which hosts exist.

`GET /api/hosts/{id}` → one object of the same shape. Unknown id → `404 {"error":"host not found"}`.

The agent-supplied strings in these objects (`hostname`, `platform`, `kernel`, …) are **untrusted**. Clients must show them as text, never as HTML.

#### Users, groups and members (M2, admin only)

Every route here answers `403 {"error":"admin only"}` to a logged-in non-admin.

| Request | Reply |
|---|---|
| `GET /api/users` | `[{"id":1,"username":"admin","is_admin":true,"created_at":"…"}]` |
| `POST /api/users` `{"username","password","is_admin"}` | `201` + the user. `400` for a bad username (1-32 of `a-z 0-9 . _ -`, lowercased) or a password that is not 8-72 bytes. `409` if the name is taken (case-insensitive). The reply never contains the password or its hash. |
| `GET /api/groups` | `[{"id":1,"name":"Office","member_count":2,"host_count":3}]` |
| `POST /api/groups` `{"name"}` | `201 {"id":1,"name":"Office","member_count":0,"host_count":0}`. `400` if the name is empty, longer than 64 characters or has control characters. `409` if it exists (case-insensitive). |
| `GET /api/groups/{id}` | The group plus `"members":[{"user_id":2,"username":"alice","role":"operator"}]` and `"hosts":[…]`, the hosts in the group as full host objects (the same shape as `GET /api/hosts`). Both lists are `[]` when empty. `404` if it does not exist. |
| `PUT /api/groups/{id}/members/{user_id}` `{"role"}` | `204`. `role` is `operator` or `viewer` (`400` otherwise). Adds the user, or changes their role if they are already a member. `404` for an unknown group or user. |
| `DELETE /api/groups/{id}/members/{user_id}` | `204`. `404` if the group or user is unknown, or the user is not a member. |

Roles (architecture.md §6): an `operator` can view, use shells and tunnels, read files and change or send files. A `viewer` can view and read files only.

#### Audit log (M2, admin only)

`GET /api/audit?limit=&before=` → a JSON array (`[]` when empty), newest first:

```json
{"id":42,"at":"2026-11-20T05:15:46Z","user_id":1,"username":"admin","agent_id":null,"host_name":null,
 "action":"login","detail":"ip=127.0.0.1"}
```

- `limit` is how many entries to return: default 50, at most 200 (a bigger number is capped, not refused). `before` is an entry id: only older entries come back, so a client pages backwards by passing the `id` of the last entry it has. Ids only grow, so new entries never shift a page. A `limit` or `before` that is not a positive number is `400`.
- The log stores ids, because it must outlive the users and hosts it mentions. `username` and `host_name` are looked up when the log is read, and are `null` when the user or host no longer exists, or never applied (a failed login has no user, an enroll has no user, a login has no host).
- `detail` is free text for people, and can contain text that came from an agent (a hostname). Clients must show it as text.

Actions written so far: `login`, `login_failed`, `logout`, `user_create`, `group_create`, `member_set`, `member_remove`, `agent_enroll`, `token_create`, `host_rename`, `host_move`, `host_delete`. A failed login records the address only, never the username that was typed.

#### Host management and enroll tokens (M2, admin only)

| Request | Reply |
|---|---|
| `PATCH /api/hosts/{id}` | `200` + the updated host object. Body: `name` and/or `group_id`. See below. |
| `DELETE /api/hosts/{id}` | `204`. The host's secret stops working immediately and a live control connection is closed. `404` if unknown. |
| `POST /api/enroll-tokens` `{"group_id"}` | `201 {"token","expires_at","group_id","group_name","install_command"}`. `group_id` is a group id, or `null` or left out for **no group**. `404` if the group is unknown. The body must be a JSON object (`{}` is fine). |

`PATCH` body fields, both optional, at least one required (`400` otherwise):
- `"name"`: the display name, 1-255 characters, no control characters. The `hostname` the agent reported does not change.
- `"group_id"`: a group id moves the host there, `null` takes it out of its group (then only admins see it), and leaving the field out keeps it where it is. An unknown group is `404`, and a value that is neither an id nor `null` is `400`.

The request is checked completely before anything changes: a good `name` with an unknown `group_id` changes nothing.

The enroll token is shown **once**, in this reply. A host that enrolls with it joins the token's group, or has no group if the token was made without one (then only admins see it until an admin moves it). `group_id` and `group_name` in the reply are `null` in that case. Like every enroll token it is valid for 24 hours and can enroll many hosts. `install_command` is the command to run on the host. Until the installer script exists (M6) it is `headnode-agent enroll -server <this server> -token <token> && headnode-agent run`. The server address is taken from the request's `Host` header, with `https` when the request came in over HTTPS (see the cookie rules above).

Tokens made with `headnode token create` on the server's command line belong to no group, so those hosts start hidden from everyone but admins until an admin moves them.

**Client authentication:**
- The browser uses the `hn_session` cookie. Requests that change data (POST/PATCH/PUT/DELETE) **and** WebSockets must carry an `Origin` header equal to the server's origin.
- `hnctl` uses the header `Authorization: Bearer <token>`. It gets the token from `POST /api/login` with `{"client":"cli"}`. Bearer-token requests skip the `Origin` check because they don't come from a browser. *(Not built yet: it arrives with `hnctl` in M5. Until then only the cookie works.)*

#### Login, logout, me (M2)

`POST /api/login` with `{"username":"admin","password":"…"}` → `200 {"id":1,"username":"admin","is_admin":true}` and a cookie:

```
Set-Cookie: hn_session=<64 hex characters>; Path=/; Max-Age=604800; HttpOnly; SameSite=Lax
```

`Secure` is added when the request came in over HTTPS, either directly or through a proxy on the same machine that sends `X-Forwarded-Proto: https`. On plain HTTP (development) it is left out, because the browser would otherwise drop the cookie.

| Status | When |
|---|---|
| 200 | Logged in. |
| 400 | Body is not valid JSON. |
| 401 | Wrong username or password. One message for both, so it doesn't reveal which usernames exist. |
| 403 | The `Origin` header is missing or belongs to another site. |
| 429 | Too many wrong passwords from this address. The `Retry-After` header says how many seconds to wait. |

`POST /api/logout` → `204`. Deletes the session on the server and clears the cookie.

`GET /api/me` → `{"id":1,"username":"admin","is_admin":true}`. Per-group roles are added with groups.

Rules for every other `/api/*` request:
- No valid session → `401 {"error":"login required"}`. This also applies to paths that do not exist.
- `POST`, `PATCH`, `PUT` and `DELETE` need an `Origin` header whose host equals the request's `Host` header; otherwise `403 {"error":"bad origin"}`. A missing `Origin` counts as wrong. Login is checked first, so a request with no login always gets `401`.
- A session lasts 7 days from login. It is not extended by use.

**Login rate limit:** each client address gets 3 wrong passwords for free. Every further one in a row adds a wait: 1 s, 2 s, 4 s, … up to 15 minutes. A successful login clears the count, and an address with no failures for an hour starts fresh. While the wait is running even the correct password is refused with `429`. Behind Caddy the client address is the last entry of `X-Forwarded-For`, and that header is believed only when the request comes from this machine.

### For the agent

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/agent/enroll` | `{token, hostname, os, arch}` → `{agent_id, agent_secret}` |
| `GET` (WS) | `/agent/connect` | Control connection (§3) |
| `GET` (WS) | `/agent/sessions/{id}` | The agent's side of a session (§4) |
| `GET` | `/agent/files/{id}` | Download a file for `fetch` |
| `GET` | `/install.sh`, `/download/headnode-agent-{os}-{arch}` | Installer and agent binary (M6) |

Every agent endpoint except `enroll` and the installer downloads must carry `Authorization: Bearer <agent_id>.<agent_secret>`.

#### `POST /agent/enroll`

The enroll token in the body is the only credential. The token is **not** consumed: it stays valid until it expires, so one token can enroll many hosts.

```json
→ {"token":"945e…","hostname":"web-01","os":"linux","arch":"amd64"}
← 200 {"agent_id":"a_1f3c9e0b7d2a4c58","agent_secret":"<64 hex characters>"}
```

| Status | When |
|---|---|
| 200 | Enrolled. The secret is sent only in this response. |
| 400 | Body is not valid JSON, `token` or `hostname` is missing, or a field is too long (`hostname` ≤ 255, `os`/`arch` ≤ 32). |
| 401 | The token is unknown or expired. The two cases are deliberately not told apart. |

The response carries `Cache-Control: no-store` because it contains the secret.

The agent saves the reply, together with the server URL, to `agent.json` (permission `0600`):

```json
{"server":"http://127.0.0.1:8080","agent_id":"a_1f3c…","agent_secret":"…"}
```

### Client-side WebSocket

| Path | Used by |
|---|---|
| `/ui/sessions/{id}` | Browser (shell, files) and `hnctl` (tunnel) |

## 2. JSON message format (control connection)

Each message is one WebSocket **text frame** with an envelope:

```json
{"type": "heartbeat", "data": { ... }}
```

In Go, the envelope is decoded first into a struct with `Data json.RawMessage`, and then `data` is decoded according to `type`. No library or reflection tricks needed.

## 3. Control connection `/agent/connect`

The agent connects with `Authorization: Bearer <agent_id>.<agent_secret>`. Wrong or missing credentials get a plain HTTP `401` and no WebSocket is created.

After the upgrade, `hello` must be the first message and must arrive within 10 seconds. Otherwise the server closes the connection (1008, or just closes it if nothing arrives at all).

Only one control connection per agent is kept. If the same agent connects again, the newest connection wins and the older one is closed (D-17).

### Agent → Server

**`hello`**: the first message, required, sent once.
```json
{"type":"hello","data":{
  "protocol": 1,
  "agent_version": "0.1.0",
  "hostname": "web-01",
  "os": "linux", "arch": "amd64",
  "platform": "Debian GNU/Linux 12 (bookworm)",
  "kernel": "6.1.0-18-amd64",
  "cpu_model": "Intel(R) Core(TM) i5-12400", "cpu_cores": 12,
  "mem_total": 16523436032,
  "lan_ips": ["192.168.10.2"]
}}
```
If `protocol` does not match, the server closes the connection with reason `"protocol mismatch"`.
The agent does not send its WAN IP. The server reads it from the connection address. The `X-Forwarded-For` header is trusted only when the request comes from Caddy on localhost.

**`heartbeat`**: every 20 seconds.
```json
{"type":"heartbeat","data":{
  "cpu_percent": 2.4,
  "mem_used": 7516192768, "mem_total": 16523436032,
  "disk_used": 41000000000, "disk_total": 250000000000,
  "uptime_sec": 67240
}}
```
The server treats the agent as **dead** if no message at all arrives within 60 seconds, and closes the connection. This is the only liveness mechanism. There is no separate ping/pong.

**`fetch_result`**: the outcome of a `fetch` command.
```json
{"type":"fetch_result","data":{"transfer_id":"t_9f…","ok":true,"error":""}}
```

### Server → Agent

**`open`**: asks the agent to open a session.
```json
{"type":"open","data":{"session_id":"s_3a7c…","kind":"shell","params":{"cols":120,"rows":32}}}
```
The agent then dials `wss://…/agent/sessions/s_3a7c…`. If the agent fails to start the session (for example, the path doesn't exist), it still dials and then immediately closes the WebSocket with the error as the reason. That way the browser gets a clear message.

**`fetch`**: asks the agent to download a file from the server.
```json
{"type":"fetch","data":{"transfer_id":"t_9f…","file_id":"f_12…","dest_path":"/root/cplane-manager","mode":"0755"}}
```
The agent writes to `dest_path + ".part"` first, checks the sha256, then renames. That way a half-written file never appears at the target path.

## 4. Sessions

`POST /api/hosts/{id}/sessions` with `{"kind": "...", "params": {...}}`. If the agent is offline, the response is `409 {"error":"host offline"}`.

### `shell`
- params: `{"cols": 120, "rows": 32}`
- The agent runs the first available of `$SHELL`, `/bin/bash`, `/bin/sh` inside a PTY. On Windows: `powershell.exe` (later).
- **Binary frames**, both ways = raw terminal bytes.
- **Text frames**, browser → agent = control: `{"type":"resize","cols":140,"rows":40}`.

### `files`
- params: `{}`
- Text-frame request/response. `id` matches each response to its request.

```json
→ {"id":1,"op":"list","path":"/root"}
← {"id":1,"ok":true,"entries":[{"name":"a.txt","is_dir":false,"size":120,"mode":"-rw-r--r--","mod_time":"2026-11-19T10:00:00Z"}]}

→ {"id":2,"op":"mkdir","path":"/root/new"}
→ {"id":3,"op":"rename","from":"/root/a.txt","to":"/root/b.txt"}
→ {"id":4,"op":"delete","path":"/root/new","recursive":true}
→ {"id":5,"op":"stat","path":"/root/b.txt"}
← {"id":5,"ok":false,"error":"no such file or directory"}
```
Paths are always absolute. The agent rejects relative paths.

### `download`
- params: `{"path": "/var/log/syslog"}`
- Used only internally by `GET /api/hosts/{id}/download`. The agent's first text frame is `{"name":"syslog","size":12345}`. Binary frames with the file contents follow, until the agent closes the connection normally.

### `tunnel`
- params: `{"target": "127.0.0.1:22"}`
- The agent calls `net.Dial("tcp", target)` and copies raw bytes both ways in binary frames.
- One TCP connection on the laptop = one `tunnel` session.

## 5. WebSocket close codes

| Code | Meaning |
|---|---|
| 1000 | Normal (session finished, EOF) |
| 1008 | Rejected: auth / permission / protocol mismatch |
| 1011 | Error in agent or server; `reason` holds a short message |
| 4000 | The other side of the session did not arrive within 15 seconds |
