# headnode agent installer for Windows.
#
#   & ([scriptblock]::Create((irm __SERVER__/install.ps1))) -Token TOKEN
#
# Run it in PowerShell opened with "Run as administrator". Run it again without
# -Token to update the agent of a host that is already enrolled.
#
# It also sets up the screen (D-50): TightVNC, as a service, listening on this
# machine only (127.0.0.1:5900), which the dashboard's VNC button reaches through
# the agent. It is installed SWITCHED OFF: nothing of this computer's screen is
# shared until someone presses Start VNC in the dashboard. Options:
#   -NoVnc              do not touch VNC (you have your own, or want no screen access)
#   -VncPassword SECRET ask for this password when connecting (1 to 8 letters or
#                       digits; VNC ignores anything past 8). Without it, the
#                       dashboard login and role are the only gate.
#
# The whole script is one script block that PowerShell reads completely before it
# runs anything: a download that is cut short is a syntax error, and does nothing.
param([string]$Token = '', [switch]$NoVnc, [string]$VncPassword = '')

$ErrorActionPreference = 'Stop'
$Server = '__SERVER__'
# TightVNC is downloaded from its makers, not bundled: it is GPL software, and the
# download is checked below to be signed by them before it is run.
$TightVncUrl = 'https://www.tightvnc.com/download/2.8.85/tightvnc-2.8.85-gpl-setup-64bit.msi'
$TightVncSigner = 'GlavSoft'
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
# The password goes onto msiexec's command line: plain letters and digits cannot
# be mistaken for quoting.
if ($VncPassword -and $VncPassword -notmatch '^[A-Za-z0-9]{1,8}$') {
    Fail '-VncPassword must be 1 to 8 letters or digits'
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
    # Delete the old program first: Move-Item -Force does not always replace an existing
    # file on Windows PowerShell 5 ("Cannot create a file when that file already exists").
    # If something still holds the old program, this fails with a clear message.
    if (Test-Path $Bin) { Remove-Item -Force -Path $Bin }
    Move-Item -Path $tmp -Destination $Bin
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

# --- the screen. Not being able to set it up must not undo the agent, which is
# already installed and running: it only costs the VNC button, so it is a warning.
function Install-Vnc {
    $listening = @(Get-NetTCPConnection -LocalPort 5900 -State Listen -ErrorAction SilentlyContinue)
    if ($listening.Count -gt 0) {
        Write-Host 'A VNC server already listens on port 5900: keeping it as it is.'
        if ($listening | Where-Object { $_.LocalAddress -notin '127.0.0.1', '::1' }) {
            Write-Warning 'It accepts connections from the network too. Restrict it to this machine (docs/windows.md).'
        }
        return
    }
    if (Get-Service -Name tvnserver -ErrorAction SilentlyContinue) {
        # What this script leaves behind (see the end of this function), so running the
        # script again to update the agent must not turn it on.
        Write-Host 'TightVNC is installed and switched off. Turn it on from the dashboard (Start VNC) when you need the screen.'
        return
    }

    # Its own new folder: a downloaded file is not trusted, so it gets nothing else around it.
    $work = Join-Path $env:TEMP ('headnode-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $msi = Join-Path $work 'tightvnc.msi'
        Write-Host 'Downloading TightVNC...'
        Invoke-WebRequest -UseBasicParsing -Uri $TightVncUrl -OutFile $msi

        $sig = Get-AuthenticodeSignature -FilePath $msi
        if ($sig.Status -ne 'Valid' -or -not $sig.SignerCertificate -or $sig.SignerCertificate.Subject -notmatch $TightVncSigner) {
            $who = if ($sig.SignerCertificate) { $sig.SignerCertificate.Subject } else { 'nobody' }
            Fail "the TightVNC download is not signed by $TightVncSigner (signature $($sig.Status), signed by $who): not installing it"
        }

        # Every property here is TightVNC's own installer setting:
        #  - a service, so the screen works at the login screen and after a reboot
        #  - connections from this machine only, which is where the agent comes from
        #  - no firewall rule: a loopback connection needs none, and none must open the port
        #  - Ctrl+Alt+Del can be sent by the dashboard's button
        $auth = if ($VncPassword) { '1' } else { '0' }
        $msiArgs = @('/i', "`"$msi`"", '/quiet', '/norestart',
            'ADDLOCAL=Server',
            'SERVER_REGISTER_AS_SERVICE=1', 'SERVER_ADD_FIREWALL_EXCEPTION=0', 'SERVER_ALLOW_SAS=1',
            'SET_ALLOWLOOPBACK=1', 'VALUE_OF_ALLOWLOOPBACK=1',
            'SET_LOOPBACKONLY=1', 'VALUE_OF_LOOPBACKONLY=1',
            'SET_USEVNCAUTHENTICATION=1', "VALUE_OF_USEVNCAUTHENTICATION=$auth")
        if ($VncPassword) { $msiArgs += @('SET_PASSWORD=1', "VALUE_OF_PASSWORD=$VncPassword") }
        Write-Host 'Installing TightVNC (this host only, as a service)...'
        $p = Start-Process -FilePath msiexec.exe -ArgumentList $msiArgs -Wait -PassThru
        if ($p.ExitCode -ne 0 -and $p.ExitCode -ne 3010) { Fail "the TightVNC installer failed (exit code $($p.ExitCode))" }
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $work
    }

    try {
        # Trust, but look: the installer starts the service, which may need a moment,
        # and the whole point is that it listens on this machine and nowhere else.
        for ($i = 0; $i -lt 15; $i++) {
            $listening = @(Get-NetTCPConnection -LocalPort 5900 -State Listen -ErrorAction SilentlyContinue)
            if ($listening.Count -gt 0) { break }
            Start-Sleep -Seconds 1
        }
        if ($listening.Count -eq 0) { Fail 'TightVNC is installed but nothing listens on port 5900' }
        if ($listening | Where-Object { $_.LocalAddress -notin '127.0.0.1', '::1' }) {
            Write-Warning 'TightVNC listens on the network, not only on this machine. Tick "Allow only loopback connections" in its settings before you turn it on (docs/windows.md).'
        } else {
            Write-Host 'TightVNC is installed, and listens on this machine only.'
        }
    } finally {
        # Off by default, like a VPN client (D-50): the screen of this computer is not
        # shared until someone turns it on, from the dashboard (Start VNC), and it can be
        # turned off again there. In a finally so that a failed check above, or a server
        # open to the network, never leaves it running. It is stopped AND set to start by
        # hand, or the next reboot would bring it back.
        Set-Service -Name tvnserver -StartupType Manual
        Stop-Service -Name tvnserver -Force
        Write-Host 'TightVNC is switched OFF. Turn it on from the dashboard (Start VNC) when you need the screen.'
    }
}

if ($NoVnc) {
    Write-Host 'Skipping VNC (-NoVnc).'
} else {
    try {
        Install-Vnc
    } catch {
        Write-Warning "The screen was not set up: $($_.Exception.Message)"
        Write-Warning 'The agent works without it. See docs/windows.md to set up VNC by hand, or run this script again.'
    }
}

Write-Host ''
Write-Host 'Done. The host appears in the dashboard within a few seconds.'
