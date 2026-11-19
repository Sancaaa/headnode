# UI Design Guide

Target style: **a quiet, warm-gray admin console**, modeled on the layout of the Tailscale admin panel (Machines page). Left sidebar with the org name on top and the user at the bottom, content on a plain background, tables without card boxes, one muted blue accent, and light **and** dark themes.

This is **layout and tone inspiration only**. We don't use Tailscale's logo, name, icons or illustrations. The colors below are our own tokens.

A visual example is in [mockup/hosts.html](mockup/hosts.html). Open it directly in a browser. It follows your OS light/dark setting.

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
│   │ Groups       │  (6 hosts)                                                 │  count pill
│   │ Audit log    │                                                            │
│                  │  HOST              ADDRESSES       VERSION      LAST SEEN  │  table, no card
│                  │  web-01            192.168.10.2    0.1.0        ● Online ⋯ │
│                  │  Office · root     203.0.113.7     Debian 12               │
│                  │  ───────────────────────────────────────────────────────── │
│ ──────────────── │  nas-studio        192.168.10.20   0.1.0        2 days ago │
│ (A) Admin        │  Office            203.0.113.7     FreeBSD 14           ⋯ │
│     admin@local  │                                                            │
└──────────────────┴────────────────────────────────────────────────────────────┘
  sidebar 240px,      main: padding 32px 40px, max width 1280px
  --bg-app
```

- **Sidebar** (240px, `--bg-app`, right border): the org/app name at the top, nav in the middle, the user block (avatar, name, email) at the bottom behind a top border.
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
| **Count pill** | `--bg-active` background, `--text-muted` text, 14px / 500, fully rounded. E.g. "6 hosts". Placed above the table. |
| **Tag badge** | Small rectangle: `--bg-active`, `--text-muted`, 12–14px / 500, `--radius-xs`, 4px padding. E.g. "Office", "root", "Update available". |
| **Status** | 8px dot + text. Online = `--ok` dot + "Online". Offline = no dot, relative time in `--text-muted` ("2 days ago"). |
| **Table** | No surrounding box. Header: uppercase small muted. Rows: 1px `--border` bottom, 8px vertical padding, hover `--bg-hover`. First column has a bold name + muted second line. The whole row links to the detail page. |
| **Usage** (CPU/RAM/disk on the detail page) | Number + thin 4px bar in `--accent`; `--warn-text` at ≥ 80%, `--danger` at ≥ 95%. |
| **Menu / popover** | `--bg`, `--radius`, `--shadow-pop`, 32px items. |
| **Dialog** | Centered, 480px wide, `--shadow-pop`, overlay `rgba(0,0,0,.4)`. Cancel (secondary) left, action right. |
| **Code block** (install command) | `--bg-app` background, `--mono` 13px, `--radius`, copy button on the right. |
| **Empty state** | Centered muted text + one primary button ("No hosts yet. Add your first host."). |

## 6. MVP pages

| Page | Contents | Milestone |
|---|---|---|
| **Hosts** | Count pill, search, table: Host · Addresses · Version · Last seen · ⋯ | M1 |
| **Host detail** | Header (name, status, tags) + tabs: *Overview* (info + usage), *Terminal*, *Files* | M1 / M3 / M4 |
| **Add host** | Dialog: pick a group → install command in a code block + copy | M2 |
| **Transfers** | Upload a file → pick hosts & path → status per host | M4 |
| **Users / Groups** | Same table style + add/edit dialog | M2 |
| **Audit log** | Table: time · user · host · action · detail, with a search filter | M2 |
| **Login** | Centered 360px form on `--bg-app`: app name, username, password, primary button | M2 |

The **Terminal** always uses a dark `#111010` background inside a `--radius` box, regardless of theme, so terminal colors render correctly.

## 7. Interface copy

- Short, plain, sentence case: "Add host", "Copy command", "Send file". No ALL CAPS except table headers.
- Each page has a one-sentence description under the title that says what the page is for.
- Relative time for recent events ("2 minutes ago", "Nov 18"), absolute time in a tooltip.
- Error messages say what happened and what to do: "Host offline. Make sure the headnode-agent service is running on this host."
