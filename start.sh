#!/bin/sh
# Sets up (and updates) the headnode server on a Linux machine from a git clone.
#
#   sudo sh start.sh                  build everything, install it, (re)start the service
#   sudo sh start.sh admin [NAME]     create the first administrator (asks for a password)
#   sudo sh start.sh caddy DOMAIN     HTTPS in front of it (installs Caddy on Debian/Ubuntu)
#   sh start.sh status                is it running?
#   sh start.sh logs                  follow the server's log
#
# To update later:  git pull && sudo sh start.sh
#
# What it puts where (all of it can be rebuilt; the data is the only thing to keep):
#   /opt/headnode/headnode         the server program
#   /opt/headnode/dist/            the agent programs, which the server hands to new hosts
#   /var/lib/headnode/             the database and uploaded files (private to the service's user)
#   /etc/systemd/system/headnode.service
#
# It is safe to run again: it rebuilds, replaces the program and restarts. It never
# touches the database.

INSTALL_DIR=/opt/headnode
DATA_DIR=/var/lib/headnode
SERVICE_USER=headnode
UNIT=/etc/systemd/system/headnode.service
# Only needed to start building: Go downloads the exact version go.mod asks for.
GO_BOOTSTRAP=1.23.4

say() { printf '%s\n' "$*"; }
die() { printf 'start: %s\n' "$*" >&2; exit 1; }

need_root() {
    [ "$(id -u)" = "0" ] || die "run this as root: sudo sh start.sh $*"
}

# Go keeps its build cache under $HOME, and some ways of becoming root leave it unset.
[ -n "${HOME:-}" ] || HOME=/root
export HOME

# Everything is done from the folder this script is in, whatever folder the person
# was in when they ran it.
cd "$(dirname "$0")" || die "cannot enter the project folder"
ROOT=$(pwd)
[ -f go.mod ] && [ -d cmd/headnode ] || die "$ROOT is not the headnode project (no go.mod or cmd/headnode)"

# ---------------------------------------------------------------- go

# have_go says whether a usable go is on the PATH. Any go from 1.21 on can fetch
# the toolchain go.mod needs by itself; older ones cannot.
have_go() {
    command -v go >/dev/null 2>&1 || return 1
    version=$(go env GOVERSION 2>/dev/null) || return 1   # "go1.23.4"
    minor=${version#go1.}
    minor=${minor%%.*}
    case "$minor" in ''|*[!0-9]*) return 1 ;; esac
    [ "$minor" -ge 21 ]
}

install_go() {
    case "$(uname -m)" in
        x86_64|amd64) goarch=amd64 ;;
        aarch64|arm64) goarch=arm64 ;;
        *) die "unsupported processor $(uname -m): install Go from go.dev/dl yourself, then run this again" ;;
    esac
    command -v curl >/dev/null 2>&1 || die "curl is needed to download Go: apt-get install -y curl"
    say "Installing Go $GO_BOOTSTRAP into /usr/local/go (it then fetches the exact version this project needs)..."
    tmp=$(mktemp -d) || die "cannot make a temporary folder"
    curl -fsSL "https://go.dev/dl/go$GO_BOOTSTRAP.linux-$goarch.tar.gz" -o "$tmp/go.tgz" || { rm -rf "$tmp"; die "could not download Go"; }
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp/go.tgz" || { rm -rf "$tmp"; die "could not unpack Go"; }
    rm -rf "$tmp"
}

ensure_go() {
    if ! have_go; then
        [ -x /usr/local/go/bin/go ] || install_go
        PATH="/usr/local/go/bin:$PATH"
        export PATH
        have_go || { install_go; have_go || die "Go is still not usable"; }
    fi
    say "Using $(go version)"
}

# ----------------------------------------------------------- noVNC

# The Screen page's viewer library (D-49, D-52). It is not kept in git: the server
# fetches this exact release itself, the first time and whenever NOVNC_VERSION
# changes, before it builds (the web files are compiled into the program).
NOVNC_VERSION=1.5.0
NOVNC_DIR=web/vendor/novnc

