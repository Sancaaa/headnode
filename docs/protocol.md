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
| `PUT` | `/api/groups/{id}/members/{user_id}` | `{role}` | M2 |
| `POST` | `/api/groups/{id}/enroll-tokens` | → `{token, expires_at, install_command}` (admin) | M2 |
| `GET` | `/api/audit?limit=&before=` | Audit log (admin) | M2 |

M1 has no login yet, so enroll tokens are created with a CLI command on the server machine (`headnode token create`), not over HTTP. That way there is never an unauthenticated endpoint that creates tokens.

**Client authentication:**
- The browser uses the `hn_session` cookie. Requests that change data (POST/PATCH/PUT/DELETE) **and** WebSockets must carry an `Origin` header equal to the server's origin.
- `hnctl` uses the header `Authorization: Bearer <token>`. It gets the token from `POST /api/login` with `{"client":"cli"}`. Bearer-token requests skip the `Origin` check because they don't come from a browser.

### For the agent

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/agent/enroll` | `{token, hostname, os, arch}` → `{agent_id, agent_secret}` |
| `GET` (WS) | `/agent/connect` | Control connection (§3) |
| `GET` (WS) | `/agent/sessions/{id}` | The agent's side of a session (§4) |
| `GET` | `/agent/files/{id}` | Download a file for `fetch` |
| `GET` | `/install.sh`, `/download/headnode-agent-{os}-{arch}` | Installer and agent binary (M6) |

Every agent endpoint except `enroll` and the installer downloads must carry `Authorization: Bearer <agent_id>.<agent_secret>`.

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
