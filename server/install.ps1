# headnode agent installer for Windows.
#
#   & ([scriptblock]::Create((irm __SERVER__/install.ps1))) -Token TOKEN
#
# Run it in PowerShell opened with "Run as administrator". Run it again without
# -Token to update the agent of a host that is already enrolled.
#
# The whole script is one script block that PowerShell reads completely before it
# runs anything: a download that is cut short is a syntax error, and does nothing.
param([string]$Token = '')

$ErrorActionPreference = 'Stop'
$Server = '__SERVER__'
$Dir = Join-Path $env:ProgramFiles 'headnode'
$Bin = Join-Path $Dir 'headnode-agent.exe'
$DataDir = Join-Path $env:ProgramData 'headnode'
$Config = Join-Path $DataDir 'agent.json'
$ServiceName = 'headnode-agent'

# throw, not exit: this runs inside the person's own PowerShell window, and
# "exit" would close it before they could read the message.
function Fail($message) {
    throw "install: $message"
}

# --- is this machine one we can install on?
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not (New-Object Security.Principal.WindowsPrincipal($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Fail 'run this in PowerShell opened with "Run as administrator"'
}
if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64' -and $env:PROCESSOR_ARCHITEW6432 -ne 'AMD64') {
    Fail "unsupported processor: $env:PROCESSOR_ARCHITECTURE (this server offers Windows on x64)"
}
if (-not (Test-Path $Config) -and -not $Token) {
    Fail 'this host is not enrolled yet: give -Token (from the dashboard: Hosts > Add host)'
}

# Windows PowerShell 5 does not use modern TLS unless told to.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# --- the program. Downloaded to a temporary file first, so a failed download
# leaves the old program where it was. A running program cannot be overwritten on
# Windows, so the service is stopped for the moment of the replacement.
Write-Host 'Downloading the agent for windows/amd64...'
New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$tmp = "$Bin.new"
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$Server/download/headnode-agent-windows-amd64.exe" -OutFile $tmp
    if ((Get-Item $tmp).Length -eq 0) { Fail 'the downloaded file is empty' }

    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing -and $existing.Status -ne 'Stopped') {
        Stop-Service -Name $ServiceName -Force
        $existing.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }
    Move-Item -Force -Path $tmp -Destination $Bin
} finally {
    Remove-Item -Force -ErrorAction SilentlyContinue $tmp
}

# --- enroll, unless this host already is
if (Test-Path $Config) {
    Write-Host "This host is already enrolled ($Config): keeping its identity."
} else {
    Write-Host "Enrolling with $Server..."
    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
    # The credentials are a secret: only the system and administrators may read
    # them, not every user of the machine (ProgramData is readable by all by default).
    & icacls $DataDir /inheritance:r /grant:r 'SYSTEM:(OI)(CI)F' 'Administrators:(OI)(CI)F' | Out-Null
    & $Bin enroll -server $Server -token $Token -config $Config
    if ($LASTEXITCODE -ne 0) { Fail 'enrolling failed (is the token right, and still valid?)' }
}

# --- start at boot, and start now
& $Bin service install -config $Config
if ($LASTEXITCODE -ne 0) { Fail 'installing the service failed' }
Write-Host ''
Write-Host 'Done. The host appears in the dashboard within a few seconds.'
