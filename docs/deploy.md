# Deploying headnode on a VPS

The server runs on a small Linux VPS (Debian or Ubuntu, with systemd) behind Caddy for HTTPS. You clone the project there, and one script, [`start.sh`](../start.sh), builds and installs everything. There is nothing to copy over from your computer.

You need: a VPS you can log into as root (or with `sudo`), a domain name whose DNS record points at it (`headnode.example.com` below), and ports 80 and 443 open.

## 1. Install it

```sh
apt-get update && apt-get install -y git curl
git clone <the project's git address> headnode
cd headnode
sudo sh start.sh
```

`start.sh` does, in this order, and says what it is doing:

1. installs Go if the machine has none (it only needs a small starter Go: the project's own exact version is then downloaded by Go itself),
2. downloads the noVNC screen viewer (a pinned release from the npm registry, into `web/vendor/novnc/`, which git ignores; D-52) if it is not there yet,
3. builds the server and the agent programs (Linux amd64 and arm64),
4. puts them in `/opt/headnode/` (owned by root, readable by everyone),
5. creates a system user `headnode` and the private data folder `/var/lib/headnode` (mode `0700`),
6. installs and starts the `headnode` service.

When it ends it says "headnode is running on 127.0.0.1:8080". The server listens on that local address only, so nothing from outside can reach it yet.

A private repository needs a way to log in for `git clone`: a deploy key, or a personal access token in the address.

## 2. Create the administrator

```sh
sudo sh start.sh admin
```

It asks for a password (nothing is shown while you type) and creates the user `admin`. For another name: `sudo sh start.sh admin sanca`.

## 3. HTTPS

Point your domain's DNS record at the VPS, then:

```sh
sudo sh start.sh caddy headnode.example.com
```

This installs Caddy (Debian and Ubuntu; elsewhere install it yourself first from caddyserver.com/docs/install), writes `/etc/caddy/Caddyfile` from `deploy/Caddyfile`, and starts it. Caddy gets the certificate by itself. If you use a firewall, allow the ports: `ufw allow 80,443/tcp` (and keep 22 for ssh).

Open `https://headnode.example.com` and log in.

## 4. Add a host

**Hosts → Add host**, pick a group, copy the command, and run it on the machine you want to manage:

```sh
curl -fsSL https://headnode.example.com/install.sh | sudo sh -s -- --token <TOKEN>
```

The host shows up within seconds. The same token works for other hosts for 24 hours.

The agent runs as **the user who typed `sudo`**, not as root: a terminal opened from the dashboard is that user's shell, and the file manager sees what that user may see. To use another account, add `--user NAME` after the token (`--user root` if you really want root). Logged in as root with no `--user`, it runs as root.

## Updating

```sh
cd headnode
git pull
sudo sh start.sh
```

It rebuilds, replaces the program and restarts the service. The data is not touched. Hosts reconnect by themselves within a minute, and database changes are applied when the server starts (D-30). To update an agent on a host, run its install command again without `--token`: `curl -fsSL https://headnode.example.com/install.sh | sudo sh`. To remove an agent: `headnode-agent service uninstall` on that host, then delete the host in the dashboard.

## Everyday commands

```sh
sh start.sh status       # is it running? does it answer?
sh start.sh logs         # the server's log, live (Ctrl+C to stop)
```

A host's log: `journalctl -u headnode-agent -f`.

## Checklist

- [ ] `ls -ld /var/lib/headnode` shows `drwx------ ... headnode headnode`. `start.sh` sets this every time. It holds password hashes, agent secrets and uploaded files, so nobody else may read it.
- [ ] The firewall allows only 22, 80 and 443. `ss -ltn` shows port 8080 on `127.0.0.1` only.
- [ ] `https://headnode.example.com` loads, and `http://` redirects to it.
- [ ] You can log in, and a host added with the one-line command appears and opens a terminal.
- [ ] Backups: copy `/var/lib/headnode/headnode.db` and `/var/lib/headnode/files/` somewhere safe. The database is in WAL mode, so stop the service for a moment first (`systemctl stop headnode`).

## If something does not work

- **"run this as root"**: put `sudo` in front, or log in as root. The script installs a system service, so it cannot run as a normal user.
- **The service does not start**: `start.sh` prints the end of the log when that happens. `sh start.sh logs` shows more. A common cause is another program already using port 8080.
- **The terminal or files do not open from the browser, but the pages load**: the proxy in front does not pass WebSockets. Caddy does by default.
- **Every login attempt from everyone seems to come from one address**: the server only believes `X-Forwarded-For` from a proxy on the same machine, so Caddy has to run on the VPS itself.
- **The install command says `404` for the download**: the agent for that processor is not in `/opt/headnode/dist`. Run `sudo sh start.sh` again and look for errors while it builds the agents.
- **Building is slow or fails on a very small VPS**: Go needs about 1 GB of memory to build. Add a swap file, or build on a bigger machine and use the update flow.
