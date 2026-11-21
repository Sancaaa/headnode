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
| `GET` | `/api/me` | Current user, what they may do, and the server's version and start time | M2 |
| `GET` | `/api/hosts` | Agents the user may see, with online status | M1 |
| `GET` | `/api/hosts/{id}` | Details of one agent | M1 |
| `PATCH` | `/api/hosts/{id}` | Change `name` / `group_id` | M2 |
| `DELETE` | `/api/hosts/{id}` | Delete an agent; its secret stops working | M2 |
| `POST` | `/api/hosts/{id}/sessions` | `{kind, params}` → `{id}` (see §4) | M3 |
| `GET` | `/api/hosts/{id}/download?path=…` | Download a file from a host (streamed), or a folder as a zip | M4 |
| `POST` | `/api/hosts/{id}/pull` | `{path}` → copy a file from a host to the server's uploads → `{id, name, size, sha256}` | M5.5 |
| `POST` | `/api/files` | Multipart upload → `{id, name, size, sha256}` | M4 |
| `GET` | `/api/files` | Every upload on the server, newest first (admin) | M5.5 |
| `DELETE` | `/api/files/{id}` | Delete an upload and, if nothing else uses it, its file on disk (admin) | M5.5 |
| `POST` | `/api/transfers` | `{file_id, agent_ids[], dest_path, mode}` → the transfer, with a status per host | M4 |
| `GET` | `/api/transfers` | The user's transfers, newest first (an admin sees everyone's, at most 50) | M4 |
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
 "version":"0.1.0","online":true,"wan_addr":"203.0.113.5","can_shell":true,
 "group_id":1,"group_name":"Office",
 "last_seen_at":"2026-11-20T04:19:22Z","created_at":"2026-11-20T04:18:06Z",
 "heartbeat":{"cpu_percent":2.4,"mem_used":7516192768,"mem_total":16523436032,
              "disk_used":41000000000,"disk_total":250000000000,"uptime_sec":67240,"vnc":false},
 "info":{"protocol":1,"agent_version":"0.1.0","hostname":"web-01","os":"linux","arch":"amd64",
         "platform":"Debian GNU/Linux 12 (bookworm)","kernel":"6.1.0-18-amd64",
         "cpu_model":"Intel(R) Core(TM) i5-12400","cpu_cores":12,
         "mem_total":16523436032,"lan_ips":["192.168.10.2"]}}
```
- `last_seen_at` is `null` if the agent never connected.
- `update_available` (always present) is `true` when the agents folder (`dist/`) holds a newer agent than the host's `version`, and then `latest_version` says which. The server reads the file `VERSION` in that folder **each time the list is built** (the build scripts write it: the agent's `Version` constant, like `0.10.0`) and compares it with `version` number by number, so `0.9.2` is older than `0.10.0`. No `VERSION` file, one that is not a version, or a host whose version is unknown, means `false`, not an error. It only says so: updating is done by hand on each host with the install command (D-66).
- `heartbeat` is `null` when the host is offline (so old numbers are never shown as live) or before its first heartbeat.
- `online` is true exactly while the agent's control connection is open.

- `info` is the agent's last `hello` data (§3), or `null` before the first hello. It is in the list too, because the Hosts page shows addresses and platform.
- `group_id` and `group_name` are `null` for a host that is in no group.
- `wan_addr` is the address the **server** saw the agent's control connection come from, as of its last connect (§3). `""` if the agent never connected. The agent sends nothing for it. Behind Caddy it is the client address from `X-Forwarded-For`; on a local test it is `127.0.0.1`.
- `can_shell` says whether the current user may open a terminal on this host (an operator in its group, or an admin). The Hosts page shows its Shell button only when this is true and the host is online. It is computed for the user making the request, so the same host can say `true` to one person and `false` to another.
- `can_files_write` is the same for sending files and changing them (an operator in its group, or an admin). The Send file dialog lists only hosts where it is true.

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

Actions written so far: `login`, `login_failed`, `logout`, `user_create`, `group_create`, `member_set`, `member_remove`, `agent_enroll`, `token_create`, `host_rename`, `host_move`, `host_delete`, `session_open`, `session_close`, `vnc_on`, `vnc_off`, `file_download`, `file_upload`, `upload_delete`, `file_mkdir`, `file_rename`, `file_copy`, `file_chmod`, `file_write`, `file_delete`, `transfer_start`, `transfer_result`. A failed login records the address only, never the username that was typed.

#### Uploading and sending files (M4)

**`POST /api/files`**: a `multipart/form-data` body with one part named `file`. Allowed for an admin and for anyone who is an operator in at least one group (`403` otherwise; `503` if the server has no files directory). The file is streamed to disk while it is hashed, so the server never holds it in memory, and it is stored as `data/files/<sha256>` (the same bytes uploaded twice are stored once). The name is cleaned: no folders, no control characters, at most 255 bytes.

```json
← 201 {"id":"f_5a1c…","name":"k3s-manager","size":15728640,"sha256":"9f86d081…"}
```

| Status | When |
|---|---|
| 400 | The body is not multipart, or has no `file` part, or the upload broke off. |
| 403 | No permission, or a missing/foreign `Origin` (it is a change request). |
| 413 | Larger than 4 GB. |

**`POST /api/transfers`**: `{"file_id":"f_…","agent_ids":["a_…","a_…"],"dest_path":"/usr/local/bin/k3s-manager","mode":"0755"}`. `mode` is three octal digits (`644`, `0755`) and defaults to `0644`. At most 200 hosts. The user needs `files_write` on **every** host, and the file must be their own upload (an admin may send any). Everything is checked before anything is sent: one bad host refuses the whole request, and none of the others is started.

With `"extract": true` the file must be a `.zip` (`400` otherwise) and `dest_path` is an **existing folder** the archive is unpacked into, instead of a path to save the file at. `mode` is not used then (it is ignored, and the transfer's `mode` is `""`): every file keeps the mode it had in the archive. The transfer object has `"extract": true`, and the audit entry `transfer_start` ends with ` unpack=yes`. This is how a whole folder is sent: the file manager's "Send to hosts…" copies the folder to the server as a zip (`pull`, below), then sends it with `extract`. See `fetch` in §3 for what the agent checks.

| Status | When |
|---|---|
| 201 | Started. Each online host was sent a `fetch`; an offline host is already `error` with `"host offline"`. |
| 400 | No hosts or more than 200, empty `dest_path`, or a bad `mode`. |
| 403 | The user can see a host but may not write to it (a `viewer`). |
| 404 | A host does not exist or the user may not see it, or the file is not found or is somebody else's. |

```json
← 201 {"id":"t_9f3a…","file_name":"k3s-manager","file_size":15728640,"dest_path":"/usr/local/bin/k3s-manager",
       "mode":"0755","started_by":"alice","created_at":"2026-11-20T09:00:00Z",
       "hosts":[{"agent_id":"a_1…","host_name":"web-01","status":"pending"},
                {"agent_id":"a_2…","host_name":"web-02","status":"error","error":"host offline"}]}
