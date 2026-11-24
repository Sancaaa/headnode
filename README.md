# headnode

Self-hosted remote host management in Go. Install `headnode-agent` on Linux or Windows machines and manage them from one web dashboard: interactive terminal, file manager, multi-host file transfers, VNC desktop access, Docker inspection and control, and local TCP port forwarding—without port forwarding on managed hosts.

```
Browser / hnctl ──HTTPS/WSS──► Caddy ──HTTP/WS──► headnode (VPS + SQLite) ◄──WSS (outbound)── headnode-agent (Linux / Windows)
```

Agents dial out to the server over a persistent WebSocket control channel. Sessions (`shell`, `files`, `download`, `tunnel`, `docker`) open a paired, single-use WebSocket relayed through the server, so machines behind NAT or home routers stay reachable.

## Features

| Area | Capabilities |
|---|---|
| **Host inventory** | Live online status, CPU, memory, disk, uptime, LAN/WAN addresses, OS/kernel/hardware metadata, and agent version checks via 20 s heartbeats. |
| **Web terminal** | Browser PTY (`xterm.js`) with resize and fullscreen keyboard lock (Linux hosts). |
| **File manager & transfers** | Browse, filter, sort, upload (up to 4 GB, SHA-256 deduplicated), inline text edit (up to 512 KB, atomic writes), rename, copy, move, `chmod`, folder zip download, and multi-host fan-out transfers (Linux hosts). |
| **Remote screen (VNC)** | Browser desktop control via `noVNC` over a loopback tunnel with zoom/pan controls and start/stop VNC toggles. Supports TightVNC on Windows, `x11vnc` on X11, and private Unix-socket `wayvnc` on Wayland. |
| **Docker** | Per-host containers, Compose stacks, images, volumes, networks, logs, inspect (env values redacted), and prune via `/var/run/docker.sock`, showing the corresponding `docker` CLI command before actions (Linux hosts). |
| **TCP port forwarding** | Forward local laptop ports to loopback ports on any managed host with `hnctl forward PORT:HOST:TARGET`. |
| **Enrollment & self-update** | One-line Linux (`curl | sh`) and Windows PowerShell installers. Outdated agents are flagged in the UI and update themselves (`sudo headnode update` or Windows tray menu) with SHA-256 verification. |
| **Windows tray app** | System tray icon showing connection state, VNC toggle, self-update trigger, and clean exit ([docs/windows.md](docs/windows.md)). |
| **Accounts, groups & audit** | Mandatory RFC 6238 TOTP 2FA for all users (browser and `hnctl`), group-scoped RBAC (`admin`, per-group `operator` and `viewer`), self-service profile/password/OTP/session management, and SQLite `audit_log`. |

## Components

| Binary | Target | Responsibility |
|---|---|---|
| `headnode` | VPS / control server | Embedded web UI, HTTP/WS API, session relay, auth/RBAC, SQLite persistence, agent binary hosting |
| `headnode-agent` | Managed hosts (Linux, Windows) | Outbound control WS, sysinfo heartbeats, PTY shell, file ops, Docker API, VNC lifecycle, loopback tunnels, Windows tray |
| `hnctl` | Operator workstation | CLI authentication, host listing, and TCP port forwarding |

## Security Model

- **Default-deny API & group RBAC**: Every `/api/*` endpoint (except login) requires a valid session; unauthorized host/file/transfer lookups return `404` to avoid resource enumeration. Revoking a user, role, host, or session terminates active WebSocket sessions immediately.
- **Session & token storage**: Passwords use `bcrypt` and every login requires a 6-digit RFC 6238 TOTP code with per-step replay protection (first login guides mandatory setup via Base32 secret / `otpauth://` URI). Session tokens, enrollment tokens, and agent secrets are 256-bit random values stored only as SHA-256 digests. Failed logins, failed OTP attempts, and failed enrollments are rate-limited with bounded memory tracking.
- **Origin & CSP**: State-changing requests and browser WebSocket upgrades verify `Origin` (host and scheme). Embedded frontend assets use strict DOM construction (`document.createElement`, no `innerHTML`) and a strict `Content-Security-Policy`.
- **Agent isolation**: File operations block reads, writes, copies, renames, deletes, and `chmod` against `agent.json` and its parent directory. Agent self-updates verify `SHA256SUMS` before executing or swapping binaries. `wayvnc` binds to a `0600` Unix socket inside the agent's `0700` config directory; `x11vnc` and TightVNC generate a fresh password per start.
- **No shell interpolation**: External commands (`docker compose`, `systemctl`, VNC binaries) always pass discrete argv slices—never `sh -c` with request data.
- **Docker note**: Adding the Linux agent's user to the `docker` group grants root-equivalent privileges on that host; enable it only per host where intended.

