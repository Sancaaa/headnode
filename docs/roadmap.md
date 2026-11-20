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

## M6 — Easy install & deploy (A2, C8)
- [ ] Cross-OS/arch builds (`GOOS`/`GOARCH`), agent binaries served by the server
- [ ] `install.sh`: download the agent, enroll, install a systemd service
- [ ] `headnode-agent service install|uninstall`
- [ ] VPS deploy guide: headnode as a service + Caddy

**Done when:** a new host can be managed from the public dashboard with **one** command.

---

## After the MVP (not scheduled)
To be chosen later by the project owner: Windows agent, agent auto-update (A3), 2FA (C3), session recording (C6), Telegram notifications (D2), HTTP web relay (B7), bulk commands (B8), remote desktop (B4/B5), CCTV (B15).
