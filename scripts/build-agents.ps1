# Builds headnode-agent for every platform the server offers at /download
# (the list is agentBinaries in server/agentdist.go) into dist/.
#
#   .\scripts\build-agents.ps1            -> dist\headnode-agent-linux-amd64, ...-arm64
#   .\scripts\build-agents.ps1 -Out C:\x  -> somewhere else (then run the server with -agents C:\x)
#
# CGO_ENABLED=0 makes a static program with no C library behind it, so one
# binary runs on any Linux of that architecture. -trimpath and -s -w leave the
# build machine's folder names and the debug tables out: a smaller file, and
# nothing about this computer inside it.
param([string]$Out = "dist")

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force $Out | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "linux"
try {
    foreach ($arch in "amd64", "arm64") {
        $env:GOARCH = $arch
        $target = Join-Path $Out "headnode-agent-linux-$arch"
        go build -trimpath -ldflags "-s -w" -o $target ./cmd/headnode-agent
        if ($LASTEXITCODE -ne 0) { throw "go build failed for linux/$arch" }
        Write-Host ("built {0} ({1:N1} MB)" -f $target, ((Get-Item $target).Length / 1MB))
    }

    # The program for Windows hosts. The .exe suffix is part of the name the server serves.
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $target = Join-Path $Out "headnode-agent-windows-amd64.exe"
    go build -trimpath -ldflags "-s -w" -o $target ./cmd/headnode-agent
    if ($LASTEXITCODE -ne 0) { throw "go build failed for windows/amd64" }
    Write-Host ("built {0} ({1:N1} MB)" -f $target, ((Get-Item $target).Length / 1MB))
}
finally {
    # Do not leave the next `go build` in this window cross-compiling by accident.
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
}