## Quick Start

### Local development

Requirements: Go 1.27+ (`go.mod` pins the exact version), `CGO_ENABLED=0` supported.

```sh
go vet ./...
go test ./...
go run ./cmd/headnode user create -username admin -admin
go run ./cmd/headnode                 # listens on 127.0.0.1:8080
```

Optional local setup:
- **Screen tab (noVNC)**: fetch vendored noVNC assets per [web/vendor/README.md](web/vendor/README.md) (`start.sh` handles this automatically on a server).
- **Agent binaries**: cross-compile Linux (`amd64`, `arm64`) and Windows (`amd64`) agents plus `dist/SHA256SUMS` with `./scripts/build-agents.sh` (or `scripts/build-agents.ps1`).

### Server flags & CLI

```sh
headnode [-addr 127.0.0.1:8080] [-db data/headnode.db] [-agents dist]
headnode user create -username <name> [-admin] [-db data/headnode.db]
headnode user totp-reset -username <name> [-db data/headnode.db]
headnode token create [-db data/headnode.db]

hnctl login <server-url>
hnctl hosts
hnctl forward 2222:<host>:127.0.0.1:22
```

### VPS deployment

Run behind Caddy on a Linux VPS using `start.sh` and the hardened systemd unit in `deploy/headnode.service`. Full instructions are in [docs/deploy.md](docs/deploy.md).

## Project Layout & Stack

```
cmd/
  headnode/         Control-plane server and CLI subcommands
  headnode-agent/   Host agent, service installers, Windows tray, self-updater
  hnctl/            Operator CLI client and TCP port forwarder
protocol/           Shared JSON wire messages and version helpers
server/             HTTP/WS handlers, relay, SQLite schema/migrations, RBAC, audit
agent/              OS collectors, Linux PTY, files/zip/fetch, Docker client, VNC, tunnels
web/                Embedded vanilla JS/CSS SPA (no build step) + vendored xterm.js
deploy/             Caddyfile and systemd unit
scripts/            Cross-compilation scripts for agent binaries and SHA256SUMS
docs/               Architecture, protocol, design guide, deploy, OS guides, decisions
```

**Tech stack**: Go standard library first, `modernc.org/sqlite` (pure-Go SQLite, WAL mode), `github.com/coder/websocket`, `github.com/creack/pty`, `golang.org/x/{crypto,sys,term}`, `fyne.io/systray` (Windows tray only), and zero-build vanilla JS/CSS embedded via `//go:embed`.

## Documentation

| Document | Scope |
|---|---|
| [docs/architecture.md](docs/architecture.md) | System flows, data model, security boundaries |
| [docs/protocol.md](docs/protocol.md) | Server/agent/browser WebSocket wire contract |
| [docs/deploy.md](docs/deploy.md) | VPS deployment behind Caddy and host enrollment |
| [docs/windows.md](docs/windows.md) | Windows agent, TightVNC, and system tray |
| [docs/linux-screen.md](docs/linux-screen.md) | X11 (`x11vnc`) and Wayland (`wayvnc`) screen sharing |
| [docs/decisions.md](docs/decisions.md) | Architecture and engineering decision log |
| [docs/design.md](docs/design.md) | UI tokens and layout specification |

## License

Licensed under the [GNU Affero General Public License v3.0](LICENSE).
