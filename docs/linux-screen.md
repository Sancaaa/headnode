# The screen of a Linux host (Raspberry Pi and other desktops)

A Linux host with a desktop can show its screen in the dashboard, like a Windows one (D-54). The install command prepares it, and the screen stays **off** until someone presses **Start VNC**.

## What you need
- A desktop that is **running**: logged in, or set to log in by itself (a Raspberry Pi with the desktop image does). A server with no desktop has no screen to share, and the installer says so.
- A Wayland desktop (the Raspberry Pi 5 default, labwc) needs **wayvnc**; an X11 desktop needs **x11vnc**. The install command installs the right one with `apt-get` if it is missing (Raspberry Pi OS already has wayvnc).

## Set up
Run the dashboard's install command on the host, as usual (Hosts, Add host). Near the end it says "The screen is ready (wayvnc), and switched OFF." Add `--no-vnc` at the end of the command to skip this.

If it says a VNC server already listens on port 5900 and is open to the network, that is Raspberry Pi OS's own VNC. headnode runs its own copy, on this machine only, so turn the stock one off:

```
sudo raspi-config nonint do_vnc 1
```

## Use it
- On the **Hosts** page the host's row shows **Start VNC**. Press it, wait a few seconds, then press **VNC** (or open the **Screen** tab).
- **Stop VNC** on the Screen page turns it off again.
- It is off again after the host reboots, and when the agent restarts or is updated. Press Start VNC again.

## If it does not work
- **No Start VNC button:** the host must be online, the agent 0.9.0 or newer (run the install command again to update it), and a desktop must be running with wayvnc or x11vnc installed.
- **Start VNC does nothing, or the VNC button never appears:** the program's output is in `~/.config/headnode/vnc.log` for the user the agent runs as (`ps -o user= -C headnode-agent`). The agent's own log is `journalctl -u headnode-agent`.
- **A black or empty screen:** on a Pi, check that the desktop is Wayland with labwc (`echo $XDG_SESSION_TYPE`), and that nobody else is already sharing it.
- wayvnc and x11vnc listen on `127.0.0.1` only, so port 5900 cannot be reached from the network.
- **x11vnc** gets a new random password each time Start VNC is pressed, in `~/.config/headnode/x11vnc.passwd` (readable by the agent's user only), and the dashboard logs in with it by itself (D-64). Other users and programs on the machine cannot take over the screen.
- **wayvnc** listens on a private Unix domain socket (`~/.config/headnode/wayvnc.sock`, mode `0600` inside the agent's `0700` config folder) instead of TCP `127.0.0.1:5900`. Its built-in authentication requires TLS/VeNCrypt or RSA-AES that breaks plain RFB in noVNC, so keeping it on a private Unix socket stops other local users on the host from connecting to the desktop while the agent's tunnel relays it to noVNC.
