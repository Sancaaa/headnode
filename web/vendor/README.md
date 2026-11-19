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
