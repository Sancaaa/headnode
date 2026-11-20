# Vendored third-party files

These files are copied here, not loaded from a CDN, so the dashboard works
without internet access and a CDN can never change what runs in the page
(D-06). They are pinned: to update one, change the version below and download
it again.

| File | What | Version | License |
|---|---|---|---|
| `xterm.js` | the terminal emulator the Terminal tab draws with | `@xterm/xterm` 5.5.0 | MIT, see `LICENSE-xterm.txt` |
| `xterm.css` | its stylesheet | `@xterm/xterm` 5.5.0 | MIT |
| `addon-fit.js` | resizes the terminal to fill its box | `@xterm/addon-fit` 0.10.0 | MIT |
| `novnc/core/`, `novnc/vendor/` | the remote screen viewer of the Screen page (D-49): noVNC's own ES modules, unmodified | `@novnc/novnc` 1.5.0 | MPL-2.0, see `novnc/LICENSE.txt` |

## How to download them

Run this from the project root in PowerShell. It only downloads four files from
the npm package contents served by jsDelivr, and does not run anything.

```powershell
$v = "web\vendor"
Invoke-WebRequest https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.js -OutFile "$v\xterm.js"
Invoke-WebRequest https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.css -OutFile "$v\xterm.css"
Invoke-WebRequest https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.js -OutFile "$v\addon-fit.js"
Invoke-WebRequest https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/LICENSE -OutFile "$v\LICENSE-xterm.txt"
```

Then restart the server: the files are compiled into the binary.

Without these files everything else works, and the Terminal tab explains that
the library is missing.

## noVNC (the Screen page)

noVNC is published as plain ES modules, which a browser loads as they are, so it
is used as the package's own `core/` and `vendor/` folders, unmodified: no
bundler, nothing built.

**On the server you do nothing:** `sudo sh start.sh` downloads the pinned release
into `web/vendor/novnc/` before it builds (D-52). The folder is not in git.

**On your own computer**, only to run the server locally with the Screen page, run
this from the project root in PowerShell. It downloads the same release from noVNC's
GitHub and copies those folders (Windows 10 and later have `tar`; no Node.js needed):

```powershell
$t = Join-Path $env:TEMP "novnc"
Remove-Item -Recurse -Force $t -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force $t | Out-Null
Invoke-WebRequest https://github.com/novnc/noVNC/archive/refs/tags/v1.5.0.tar.gz -OutFile "$t\novnc.tgz"
tar -xzf "$t\novnc.tgz" -C $t
Remove-Item -Recurse -Force web\vendor\novnc -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force web\vendor\novnc | Out-Null
Copy-Item -Recurse "$t\noVNC-1.5.0\core" web\vendor\novnc\core
Copy-Item -Recurse "$t\noVNC-1.5.0\vendor" web\vendor\novnc\vendor
Copy-Item "$t\noVNC-1.5.0\LICENSE*" web\vendor\novnc\
Copy-Item "$t\noVNC-1.5.0\AUTHORS" web\vendor\novnc\ -ErrorAction SilentlyContinue
```

The page loads `web/vendor/novnc/core/rfb.js`. Because noVNC's license (MPL-2.0)
applies to the files themselves, they are kept unmodified with their license text
next to them. Then restart the server. Without these files everything else works,
and the Screen page explains that the viewer library is missing.
