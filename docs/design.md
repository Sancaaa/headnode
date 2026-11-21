# UI Design Guide

Target style: **a quiet, warm-gray admin console**, modeled on the layout of the Tailscale admin panel (Machines page). Left sidebar with the org name on top and the user at the bottom, content on a plain background, tables without card boxes, one muted blue accent, and light **and** dark themes.

This is **layout and tone inspiration only**. We don't use Tailscale's logo, name, icons or illustrations. The colors below are our own tokens.

A visual example is in [mockup/hosts.html](mockup/hosts.html). Open it directly in a browser. It follows your OS light/dark setting. (It was drawn before the Hosts table changed and still shows a Version column and no Shell button. The list of columns in §6 is the current one.)

## 1. Principles

1. **Content is the page.** No card boxes around the main table. The page background is the canvas, and rows are separated by hairlines.
2. **One accent color** (muted blue) for the primary action, links and focus. Everything else is warm gray. Status colors only for status.
3. **Quiet chrome.** The sidebar is a slightly different gray, not a colored bar. No gradients, and shadows only on popovers and dialogs.
4. **Dense but readable.** 14px base text, rows ~56px with a bold name on top and muted details below.
5. **No CSS framework, no external fonts** (`D-06`). One `style.css` with CSS variables. `Inter` is used if it's installed locally, otherwise the system font.
6. **Light and dark from the start** (`D-15`), driven by `prefers-color-scheme`.

## 2. Tokens (CSS variables)

```css
:root {
  /* surfaces */
  --bg:            #ffffff;  /* main content */
  --bg-app:        #f7f5f4;  /* sidebar, page edges */
  --bg-hover:      #faf9f8;  /* table row hover */
  --bg-active:     #eeebea;  /* active nav item, neutral badges */

  /* borders */
  --border:        #eeebea;  /* hairlines between rows/sections */
  --border-input:  #dad6d5;  /* inputs, secondary buttons */
  --border-hover:  #afacab;

  /* text */
  --text:          #232222;
  --text-muted:    #706e6d;
  --text-disabled: #afacab;
  --text-link:     #3f5db3;

  /* accent & status */
  --accent:        #4b70cc;  /* primary button */
  --accent-hover:  #3f5db3;
  --focus:         #85aaf5;
  --ok:            #1ea672;  /* status dot */
  --ok-text:       #0e6245;
  --warn-text:     #983705;
  --danger:        #b22d30;
  --danger-text:   #940821;

  /* shape */
  --radius:        6px;
  --radius-xs:     2px;
  --shadow-button: 0 1px 1px rgba(0,0,0,.08);
  --shadow-pop:    0 0 0 1px var(--border), 0 15px 35px rgba(0,0,0,.12), 0 5px 15px rgba(0,0,0,.08);

  --font: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
  --mono: SFMono-Regular, Consolas, "Liberation Mono", Menlo, monospace;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg:            #1f1e1e;
    --bg-app:        #181717;
    --bg-hover:      #232222;
    --bg-active:     #2e2d2d;
    --border:        #2e2d2d;
    --border-input:  #444342;
    --border-hover:  #706e6d;
    --text:          #f7f5f4;
    --text-muted:    #afacab;
    --text-disabled: #444342;
    --text-link:     #6c94ec;
    --accent:        #3f5db3;
    --accent-hover:  #4b70cc;
    --focus:         #324994;
    --ok:            #09825d;
    --ok-text:       #1ea672;
    --warn-text:     #d97917;
    --danger:        #d04841;
    --danger-text:   #e46c63;
    --shadow-button: 0 1px 1px rgba(0,0,0,.3);
  }
}
```

Rule: code never uses a hex color directly. Always `var(--…)`, so both themes keep working.

## 3. Typography & spacing

| Element | Style |
|---|---|
| Body | 14px / 1.43, letter-spacing `-0.015em`, antialiased |
| Page title (`h1`) | 30px / 600, tight letter-spacing |
| Page description | 14px, `--text`, max width 640px, directly under the title |
| Section title (`h2`) | 18px / 600 |
| Table header | 12px / 600, UPPERCASE, letter-spacing `0.05em`, `--text-muted` |
| Row primary text | 14px / 600 |
| Row secondary text | 14px / 400, `--text-muted` |
| IPs, versions, paths | `font-variant-numeric: tabular-nums`; `--mono` only for paths and commands |