```

**`GET /api/transfers`** returns an array (`[]` when empty) of these objects, newest first: the user's own, or everyone's for an admin, at most 50. It is what the Transfers page lists.

**`GET /api/transfers/{id}`** returns the same object with the current status of each host: `pending`, `ok` or `error` (with `error` text). Only the user who started the transfer, or an admin, can read it (`404` for anyone else). `host_name` is `null` if the host has been deleted since.

**`POST /api/hosts/{id}/pull`**: `{"path":"/etc/app/app.conf"}`. Copies a file **or a folder** from a host to the server and stores it as an upload (a folder arrives as a zip archive named `<folder>.zip`, built as it is sent, exactly as for a folder download; the reply then has `"archive": true`, and it is sent on with `extract`) (the same as `POST /api/files`, so it is listed, can be sent with `POST /api/transfers`, and an admin can delete it). It is what "Send to hosts…" in the file manager uses. The bytes come over a `download` session and are streamed to disk while they are hashed. The user needs `files_read` on the host **and** the right to upload (`403` otherwise); `404` for a host they cannot see. The reply is the same as an upload's, plus `archive`: `201 {"id","name","size","sha256","archive":false}`. The audit log has `file_upload` with `from=<path>` (and ` (a folder, zipped)` for a folder), on that host.

| Status | When |
|---|---|
| 400 | No `path`, or a path over 4096 bytes. |
| 403 | No permission to read on the host, no right to upload, or a missing/foreign `Origin`. |
| 404 | The host does not exist or cannot be seen, or the file is not there. |
| 409 | The host is offline, or the file changed size while it was being copied (a log that is written to): nothing is stored. |
| 413 | Larger than 4 GB. |
| 502 | The host broke off in the middle: nothing is stored. |

**`GET /api/files`** (admin only, `403` for anyone else) returns an array (`[]` when empty) of every upload, newest first, at most 500. Uploads are never removed by the server itself.
```json
← 200 [{"id":"f_5a1c…","name":"k3s-manager","size":15728640,"uploaded_by":"alice",
        "created_at":"2026-11-20T09:00:00Z","in_use":false}]
