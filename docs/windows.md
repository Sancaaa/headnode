# Managing a Windows host

A Windows host joins headnode like a Linux one and shows its **status** (online, CPU, memory, disk, uptime) in the dashboard. It has no terminal and no file manager yet. To control its **screen**, you run a normal VNC server on it, and reach that through headnode's tunnel with `hnctl`. headnode itself never touches the screen (D-44), so the VNC server does the work and headnode only carries the connection to it, from anywhere, without opening a port on the Windows machine.

```
  your laptop                    headnode server                 the Windows host
  VNC viewer ── localhost:5900 ──► hnctl ══ tunnel ══► agent ──► VNC server (127.0.0.1:5900)
```

You need: 64-bit Windows 10 or 11 (or Server 2016 or later), a login with administrator rights on it, and an enroll token from the dashboard.

## 1. Add the host

In the dashboard: **Hosts → Add host**, pick a group, and copy the **Windows** command. On the Windows machine, open PowerShell **as administrator** (right-click, Run as administrator) and paste it:

```powershell
& ([scriptblock]::Create((irm https://headnode.example.com/install.ps1))) -Token <TOKEN>
```

It downloads the agent from your server into `C:\Program Files\headnode\`, enrolls the host, keeps the credentials in `C:\ProgramData\headnode\agent.json` (readable by the system and administrators only), and installs a Windows service called `headnode-agent` that starts at boot. The host is *online* in the dashboard within seconds.

- **To update the agent**, run the same command again without `-Token`.
- **To remove it**, in an administrator PowerShell: `& "C:\Program Files\headnode\headnode-agent.exe" service uninstall`, then delete the host in the dashboard.
- **The agent's log** is `C:\ProgramData\headnode\agent.log`.
- **Windows Defender or SmartScreen** may warn about the program because it is not signed. If it removes the file, add an exclusion for `C:\Program Files\headnode\`; if it only warns, the script still works.

The same command also sets up the screen (D-50): it downloads TightVNC from tightvnc.com, checks that the file is signed by its makers, and installs it as a service that **accepts connections from this machine only**. It is installed **switched off**, like a VPN client that is installed but not connected: the host's row in the dashboard shows **Start VNC**, and nothing of the screen is shared until someone presses it. After that the **VNC** button appears, and **Stop VNC** (on the Screen page) switches it off again. You have nothing else to install; step 2 is only for when that did not work. The command takes two options at the end:

- `-VncPassword SECRET` makes the screen ask for a password (1 to 8 letters or digits) on every connection. Without it, the dashboard login and the operator role are the only gate. Use it on a machine where several people are logged in at once, because any of them could reach the screen from inside the machine.
- `-NoVnc` leaves VNC alone, if you use your own or want no screen access.

If a VNC server already listens on port 5900, the script keeps it as it is and only warns when it is open to the network.

**Switching the screen on and off (D-51).** A VNC server uses CPU and memory all the time, even when nobody is watching, and shares the screen while it runs, so the installer leaves it off. The Hosts page shows **Start VNC** where the VNC button would be; one click starts it and the VNC button comes back within seconds. **Stop VNC** on its Screen page stops it again and sets it to start only by hand, so it stays off after a reboot; on that page the same button then reads **Start VNC**, and the screen connects by itself once the server is up. (Starting it sets it to start at boot, until you stop it again.) You need the same operator role as for the screen itself, and every start and stop is in the audit log. This works for the TightVNC the install command set up (the service named `tvnserver`).

## 2. Install a VNC server by hand, for loopback only

Skip this if the install command set up TightVNC (the VNC button is there). Otherwise: headnode needs a VNC server listening on `127.0.0.1:5900` **and nowhere else**. A VNC server open to the network is a hole headnode cannot close for you, and VNC passwords are weak.

**TightVNC** (free, tightvnc.com):

1. Run the installer and choose a *Custom* install. Keep **TightVNC Server** and turn off the Viewer if you do not need it. Tick **Register TightVNC Server as a system service** (so it works at the login screen and after a reboot).
2. When it asks, set a **remote access password** (and an administrative password). Use a long one.
3. Open **TightVNC Server → Configuration** (Service Mode):
   - on **Access Control**, tick **Allow only loopback connections** (some versions word it "Allow loopback connections" and "Only"; the point is that nothing but this same machine may connect);
   - leave the port at `5900`.
4. Windows Firewall: the installer may add a rule that allows port 5900 from the network. **Delete or disable it** (Windows Defender Firewall → Advanced settings → Inbound Rules → TightVNC). The tunnel connects from the machine itself, so it needs no rule.

UltraVNC works too: in its server settings enable **Loopback Connections → Allow only loopback**, and set a password.

Check it from the host itself, in PowerShell:

```powershell
Get-NetTCPConnection -LocalPort 5900 -State Listen | Select-Object LocalAddress, LocalPort
```

`LocalAddress` must say `127.0.0.1`, not `0.0.0.0`.

## 3. Connect from the browser

In the dashboard, click **VNC** on the host's row (or its **Screen** tab). Both appear only when the agent finds a VNC server on the host, within a minute of it starting; an agent older than 0.8.0 never reports it, so update it by running the install command again. The screen opens in the page, scaled to the window. If the VNC server has a password, the page asks for it once per connection (it is not stored). It has a **Ctrl+Alt+Del** button for the Windows login screen and **Disconnect** to end the session. If the page says nothing is listening on port 5900, the VNC server is not running or is not on the standard port: check step 2. You need the **operator** role in the host's group. Closing the tab ends the session on the host.

The browser can only reach port 5900. For anything else (RDP, SSH, a VNC server on another port) use `hnctl`, below.

## 4. Or connect from your laptop with a viewer program

On your own computer, log in once and forward a local port to the Windows host's VNC port (see `hnctl` in the main docs):

```
hnctl login -server https://headnode.example.com
hnctl forward 5900:<host-name>:127.0.0.1:5900
```

Leave that running, and point any VNC viewer (TightVNC Viewer, RealVNC Viewer, UltraVNC Viewer, the viewer of your choice) at `localhost:5900`. Type the VNC password. You see the screen, including the **login screen**, because the VNC server runs as a service. If port 5900 is busy on your laptop, use another: `hnctl forward 5901:<host-name>:127.0.0.1:5900` and connect to `localhost:5901`.

You need the **operator** role in the host's group, like for a terminal on a Linux host: the tunnel is as powerful as a shell (D-35).

## Optional: RDP and SSH

The same tunnel carries anything on the host's own loopback:

- **RDP** (Windows Pro, Enterprise or Server only; the Home edition cannot be a host): enable Remote Desktop in Settings, then `hnctl forward 3389:<host-name>:127.0.0.1:3389` and open `mstsc` on `localhost:3389`. RDP is faster than VNC over a slow link. Windows listens for RDP on all addresses by default, so keep the firewall's default of blocking it from the internet.
- **SSH**: install **OpenSSH Server** (Settings → Apps → Optional features), then `hnctl forward 2222:<host-name>:127.0.0.1:22` and `ssh -p 2222 <windows user>@localhost`. This gives you a command line.

## What does not work on Windows yet

The Hosts page hides **Shell** and **Files** for Windows hosts, because the agent has no terminal and file manager for Windows yet. Sending files to a Windows host from the Transfers page is not offered either. The screen is available in the browser (Screen tab) and with any VNC viewer through `hnctl`.