Spacing scale (multiples of 4px): **4 · 8 · 12 · 16 · 24 · 32 · 40**.

## 4. Layout

```
┌──────────────────┬────────────────────────────────────────────────────────────┐
│ ▣ headnode       │                                                            │
│                  │  Hosts                                       [Add host ▾]  │  h1 + primary button
│ ▤ Hosts          │  Manage the machines connected to headnode.                │  description
│ ⇅ Transfers      │                                                            │
│ ⚿ Admin       ▾  │  [🔍 Search by name, OS, IP…              ] [≡ Filters ▾]  │  search + filter
│   │ Users        │                                                            │
│   │ Groups       │  (6 hosts · 4 online)                                      │  count pill
│   │ Uploads      │                                                            │
│   │ Audit log    │                                                            │
│                  │  HOST              ADDRESSES     USAGE    STATUS           │  table, no card
│                  │  web-01            192.168.10.2  CPU 3%   ● Online [Shell] │
│                  │  Office · root     WAN 203.0.113.7                         │
│                  │  ───────────────────────────────────────────────────────── │
│ ──────────────── │  nas-studio        192.168.10.13 —        2 days ago       │
│ (A) Admin        │  Office            WAN 203.0.113.7                         │
│     admin@local  │                                                            │
└──────────────────┴────────────────────────────────────────────────────────────┘
  sidebar 240px,      main: padding 32px 40px, max width 1280px
  --bg-app
```

- **Sidebar** (240px, `--bg-app`, right border): the org/app name at the top, nav in the middle, only the user block (avatar, name, role) at the bottom behind a top border. The server's version and uptime sit under the app name at the top (the server line, §5), and Log out is in a menu that opens from the user block.
- **Nav items**: icon + label, 32px tall, 6px radius. Active = `--bg-active` background + weight 500. Hover = `--bg-active` background.
- **Nested nav** (for Admin): children indented and connected by a 1px vertical guide line in `--border-input`.
- **Main**: `--bg`, padding 32px 40px (24px on small screens), content max width 1280px.
- Below 1024px the sidebar becomes a top bar with a ☰ menu. Below 768px, less important table columns are hidden (Version first, then Addresses), and their key info moves under the host name.

## 5. Components

