# headnode

Self-hosted remote management. You install an **agent** on each host (Linux, later Windows) and manage all of them from one **web dashboard**. Features: web terminal, file manager, sending files to many hosts at once, and TCP tunnels. All of it works even when hosts sit behind NAT.

This project has two goals:
1. Be a tool that actually gets used to manage my own machines.
2. **Be a way to learn** the technology behind it (reverse connections, WebSocket relays, PTYs, auth). The code is kept deliberately simple, and every design decision is written down.

> Status: **design phase**. No code yet. See the [roadmap](docs/roadmap.md).

## How it works, in one picture

```
 Browser ──HTTPS/WS──►  headnode (VPS)  ◄──WSS (agent dials out)──  headnode-agent (each host)
```

The agent opens the connection to the server, not the other way around. That is why hosts behind NAT or a router stay reachable without any port forwarding on the router. The full explanation is in [docs/architecture.md](docs/architecture.md).

## Documentation map

| Document | Contents | Read it when… |
|---|---|---|
| [docs/architecture.md](docs/architecture.md) | Components, flows, data model, security | you want to understand the system |
| [docs/protocol.md](docs/protocol.md) | Message contract between server, agent and browser | you write server or agent code |
| [docs/decisions.md](docs/decisions.md) | Design decision log (why X, not Y) | you wonder "why is it like this?" |
| [docs/roadmap.md](docs/roadmap.md) | Milestones M0–M6 with done criteria | you want to know what comes next |
| [docs/principles.md](docs/principles.md) | Ground rules: YAGNI, code style, git | before writing code |
| [docs/design.md](docs/design.md) | UI guide (minimal dashboard style) | you build a web page |
| [docs/deploy.md](docs/deploy.md) | Run the server on a VPS behind Caddy, and add hosts with one command | you put it on the internet |

## Components

| Binary | Runs on | Job |
|---|---|---|
| `headnode` | VPS (public) | Dashboard, API, login, session relay, SQLite |
| `headnode-agent` | Every managed host | Reports status; opens shell, file and tunnel sessions on the server's request |
| `hnctl` | Your laptop (from M5) | Local port forwarding, e.g. `ssh -p 2222 localhost` |

## Development requirements

- Go 1.22+ (for the newer `net/http` routing patterns). **Not installed on this machine yet.**
- One Linux machine to test the agent on (VM, Proxmox LXC, or WSL).
- A modern browser.

Build and run commands will be added here starting with M1.
