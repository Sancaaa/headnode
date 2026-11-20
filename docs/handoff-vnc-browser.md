# Handoff: finish the browser screen (VNC) for Windows hosts

Paste everything below the line into a new Claude session opened in this project.

---

You are continuing work on **headnode** (Go; server + `headnode-agent` for Linux and Windows + `hnctl`), a learning project. Read `CLAUDE.md`, `docs/principles.md`, `docs/architecture.md`, `docs/protocol.md` and `docs/decisions.md` (D-35, D-44, D-49 to D-53) first, and follow `CLAUDE.md` (stdlib first, no new layers, protocol changes update `docs/protocol.md`, decisions go in `docs/decisions.md`, English only). **The owner runs every terminal command, test and git operation themselves; only write code and docs, and end with PowerShell commands for them to run.**

## Goal (milestone M8, `docs/roadmap.md`)
From the public dashboard (https://headnode.sanca.site), a Windows host's screen opens in a browser tab: mouse and keyboard work, Ctrl+Alt+Del works, closing the tab ends the session on the host. Installing a host must stay **one command** (`install.ps1`), with nothing for the client to set up by hand, and the screen must be **off by default** (Start VNC / Stop VNC in the dashboard, like Tailscale).

## How it works (all written, all committed to the working tree)
- Browser: `web/app.js` (Screen page, `mountScreen`) opens a `tunnel` session to `127.0.0.1:5900` on the host and gives its WebSocket to **noVNC 1.5.0** (`web/vendor/novnc`, downloaded by `start.sh` on the VPS, git-ignored, D-52).
- Server: relays the session opaquely; a browser (cookie login) may tunnel to port 5900 only (`server/sessions.go`). `POST /api/hosts/{id}/vnc` sends the agent `vnc_set` (`server/vnc.go`).
- Agent (`agent/vnc*.go`, `agent/conn.go`): reports `vnc` (something listens on 5900) and `vnc_control` (a `tvnserver` service exists) in each heartbeat; starts and stops the `tvnserver` Windows service on `vnc_set`.
- Installer (`server/install.ps1`, served at `/install.ps1`, embedded in the server binary): installs the agent as a service, then installs TightVNC from tightvnc.com as a loopback-only service and switches it off.

## The open problem
The screen never appears. The browser console shows, from noVNC's `rfb.js`:

    Failed when connecting: No supported sub-auth types!
    _negotiateTightAuth <- _negotiateAuthentication <- _initMsg <- _handleMessage

The tunnel itself works (agent log: `tunnel opened target=127.0.0.1:5900`; TightVNC log: client connected, then closed "gracefully" by the viewer side about 0.17 s later). The same VNC server works with a normal viewer through `hnctl forward`.

Evidence gathered:
- The Windows laptop used for testing has **TightVNC 4.0.2** (`(Get-Item "C:\Program Files\TightVNC\tvnserver.exe").VersionInfo.ProductVersion` prints `4.0.2.0`). TightVNC 4.0 (October 2026) added SRP-6a login; its pages say traditional VNC login "remains available for compatibility" but not how to enable it.
- noVNC 1.5.0 (`core/rfb.js`) walks the server's security-type list in the server's order and takes the first type it supports, then for Tight (16) accepts only sub-auth `STDVNOAUTH__`, `STDVVNCAUTH_`, `TGHTULGNAUTH`. TightVNC 2.x's server source (`RfbInitializer.cpp`) offers `[primSecType, 16]` for RFB 3.7/3.8, so noVNC would pick the plain type. The 4.0.2 server evidently does not.
- Telling noVNC the server speaks RFB 3.3 (rewriting the banner) made TightVNC 4.0.2 answer `No compatible connection security method is available`. Removing type 16 from the list changed nothing. Both workarounds were removed again (see D-53).
- Chosen direction: the installer pins **TightVNC 2.8.90** (`$TightVncUrl` in `install.ps1`) and, with `-ReplaceVnc`, uninstalls an installed TightVNC 4 first. TightVNC 2.8.90's handshake has NOT been verified against noVNC; the 2.x source read was an older 2.x mirror.

## What is failing right now
Running `& ([scriptblock]::Create((irm https://headnode.sanca.site/install.ps1))) -ReplaceVnc` on the laptop prints
`WARNING: The screen was not set up: Specified cast is not valid.` and nothing is uninstalled or installed. I could not find a cast in the current `Install-Vnc` that can fail (the version is read from `tvnserver.exe` inside a try/catch). The most likely explanation is that the VPS still serves an **older** `install.ps1` (the server embeds it at build time; redeploy is `git pull && sudo sh start.sh`). The script now prints `headnode installer, revision 7` first and prints the failing line on error, so check which revision ran. If it says revision 7, the `At:` warning shows the failing line.

## What to do
1. Have the owner confirm the revision printed and the `At:` line, and fix the real cause of the cast error.
2. Get TightVNC 2.8.90 installed by the script on a machine (uninstalling 4.0.2 first), then verify in the browser that the Screen page connects (use `localStorage.setItem('headnode-vnc-debug','1')` and reload to print noVNC's "Server security types" and "Server Tight authentication types" lines if it still fails).
3. If TightVNC 2.8.90 also fails with noVNC, read the debug lines, and decide between: a TightVNC setting that makes it offer plain VNC login, another VNC server (UltraVNC is the usual noVNC partner on Windows; check its silent-install options and licence), or a different approach. Record the decision in `docs/decisions.md`, update `docs/windows.md`, `docs/protocol.md` (install.ps1 section) and `docs/roadmap.md`, and keep the installer one command with the screen off by default.
4. Do not add workarounds that rewrite the VNC login handshake in the browser: they were tried and removed.
5. When it works, tick the M8 boxes in `docs/roadmap.md`.

Useful files: `server/install.ps1`, `server/install.go`, `server/install_test.go`, `web/app.js` (search `mountScreen`), `agent/vnc.go`, `agent/vnc_windows.go`, `agent/conn.go`, `server/vnc.go`, `start.sh`, `docs/windows.md`.