| Component | Rules |
|---|---|
| **Primary button** | `--accent` background and border, white text, 36px tall, 12px horizontal padding, `--radius`, `--shadow-button`, weight 500. At most **one** per page. |
| **Secondary button** | `--bg` background, `--border-input` border, `--text` text; hover border `--border-hover`. |
| **Minimal button** | No border or background; hover `--bg-active`. Used for the row `⋯` menu. |
| **Danger action** | Lives inside a menu or dialog, in `--danger-text`. Always confirmed in a dialog that names the object. |
| **Input** | 36px tall, `--border-input`, `--radius`, `--bg` background, placeholder `--text-disabled`. Focus: 2px `--focus` outline. Search inputs have a leading icon. |
| **Count pill** | `--bg-active` background, `--text-muted` text, 14px / 500, fully rounded. E.g. "6 hosts". Placed above the table. On the Hosts page it also says how many are online: "6 hosts · 4 online", counted in the browser from the list the page already has. |
| **Server line** | At the top of the sidebar, under the app name "headnode": one 12px muted line, "0.1.0 · up 3h 12m". The version and start time come from `/api/me`, and the browser counts the uptime (updated every 30 s). Empty when logged out. |
| **User block** | The only thing at the bottom of the sidebar: avatar, name and role, as one button that tints on hover. Clicking it opens a small menu above it with **Log out** (same look as the path bar's folder menu); a click elsewhere or Escape closes it. Log out is not always on screen, so it cannot be hit by accident. |
| **Tag badge** | Small rectangle: `--bg-active`, `--text-muted`, 12–14px / 500, `--radius-xs`, 4px padding. E.g. "Office", "root", "Update available". |
| **Status** | 8px dot + text. Online = `--ok` dot + "Online". Offline = no dot, relative time in `--text-muted` ("2 days ago"). |
| **Table** | No surrounding box. Header: uppercase small muted. Rows: 1px `--border` bottom, 8px vertical padding, hover `--bg-hover`. First column has a bold name + muted second line. The whole row links to the detail page. |
| **Row action** | A small secondary button (28px tall) at the end of a row, shown only when it can work. On the Hosts page: **Files** (folder picture), for any online host, **Shell** (`>_`), for an online host the current user may open a terminal on, and **VNC** (monitor picture), for an online host the user may open a terminal on and that has a screen, whether or not VNC is running (D-65): a Windows host, or one where the agent found a VNC server or can start one (a server with no display shows none). It opens the host's Screen page. The Screen tab follows the same rule. The Screen page's bar has the Start / Stop VNC switch (Start is the highlighted button while there is no screen), labelled **Stop VNC** or **Start VNC** after the host's real state: the page asks the server for the host every 2 seconds while it is open (it is otherwise drawn once), and when VNC is started from it, the screen connects by itself as soon as the host reports it up. While the request runs the button says "Starting…" or "Stopping…" and stays disabled until the heartbeat shows the new state. Windows hosts have no Shell and no Files yet, so those two are hidden for them. It is a real link, and its click must not also trigger the row's own link. The label starts with the `>_` prompt, drawn as monospace text (no icon files). The column header is "Action". |
| **Addresses cell** | Two lines, always both: `LAN: <ips>` in bold (the host's own addresses, comma-separated) and `WAN: <ip>` muted (the address the server saw the agent connect from). A dash stands in for an address that is not known yet. On the Hosts table every cell is centered vertically. |
| **Path bar** (file manager) | Works like the Explorer's address bar, 36px tall, with the input's border. Each folder of the path is a button that goes there, and a `>` after it opens a small list of the folders inside it (a menu, `--shadow-pop`, closes on a click elsewhere or Escape). Clicking the empty part of the bar turns it into a text box (monospace): Enter goes, Escape or clicking away goes back to buttons. **Back** (left of the bar) returns to the previously visited folder, like a browser's Back, not to the parent: after `/home/sanca` then `/home`, it returns to `/home/sanca`. |
| **Toolbar button** | A secondary button with a small picture before its label (Back, Refresh, New folder, Upload). Pictures are plain SVG paths in the 16x16 box, stroked with the text color: no icon files, no third-party icons. |
| **File list tools** (file manager) | Under the path bar: a **filter box** (narrows the folder as you type, only what is drawn) and a **Hidden files** switch (off by default; a muted line says how many hidden items are not shown). Column titles **Name / Size / Modified** are buttons that sort (▲ ▼, folders always stay first). A checkbox on every row and one in the header **select**; while something is ticked a bar shows "N items selected" with **Move to…**, **Copy to…**, **Send to hosts…** (for people who may upload) and **Delete**. There is no Clear button: the header checkbox deselects everything. Anything a filter hides is un-ticked. Read-only sessions have no checkboxes. |
| **Row actions** (file manager) | Pictures with tooltips: **Download** (a folder downloads as a zip), **Copy path**, **Rename**, **Delete**, and **⋯** which opens a menu with **Edit** / **View** (files), **Properties**, **Move to…**, **Copy to…**, **Permissions…** (these three need write access) and **Send to hosts…** (files and folders, for people who may upload: it copies the item to the server and opens the Send dialog with it chosen; the same button in the selection bar sends everything ticked). A folder is sent as a whole: it is unpacked on each host, inside the folder you name, and a host that already has a folder of that name is refused and left untouched. **Rename** turns the name into a text box in the row, with the extension left out of the selection; Enter renames, Escape or clicking away cancels. Move and Copy open a dialog with a small **folder picker** (Up, and the folders inside the current one). A copy takes a free name (`name (1)`). |
| **Editor** (file manager) | **Edit** in a row's ⋯ menu (**View** in a read-only session) opens a wide dialog with the file in a monospace text box (`--mono` 13px, no wrapping, up to 512 KB). **Save** writes it back and closes; Cancel or Escape asks "Close without saving your changes?" when something was typed. A file that is not plain text, or is too big, says so in the dialog. If the file changed on the host since it was opened, the save is refused and says to open it again. |
| **Several files at once** | The Upload dialog of the file manager and the Send files dialog of the Transfers page take one or more files (the file picker's multi-select). They are handled one after another with a "(2 of 5) name" status: each file is its own upload and, in Send files, its own transfer (same hosts, folder and mode). With several files, "Save as" is off and each keeps its own name. If one fails, the others still go (file manager), or sending stops there and only the unsent files stay selected (Send files), so pressing Send again never sends a file twice. |
| **Drag and drop** (file manager) | Dropping files from the computer anywhere on the page uploads them into the folder on show (dashed outline while hovering). Dragging a row (or all ticked rows) onto a folder row, or onto a folder of the path bar, moves them there: the target gets the focus outline. Rows are draggable only when the session can change files. |
| **Icon button** | 28px, no border or background until hovered, muted color (`--danger-text` for destructive ones). Used for actions that repeat on every row (Download, Rename, Delete). It has no visible words, so it always carries a tooltip (`title`) and an `aria-label`. |
| **Usage** (CPU/RAM/disk on the detail page) | Number + thin 4px bar in `--accent`; `--warn-text` at ≥ 80%, `--danger` at ≥ 95%. |
| **Menu / popover** | `--bg`, `--radius`, `--shadow-pop`, 32px items. |
| **Dialog** | Centered, 480px wide, `--shadow-pop`, overlay `rgba(0,0,0,.4)`. Cancel (secondary) left, action right. |
| **Code block** (install command) | `--bg-app` background, `--mono` 13px, `--radius`, copy button on the right. |
| **Empty state** | Centered muted text + one primary button ("No hosts yet. Add your first host."). |

## 6. MVP pages

| Page | Contents | Milestone |
|---|---|---|
| **Hosts** | Count pill, search, table: Host · Addresses (LAN + WAN) · Usage · Status · Action (Shell) | M1 / M3 |
| **Host detail** | Header (name, status, tags) + tabs: *Overview* (info + usage), *Terminal*, *Files* | M1 / M3 / M4 |
| **Add host** | Dialog: pick a group → install command in a code block + copy | M2 |
| **Transfers** | Upload a file → pick hosts & path → status per host | M4 |
| **Uploads** (admin) | Table: uploaded · file (with size) · by · Delete. Delete asks for confirmation and names the file; a file a transfer is still using shows "In use" instead. | M5.5 |
| **Users / Groups** | Same table style + add/edit dialog | M2 |
| **Audit log** | Table: time · user · host · action · detail, with a search filter | M2 |
| **Login** | Centered 360px form on `--bg-app`: app name, username, password, primary button | M2 |

The **Screen** tab (only for hosts whose agent reports a VNC server on 5900, like the **VNC** button on the row, D-50) shows the remote screen of a VNC server on the host's `127.0.0.1:5900`, drawn by noVNC on a `--terminal-bg` box that fills the window under a thin bar: connection status on the left, **Ctrl+Alt+Del** and **Disconnect / Reconnect** on the right. The screen is scaled to fit and the host's resolution is never changed. The VNC password is asked for in a dialog and is never stored. When nothing listens on port 5900 the bar says so and points at `docs/windows.md`.

The **Terminal** always uses a dark `#111010` background inside a `--radius` box, regardless of theme, so terminal colors render correctly. The Terminal and the Screen page each have a **Full screen** button (the browser's own full-screen mode on the box; the terminal or viewer resizes to fit). In full screen, where the browser allows it, the page keeps Esc (and on the Screen page Tab, Alt and the Windows key) for the host instead of leaving full screen: hold Esc to leave.

The **Screen** page's picture can be zoomed and panned (D-65), for a host with several monitors: a small bar over the top of the screen, faint until the mouse is near and inside the box so it is there in full screen, has **Fit**, **−**, the zoom in percent, **+**, **1:1** and **Pan**. Zoomed in, the picture has scrollbars; **Pan** is a hand that drags it, and while it is on nothing is sent to the host.

## 7. Interface copy

- Short, plain, sentence case: "Add host", "Copy command", "Send file". No ALL CAPS except table headers.
- Each page has a one-sentence description under the title that says what the page is for.
- Relative time for recent events ("2 minutes ago", "Nov 18"), absolute time in a tooltip.
- Error messages say what happened and what to do: "Host offline. Make sure the headnode-agent service is running on this host."
