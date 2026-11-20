# Roadmap

Each milestone is a **vertical slice**: it ends with a demo that runs end to end. Work through them in order, and don't start the next milestone until every done criterion of the current one is checked.

Feature codes (`A1`, `B2`, …) refer to the MeshCentral feature list chosen at the start of the project.

**Active milestone: M6**

---

## M0 — Foundation
- [x] Own git repo, `.gitignore`
- [x] Docs: README, architecture, protocol, decisions, roadmap, principles, design
- [x] Install Go 1.22+ on the laptop
- [x] Prepare one Linux machine to test the agent on (VM / LXC / WSL)
- [x] `go mod init headnode`, empty folder layout, a server `main.go` that answers "ok"

**Done when:** `go run ./cmd/headnode` runs and `http://localhost:8080` responds.

## M1 — Agent connects & host list (A1, A4, A5, C7, D1)
- [x] `headnode token create`: create an enroll token from the CLI
- [x] `POST /agent/enroll`: the agent gets an id + secret and saves them to `agent.json`
- [x] WS `/agent/connect`: `hello` + `heartbeat` (info read from `/proc`)
- [x] Reconnect with backoff
- [x] In-memory registry of online agents, `last_seen_at` in SQLite
- [x] **Hosts** page: host table, online/offline status, CPU/RAM/disk
- [x] **Host detail** page: full info

**Done when:** you start the agent on a VM and the host shows up *online* within 2 seconds. Stop the agent, and the host turns *offline* within 60 seconds. Restart the server, and the agent reconnects on its own.

⚠ M1 has no login yet, so the server listens **only** on `127.0.0.1` (the default). Don't deploy it to a VPS yet.

## M2 — Login, groups, permissions, audit (C1, C2, C5, A7)
- [x] First user created from the CLI: `headnode user create --admin`
- [x] Login/logout, cookie session, auth + Origin middleware
- [x] Groups, members + roles, the `can()` function
- [x] Enroll tokens from the dashboard (admin)
- [x] Rename / move group / delete host
- [x] Audit log + **Audit** page
- [x] Simple login rate limit

**Done when:** without login, every `/api/*` returns 401. A viewer cannot see hosts outside their groups. Every action above appears in the audit log.

## M3 — Web terminal (B1)
- [x] `POST /api/hosts/{id}/sessions` + the `open` message
- [x] Relay: pair browser WS ⇄ agent WS, 15-second timeout
- [x] Agent: shell in a PTY, resize
- [x] Terminal page with xterm.js (vendored)
- [x] Hosts row: Shell action
- [x] WAN address (seen by server)

**Done when:** `htop` and `vim` on the VM work normally from the browser, resizing the window resizes the terminal, and closing the tab kills the shell process on the host.

## M4 — File manager & bulk file send (B2, B3)
- [x] `files` session: list, mkdir, rename, delete, stat
- [x] Download from a host (`download` session → HTTP response)
- [x] Upload to the server + `POST /api/transfers` + `fetch` on the agent (`.part` → check sha256 → rename)
- [x] File manager page + "Send to multiple hosts" dialog
- [x] File change audit (D-37): the agent reports each mkdir/rename/delete, the server audits it under the session's user

**Done when:** the K3s Manager binary (~15 MB) is sent to 3 hosts at once with mode `0755`, with status visible per host, and a 1 GB file downloads without the server's RAM spiking.

## M5 — TCP tunnel / port forward (B6)
- [x] `tunnel` session in the agent
- [x] `hnctl login`, `hnctl forward 2222:<host>:127.0.0.1:22`
- [x] Host list via `hnctl hosts`

**Done when:** `ssh -p 2222 root@localhost` from the laptop logs into a VM behind NAT.