ensure_novnc() {
    if [ -f "$NOVNC_DIR/core/rfb.js" ] && [ "$(cat "$NOVNC_DIR/VERSION" 2>/dev/null)" = "$NOVNC_VERSION" ]; then
        return 0
    fi
    command -v curl >/dev/null 2>&1 || die "curl is needed to download noVNC: apt-get install -y curl"
    command -v tar >/dev/null 2>&1 || die "tar is needed to unpack noVNC: apt-get install -y tar"
    say "Downloading the noVNC screen viewer $NOVNC_VERSION from GitHub..."
    tmp=$(mktemp -d) || die "cannot make a temporary folder"
    # noVNC's own release on GitHub (its npm package is meant for bundlers and does
    # not have the plain browser files). A tag is a pinned version, so it is the same
    # files every time. Unpacked in its own new folder: the download is not trusted.
    mkdir "$tmp/x" || die "cannot make a temporary folder"
    curl -fsSL "https://github.com/novnc/noVNC/archive/refs/tags/v$NOVNC_VERSION.tar.gz" -o "$tmp/novnc.tgz" || { rm -rf "$tmp"; die "could not download noVNC"; }
    tar -xzf "$tmp/novnc.tgz" -C "$tmp/x" || { rm -rf "$tmp"; die "could not unpack noVNC"; }
    src="$tmp/x/noVNC-$NOVNC_VERSION"
    if [ ! -f "$src/core/rfb.js" ] || [ ! -d "$src/vendor" ]; then
        say "What was downloaded:"
        ls "$tmp/x" "$src" 2>&1 | sed 's/^/  /'
        rm -rf "$tmp"
        die "the noVNC download does not look right (no core/rfb.js and vendor/)"
    fi
    rm -rf "$NOVNC_DIR"
    mkdir -p "$NOVNC_DIR" || die "cannot create $NOVNC_DIR"
    cp -R "$src/core" "$src/vendor" "$NOVNC_DIR/" || { rm -rf "$tmp"; die "could not copy noVNC into place"; }
    cp "$src"/LICENSE* "$src/AUTHORS" "$NOVNC_DIR/" 2>/dev/null || true
    printf '%s\n' "$NOVNC_VERSION" > "$NOVNC_DIR/VERSION"
    rm -rf "$tmp"
}

# ----------------------------------------------------------- install

