# Headnode logo

The logo is a hexagon mesh: one big node in the middle (the **head**) linked to six smaller nodes (the **hosts**). The decision and what was rejected are in [D-55](../decisions.md).

![Headnode mark](headnode-mark.svg)

## 1. What it means

| Part | Meaning |
|------|---------|
| Big center node | The headnode server, the one place that manages everything. |
| Six outer nodes | Hosts running `headnode-agent`. |
| Solid spokes | The control link between the head and each host. |
| Faded hexagon ring | The fleet seen as one group. It is not a real data path: hosts never talk to each other, only to the head (D-01, D-02). |

The idea comes from mesh networking, which suits an admin tool whose job is "make a network of machines easy to manage". It is our own drawing. No logo, shape or asset of another product is used (see [design.md](../design.md) and D-15).

## 2. Files

| File | Use |
|------|-----|
| [headnode-mark.svg](headnode-mark.svg) | The mark alone, in color. Default choice: favicon, app icon, avatar. |
| [headnode-mark-mono.svg](headnode-mark-mono.svg) | The mark in one color (`currentColor`). For print, stamps, or a place where color is not allowed. |
| [headnode-lockup.svg](headnode-lockup.svg) | Mark + the word `headnode`, for light backgrounds. |
| [headnode-lockup-dark.svg](headnode-lockup-dark.svg) | Same, for dark backgrounds. Only the text color changes. |

All files are plain SVG, so they stay sharp at any size and can be edited in a text editor.

`mono` only follows CSS `color` when the SVG is placed **inline** in HTML. Loaded through `<img>`, it stays `#232222`.

The folders `v2/`, `v3/`, `v4/`, the `preview.html` page and the numbered files `1-hub.svg` to `5-h-monogram.svg` in this folder are the earlier design rounds (section 7). They are a record and can be deleted at any time without affecting the logo.

## 3. Colors

| Where | Color |
|-------|-------|
| Mark (both themes) | `#4b70cc` |
| Faded ring | the same color at 30% opacity |
| Text on light | `#232222` |
| Text on dark | `#f7f5f4` |

`#4b70cc` is the UI's `--accent` in the light theme ([design.md](../design.md) §2). In the dark theme the UI changes `--accent` to `#3f5db3`, because that suits a button fill. The logo does **not** follow that change: a logo that changes color between themes stops being recognizable. `#4b70cc` is clearly visible on the dark background `#181717`, so one color is enough.

The ring uses opacity instead of a gray, so it automatically becomes lighter on any background, and the mono version works with any single color.

## 4. Geometry

Use this if the mark ever needs to be redrawn. The drawing is on a 64 x 64 grid with the center at (32, 32).

| Part | Value |
|------|-------|
| Hexagon | pointy-top, radius 24, vertices (32,8) (52.78,20) (52.78,44) (32,56) (11.22,44) (11.22,20) |
| Outer nodes | circles, radius 5, on the six vertices |
| Head node | circle, radius 8, at the center |
| Spokes | center to each vertex, stroke 3, round caps |
| Ring | the hexagon outline, stroke 2.5, round joins, 30% opacity |

The head is 1.6 times the radius of the outer nodes. That ratio is what makes the center read as "the head" even at small sizes, so keep it if you change the sizes.

## 5. The word mark

The text is `headnode`: **lowercase, one word**, weight 600, letter-spacing -0.5 (at font size 34 in the lockup files). Lowercase matches the binary names (`headnode`, `headnode-agent`, `hnctl`, D-13).

The font is `Inter` if installed, otherwise the system font. This is the same rule as the UI (design.md §1 point 5, no external fonts). The result is that the text in the lockup files can look a little different from machine to machine. If the exact letterforms ever matter (print, a store listing), open the lockup in a vector editor and convert the text to outlines.

In HTML, prefer the mark as an image or inline SVG and write the name as normal text next to it. That keeps the text selectable and uses the UI font.

## 6. Size, space and use

- **Clear space:** keep at least the width of the head node (a quarter of the mark's width) free around the mark. In a lockup, the gap between mark and text is already set.
- **Minimum size:** 24 px for the mark and 24 px high for the lockup. Below that the thin ring and spokes blur.
- **Favicon (16 px and 32 px):** the mark works as is. At 16 px the ring disappears and it looks like a six-armed star. That is acceptable for a favicon and for nothing else. If it feels too weak, make a separate favicon with thicker spokes rather than changing the main mark.
- **Backgrounds:** use the color mark on white, on the warm grays of the UI, and on `#181717`. On a busy photo or a color other than these, use the mono version in white or black.

Do not:
- change the accent color, or use a gradient, shadow or outline on the mark;
- stretch it, rotate it, or remove the head node or any spoke (the six spokes are part of the idea);
- write the name in capitals or as two words ("Head Node");
- put it inside another shape or badge. The filled-hexagon badge (round 4, option 4) was designed but not chosen.

## 7. How it was chosen

| Round | Folder | Idea | Result |
|-------|--------|------|--------|
| 1 | `preview.html` | Network hubs, H letters, terminal | None chosen. |
| 2 | `v2/` | Dot grids (a simple, quiet style) | None chosen. |
| 3 | `v3/` | Mesh: triangle, hexagon wheel, H mesh | The hexagon wheel was liked. |
| 4 | `v4/` | Six variants of the hexagon | **Option 5, "Spokes first", chosen.** |

"Spokes first" won over the full wheel because the faded ring puts the focus on the head-to-host links, which is what the product does.

## 8. Where it is used

The color mark is in the dashboard ([D-56](../decisions.md)):

- **Tab icon:** `<link rel="icon" type="image/svg+xml" href="/headnode-mark.svg">` in `web/index.html`.
- **Sidebar and login form:** the mark as an `<img>` (28 px) beside the name `headnode`, written as normal text (section 5), in place of the old "HN" square. Style: `.org-logo` in `web/style.css`.

`web/headnode-mark.svg` is a **copy** of `headnode-mark.svg` in this folder, because the UI files are embedded from `web/` (D-06). **This folder has the original: change it here first, then copy it over `web/headnode-mark.svg`.**

Not used yet: the mono mark and the lockups (nothing in the UI needs them), and a separate favicon with thicker spokes (section 6: only if the 16 px tab icon turns out too weak).
