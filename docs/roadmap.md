# Roadmap

Each milestone is a **vertical slice**: it ends with a demo that runs end to end. Work through them in order, and don't start the next milestone until every done criterion of the current one is checked.

Feature codes (`A1`, `B2`, …) refer to the MeshCentral feature list chosen at the start of the project.

**Milestones M0 to M10 are done: version 1. M11 (accounts) is built and waits for its test below.** What may come next is in "After the MVP" at the end.

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
- [x] Send several items and whole folders from a host to other hosts (D-43): zip, `extract`, safe unpack on the agent

**Done when:** deleting an upload from the dashboard removes its row and (when nothing else uses it) its file in `data/files/`, a brand-new data directory has mode `0700`, the Hosts page shows the online count, and the sidebar shows the server version and uptime.

## M6 — Easy install & deploy (A2, C8)
- [x] Cross-OS/arch builds (`GOOS`/`GOARCH`), agent binaries served by the server (D-40)
- [x] `install.sh`: download the agent, enroll, install a systemd service (D-46); the agent runs as the installing user (D-48)
- [x] `headnode-agent service install|uninstall` (D-46)
- [x] VPS deploy guide: headnode as a service + Caddy, from a git clone with `start.sh` (`docs/deploy.md`, `deploy/`, D-45). Its checklist must include: **an existing data directory must be `chmod 700`** (the server creates new ones `0700`, but `os.MkdirAll` never changes a directory that already exists, and this one holds password hashes, agent secret hashes and uploaded files)

**Done when:** a new host can be managed from the public dashboard with **one** command.

---

Remote Windows is planned as two milestones (D-44). The idea: the agent never touches the screen. A normal VNC server on the host does that, listening on loopback only, and the agent reaches it with the `tunnel` session that already exists (D-35 stays as it is).

## M7 — Windows agent, minimal (D-12, D-44)
- [x] Windows system info (`sysinfo_windows.go`): hostname, OS version, CPU, RAM, disks, uptime (D-47)
- [x] The agent runs as a Windows service: `headnode-agent service install|uninstall` on Windows (D-47)
- [x] `windows/amd64` added to the build script and to the served agent list (D-40, D-47)
- [x] `install.ps1`: download the agent, enroll, install the service (same flow as `install.sh`)
- [x] Hosts page: Shell and Files are hidden for Windows hosts until they work there
- [x] Guide: TightVNC (or UltraVNC) on Windows, as a service, **loopback connections only**, with a password; optional: OpenSSH Server and RDP, reached with `hnctl forward` (`docs/windows.md`)

**Done when:** a Windows host is added with one command and shows *online* with CPU/RAM/disk. `hnctl forward 5900:<host>:127.0.0.1:5900` plus a VNC viewer on the laptop controls its screen, including the login screen. On a Windows Pro host, `hnctl forward 3389:<host>:127.0.0.1:3389` plus `mstsc` opens RDP.

## M8 — VNC in the browser (B4)
- [x] noVNC added, with a decisions entry (D-49); the server downloads it itself in `start.sh` (D-52)
- [x] The browser may open a `tunnel` session to `127.0.0.1:5900` only: same role as a shell in `can()`, audited, Origin-checked
- [x] Viewer page (**Screen** tab): screen scaled to the window, Ctrl+Alt+Del button, disconnect; a clear message when nothing listens on 5900
- [x] **VNC** button on host rows, and the Screen tab, shown for hosts that have a screen (Windows, or where the agent found a VNC server or can start one): none for a server with no display (D-50, D-65)
- [x] Screen page: Start VNC from the page, and zoom, pan and 1:1 for a host with several monitors (D-65)
- [x] **Start VNC** / **Stop VNC** from the dashboard for Windows hosts, to save the machine's resources (D-51)
- [x] `install.ps1` installs TightVNC 2.8.90 itself, loopback only, switched off, so a client runs one command and gets the screen (D-50, D-53); `-NoVnc`, `-VncPassword` and `-ReplaceVnc` options
- [x] Linux desktops (tested on a Raspberry Pi 5): `install.sh` prepares wayvnc or x11vnc, and Start VNC / Stop VNC run it on `127.0.0.1:5900` (D-54)

**Done when:** from the public dashboard, a Windows host's screen opens in a browser tab, mouse and keyboard work, Ctrl+Alt+Del reaches the login screen, and closing the tab ends the session on the host. A Linux host with a VNC server works the same way. A Windows host added with the one install command shows **Start VNC** (the screen is off by default, D-50), and after one click its VNC button, with nothing else to install.