## M5.5 — Pre-production polish
- [x] An admin can delete an uploaded file (Uploads page, confirm dialog, audited). The blob leaves the disk only when no other upload shares it, and a file that is being sent cannot be deleted (D-38)
- [x] The data directory is created `0700` (it holds password hashes, agent secret hashes and uploads)
- [x] Hosts page: "N hosts · M online" in the count pill, computed in the browser
- [x] Sidebar: a muted line with the server version and uptime, from `/api/me`
- [x] File manager quality of life (D-39): upload keeps both files, Move, Copy, Copy path, sort, filter, hidden files, multi-select, rename in the row, Properties (with a folder's size), chmod
- [x] File manager, third round (D-41): download a folder as a zip, view/edit a text file, send a file from a host to other hosts, drag and drop to upload and to move
- [x] Several files per upload and per send (D-42)
- [ ] Send several items and whole folders from a host to other hosts (D-43): zip, `extract`, safe unpack on the agent

**Done when:** deleting an upload from the dashboard removes its row and (when nothing else uses it) its file in `data/files/`, a brand-new data directory has mode `0700`, the Hosts page shows the online count, and the sidebar shows the server version and uptime.

## M6 — Easy install & deploy (A2, C8)
- [x] Cross-OS/arch builds (`GOOS`/`GOARCH`), agent binaries served by the server (D-40)
- [ ] `install.sh`: download the agent, enroll, install a systemd service (D-46)
- [ ] `headnode-agent service install|uninstall` (D-46)
- [ ] VPS deploy guide: headnode as a service + Caddy, from a git clone with `start.sh` (`docs/deploy.md`, `deploy/`, D-45). Its checklist must include: **an existing data directory must be `chmod 700`** (the server creates new ones `0700`, but `os.MkdirAll` never changes a directory that already exists, and this one holds password hashes, agent secret hashes and uploaded files)

**Done when:** a new host can be managed from the public dashboard with **one** command.

---

Remote Windows is planned as two milestones (D-44). The idea: the agent never touches the screen. A normal VNC server on the host does that, listening on loopback only, and the agent reaches it with the `tunnel` session that already exists (D-35 stays as it is).

## M7 — Windows agent, minimal (D-12, D-44)
- [ ] Windows system info (`sysinfo_windows.go`): hostname, OS version, CPU, RAM, disks, uptime
- [ ] The agent runs as a Windows service: `headnode-agent service install|uninstall` on Windows
- [ ] `windows/amd64` added to the build script and to the served agent list (D-40)
- [ ] `install.ps1`: download the agent, enroll, install the service (same flow as `install.sh`)
- [ ] Hosts page: Shell and Files are hidden for Windows hosts until they work there
- [ ] Guide: TightVNC (or UltraVNC) on Windows, as a service, **loopback connections only**, with a password; optional: OpenSSH Server and RDP, reached with `hnctl forward`

**Done when:** a Windows host is added with one command and shows *online* with CPU/RAM/disk. `hnctl forward 5900:<host>:127.0.0.1:5900` plus a VNC viewer on the laptop controls its screen, including the login screen. On a Windows Pro host, `hnctl forward 3389:<host>:127.0.0.1:3389` plus `mstsc` opens RDP.

## M8 — VNC in the browser (B4)
- [ ] noVNC vendored (like xterm.js), with a decisions entry
- [ ] The browser may open a `tunnel` session to `127.0.0.1:5900` only: same role as a shell in `can()`, audited, Origin-checked
- [ ] Viewer page: screen scaled to the window, Ctrl+Alt+Del button, disconnect; a clear message when nothing listens on 5900
- [ ] **VNC** button on host rows

**Done when:** from the public dashboard, a Windows host's screen opens in a browser tab, mouse and keyboard work, Ctrl+Alt+Del reaches the login screen, and closing the tab ends the session on the host. A Linux host with a VNC server works the same way.

---

## After the MVP (not scheduled)
To be chosen later by the project owner: shell and file manager on Windows, RDP in the browser, agent auto-update (A3), 2FA (C3), session recording (C6), Telegram notifications (D2), HTTP web relay (B7), bulk commands (B8), CCTV (B15).
