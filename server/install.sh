#!/bin/sh
# headnode agent installer.
#
#   curl -fsSL __SERVER__/install.sh | sudo sh -s -- --token TOKEN
#
# The agent runs as the user who typed sudo, not as root: a terminal opened from
# the dashboard is that user's shell, and the file manager sees what that user
# may see. Use --user NAME to run it as someone else (--user root for root).
#
# Run it again, without --token, to update the agent of a host that is already
# enrolled. It needs root (to install), Linux with systemd, and curl or wget.
#
# Everything happens inside main, which is called on the last line: if the
# download of this script is cut short, the shell reaches the end of the file
# without ever calling main, so a half-downloaded script does nothing.

SERVER='__SERVER__'
BIN=/usr/local/bin/headnode-agent
CONFIG_DIR=/etc/headnode
CONFIG="$CONFIG_DIR/agent.json"
UNIT=/etc/systemd/system/headnode-agent.service

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

usage() {
    say "usage: curl -fsSL $SERVER/install.sh | sudo sh -s -- [--token TOKEN] [--user NAME]"
    say ""
    say "  --token TOKEN   the enroll token from the dashboard (Add host); needed the"
    say "                  first time. Without it, an enrolled host is only updated."
    say "  --user NAME     the account the agent runs as: terminals and files from the"
    say "                  dashboard are that user's. Default: the user who ran sudo"
    say "                  (or, on an update, the one chosen before). root is allowed."
}

# fetch URL FILE: download with curl, or wget when there is no curl.
fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        die "neither curl nor wget is installed"
    fi
}

main() {
    set -eu

    TOKEN=''
    RUN_USER=''
    while [ $# -gt 0 ]; do
        case "$1" in
            --token)
                [ $# -ge 2 ] || die "--token needs a value"
                TOKEN="$2"
                shift 2
                ;;
            --user)
                [ $# -ge 2 ] || die "--user needs a value"
                RUN_USER="$2"
                shift 2
                ;;
            -h|--help) usage; exit 0 ;;
            *) usage >&2; die "unknown option: $1" ;;
        esac
    done

    # --- is this machine one we can install on?
    [ "$(uname -s)" = "Linux" ] || die "only Linux is supported"
    [ "$(id -u)" = "0" ] || die "run this as root (put sudo before sh)"
    case "$(uname -m)" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) die "unsupported processor: $(uname -m) (this server offers amd64 and arm64)" ;;
    esac
    if ! command -v systemctl >/dev/null 2>&1 || [ ! -d /run/systemd/system ]; then
        die "systemd was not found. Run the agent by hand instead: $BIN enroll -server $SERVER -token TOKEN, then $BIN run"
    fi

    # --- the token (first install) or an existing enrollment (update)
    if [ ! -f "$CONFIG" ] && [ -z "$TOKEN" ]; then
        usage >&2
        die "this host is not enrolled yet: give --token"
    fi

    # --- who the agent runs as. Whatever it runs as is what a terminal opened from
    # the dashboard is: so by default it is the person who ran this, not root. An
    # update keeps the choice made when it was installed.
    if [ -z "$RUN_USER" ] && [ -f "$UNIT" ]; then
        RUN_USER=$(sed -n 's/^User=//p' "$UNIT" | head -n 1)
        [ -n "$RUN_USER" ] || RUN_USER=root    # an older unit with no User line ran as root
    fi
    [ -n "$RUN_USER" ] || RUN_USER="${SUDO_USER:-root}"
    case "$RUN_USER" in
        *[!a-z0-9_-]*|'') die "\"$RUN_USER\" is not a valid user name" ;;
    esac
    id "$RUN_USER" >/dev/null 2>&1 || die "the user $RUN_USER does not exist on this machine"
    if [ "$RUN_USER" = "root" ]; then
        say "The agent will run as root: terminals from the dashboard open as root."
        say "(To run it as a normal user, run this again with --user NAME.)"
    else
        say "The agent will run as $RUN_USER: terminals and files from the dashboard are theirs."
    fi

    # --- the program. Into a temporary file next to its final place, then moved
    # over it: the move is atomic, so a running agent is never replaced by half a
    # file, and a failed download leaves the old program where it was.
    say "Downloading the agent for linux/$arch..."
    tmp="$BIN.new.$$"
    trap 'rm -f "$tmp"' EXIT
    fetch "$SERVER/download/headnode-agent-linux-$arch" "$tmp"
    [ -s "$tmp" ] || die "the downloaded file is empty"
    chmod 0755 "$tmp"
    mv -f "$tmp" "$BIN"
    trap - EXIT

    # --- enroll, unless this host already is
    if [ -f "$CONFIG" ]; then
        say "This host is already enrolled ($CONFIG): keeping its identity."
    else
        say "Enrolling with $SERVER..."
        mkdir -p "$CONFIG_DIR"
        chmod 0700 "$CONFIG_DIR"
        "$BIN" enroll -server "$SERVER" -token "$TOKEN" -config "$CONFIG"
        chmod 0600 "$CONFIG"
    fi

    # The credentials folder and file belong to whoever runs the agent: they are a
    # secret, and the service could not read them otherwise.
    chown "$RUN_USER" "$CONFIG_DIR" "$CONFIG"
    chmod 0700 "$CONFIG_DIR"
    chmod 0600 "$CONFIG"

    # --- start at boot, and start now
    "$BIN" service install -config "$CONFIG" -user "$RUN_USER"
    say ""
    say "Done. The host appears in the dashboard within a few seconds."
}

main "$@"