```
`uploaded_by` is `null` if that user has been deleted. `in_use` is true while a transfer of the file is still waiting for a host.

**`DELETE /api/files/{id}`** (admin only, and Origin-checked like every change) deletes one upload. The same bytes uploaded twice are two uploads that share one file on disk (`data/files/<sha256>`), so the file on disk is removed only when no other upload still points at it. Past transfers of it stay in the transfer list.

| Status | When |
|---|---|
| 204 | Deleted. The audit log has `upload_delete`, with the file name, the first 12 characters of its hash, and `blob=removed` or `blob=kept (another upload uses it)`. |
| 403 | Not an admin, or a missing/foreign `Origin`. |
| 404 | No such upload. |
| 409 | A transfer of this file is still waiting for a host (`{"error":"this file is being sent to a host right now: …"}`). Delete it after the transfer finishes. A transfer that has been unanswered for more than an hour no longer blocks. |
| 500 | The row was removed but the file could not be deleted from disk. The server log says why. |

Status is kept in the server's memory (D-03) for 24 hours. A host whose control connection drops while its transfer is pending is marked `error` with `"the host disconnected before it reported a result"`, since it can no longer report. The file may in fact have arrived. The audit log has `file_upload`, `transfer_start` (once, with the file, the first 12 characters of its hash, the destination and the number of hosts) and `transfer_result` (once per host).

#### Starting and stopping a host's VNC server (D-51)

`POST /api/hosts/{id}/vnc` with `{"enabled": true|false}` → `202 {"ok":true}` as soon as the agent has been told (the result shows in `heartbeat.vnc` a few seconds later). It needs the same permission as a shell (operator in the host's group, or admin). `404` for a host the user may not see, `403` without the permission, `409` when the host is offline or its agent does not report `vnc_control` (it has no VNC server it can control). Audited as `vnc_on` or `vnc_off`. See `vnc_set` in §3.

#### Host management and enroll tokens (M2, admin only)

| Request | Reply |
|---|---|
| `PATCH /api/hosts/{id}` | `200` + the updated host object. Body: `name` and/or `group_id`. See below. |
| `DELETE /api/hosts/{id}` | `204`. The host's secret stops working immediately and a live control connection is closed. `404` if unknown. |
| `POST /api/enroll-tokens` `{"group_id"}` | `201 {"token","expires_at","group_id","group_name","install_command"}` (`install_command` is `curl -fsSL <server>/install.sh \| sudo sh -s -- --token <token>`; `install_command_windows` is `& ([scriptblock]::Create((irm <server>/install.ps1))) -Token <token>`). `group_id` is a group id, or `null` or left out for **no group**. `404` if the group is unknown. The body must be a JSON object (`{}` is fine). |

`PATCH` body fields, both optional, at least one required (`400` otherwise):
- `"name"`: the display name, 1-255 characters, no control characters. The `hostname` the agent reported does not change.
- `"group_id"`: a group id moves the host there, `null` takes it out of its group (then only admins see it), and leaving the field out keeps it where it is. An unknown group is `404`, and a value that is neither an id nor `null` is `400`.

The request is checked completely before anything changes: a good `name` with an unknown `group_id` changes nothing.

The enroll token is shown **once**, in this reply. A host that enrolls with it joins the token's group, or has no group if the token was made without one (then only admins see it until an admin moves it). `group_id` and `group_name` in the reply are `null` in that case. Like every enroll token it is valid for 24 hours and can enroll many hosts. `install_command` is the command to run on the host. Until the installer script exists (M6) it is `headnode-agent enroll -server <this server> -token <token> && headnode-agent run`. The server address is taken from the request's `Host` header, with `https` when the request came in over HTTPS (see the cookie rules above).

Tokens made with `headnode token create` on the server's command line belong to no group, so those hosts start hidden from everyone but admins until an admin moves them.

**Client authentication:**
- The browser uses the `hn_session` cookie. Requests that change data (POST/PATCH/PUT/DELETE) **and** WebSockets must carry an `Origin` header equal to the server's origin.
- `hnctl` uses the header `Authorization: Bearer <token>`. It gets the token from `POST /api/login` with `{"client":"cli"}`: the reply then carries `"token"` (64 hex characters, shown once) and **no cookie is set**. The token is the same kind of session as a cookie (7 days, stored only as a hash), and `POST /api/logout` with it ends it.
- Bearer-token requests skip the `Origin` check, because that check protects credentials a browser adds by itself (a cookie goes along with every request to the site, including ones another website triggers), and a bearer token is only sent by code that was given it. For the same reason `POST /api/login` with `"client":"cli"` needs no `Origin`; any other login still does.
- A request that carries an `Authorization` header is judged on it alone. An unknown token, an empty one, or any other scheme (`Basic …`, lowercase `bearer …`) is `401`, even if a valid cookie is sent along.
- The browser WebSocket `/ui/sessions/{id}` accepts a bearer token too, in the `Authorization` header of the handshake. This is what `hnctl forward` uses.

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

`GET /api/me` → `{"id":1,"username":"admin","is_admin":true,"can_upload":true,"server_version":"0.1.0","server_started_at":"2026-11-20T08:00:00Z"}`. `can_upload` is true for an admin and for anyone who is an operator in at least one group; the page shows the Transfers link only to them. `server_version` and `server_started_at` feed the line under "headnode" at the top of the sidebar (`0.1.0 · up 3h 12m`): the server sends the moment it started and the browser counts the uptime, so nothing needs to be asked again.

Rules for every other `/api/*` request:
- No valid session → `401 {"error":"login required"}`. This also applies to paths that do not exist.
- `POST`, `PATCH`, `PUT` and `DELETE` need an `Origin` header whose host equals the request's `Host` header; otherwise `403 {"error":"bad origin"}`. A missing `Origin` counts as wrong. Login is checked first, so a request with no login always gets `401`.
- A session lasts 7 days from login. It is not extended by use.

**Login rate limit:** each client address gets 3 wrong passwords for free. Every further one in a row adds a wait: 1 s, 2 s, 4 s, … up to 15 minutes. A successful login clears the count, and an address with no failures for an hour starts fresh. While the wait is running even the correct password is refused with `429`. An attempt counts as a failure from the moment it is let through, before the password is checked, and a successful login takes it back (D-61): attempts sent all at once get the same 4 tries as attempts typed one by one. At most 4 password checks run at the same time; a login that has waited 10 seconds for its turn gets `503`. Behind Caddy the client address is the last entry of `X-Forwarded-For`, and that header is believed only when the request comes from this machine.

### For the agent

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/agent/enroll` | `{token, hostname, os, arch}` → `{agent_id, agent_secret}` |
| `GET` (WS) | `/agent/connect` | Control connection (§3) |
| `GET` (WS) | `/agent/sessions/{id}` | The agent's side of a session (§4) |
| `GET` | `/agent/files/{id}` | Download a file for `fetch`. Served only to an agent that has a pending transfer of exactly that file (`404` for anyone else, `401` without credentials). Supports `Range`. |
| `GET` | `/download/headnode-agent-{os}-{arch}` | The agent program for one platform. **No login**: see below. |
| `GET` | `/install.sh`, `/install.ps1` | The installer script for Linux / for Windows, with this server's address filled in. **No login.** |

Every agent endpoint except `enroll` and the installer downloads must carry `Authorization: Bearer <agent_id>.<agent_secret>`.

#### `GET /install.sh` (M6)

A POSIX shell script, `text/x-shellscript`, that a new host pipes into `sh`. It is what the dashboard's **Add host** command runs:

```sh
curl -fsSL https://headnode.example.com/install.sh | sudo sh -s -- --token <TOKEN>
```

**The screen (D-54).** After the service, the script prepares the machine's screen, unless `--no-vnc` is given: if a Wayland desktop is running for the agent's user it makes sure `wayvnc` is installed (with `apt-get`, when missing), if only X11 is running `x11vnc`, and if there is no desktop it says so and does nothing (a server needs no screen). It starts nothing: the screen is off until **Start VNC** in the dashboard (`vnc_set`), when the agent starts the program as the agent's own user on `127.0.0.1:5900` with no login of its own, and **Stop VNC** ends it. A VNC server already on port 5900 is left alone, with a hint if it is open to the network (Raspberry Pi OS's own does that: `sudo raspi-config nonint do_vnc 1` turns it off).

The server fills in its own address (`__SERVER__` in the script) from the request: the scheme comes from `X-Forwarded-Proto` behind Caddy and the host from the `Host` header. Because the client chooses the `Host` header, the address is checked against a strict pattern (a scheme, a host name or IP address, an optional port) and the request is refused with `400` if it does not match, so nothing can end the quotes the address sits in. The reply has `Cache-Control: no-store`. No login is needed, and the script holds no secret: the token is given on the command line.

What the script does, as root, on Linux with systemd (`amd64` or `arm64`): downloads `/download/headnode-agent-linux-<arch>` to `/usr/local/bin/headnode-agent` (through a temporary file and an atomic rename, so a running agent is never replaced by half a file); if `/etc/headnode/agent.json` does not exist, enrolls with `--token` and saves it there (folder `0700`, file `0600`); then runs `headnode-agent service install -user <name>`, which writes a systemd unit, enables it and restarts it. Run again **without** `--token` on a host that is enrolled, it only updates the program and restarts the service.

**Who the agent runs as (D-48).** Installing needs root, but the agent does not run as root unless asked: it runs as the account given with `--user NAME`, and without it as the user who typed `sudo` (`$SUDO_USER`); an update keeps the account chosen at install time (the unit's `User=` line; a unit with no such line, from before the choice existed, gets the default like a new install), and a script run from a root login with no `--user` runs the agent as root (the script says so). A terminal opened from the dashboard is that account's shell, and the file manager sees what that account may see. The credentials folder and file are owned by that account and private (`0700` / `0600`); the unit has `User=<name>` (`User=root` when root was chosen) and `WorkingDirectory=~` (the account's home folder). The user name is checked (`[a-z_][a-z0-9_-]{0,31}`) before it goes in the unit file, and the account must exist. All of it runs inside one function called on the last line, so a download that is cut short does nothing.

#### `GET /install.ps1` (M7)

The same for Windows hosts (D-47): a PowerShell script, served with this server's address filled in, checked and cached exactly like `install.sh`. The dashboard's **Add host** shows it as the Windows command (`install_command_windows` in the enroll token reply):

```powershell
& ([scriptblock]::Create((irm https://headnode.example.com/install.ps1))) -Token <TOKEN>
```

Run in PowerShell opened as administrator, on 64-bit Windows. It downloads `/download/headnode-agent-windows-amd64.exe` to `C:\Program Files\headnode\headnode-agent.exe` (through a temporary file; a running service is stopped for the moment of the replacement, because Windows cannot overwrite a running program); if `C:\ProgramData\headnode\agent.json` does not exist, restricts that folder to `SYSTEM` and `Administrators` and enrolls with `-Token`; then runs `headnode-agent service install`, which registers the Windows service `headnode-agent` (automatic start, restart after a failure) and starts it. Run again without `-Token`, it only updates and restarts. The script is one script block that PowerShell reads completely before running, so a download cut short is a syntax error and does nothing. It reports errors with `throw`, never `exit`, because it runs inside the person's own window.

After the service, it sets up the screen (D-50), unless `-NoVnc` is given: if nothing listens on port 5900 and no `tvnserver` service exists, it downloads TightVNC's MSI from tightvnc.com, requires a valid Authenticode signature from GlavSoft, and installs it silently as a service that accepts loopback connections only (no firewall rule, Ctrl+Alt+Del allowed), with VNC authentication on and a random first password (D-64; `-VncPassword` is ignored now). Each time the agent starts TightVNC it writes a new password to `HKLM\SOFTWARE\TightVNC\Server` (`Password`, `UseVncAuthentication` = 1) and makes that key readable by the system and administrators only. **The tray icon (D-57).** At the end, unless `-NoTray` is given, the script runs `headnode-agent service allow -sid <SID of the person running it>` (the permissions of the `headnode-agent` service get one extra rule for that user: query, start, and send the icon's control codes, SDDL `CCLCRPLOCRRC`; `service install` first resets them, and `tvnserver`'s to the Windows default, D-58). The icon talks to the agent's service with user-defined service control codes, local to the host and not part of the protocol with the server: `128` turn off (the agent sets itself to start by hand and stops), `129` start VNC, `130` stop VNC (the agent switches `tvnserver` as LocalSystem). Turning on is an ordinary service start; a started agent sets itself to start at boot, adds the per-user login entry `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` value `headnode-tray` = `"<program>" tray`, and starts `headnode-agent tray` hidden. The agent writes its connection state to the registry, `HKLM\SOFTWARE\headnode`, value `Status` (`connected <unix time>` or `disconnected <unix time>`, refreshed with every heartbeat; D-63, it was a file under `%ProgramData%` before) for the icon to read: local to the host, not part of the protocol with the server.

If a TightVNC of version 4 or later is installed, which the browser viewer cannot log in to (D-53), it is left alone with a warning, unless `-ReplaceVnc` is given: then it is uninstalled (`msiexec /x`, by its product code from the uninstall registry key) first. It then checks that port 5900 listens on this machine only, and finally **switches the service off**: stopped, and start type manual (D-50). The screen is shared only after someone presses Start VNC in the dashboard (`vnc_set`, D-51). Run again, the script leaves an installed TightVNC as it is. A failure here is a warning and never undoes the agent.

#### `GET /download/headnode-agent-{os}-{arch}` (M6)

The agent program, as `application/octet-stream`, for a platform the server has: `headnode-agent-linux-amd64`, `headnode-agent-linux-arm64` and `headnode-agent-windows-amd64.exe` (the `.exe` is part of the name; D-12, D-47). It needs no login, because a host that is about to enroll has nothing to log in with, and the install command has to fetch it first. That is safe because the program holds no secret: a host belongs to this server only through its enroll token. `Range` is supported, and `Cache-Control: no-cache` makes sure a rebuilt program reaches the next install.

| Status | When |
|---|---|
| 200 / 206 | The program (or the part asked for). |
| 404 | The name is not one of the offered platforms (the list is fixed in the server, so a name can never reach another file), or the server has no agents folder, or that build is not in it (`scripts/build-agents` makes them). |

The server reads them from the folder given with `headnode -agents DIR` (default `dist/`). `scripts/build-agents.ps1` (Windows) and `scripts/build-agents.sh` build every platform into it.

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
The agent does not send its WAN IP. The server reads it from the connection address on every connect and stores it (`wan_addr` in the host object, §1). The `X-Forwarded-For` header is trusted only when the request comes from Caddy on localhost, and then its **last** entry is used (the one Caddy added, not anything the client wrote).

A **Windows** agent (D-47) sends the same two messages with `"os": "windows"`. Its `platform` is the product name from the registry (`"Windows 11 Pro 23H2"`, with the usual "Windows 10" name of Windows 11 corrected from the build number), `kernel` is the system version (`"10.0.22631"`), and `cpu_model` is the processor name. In the heartbeat, `mem_used` is total minus *available* memory (like `MemAvailable` on Linux), the disk numbers are the system drive (usually `C:`), and `uptime_sec` is the time since boot. Nothing else in the protocol changes: the server and the dashboard treat it like any host, except that the page hides **Shell** and **Files** for `"os": "windows"` hosts, which have neither yet.

**`heartbeat`**: every 20 seconds.
```json
{"type":"heartbeat","data":{
  "cpu_percent": 2.4,
  "mem_used": 7516192768, "mem_total": 16523436032,
  "disk_used": 41000000000, "disk_total": 250000000000,
  "uptime_sec": 67240,
  "vnc": true, "vnc_control": true, "vnc_password": "Zx9Qw2Lm"
}}
```
`vnc` is `true` when something accepts connections on `127.0.0.1:5900` on the host (the agent checks once a minute, D-50). The dashboard shows the VNC button and the Screen tab only for hosts where it is `true`. `vnc_control` is `true` when the agent can start and stop the host's VNC server (on Windows a TightVNC service is installed, D-51; on Linux a desktop session is running and `wayvnc` (Wayland) or `x11vnc` (X11) is installed, D-54): the dashboard then offers **Start VNC** and **Stop VNC**. An agent older than 0.8.0 sends neither, which reads as `false`. After a `vnc_set` the agent sends a heartbeat at once, outside the 20-second rhythm, so the new `vnc` value shows up in a few seconds. `vnc_password` (D-64) is the password of the VNC server the agent started: a new random one each time it starts it (TightVNC on Windows, x11vnc on Linux; wayvnc takes none, so it is absent there). It is sent only while `vnc` is `true`. The server keeps it in memory only, removes it from every heartbeat it hands out (the host list and the host page), and gives it only in the reply to a screen tunnel, below.

The server treats the agent as **dead** if no message at all arrives within 60 seconds, and closes the connection. This is the only liveness mechanism. There is no separate ping/pong.

**`fetch_result`**: the outcome of a `fetch` command.
```json
{"type":"fetch_result","data":{"transfer_id":"t_9f…","ok":true,"error":""}}
```

**`file_change`**: what one `mkdir`, `rename` or `delete` in a `files` session did (D-37). Sent after the operation ran, whether it worked or not.
```json
{"type":"file_change","data":{"session_id":"s_3a7c…","op":"rename","path":"/root/a.txt","to":"/root/b.txt","ok":true}}
{"type":"file_change","data":{"session_id":"s_3a7c…","op":"delete","path":"/etc/shadow","recursive":false,"ok":false,"error":"permission denied"}}
```
`op` is `mkdir`, `rename`, `copy`, `chmod`, `write` or `delete`. `path` is the path acted on (for a rename or a copy, the old one). `to` is the new path and appears only for a rename or a copy. `recursive` appears only for a delete, and only when true. `mode` appears only for a chmod: the mode that was asked for, even when it was refused. `size` appears only for a write: how many bytes were sent to be written (the text itself is never reported or audited: a file can hold a password). `error` is short text and appears only when `ok` is false. The report says **what** happened and never **who** did it: the server finds the user from `session_id`, which it created for that user.

Not reported: reads (`list`, `stat`, `home`, `props`), and any change a read-only session refused (nothing was attempted). The server drops, with a warning in its log, a report whose session id is unknown, is not a `files` session, or was opened on a different agent. It keeps the owner of an ended `files` session for 5 minutes, because this message travels on the control connection and the session's traffic on another, so the last report can arrive just after the session ended. A report that cannot be sent is lost (the agent logs a warning); it never breaks the session. The server records each one as the audit action `file_mkdir`, `file_rename`, `file_copy`, `file_chmod`, `file_write` or `file_delete`, with `path=<path>` (a rename or a copy: `path=<old> -> <new>`, a recursive delete: `path=<path> recursive=true`, a chmod: `path=<path> mode=<mode>`, a write: `path=<path> size=<bytes>`) and, on failure, ` FAILED: <error>`.

### Server → Agent

**`open`**: asks the agent to open a session.
```json
{"type":"open","data":{"session_id":"s_3a7c…","kind":"shell","params":{"cols":120,"rows":32}}}
```
The agent then dials `wss://…/agent/sessions/s_3a7c…`. If the agent fails to start the session (for example, the path doesn't exist), it still dials and then immediately closes the WebSocket with the error as the reason. That way the browser gets a clear message.

**`fetch`**: asks the agent to download a file from the server.
```json
{"type":"fetch","data":{"transfer_id":"t_9f…","file_id":"f_12…","dest_path":"/root/cplane-manager","mode":"0755",
                        "sha256":"9f86d081…","size":15728640}}
```
The agent writes to `dest_path + ".part"` first, checks the sha256, then renames. That way a half-written file never appears at the target path.

In detail, the agent: (1) requires `dest_path` to be absolute and `mode` to be at most `0777` (no setuid, setgid or sticky bits); (2) downloads `GET /agent/files/{file_id}` with its own credentials; (3) writes the bytes to a temporary `.part` file in the same folder, named `<name>.<random>.part` and created `0600`, hashing them as they arrive (the random part means two transfers to the same path, or an attempt that was killed half way, never share a file); (4) compares the byte count with `size` and the hash with `sha256`; (5) flushes the file to disk, sets its `mode`, and renames it over `dest_path`. An existing file at `dest_path` is replaced, and because the replacement is a rename, a program that is running right now can be replaced too. If anything fails, the `.part` file is removed and `dest_path` is exactly as it was. The folder of `dest_path` must already exist.

**`fetch` with `"extract": true`** (a whole folder, D-43): the file is a zip archive and `dest_path` is an existing **folder**. Steps 2 to 4 are the same, with the temporary file inside `dest_path`; then, instead of renaming it, the agent unpacks the archive and removes it. The archive comes from outside the agent, so it is treated as hostile: every entry path is checked **before anything is written**, and one that is absolute, contains `..` or a backslash makes the whole archive refused (`"the archive is not safe to unpack…"`); links and special files are refused the same way; at most 100,000 entries and 16 GB, and the size is enforced while writing, not only trusted from the headers; modes are kept but only the nine rwx bits (nothing arrives setuid). Everything is unpacked into a staging folder inside `dest_path` first, and the top-level items are moved in only if **none of them exists yet** (`"\"project\" already exists in the destination folder"`): a refused or failed unpack leaves `dest_path` exactly as it was. Files that are in `dest_path` already and are not in the archive are never touched. An agent older than 0.6.0 does not know `extract`: it fails the transfer (it cannot rename a file over a folder).

Then it answers with `fetch_result`, whose `error` is short plain text (`"permission denied"`, `"the file did not arrive intact (checksum mismatch)"`, …). A transfer to a host that is offline is not queued: it fails at once with `"host offline"` (D-03).

**`vnc_set`**: start or stop the host's VNC server (D-51).
```json
{"type":"vnc_set","data":{"enabled":false}}
```
`enabled: false` stops the VNC service **and** sets it to start only by hand, so it does not come back at the next boot and use the resources again. `enabled: true` sets it to start at boot and starts it now. There is no reply: success or failure shows in the next heartbeat's `vnc`, and a failure is logged on the host. An agent without a controllable VNC server (`vnc_control` false) ignores the message with a warning in its log; an agent older than 0.8.0 logs "unknown message".

## 4. Sessions

`POST /api/hosts/{id}/sessions` with `{"kind": "...", "params": {...}}` → `201 {"id":"s_…"}`. `"shell"` (M3), `"files"` (M4) and `"tunnel"` (M5) exist so far. A `files` reply also has `"readonly"`, see below. A `tunnel` reply whose target port is `5900` also has `"vnc_password"` when the host reported one (D-64): the dashboard's viewer logs in with it by itself. The reply is `Cache-Control: no-store`, and the password is never written to the audit log.

| Status | When |
|---|---|
| 201 | The session was created and the agent was told to open it (`open` on its control connection). |
| 400 | Unsupported `kind`, or a body that is not valid JSON. |
| 403 | The user can see the host but their role does not allow this kind of session: a `viewer` gets no shell (`shell` needs `operator`). Every role in a group may open a `files` session, so for `files` this only happens to someone with no role at all. |
| 404 | The host does not exist, or the user may not see it (same answer for both). |
| 409 | The host is offline: `{"error":"host offline"}`. |

Opening a session is audited (`session_open`), and so is its end (`session_close`, with the duration). Both are used for every kind of session, and the detail starts with `kind=<kind>`; the Audit page reads that to say "Terminal", "Files" or "Tunnel" opened or closed.

**Connecting the two sides.** After `POST`, two WebSockets must arrive:
- the browser's: `GET /ui/sessions/{id}`. It needs the login cookie **and** an `Origin` header equal to the server's. The id only works for the user who created the session (anyone else gets `404`, admins included), and only once.
- the agent's: `GET /agent/sessions/{id}`, with the agent's `Authorization: Bearer` header. Only the agent the session was opened on may attach (`404` for another agent), and only once.

The server pairs them and copies every message from one to the other without reading it: text stays text, binary stays binary. It sends a ping on both sides every 30 seconds, so a peer that vanished without saying goodbye (a sleeping laptop) is noticed and the session ends.

If the other side has not arrived 15 seconds after the session was created, the session is cancelled: whoever is waiting is closed with code `4000`. When one side closes, the other side is closed with the same code and reason. That is how "the shell exited" or "cannot start a shell: …" from the agent reaches the browser.

### `shell`
- params: `{"cols": 120, "rows": 32}`
- The agent runs the first available of `$SHELL`, `/bin/bash`, `/bin/sh` inside a PTY. On Windows: `powershell.exe` (later).
- **Binary frames**, both ways = raw terminal bytes.
- **Text frames**, browser → agent = control: `{"type":"resize","cols":140,"rows":40}`.

### `files`
- The browser asks with `{"kind":"files","params":{}}` and gets `201 {"id":"s_…","readonly":true|false}`. Any `params` it sends are ignored.
- The **server** decides what the session may do and tells the agent in `open`: `{"readonly":true}` when the user has the `files_read` permission but not `files_write` (a viewer). In a read-only session the agent refuses `mkdir`, `rename`, `copy`, `chmod`, `write` and `delete` with `"this session is read-only"`. This is how a role is enforced even though the relay never reads the requests (D-27, D-31). A user without `files_read` gets `403`.
- Text-frame request/response. `id` matches each response to its request. Binary frames are ignored.
- The relay never reads these requests, so the **agent** reports every `mkdir`, `rename`, `copy`, `chmod`, `write` and `delete` it ran to the server as a `file_change` message on the control connection (§3, D-37), and the server audits it under the session's user.

```json
→ {"id":1,"op":"list","path":"/root"}
← {"id":1,"ok":true,"entries":[{"name":"a.txt","is_dir":false,"is_link":false,"size":120,"mode":"-rw-r--r--","mod_time":"2026-11-19T10:00:00Z"}]}

→ {"id":2,"op":"mkdir","path":"/root/new"}
→ {"id":3,"op":"rename","from":"/root/a.txt","to":"/root/b.txt"}
→ {"id":4,"op":"delete","path":"/root/new","recursive":true}
→ {"id":5,"op":"stat","path":"/root/b.txt"}
← {"id":5,"ok":false,"error":"no such file or directory"}

→ {"id":6,"op":"copy","from":"/root/app","to":"/tmp/app (1)"}
→ {"id":7,"op":"chmod","path":"/root/run.sh","mode":"0755"}
→ {"id":9,"op":"read","path":"/root/app.conf"}
← {"id":9,"ok":true,"text":"port = 8080\n","version":"1763600000000000000-12"}
→ {"id":10,"op":"write","path":"/root/app.conf","content":"port = 9090\n","version":"1763600000000000000-12"}
← {"id":10,"ok":true,"version":"1763600100000000000-12"}
→ {"id":8,"op":"props","path":"/root/app"}
← {"id":8,"ok":true,"props":{"name":"app","is_dir":true,"is_link":false,"mode":"drwxr-xr-x","perm":"0755",
     "owner":"root","group":"root","mod_time":"2026-11-19T10:00:00Z","size":8123456,"files":120,"dirs":9}}
```
Paths are always absolute. The agent rejects relative paths (`"path must be absolute"`).

Details of each operation:
- `list`: entries are sorted by name. A directory with more than 2000 entries is cut off and the response has `"truncated":true`. For a symbolic link, `is_link` is true and `is_dir` says whether the link **points at** a directory, so `/bin` opens like one. A broken link is a plain entry.
- `stat`: `{"id":5,"ok":true,"entry":{…}}`. It describes a link itself, not its target.
- `home`: `{"id":6,"op":"home"}` → `{"id":6,"ok":true,"path":"/root"}`, the folder the file manager starts in.
- `mkdir` makes **one** level: a missing parent is an error. It fails if the path exists, with `"a folder with that name already exists"` or `"a file with that name already exists"`, whichever is in the way.
- `rename` fails with `"destination already exists"` instead of replacing a file.
- `delete`: a directory that is not empty needs `"recursive":true`. Deleting the root directory is always refused. A path that is not there is an error.
- `copy` copies a file, or a folder with everything in it, to a path that does **not** exist: like `rename`, it fails with `"destination already exists"` instead of replacing anything (the browser picks a free name such as `report (1).pdf` first). Modes are kept. Symbolic links inside a folder are copied as links, never followed. A folder cannot be copied into itself (`"cannot copy a folder into itself"`), devices, sockets and pipes are refused, and a folder with more than 100,000 entries is refused before anything is written, so copying `/` by mistake does nothing. If a copy fails half way, what it created is removed.
- `chmod` sets the permission bits of **one** file or folder, not what is inside it. `mode` is octal, three or four digits (`755`, `0755`), and at most `0777`: setuid, setgid and sticky bits are refused with `"mode must be at most 0777"`, like in a file send. An empty `mode` is refused too.
- `read` is a read (a read-only session may use it, to *view*): it returns `text` and a `version`, for the editor. Only a regular file of at most 512 KB (`MaxEditBytes`) that is valid UTF-8 and holds no NUL byte: anything else is refused (`"this is not a text file (it has binary data in it)"`, `"the file is too big to edit here (the limit is 512 KB): download it instead"`, `"is a directory"`). `version` changes whenever the file is written (it is made from the modification time and the size).
- `write` replaces the text of an existing file and needs the `version` that `read` returned. If the file is not at that version any more, it is refused with `"the file changed on the host since it was opened: close this window and open it again"` and nothing is written, so a change made on the host in the meantime is never lost. A missing `version` is refused, a path that does not exist is an error (it never creates a file), and `content` is at most 512 KB. The bytes are written into the file itself, not into a new file renamed over it, so the owner, the mode and links stay as they are; the price is that a crash in the middle leaves a half-written file. The answer has the new `version`, so a second save can follow.
- `props` is a read, so a read-only session may use it: `{"id":8,"ok":true,"props":{…}}` with `name`, `is_dir`, `is_link` (and `link_target`), `mode` (like `-rw-r--r--`), `perm` (octal, like `0644`), `owner` and `group` (names, or numbers when a name cannot be found; left out on a host where there is no owner), `mod_time`, and `size`. For a link it describes the link itself. For a folder, `size` is the total of the files inside, with `files` and `dirs` counted at any depth, links not followed, and folders it cannot read skipped. It stops after 100,000 entries and then has `"truncated":true`: the numbers are what was counted, so the real ones are higher.
- Errors are the operating system's own short wording ("no such file or directory", "permission denied"), or one of the messages above. An unknown `op` is `"unknown operation"`, and a frame that is not valid JSON gets `{"ok":false,"error":"invalid request"}`. A failed request never ends the session.

### `download`
- params: `{"path": "/var/log/syslog"}`
- Used only internally by `GET /api/hosts/{id}/download`: the **server** plays the browser's part and no browser ever attaches (its id is never sent to one). The agent's first text frame is `{"name":"syslog","size":12345}`. Binary frames with the file contents follow, in chunks of 64 KB, until the agent closes the connection normally (1000).
- **A folder** is sent as a **zip archive**, built while it is sent (never on the host's disk, and the agent holds one 64 KB chunk of it). The header is `{"name":"project.zip","size":-1}`: a size of `-1` means it is not known until the end, so the server sends the browser no `Content-Length` and the response is chunked. Everything is inside one folder named like the original (so unpacking never spills files), links and special files are not followed or stored, and anything that cannot be read (no permission) is left out and named in a `SKIPPED.txt` at the top of the archive. A folder with more than 100,000 entries fails. A failure after the first byte is a close with an error code, never a normal one, so the browser sees a failed download and not a short archive that looks complete.
- If the file cannot be read there is no header frame: the agent closes with code `1011` and the reason in its own words (`"no such file or directory"`, `"permission denied"`, `"is a directory"`, `"not a regular file"`, `"path must be absolute"`). Only regular files are sent, so reading `/dev/zero` cannot run forever.

**The HTTP side:** `GET /api/hosts/{id}/download?path=/var/log/syslog`. It needs the `files_read` permission, which every role in a group has, so a viewer may download.

| Status | When |
|---|---|
| 200 | The file is on its way. `Content-Length` is the file's size, `Content-Disposition: attachment; filename=…` makes the browser save it, `Content-Type` is always `application/octet-stream` with `nosniff`, so a file is never rendered as a page. The name is cleaned first: no folders, quotes or control characters. |
| 400 | No `path`, a `path` over 4096 bytes or with a NUL byte, or the agent refused it (`is a directory`, `not a regular file`, `path must be absolute`). The message is the agent's reason. |
| 403 | The user may not read files on this host, or the agent said `permission denied`. |
| 404 | The host does not exist or the user may not see it, or the agent said `no such file or directory`. |
| 409 | The host is offline. |
| 502 | The host closed the connection without a reason, or sent something unexpected. |
| 504 | The host did not answer within 15 seconds. |

The file is streamed: the server holds one chunk at a time and nothing is saved to disk, so memory use does not grow with the file. If the agent fails **after** the response has started, the server cuts the connection, so the browser reports a failed download instead of keeping a short file that looks complete. A browser that stops reading for a minute is dropped. The start of a transfer is audited as `file_download` with the path and size.

### `tunnel`
- The client asks with `{"kind":"tunnel","params":{"target":"127.0.0.1:22"}}` and gets `201 {"id":"s_…"}`. It needs the same role as a shell (`operator`): a tunnel reaches whatever the host can reach on its own loopback, which is as much as a shell does. A viewer gets `403`, and the host must be online (`409`).
- **The target must be on the host itself** (D-35): `localhost`, or a loopback address (`127.0.0.0/8`, `::1`), with a port from 1 to 65535. Anything else (another machine on the host's network, any other name, `0.0.0.0`, a link-local or metadata address) is `400` with the reason. The server checks it, and the agent checks it again before it dials.
- **A browser may tunnel to port 5900 only** (D-49). A request made with a **cookie** login (the dashboard) whose target port is not `5900` is `403` with `"the browser may only open a tunnel to a VNC server (port 5900): use hnctl forward for other ports"`; the host part is still limited to loopback as above. A request made with a **bearer token** (`hnctl`, D-36) may use any port. The dashboard's **Screen** page opens `{"kind":"tunnel","params":{"target":"127.0.0.1:5900"}}`, attaches its WebSocket to the session, and gives that WebSocket to noVNC, which speaks the VNC protocol (RFB) over it: the bytes are the same as for `hnctl forward 5900:…`.
- The agent connects to the target with a 10-second timeout and copies raw bytes both ways in binary frames, until either side closes. If it cannot connect it closes with code `1011` and the reason in the operating system's words (`"connect: connection refused"`), which `hnctl` prints.
- One TCP connection on the laptop = one `tunnel` session, so an `ssh` and an `scp` at the same time are two sessions.
- What flows is whatever the program on the laptop speaks. For `ssh` it is the SSH protocol, encrypted end to end between `ssh` and `sshd`: the tunnel, the server and the agent only move bytes. The audit log records `session_open` with `kind=tunnel target=127.0.0.1:22` and `session_close` with the duration.

`hnctl forward 2222:web-01:127.0.0.1:22` opens a listening port on the laptop (on `127.0.0.1` by default) and, for each connection, does exactly this: `POST /api/hosts/{id}/sessions` with its bearer token, then connects `/ui/sessions/{id}` and copies bytes between the local connection and the WebSocket.

## 5. WebSocket close codes

| Code | Meaning |
|---|---|
| 1000 | Normal (session finished, EOF) |
| 1008 | Rejected: auth / permission / protocol mismatch |
| 1011 | Error in agent or server; `reason` holds a short message |
| 4000 | The other side of the session did not arrive within 15 seconds |
