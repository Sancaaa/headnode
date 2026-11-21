# headnode

Self-hosted remote management. You install an **agent** on each host (Linux or Windows) and manage all of them from one **web dashboard**. Features: web terminal, file manager, sending files to many hosts at once, TCP tunnels, and the host's **screen in the browser** (VNC). All of it works even when hosts sit behind NAT.

This project has two goals:
1. Be a tool that actually gets used to manage my own machines.
2. **Be a way to learn** the technology behind it (reverse connections, WebSocket relays, PTYs, auth). The code is kept deliberately simple, and every design decision is written down.

> Status: **version 1**. Milestones M0 to M9 are done (see the [roadmap](docs/roadmap.md)). Tried on a Linux server behind Caddy, a Windows 11 laptop and a Raspberry Pi 5.

## How it works, in one picture

```
 Browser ──HTTPS/WS──►  headnode (VPS)  ◄──WSS (agent dials out)──  headnode-agent (each host)
```

The agent opens the connection to the server, not the other way around. That is why hosts behind NAT or a router stay reachable without any port forwarding on the router. The full explanation is in [docs/architecture.md](docs/architecture.md).

## What you can do

| From the dashboard | How |
|---|---|
| See every host: online, CPU, memory, disk | Hosts page, filled by the agent's heartbeat |
| Open a terminal on a Linux host | Terminal tab (full screen available) |
| Browse, edit, upload and download files; send files or folders to many hosts | Files tab, Transfers |
| See and control a host's screen, with zoom and pan for several monitors | Screen tab: noVNC through the agent's tunnel; the host's VNC server is switched on and off from the page |
| Forward a port from your laptop to a host (`ssh`, RDP, ...) | `hnctl forward` |
| Add a host with one command | Hosts, Add host: a `curl ... \| sh` line for Linux, a PowerShell line for Windows |
| Know which agents are out of date, and update them | An **Update available** tag on the Hosts page, with how to update that host: `sudo headnode update` on Linux, the tray icon's **Update headnode…** on Windows (the agent updates itself; you choose when) |
| Audit who did what | Audit page |

On a Windows host the person at the machine also gets a **tray icon** (see [docs/windows.md](docs/windows.md)): blue when connected, with **Exit** to turn headnode off completely, and a **headnode** app in the Start menu to turn it back on.

## Documentation map

| Document | Contents | Read it when… |
|---|---|---|
| [docs/architecture.md](docs/architecture.md) | Components, flows, data model, security | you want to understand the system |
| [docs/protocol.md](docs/protocol.md) | Message contract between server, agent and browser | you write server or agent code |
| [docs/decisions.md](docs/decisions.md) | Design decision log (why X, not Y) | you wonder "why is it like this?" |
| [docs/roadmap.md](docs/roadmap.md) | Milestones M0 to M9 with done criteria | you want to know what was built, and what may come |
| [docs/principles.md](docs/principles.md) | Ground rules: YAGNI, code style, git | before writing code |
| [docs/design.md](docs/design.md) | UI guide (minimal dashboard style) | you build a web page |
| [docs/deploy.md](docs/deploy.md) | Run the server on a VPS behind Caddy, and add hosts with one command | you put it on the internet |
| [docs/windows.md](docs/windows.md) | Add a Windows host, its screen, and the tray icon | you manage Windows machines |
| [docs/linux-screen.md](docs/linux-screen.md) | The screen of a Linux desktop (Raspberry Pi and others) | you want a Linux host's screen in the dashboard |
| [docs/logo/README.md](docs/logo/README.md) | The logo, its files, colors and rules | you use or change the logo |

## Components

| Binary | Runs on | Job |
|---|---|---|
| `headnode` | VPS (public) | Dashboard, API, login, session relay, SQLite |
| `headnode-agent` | Every managed host (Linux, Windows) | Reports status; opens shell, file and tunnel sessions on the server's request; starts and stops the host's VNC server; on Windows, the tray icon |
| `hnctl` | Your laptop | Local port forwarding, e.g. `ssh -p 2222 localhost` |

## Running it

Everything is built with the Go standard toolchain. The server on a VPS is installed from a git clone with one script, and hosts are added with the one command the dashboard shows. The steps are in [docs/deploy.md](docs/deploy.md). For development on your own machine:

```
go vet ./...
go test ./...
go run ./cmd/headnode          # the server, on 127.0.0.1:8080
```

The Screen page needs the noVNC library: `start.sh` downloads it on a server, and [web/vendor/README.md](web/vendor/README.md) has the command for a local copy. The agents served to new hosts are built with `scripts/build-agents.ps1` (or `.sh`).

## Development requirements

- Go 1.22 or newer (the project's `go.mod` pins the exact version).
- A browser; Linux and Windows machines to try the agents on.
- Dependencies are few, and the standard library comes first ([docs/decisions.md](docs/decisions.md) says why each was added): `modernc.org/sqlite`, `github.com/coder/websocket`, `creack/pty`, `golang.org/x/*`, and `fyne.io/systray` (Windows tray icon only).
