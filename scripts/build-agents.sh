#!/bin/sh
# Builds headnode-agent for every platform the server offers at /download
# (the list is agentBinaries in server/agentdist.go) into dist/.
#
#   ./scripts/build-agents.sh          -> dist/headnode-agent-linux-amd64, ...-arm64
#   ./scripts/build-agents.sh /some/dir
#
# Same as build-agents.ps1, for building on the server itself.
set -eu

out="${1:-dist}"
mkdir -p "$out"

for arch in amd64 arm64; do
    target="$out/headnode-agent-linux-$arch"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "$target" ./cmd/headnode-agent
    echo "built $target"
done

# Windows: the program for Windows hosts (D-47). The .exe suffix is part of the
# name the server serves.
target="$out/headnode-agent-windows-amd64.exe"
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$target" ./cmd/headnode-agent
echo "built $target"

# SHA-256 checksums for every built binary, verified by `headnode-agent update`
# before executing or installing a download. Written before VERSION so a failed
# build never announces a new version without matching checksums.
(
    cd "$out"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum headnode-agent-linux-amd64 headnode-agent-linux-arm64 headnode-agent-windows-amd64.exe > SHA256SUMS
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 headnode-agent-linux-amd64 headnode-agent-linux-arm64 headnode-agent-windows-amd64.exe > SHA256SUMS
    else
        echo "could not write SHA256SUMS: neither sha256sum nor shasum found" >&2
        exit 1
    fi
)
echo "wrote $out/SHA256SUMS"

# The version of what was just built, for the dashboard's "update available" (D-66):
# the Version constant of the agent package. Written last, so a failed build leaves
# the old version in place.
version=$(sed -n 's/^const Version = "\([0-9.]*\)"$/\1/p' agent/conn.go)
[ -n "$version" ] || { echo "could not read the agent version from agent/conn.go" >&2; exit 1; }
printf '%s' "$version" > "$out/VERSION"
echo "agent version $version"
