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
    say "usage: curl -fsSL $SERVER/install.sh | sudo sh -s -- [--token TOKEN] [--user NAME] [--no-vnc]"
    say ""
    say "  --token TOKEN   the enroll token from the dashboard (Add host); needed the"
    say "                  first time. Without it, an enrolled host is only updated."
    say "  --user NAME     the account the agent runs as: terminals and files from the"
    say "                  dashboard are that user's. Default: the user who ran sudo"
    say "                  (or, on an update, the one chosen before). root is allowed."
    say "  --no-vnc        do not prepare this machine's screen for the dashboard (a"
    say "                  machine with a desktop gets wayvnc or x11vnc, switched off)."
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

# --- the screen (D-54). A machine with a desktop can show it in the dashboard: the
# agent starts a VNC server on this machine only (127.0.0.1:5900) when somebody
# presses Start VNC, and stops it again. All this script does is make sure the
# program is there, and say so. Nothing is started, so the screen is OFF until then.

# has_wayland USER: is a Wayland desktop (Raspberry Pi OS since Bookworm) running?
# root's agent looks in every user's session, the others in their own.
has_wayland() {
    if [ "$1" = "root" ]; then dirs=/run/user/*; else dirs="/run/user/$(id -u "$1")"; fi
    for d in $dirs; do
        for s in "$d"/wayland-*; do
            [ -e "$s" ] || continue
            case "$s" in *.lock) continue ;; esac
            return 0
        done
    done
    return 1
}

has_x11() {
    for s in /tmp/.X11-unix/X*; do
        [ -e "$s" ] && return 0
    done
    return 1
}

# install_package NAME: from the system's own package list (apt). Quiet, and false
# when it cannot be done, so the caller can say what to do by hand.
install_package() {
    command -v "$1" >/dev/null 2>&1 && return 0
    command -v apt-get >/dev/null 2>&1 || return 1
    say "Installing $1..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y "$1" >/dev/null 2>&1 && return 0
    apt-get update >/dev/null 2>&1 || return 1
    DEBIAN_FRONTEND=noninteractive apt-get install -y "$1" >/dev/null 2>&1
}

setup_vnc() {
    user=$1
    if has_wayland "$user"; then
        program=wayvnc
    elif has_x11; then
        program=x11vnc
    else
        say "No desktop is running for $user, so there is no screen to share (a server needs nothing)."
        say "To add one later, log in to the desktop and run this command again."
        return 0
    fi
    install_package "$program" || {
        say "$program is not installed and could not be installed: install it yourself, then run this command again."
        return 0
    }

    # Something already on port 5900 is not ours to touch, but it matters: the stock
    # VNC of Raspberry Pi OS listens on the network and asks for a login that a browser
    # cannot give, so the dashboard would show a screen it cannot open.
    if command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':5900 '; then
        say "A VNC server already listens on port 5900: it is left as it is."
        if ss -ltn 2>/dev/null | grep ':5900 ' | grep -qv '127\.0\.0\.1:5900\|\[::1\]:5900'; then
            say "It accepts connections from the network too. headnode runs its own copy, on this"
            say "machine only, so turn that one off. On Raspberry Pi OS:  sudo raspi-config nonint do_vnc 1"
            say "Then press Start VNC in the dashboard."
        fi
        return 0
    fi
    say "The screen is ready ($program), and switched OFF. Press Start VNC in the dashboard when you need it."
}

main() {
    set -eu

    TOKEN=''
    RUN_USER=''
    NO_VNC=''
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
            --no-vnc)
                NO_VNC=1
                shift
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
    # update keeps the choice made when it was installed (the User= line of the
    # unit). A unit with no such line is from before there was a choice: it ran as
    # root only because nothing said otherwise, so it gets the default too.
    if [ -z "$RUN_USER" ] && [ -f "$UNIT" ]; then
        RUN_USER=$(sed -n 's/^User=//p' "$UNIT" | head -n 1)
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

    # --- the screen. A failure here costs only the VNC button: the agent is already running.
    if [ -n "$NO_VNC" ]; then
        say "Skipping VNC (--no-vnc)."
    else
        setup_vnc "$RUN_USER" || say "The screen was not set up. The agent works without it."
    fi
    say ""
    say "Done. The host appears in the dashboard within a few seconds."
}

main "$@"