install_all() {
    need_root
    command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || die "systemd was not found: this script installs a systemd service"
    ensure_go
    ensure_novnc

    # No C compiler needed: the database driver is pure Go.
    CGO_ENABLED=0
    export CGO_ENABLED

    say "Building the server..."
    mkdir -p "$INSTALL_DIR/dist"
    go build -trimpath -ldflags "-s -w" -o "$INSTALL_DIR/headnode.new" ./cmd/headnode || die "the build failed"

    say "Building the agents (linux amd64 and arm64)..."
    sh scripts/build-agents.sh "$INSTALL_DIR/dist" || die "building the agents failed"

    # The program, and the folder it lives in, belong to root and are readable by
    # everyone: the service's user only needs to run them, never to change them.
    chown root:root "$INSTALL_DIR" "$INSTALL_DIR/dist" "$INSTALL_DIR"/dist/* "$INSTALL_DIR/headnode.new"
    chmod 0755 "$INSTALL_DIR" "$INSTALL_DIR/dist" "$INSTALL_DIR"/dist/* "$INSTALL_DIR/headnode.new"

    # A system account with no login and no home folder to speak of. The data
    # folder is private to it: it holds password hashes, agent secrets and uploads.
    if ! id "$SERVICE_USER" >/dev/null 2>&1; then
        say "Creating the system user $SERVICE_USER..."
        useradd --system --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER" \
            || die "could not create the user $SERVICE_USER"
    fi
    mkdir -p "$DATA_DIR"
    chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
    chmod 0700 "$DATA_DIR"

    # Stop, replace, start: a program that is running cannot always be overwritten.
    systemctl stop headnode 2>/dev/null || true
    mv -f "$INSTALL_DIR/headnode.new" "$INSTALL_DIR/headnode"
    install -m 0644 deploy/headnode.service "$UNIT"
    systemctl daemon-reload
    systemctl enable headnode >/dev/null 2>&1
    systemctl start headnode || { journalctl -u headnode -n 20 --no-pager; die "the service did not start (the log is above)"; }

    sleep 1
    if systemctl is-active --quiet headnode; then
        say ""
        say "headnode is running on 127.0.0.1:8080."
        say ""
        say "Next (the first time):"
        say "  1. sudo sh start.sh admin               create the first administrator"
        say "  2. sudo sh start.sh caddy YOUR.DOMAIN   HTTPS, so you can reach it from outside"
        say "  3. log in, Hosts > Add host, and run the command it shows on a host"
    else
        journalctl -u headnode -n 20 --no-pager
        die "the service is not running (the log is above)"
    fi
}

# ------------------------------------------------------------- admin

# run_as_service_user runs a command as the service's user, with the terminal
# still attached (the password is asked for there). runuser, unlike sudo, needs
# no configuration and is always there where systemd is.
run_as_service_user() {
    if command -v runuser >/dev/null 2>&1; then
        runuser -u "$SERVICE_USER" -- "$@"
    else
        sudo -u "$SERVICE_USER" "$@"
    fi
}

make_admin() {
    need_root admin
    [ -x "$INSTALL_DIR/headnode" ] || die "the server is not installed yet: run  sudo sh start.sh  first"
    name=${1:-admin}
    case "$name" in *[!A-Za-z0-9._-]*|'') die "use letters, digits, dot, dash or underscore in the user name" ;; esac
    say "Creating the administrator \"$name\"..."
    # From /: the service's user may not be allowed to enter the folder this script is in.
    (cd / && run_as_service_user "$INSTALL_DIR/headnode" user create -username "$name" -admin -db "$DATA_DIR/headnode.db")
}

# ------------------------------------------------------------- caddy

install_caddy() {
    command -v caddy >/dev/null 2>&1 && return 0
    command -v apt-get >/dev/null 2>&1 || die "Caddy is not installed, and this is not Debian or Ubuntu: install it from caddyserver.com/docs/install, then run this again"
    say "Installing Caddy from its official package repository..."
    apt-get update -y >/dev/null
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gpg >/dev/null || die "could not install Caddy's prerequisites"
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg || die "could not fetch Caddy's signing key"
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o /etc/apt/sources.list.d/caddy-stable.list || die "could not add Caddy's repository"
    chmod o+r /usr/share/keyrings/caddy-stable-archive-keyring.gpg /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -y >/dev/null
    apt-get install -y caddy >/dev/null || die "could not install Caddy"
}

setup_caddy() {
    need_root caddy
    domain=${1:-}
    [ -n "$domain" ] || die "give your domain name: sudo sh start.sh caddy headnode.example.com"
    # A host name only. It goes into a config file, so nothing else may get in.
    case "$domain" in
        *[!A-Za-z0-9.-]*|.*|-*|*..*|*.) die "\"$domain\" is not a plain domain name" ;;
        *.*) ;;
        *) die "\"$domain\" is not a domain name with a dot in it" ;;
    esac
    [ -f deploy/Caddyfile ] || die "deploy/Caddyfile is missing"

    install_caddy
    mkdir -p /etc/caddy
    if [ -f /etc/caddy/Caddyfile ] && ! grep -q 'reverse_proxy 127.0.0.1:8080' /etc/caddy/Caddyfile; then
        cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak.$(date +%Y%m%d%H%M%S)"
        say "The old /etc/caddy/Caddyfile was kept as a .bak copy."
    fi
    sed "s/headnode\\.example\\.com/$domain/g" deploy/Caddyfile > /etc/caddy/Caddyfile
    systemctl enable caddy >/dev/null 2>&1
    systemctl restart caddy || { journalctl -u caddy -n 20 --no-pager; die "Caddy did not start (the log is above)"; }
    say ""
    say "Caddy is serving https://$domain and getting its certificate."
    say "That needs the domain's DNS record to point at this machine, and ports 80 and 443 open."
    say "If you use ufw:  ufw allow 80,443/tcp"
}

# ------------------------------------------------------------- the rest

case "${1:-install}" in
    install|update) install_all ;;
    admin)          shift; make_admin "$@" ;;
    caddy)          shift; setup_caddy "$@" ;;
    status)
        systemctl status headnode --no-pager || true
        say ""
        if command -v curl >/dev/null 2>&1; then
            curl -fsS -o /dev/null -w 'http://127.0.0.1:8080 answers with HTTP %{http_code}\n' http://127.0.0.1:8080/ || say "nothing answers on 127.0.0.1:8080"
        fi
        ;;
    logs)           exec journalctl -u headnode -f ;;
    -h|--help|help) sed -n '2,15p' "$0" ;;
    *)              die "unknown command \"$1\" (try: install, admin, caddy, status, logs)" ;;
esac
