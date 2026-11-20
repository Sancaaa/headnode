#!/bin/sh
# headnode agent installer.
#
#   curl -fsSL __SERVER__/install.sh | sudo sh -s -- --token TOKEN
#
# Run it again, without --token, to update the agent of a host that is already
# enrolled. It needs root, Linux with systemd, and curl or wget.
#
# Everything happens inside main, which is called on the last line: if the
# download of this script is cut short, the shell reaches the end of the file
# without ever calling main, so a half-downloaded script does nothing.

SERVER='__SERVER__'
BIN=/usr/local/bin/headnode-agent
CONFIG_DIR=/etc/headnode
CONFIG="$CONFIG_DIR/agent.json"

say() { printf '%s\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

usage() {
    say "usage: curl -fsSL $SERVER/install.sh | sudo sh -s -- [--token TOKEN]"
    say ""
    say "  --token TOKEN   the enroll token from the dashboard (Add host); needed the"
    say "                  first time. Without it, an enrolled host is only updated."
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
    while [ $# -gt 0 ]; do
        case "$1" in
            --token)
                [ $# -ge 2 ] || die "--token needs a value"
                TOKEN="$2"
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

    # --- start at boot, and start now
    "$BIN" service install -config "$CONFIG"
    say ""
    say "Done. The host appears in the dashboard within a few seconds."
}

main "$@"