## M9 — Windows tray icon: see that headnode is on, and exit it at the laptop (like Tailscale)
The idea: a person at the Windows machine must be able to turn headnode off, and be **certain** it is off, without typing commands. The dashboard cannot do this (the agent is the link to it), so the switch lives on the machine itself.
- [x] The agent service lets the person who installed it start it and send it the icon's control codes without administrator rights (the service's permissions are set at install time), so a program in their own session can turn it off and on; never the right to reconfigure a service (D-58)
- [x] `headnode-agent tray`: a tray icon in the user's session, in the same `.exe`: blue (the logo's colour) when the agent is connected, grey when it is running but cannot reach the server (D-60)
- [x] Menu: the status line, **Start VNC / Stop VNC** (the screen, D-51), **Exit** (D-59, D-60). No "Turn off / on" item and no "off" state in the icon: headnode is on with its icon, or off with none
- [x] "Off" really is off: after Exit the service is stopped **and** set not to start at boot, the icon is gone, and nothing of headnode runs until the headnode app is opened
- [x] `install.ps1` starts the tray at login (a per-user entry in the registry's Run key) and starts it now; `-NoTray` skips it; uninstalling the service removes the entry
- [x] Decisions entry for how the icon is drawn and how it talks to the service: the library `fyne.io/systray` (approved by the owner) and service permissions with control codes, never the right to reconfigure a service (D-57, D-58)
- [x] Guide in `docs/windows.md`: what the icon does, how to check by hand that headnode is off (`Get-Service headnode-agent`, `Get-Process headnode-agent`, nothing on port 5900)
- [x] **Exit** in the menu (in place of Quit) turns headnode off completely and closes the icon; **headnode** is a searchable Start menu app that shows the icon again and turns headnode on (D-59)

**Done when:** after installing with the one command, a blue tray icon appears; **Exit** closes the icon, the host goes *offline* in the dashboard within a minute, `Get-Service headnode-agent` says `Stopped` and `Manual` (D-58), no `headnode-agent` process exists, and a reboot does not bring it back or show an icon; typing **headnode** in the Start menu and opening it shows the icon and turns all of it back on. Linux has no tray (a Raspberry Pi or a server is managed with `systemctl`).

## M10 — Update notice: the dashboard says which agents are behind, the update stays manual (D-66)
The idea: the installers already update a host (run the install command again without a token). What is missing is knowing **which** hosts need it. The server tells you; you update each machine yourself.
- [x] The agent build script writes the agent version to `dist/VERSION`, next to the programs
- [x] The server reads `dist/VERSION` when it needs it (not once at start: agents can be rebuilt without a restart, D-40) and compares it with the version each agent sends in `hello`, number by number (`0.10.0` is newer than `0.9.2`)
- [x] Hosts page: an **Update available** badge on hosts that are behind, and "· N outdated" in the count pill
- [x] **How to update** dialog on such a host: the command for its OS and its agent, with a Copy button (D-67): `sudo headnode update` on Linux (paste it in the Shell tab), the tray's **Update headnode…** on Windows; the install command once, for an agent older than 0.11.0
- [x] The agent updates itself (D-67): `headnode-agent update`, on Linux `sudo headnode update` (`install.sh` links `headnode` to the agent). It reads `/download/VERSION`, downloads the program for its system next to the installed one, runs it with `version` to check it, and swaps it in and restarts the service. "Up to date" changes nothing
- [x] Windows tray: an **Update headnode…** menu item that starts that command elevated and with **no window**, behind the system's own administrator prompt. If the prompt is cancelled, nothing changes; if the update fails, the icon says why; if it works, the icon closes and comes back. From afar: Screen tab → tray icon → Update → prompt, through VNC
- [x] No new protocol message; no `dist/VERSION` means no badges and "cannot ask the server", not a crash

**Done when:** after building a newer agent into `dist/`, the hosts with the older version show *Update available* without restarting the server; running the command from the dialog (`sudo headnode update` on a Linux host) makes its badge disappear after it reconnects. On a Windows host, **Update headnode…** in the tray, after the administrator prompt and with no window, does the same and leaves one blue icon. Running `sudo headnode update` again says the host is up to date and changes nothing.

## M11 — Accounts: the last MVP slice (D-69)
The idea: today a password is set once, when the user is created, and never again; and `hnctl` tokens cannot be revoked from the dashboard (D-36). Fix the account basics. No picture upload: everyone gets one placeholder picture.

**Before building:** put the picture at **`web/sadaharu.jpg`** (next to `headnode-mark.svg`). It is compiled into the server like the logo, so the build fails until the file is there.

**Profile page (every user, for themselves)**
- [x] Change password: the current password is required; afterwards every **other** session of that user ends, `hnctl` tokens included; audited
- [x] Display name (optional), shown in the sidebar instead of the username; the username itself never changes
- [x] Logged-in sessions: browsers and `hnctl` tokens, with when they were last used, and **Sign out everywhere else** (each one can also be signed out on its own)
- [x] Reached by clicking the user block at the bottom of the sidebar (its menu: Profile, Log out)

**Users page (admin)**
- [x] Reset a user's password (ends all of that user's sessions)
- [x] Disable / enable a user instead of deleting: a disabled user cannot log in, and their sessions, tokens and open shells end at once
- [x] Make admin / remove admin
- [x] Guards: nobody can disable or demote themselves, and the last enabled admin cannot be disabled or demoted
- [x] Every change above is in the audit log

**Picture**
- [x] `web/sadaharu.jpg` added to the `//go:embed` line in `web/embed.go` and served like the logo
- [x] The sidebar user block and the Profile page show it for **every** user, in place of the initials

**Done when:** a user changes their password and their other browser and `hnctl` are logged out; an admin disables a user and that user's open shell closes and login fails; the last admin cannot remove their own admin rights; every one of these appears in the audit log; the sidebar shows the placeholder picture and the display name.

---

## After the MVP (not scheduled)
To be chosen later by the project owner: shell and file manager on Windows, RDP in the browser, one-click agent update from the dashboard (A3, see D-66 for what it needs first), 2FA (C3, its settings would live on the Profile page of M11), profile picture upload (D-69), session recording (C6), Telegram notifications (D2), HTTP web relay (B7), bulk commands (B8), CCTV (B15).
