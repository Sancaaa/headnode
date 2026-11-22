'use strict';

// The whole UI in one file, read top to bottom:
//   1. helpers          h(), icons, formatting
//   2. api & dialogs    talking to the server, the pop-up forms
//   3. pages            Hosts, Host detail, Users, Groups, Group detail
//   4. login & sidebar
//   5. the loop         pick the page from the URL, fetch, draw, repeat every 2 s
//
// Admin buttons are hidden from non-admins here only for convenience. The
// server refuses their requests anyway (admin-only routes), so nothing here
// is a security boundary.

// ---------------------------------------------------------------- helpers

// h("div", {class: "x"}, "text", child) builds a DOM element.
// Strings become text nodes, never HTML. Hostnames, kernels and so on come
// from agents, so a host named "<img src=x onerror=...>" must show up as plain
// text instead of running. Never put agent data into innerHTML (D-19).
function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs || {})) {
    if (value != null && value !== false) el.setAttribute(name, value);
  }
  // flat(Infinity): children may be arrays of arrays, like the dt/dd pairs.
  for (const child of children.flat(Infinity)) {
    if (child != null && child !== false) el.append(child);
  }
  return el;
}

// An icon is one SVG path in a 16x16 box. Built with createElementNS because
// SVG elements need their own namespace, and still without innerHTML.
function icon(pathData) {
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  const path = document.createElementNS(ns, 'path');
  path.setAttribute('d', pathData);
  svg.append(path);
  return svg;
}
const ICON_HOSTS = 'M3.5 3h9A1.5 1.5 0 0 1 14 4.5v5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 9.5v-5A1.5 1.5 0 0 1 3.5 3zM5.5 14h5M8 11v3';
const ICON_USERS = 'M8 8a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5zM3 14c.6-2.6 2.6-4 5-4s4.4 1.4 5 4';
// Pictures for the file manager's toolbar and file actions. Each is one SVG path
// in the same 16x16 box as the sidebar icons.
const ICON_BACK = 'M13 8H3M7 4L3 8l4 4';
const ICON_REFRESH = 'M2.5 8a5.5 5.5 0 0 1 9.4-3.9L13.5 5.7M13.5 2.5v3.2h-3.2M13.5 8a5.5 5.5 0 0 1-9.4 3.9L2.5 10.3M2.5 13.5v-3.2h3.2';
const ICON_FOLDER = 'M2 4.5A1.5 1.5 0 0 1 3.5 3h2.6l1.4 1.5h5A1.5 1.5 0 0 1 14 6v5.5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5z';
const ICON_NEW_FOLDER ='M2 4.5A1.5 1.5 0 0 1 3.5 3h2.6l1.4 1.5h5A1.5 1.5 0 0 1 14 6v5.5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5zM8 7v4M6 9h4';
const ICON_UPLOAD = 'M8 11V3M4.5 6.5L8 3l3.5 3.5M3 13h10';
const ICON_DOWNLOAD = 'M8 3v8M4.5 7.5L8 11l3.5-3.5M3 13h10';
const ICON_RENAME = 'M3 13l.7-3L10.5 3.2a1.4 1.4 0 0 1 2 2L5.7 12.3zM9.5 4.2l2.3 2.3';
const ICON_DELETE = 'M3 4.5h10M6.5 4.5V3h3v1.5M4.5 4.5l.6 8.5h5.8l.6-8.5M7 7v4M9 7v4';
const ICON_CHEVRON = 'M6 3l5 5-5 5';
const ICON_COPY_PATH = 'M6 6h7v7.5H6zM3 10V2.5h7';
const ICON_MORE = 'M3 8h1.2M7.4 8h1.2M11.8 8h1.2';
const ICON_TRANSFERS ='M5 2v12M2 5l3-3 3 3M11 14V2M8 11l3 3 3-3';
const ICON_AUDIT ='M3.5 2.5h9v11h-9zM5.5 5.5h5M5.5 8h5M5.5 10.5h3';
const ICON_UPLOADS = 'M4 2h5l3 3v9H4zM9 2v3h3';
const ICON_GROUPS ='M6 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM2 13c.4-2 1.9-3 4-3s3.6 1 4 3M11 7a1.7 1.7 0 1 0 0-3.4M11.5 10c1.6.1 2.5 1 2.8 3';

function button(label, className, onClick) {
  const b = h('button', { class: className, type: 'button' }, label);
  b.addEventListener('click', onClick);
  return b;
}

// A toolbar button with a small picture in front of its label.
function iconButton(label, pathData, onClick) {
  const b = h('button', { class: 'btn', type: 'button' }, icon(pathData), label);
  b.addEventListener('click', onClick);
  return b;
}

// A button that is only a picture, for actions that repeat on every row. The
// words move to the tooltip and to the accessible name, so a screen reader
// still says "Delete".
function iconOnly(label, pathData, onClick, extraClass) {
  const b = h('button', {
    class: 'icon-button' + (extraClass ? ' ' + extraClass : ''), type: 'button', title: label, 'aria-label': label,
  }, icon(pathData));
  b.addEventListener('click', onClick);
  return b;
}

function formatBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return (i === 0 || n >= 10 ? Math.round(n) : n.toFixed(1)) + ' ' + units[i];
}

function percent(used, total) {
  return total > 0 ? Math.round((100 * used) / total) : 0;
}

function formatDuration(sec) {
  const days = Math.floor(sec / 86400);
  const hours = Math.floor((sec % 86400) / 3600);
  const minutes = Math.floor((sec % 3600) / 60);
  if (days > 0) return days + 'd ' + hours + 'h';
  if (hours > 0) return hours + 'h ' + minutes + 'm';
  return minutes + 'm';
}

// "3 minutes ago" for recent times, "Nov 18" for older ones (design.md §7).
function relativeTime(iso) {
  const then = new Date(iso);
  const sec = Math.round((Date.now() - then) / 1000);
  const ago = (n, unit) => n + ' ' + unit + (n === 1 ? '' : 's') + ' ago';
  if (sec < 60) return 'just now';
  if (sec < 3600) return ago(Math.floor(sec / 60), 'minute');
  if (sec < 86400) return ago(Math.floor(sec / 3600), 'hour');
  if (sec < 7 * 86400) return ago(Math.floor(sec / 86400), 'day');
  return then.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// The status shown next to a host: a green dot while online, otherwise when
// it was last seen. The exact time is in the tooltip.
function statusText(host) {
  if (host.online) return h('span', {}, h('span', { class: 'dot' }), 'Online');
  if (!host.last_seen_at) return h('span', { class: 'sub' }, 'Never connected');
  return h('span', { class: 'sub', title: new Date(host.last_seen_at).toLocaleString() },
    relativeTime(host.last_seen_at));
}

// design.md §5, "Usage": normal below 80 %, warning from 80 %, danger from 95 %.
function usageClass(pct) {
  if (pct >= 95) return 'danger';
  if (pct >= 80) return 'warn';
  return '';
}

function hostLink(id) {
  return '#/hosts/' + id;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function count(n, singular, plural) {
  return n + ' ' + (n === 1 ? singular : plural);
}

// Title, optional description, and the page's buttons on the right.
function pageHead(title, description, ...actions) {
  return h('header', { class: 'page-head' },
    h('div', {}, h('h1', {}, title), description ? h('p', { class: 'desc' }, description) : null),
    h('div', { class: 'page-actions' }, actions));
}

function field(label, control) {
  return h('label', { class: 'field' }, h('span', {}, label), control);
}

const isAdmin = () => Boolean(me && me.is_admin);

// ---------------------------------------------------------- api & dialogs

// One place for talking to the server. It never throws on an HTTP error: the
// caller looks at ok / status / data. A 401 means the session ended, so go
// back to the login form from here, whoever made the request.
async function api(method, path, body) {
  const options = { method };
  if (body !== undefined) {
    options.headers = { 'Content-Type': 'application/json' };
    options.body = JSON.stringify(body);
  }
  let resp;
  try {
    resp = await fetch(path, options);
  } catch (err) {
    return { ok: false, status: 0, data: { error: 'Cannot reach the server.' } };
  }
  let data = null;
  if (resp.status !== 204) {
    try { data = await resp.json(); } catch (err) { /* not JSON: data stays null */ }
  }
  if (resp.status === 401) {
    me = null;
    routeId++;
    lastKey = null;
    refresh();
  }
  return { ok: resp.ok, status: resp.status, data };
}

// Uploads one file to the server (protocol.md §1) and reports progress as a
// fraction from 0 to 1. fetch() cannot report how much of an upload has been
// sent, XMLHttpRequest can. It resolves with the same shape api() does.
function uploadFile(file, onProgress) {
  return new Promise((resolve) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/files');
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded / event.total);
    };
    xhr.onload = () => {
      let data = null;
      try { data = JSON.parse(xhr.responseText); } catch (err) { /* not JSON: data stays null */ }
      if (xhr.status === 401) { // the session ended, like in api()
        me = null;
        routeId++;
        lastKey = null;
        refresh();
      }
      resolve({ ok: xhr.status >= 200 && xhr.status < 300, status: xhr.status, data });
    };
    xhr.onerror = () => resolve({ ok: false, status: 0, data: { error: 'Cannot reach the server.' } });
    const form = new FormData();
    form.append('file', file);
    xhr.send(form);
  });
}

function errorText(result) {
  return (result.data && result.data.error) || 'Request failed (HTTP ' + result.status + ').';
}

// Fetches a list for a dialog. If that fails it tells the user and returns null.
async function loadList(path) {
  const result = await api('GET', path);
  if (!result.ok) {
    messageDialog('Could not load the list', errorText(result));
    return null;
  }
  return result.data;
}

// A pop-up form on the browser's own <dialog> element: it traps focus, closes
// on Esc and dims the page without any code from us. It lives outside #view,
// so the 2-second redraw of the page never wipes what is being typed.
//
// onSubmit returns one of:
//   null / undefined  success, close the dialog
//   a string          an error to show, the dialog stays open
//   an array of nodes success with a result to show (the enroll command)
// Two optional extras for the editor: wide makes the dialog big, and canClose
// is asked before Cancel or Escape closes it (return false to keep it open, for
// "you have unsaved changes").
// onClose runs when the dialog has closed, however it closed (the password
// prompt of the screen viewer uses it to learn that it was cancelled).
function openDialog({ title, fields, submitLabel, cancelLabel = 'Cancel', danger = false, wide = false, canClose = null, onClose = null, onSubmit }) {
  const heading = h('h2', { class: 'dialog-title' }, title);
  const error = h('div', { class: 'form-error', role: 'alert' });
  const submit = h('button', { class: danger ? 'btn btn-danger' : 'btn btn-primary', type: 'submit' }, submitLabel);
  const cancel = cancelLabel ? button(cancelLabel, 'btn', () => { if (!canClose || canClose()) dlg.close(); }) : h('span');
  const form = h('form', {}, heading, fields, error, h('div', { class: 'dialog-actions' }, cancel, submit));
  const dlg = h('dialog', { class: wide ? 'dialog dialog-wide' : 'dialog' }, form);
  dlg.addEventListener('cancel', (event) => { // Escape
    if (canClose && !canClose()) event.preventDefault();
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    submit.disabled = true;
    error.textContent = '';
    const result = await onSubmit();
    if (Array.isArray(result)) {
      const done = button('Done', 'btn btn-primary', () => dlg.close());
      // replaceChildren does not unpack arrays, unlike h(), so spread the result.
      form.replaceChildren(heading, ...result, h('div', { class: 'dialog-actions end' }, done));
      return;
    }
    if (result) {
      error.textContent = result;
      submit.disabled = false;
      return;
    }
    dlg.close();
  });
  // However it was closed (Done, Cancel, Esc), show the latest data.
  dlg.addEventListener('close', () => {
    dlg.remove();
    if (onClose) onClose();
    // Show the latest data. A static page (the terminal, the file manager) keeps
    // its own state, and rebuilding it would throw away its session, so it
    // refreshes itself after a change instead.
    if (!currentRoute().static) {
      lastKey = null;
      refresh();
    }
  });

  document.body.append(dlg);
  dlg.showModal();
}

function messageDialog(title, text) {
  openDialog({ title, fields: [h('p', {}, text)], submitLabel: 'OK', cancelLabel: null, onSubmit: async () => null });
}

// "Delete X? ..." Dangerous actions always go through here and name the object
// (design.md §5).
function confirmDialog({ title, message, confirmLabel, onConfirm }) {
  openDialog({
    title,
    fields: [h('p', {}, message)],
    submitLabel: confirmLabel,
    danger: true,
    onSubmit: async () => {
      const result = await onConfirm();
      return result.ok ? null : errorText(result);
    },
  });
}

// ------------------------------------------------------------- Hosts page

function renderHosts(hosts) {
  const addHost = isAdmin() ? button('Add host', 'btn btn-primary', addHostDialog) : null;
  // Counted here from the list the page already has, so there is no extra request.
  const online = hosts.filter((host) => host.online).length;
  const outdated = hosts.filter((host) => host.update_available).length; // M10, D-66
  const page = [
    pageHead('Hosts', 'Manage the machines connected to headnode.', addHost),
    h('div', { class: 'pill' }, count(hosts.length, 'host', 'hosts') + ' · ' + online + ' online' +
      (outdated > 0 ? ' · ' + outdated + ' outdated' : '')),
  ];

  if (hosts.length === 0) {
    page.push(h('div', { class: 'empty' }, isAdmin()
      ? 'No hosts yet. Use "Add host" to get the command that connects one.'
      : 'No hosts to show. Ask an administrator to add you to a group.'));
    return page;
  }

  page.push(hostsTable(hosts));
  return page;
}

// The table of hosts, used by the Hosts page and by a group's page.
function hostsTable(hosts) {
  return h('table', { class: 'hosts-table' },
    h('thead', {}, h('tr', {},
      h('th', { class: 'col-host' }, 'Host'),
      h('th', { class: 'col-addr' }, 'Addresses'),
      h('th', { class: 'col-usage' }, 'Usage'),
      h('th', {}, 'Status'),
      h('th', { class: 'col-act' }, 'Action'))),
    h('tbody', {}, hosts.map(hostRow)));
}

// "Update available" on a host whose agent is older than the newest one this server has
// (M10, D-66). It only says so: clicking it shows how to update that host by hand.
function updateBadge(host) {
  if (!host.update_available) return null;
  const badge = h('button', {
    class: 'tag tag-update', type: 'button',
    title: 'Agent ' + host.version + ', newest is ' + host.latest_version,
  }, 'Update available');
  badge.addEventListener('click', (event) => {
    event.stopPropagation(); // not also the row's own link
    updateDialog(host);
  });
  return h('div', {}, badge);
}

// The first agent that can update itself (`headnode update`, the tray's Update
// headnode…, D-67). A host on an older one is updated once with the install command.
const SELF_UPDATE_SINCE = '0.11.0';

// Whether a version like "0.9.2" is at least another, comparing number by number.
function versionAtLeast(version, minimum) {
  const a = String(version).split('.').map(Number);
  const b = minimum.split('.').map(Number);
  if (a.some(Number.isNaN)) return false;
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i] || 0;
    const y = b[i] || 0;
    if (x !== y) return x > y;
  }
  return true;
}

// How to update one host, for its operating system and the agent it runs. An agent from
// 0.11.0 on updates itself: it fetches the newest program from this server, checks that
// it runs, and swaps it in. An older one is updated once by the install command, which
// does the same; the address in it is the one this page was loaded from, which is the
// server the host belongs to.
function updateDialog(host) {
  const server = location.origin;
  const selfUpdate = versionAtLeast(host.version, SELF_UPDATE_SINCE);
  const fields = [
    h('p', {}, host.name + ' runs headnode-agent ' + host.version + '. This server has ' + host.latest_version + '. ' +
      'The update keeps the host\'s identity: it replaces the program and restarts the agent.'),
  ];
  if (isWindows(host)) {
    if (selfUpdate) {
      fields.push(
        h('p', {}, 'On the Windows machine, click the headnode icon in the tray and choose Update headnode…, then approve ' +
          'the prompt that asks for an administrator. Nothing else opens: the icon closes and comes back when it is done. ' +
          'From afar: the Screen tab, then the tray icon, then the same menu item.'),
        h('p', { class: 'sub' }, 'The agent restarts during the update, so a screen opened through it closes: Reconnect after a few seconds. ' +
          'If the update fails, the tray icon says why.'));
    } else {
      fields.push(
        h('p', {}, 'This agent is too old to update itself. Once, in PowerShell opened as administrator on the machine (or through the Screen tab):'),
        commandBlock('& ([scriptblock]::Create((irm ' + server + '/install.ps1)))'),
        h('p', { class: 'sub' }, 'After that, Update headnode… in the tray icon does it.'));
    }
  } else if (selfUpdate) {
    fields.push(
      h('p', {}, 'On the host, in its Shell tab here (or any terminal on it), run:'),
      commandBlock('sudo headnode update'),
      h('p', { class: 'sub' }, 'It needs sudo, so it asks for the password. The agent restarts at the end, which ends the shell ' +
        'the command was run from: open the Shell tab again after a few seconds.'));
  } else {
    fields.push(
      h('p', {}, 'This agent is too old to update itself. Once, on the host, in its Shell tab here (or any terminal on it), run:'),
      commandBlock('curl -fsSL ' + server + '/install.sh | sudo sh'),
      h('p', { class: 'sub' }, 'After that, sudo headnode update does it. The command needs sudo, so it asks for the password, and ' +
        'the agent restarts at the end, which ends the shell: open the Shell tab again after a few seconds.'));
  }
  fields.push(h('p', { class: 'sub' }, 'The badge goes away after the host reconnects with the new version.'));
  openDialog({
    title: 'Update ' + host.name,
    fields,
    submitLabel: 'Done',
    cancelLabel: null,
    onSubmit: async () => null,
  });
}

function hostRow(host) {
  const info = host.info || {};
  const ips = info.lan_ips || [];

  const row = h('tr', {},
    h('td', { class: 'col-host' },
      h('a', { class: 'name', href: hostLink(host.id) }, host.name),
      h('div', { class: 'sub' }, [info.platform || host.os, host.group_name].filter(Boolean).join(' · ')),
      // Only admins can see a host that is in no group, so this is a hint for them.
      host.group_id == null ? h('div', {}, h('span', { class: 'tag' }, 'No group')) : null,
      updateBadge(host)),
    h('td', { class: 'col-addr num' },
      // LAN: the host's own addresses (bold, they are what identifies it on its
      // network). WAN: the address the server saw the agent connect from, which
      // is how the host looks from outside. Both lines are always shown, with a
      // dash when the address is not known yet.
      h('div', { class: ips.length > 0 ? 'lan' : 'sub' }, 'LAN: ' + (ips.length > 0 ? ips.join(', ') : '—')),
      h('div', { class: 'sub' }, 'WAN: ' + (host.wan_addr || '—'))),
    h('td', { class: 'col-usage num' }, usageSummary(host)),
    h('td', {}, statusText(host)),
    h('td', { class: 'col-act' }, filesButton(host), shellButton(host), vncButton(host)));

  // The whole row is clickable. The name is a real link too, so the page
  // still works from the keyboard.
  row.addEventListener('click', () => { location.hash = hostLink(host.id); });
  return row;
}

// Where the terminal for a host lives. The Terminal tab on the host page and the
// Shell button on the Hosts page both use it, so they always open the same page.
function terminalHref(hostId) {
  return hostLink(hostId) + '/terminal';
}

// The Shell button on a host's row. It only exists when it can work: the host
// is online and the server says this user may open a shell on it (can_shell,
// computed from the same role table as the server's check, so the button is
// never offered to someone the server would refuse).
// Windows hosts have no shell and no file manager yet (D-44, D-47): they are
// managed with a VNC server through a tunnel. Their Shell and Files buttons and
// tabs stay hidden until those work there, rather than open a page that can
// only fail.
const isWindows = (host) => host.os === 'windows';

function shellButton(host) {
  if (!host.online || !host.can_shell || isWindows(host)) return null;
  // A real link styled as a button, so it works from the keyboard and opens in
  // a new tab with a middle click, like the other links.
  // ">_" is a shell prompt, drawn as plain text in the monospace font, so there
  // is no icon file and nothing to load. It is decoration: screen readers just
  // read "Shell".
  const link = h('a', { class: 'btn btn-small', href: terminalHref(host.id) },
    h('span', { class: 'prompt', 'aria-hidden': 'true' }, '>_'),
    'Shell');
  // The whole row opens the host page when clicked. Do not let this click also
  // reach the row, or the row would send the browser to the host page instead.
  link.addEventListener('click', (event) => event.stopPropagation());
  return link;
}

// The Files button on a host's row. Every role in a group may browse files
// (files_read), and a host is only listed to people who have a role in its
// group or are admins, so no permission flag is needed: the button only needs
// the host to be online, because a files session needs the agent. What the
// person may then do is decided by the session itself (read-only or not).
function filesButton(host) {
  if (!host.online || isWindows(host)) return null;
  const link = h('a', { class: 'btn btn-small', href: filesHref(host.id) }, icon(ICON_FOLDER), 'Files');
  link.addEventListener('click', (event) => event.stopPropagation()); // not also the row's own link
  return link;
}

// Where the remote screen of a host lives (D-49): a VNC server on the host,
// reached through a tunnel, drawn by noVNC.
function screenHref(hostId) {
  return hostLink(hostId) + '/screen';
}

// Whether the host has a VNC server to show (D-50): the agent looks at
// 127.0.0.1:5900 and says so in its heartbeat. An offline host has no heartbeat,
// and an agent older than 0.8.0 does not send the flag, so both read as "no".
function hasVnc(host) {
  return Boolean(host.online && host.heartbeat && host.heartbeat.vnc);
}

// Whether this host has a screen to show at all (D-65): a Windows host always has one,
// and any other host has one if the agent found a VNC server running, or can start
// one, which it can only when a desktop is running (wayvnc or x11vnc is installed for
// it). A headless server (no display) is neither, so it gets no Screen tab and no VNC
// button. A tunnel needs the same role as a shell, so a person without it sees none either.
function hasScreen(host) {
  return Boolean(host.can_shell && (isWindows(host) || hasVnc(host) || canSwitchVnc(host)));
}

// The VNC button on a host's row. The host has to be online and have a screen. It is
// offered whether or not VNC is running (D-65): the Screen page it opens shows the
// screen, or, when VNC is switched off, a Start VNC button, or what is missing.
function vncButton(host) {
  if (!host.online || !hasScreen(host)) return null;
  const link = h('a', { class: 'btn btn-small', href: screenHref(host.id) }, icon(ICON_HOSTS), 'VNC');
  link.addEventListener('click', (event) => event.stopPropagation()); // not also the row's own link
  return link;
}

// Whether the agent can start and stop this host's VNC server (D-51, D-54): a
// TightVNC service on Windows (install.ps1 sets it up), or wayvnc / x11vnc on a
// Linux desktop (install.sh makes sure it is installed).
function canSwitchVnc(host) {
  return Boolean(host.online && host.can_shell && host.heartbeat && host.heartbeat.vnc_control);
}

// "CPU 3% / RAM 7% / Disk 5%", with a warning color per value. Offline hosts
// have no heartbeat, so they show a dash instead of old numbers.
function usageSummary(host) {
  const hb = host.heartbeat;
  if (!hb) return h('span', { class: 'sub' }, '—');
  const line = (label, pct) => h('div', { class: usageClass(pct) }, label + ' ' + pct + '%');
  return [
    line('CPU', Math.round(hb.cpu_percent)),
    line('RAM', percent(hb.mem_used, hb.mem_total)),
    line('Disk', percent(hb.disk_used, hb.disk_total)),
  ];
}

// "Add host" on the Hosts page: choose the group the new host joins, or no
// group. "No group" is the first and default choice: a host in the wrong group
// would be visible to people who should not see it, while an ungrouped one is
// only visible to administrators, who can move it afterwards.
async function addHostDialog() {
  const groups = await loadList('/api/groups');
  if (!groups) return;

  const select = h('select', { class: 'input', name: 'group' },
    h('option', { value: '' }, 'No group (administrators only)'),
    groups.map((g) => h('option', { value: g.id }, g.name)));
  openDialog({
    title: 'Add host',
    fields: [
      h('p', { class: 'sub' }, 'Pick the group the new host joins. You can move it to another group later.'),
      field('Group', select),
    ],
    submitLabel: 'Create enroll token',
    onSubmit: async () => {
      const result = await api('POST', '/api/enroll-tokens', {
        group_id: select.value === '' ? null : Number(select.value),
      });
      return result.ok ? enrollResult(result.data) : errorText(result);
    },
  });
}

// "Add host" on a group's page: the host joins that group, so there is nothing
// to choose. The token is created at once and the command is shown.
async function addHostToGroup(group) {
  const result = await api('POST', '/api/enroll-tokens', { group_id: group.id });
  if (!result.ok) {
    messageDialog('Could not create the enroll token', errorText(result));
    return;
  }
  openDialog({
    title: 'Add host to ' + group.name,
    fields: enrollResult(result.data),
    submitLabel: 'Done',
    cancelLabel: null,
    onSubmit: async () => null,
  });
}

// What to show once the server has made a token: the command to run on the host.
function enrollResult(data) {
  const where = data.group_name
    ? 'The host joins the group "' + data.group_name + '".'
    : 'The host will have no group, so only administrators can see it until you move it to one.';
  return [
    h('p', {}, 'Run one command on the host, in a terminal on that machine. It downloads the agent from this server, ' +
      'connects the host and keeps it running (as a service, started at boot). ' + where),
    h('h3', { class: 'command-title' }, 'Linux'),
    commandBlock(data.install_command),
    h('p', { class: 'sub' }, 'With systemd (x86-64 or ARM64). The agent runs as the user who types this (with sudo: ' +
      'that user, not root), so a terminal opened from here is that user\'s shell. Add --user NAME at the end ' +
      'to pick another account; logged in as root, the agent runs as root.'),
    // Older servers do not send this one: the Linux command is then all there is.
    // The list is spread, not nested: the dialog puts these in with replaceChildren,
    // which does not flatten arrays (see openDialog).
    ...(data.install_command_windows ? [
      h('h3', { class: 'command-title' }, 'Windows'),
      commandBlock(data.install_command_windows),
      h('p', { class: 'sub' }, 'In PowerShell opened with "Run as administrator" (64-bit Windows). ' +
        'Windows hosts show their CPU, memory and disk; their screen is reached with VNC (docs/windows.md).'),
    ] : []),
    h('p', { class: 'sub' }, 'The token is shown only now, is valid for 24 hours and works for several hosts.'),
  ];
}

// A command in a code box, with its own Copy button.
function commandBlock(command) {
  const code = h('div', { class: 'code-block' }, command);
  const copy = button('Copy command', 'btn', async () => {
    try {
      await navigator.clipboard.writeText(command);
      copy.textContent = 'Copied';
    } catch (err) {
      // The clipboard API only works on https:// and localhost. Elsewhere,
      // select the text so Ctrl+C works.
      const range = document.createRange();
      range.selectNodeContents(code);
      getSelection().removeAllRanges();
      getSelection().addRange(range);
      copy.textContent = 'Press Ctrl+C';
    }
  });
  return h('div', {}, code, copy);
}

// ------------------------------------------------------- Host detail page

function renderHost(host) {
  document.title = host.name + ' · headnode';
  const info = host.info || {};

  const actions = isAdmin() ? [
    button('Edit', 'btn', () => editHostDialog(host)),
    button('Delete', 'btn danger-text', () => deleteHostDialog(host)),
  ] : [];

  const page = [
    h('a', { class: 'back', href: '#/' }, '← Hosts'),
    pageHead(host.name, null, actions),
    h('div', { class: 'status' }, statusText(host)),
    hostTabs(host, 'overview'),
  ];

  if (host.online && host.heartbeat) {
    page.push(h('h2', {}, 'Usage'), usageBlock(host.heartbeat));
  } else if (!host.online) {
    page.push(h('div', { class: 'notice' },
      'Host offline. Make sure the headnode-agent service is running on this host.'));
  }

  const facts = [
    ['Group', host.group_name || 'No group (visible to administrators only)'],
    ['Hostname', host.hostname],
    ['Platform', info.platform],
    ['Kernel', info.kernel],
    ['Architecture', host.os + '/' + host.arch],
    ['CPU', info.cpu_model ? info.cpu_model + ' · ' + info.cpu_cores + ' cores' : ''],
    ['Memory', info.mem_total ? formatBytes(info.mem_total) : ''],
    ['Addresses', (info.lan_ips || []).join(', ')],
    ['Agent version', host.version],
    ['Agent ID', h('span', { class: 'mono' }, host.id)],
    ['Enrolled', new Date(host.created_at).toLocaleString()],
    ['Last seen', host.last_seen_at ? new Date(host.last_seen_at).toLocaleString() : 'Never'],
  ];
  page.push(h('h2', {}, 'Details'), h('dl', { class: 'facts' },
    facts.map(([label, value]) => [
      h('dt', {}, label),
      h('dd', {}, value || '—'),
    ])));
  return page;
}

function usageBlock(hb) {
  const rows = [
    ['CPU', Math.round(hb.cpu_percent), Math.round(hb.cpu_percent) + '%'],
    ['Memory', percent(hb.mem_used, hb.mem_total),
      formatBytes(hb.mem_used) + ' of ' + formatBytes(hb.mem_total)],
    ['Disk /', percent(hb.disk_used, hb.disk_total),
      formatBytes(hb.disk_used) + ' of ' + formatBytes(hb.disk_total)],
  ];
  const block = h('div', { class: 'usage' }, rows.map(([label, pct, text]) => {
    const fill = h('div', { class: 'bar-fill' });
    // Set through the DOM, not a style="" attribute: the Content-Security-Policy
    // blocks inline style attributes but allows this.
    fill.style.width = Math.min(pct, 100) + '%';
    return h('div', { class: 'usage-row ' + usageClass(pct) },
      h('div', { class: 'usage-label' }, label),
      h('div', { class: 'usage-body num' }, text, h('div', { class: 'bar' }, fill)));
  }));
  block.append(
    h('div', { class: 'usage-row' },
      h('div', { class: 'usage-label' }, 'Uptime'),
      h('div', { class: 'usage-body num' }, formatDuration(hb.uptime_sec))));
  return block;
}

async function editHostDialog(host) {
  const groups = await loadList('/api/groups');
  if (!groups) return;

  const name = h('input', { class: 'input', name: 'name', value: host.name, required: '', maxlength: '255' });
  const group = h('select', { class: 'input', name: 'group' },
    h('option', { value: '' }, 'No group (administrators only)'),
    groups.map((g) => h('option', { value: g.id, selected: g.id === host.group_id ? '' : null }, g.name)));

  openDialog({
    title: 'Edit host',
    fields: [field('Name', name), field('Group', group)],
    submitLabel: 'Save',
    onSubmit: async () => {
      const result = await api('PATCH', '/api/hosts/' + host.id, {
        name: name.value,
        group_id: group.value === '' ? null : Number(group.value),
      });
      return result.ok ? null : errorText(result);
    },
  });
}

function deleteHostDialog(host) {
  confirmDialog({
    title: 'Delete host',
    message: 'Delete "' + host.name + '"? Its agent is disconnected and its credentials stop working. ' +
      'To manage the machine again you must enroll it again. This cannot be undone.',
    confirmLabel: 'Delete host',
    onConfirm: async () => {
      const result = await api('DELETE', '/api/hosts/' + host.id);
      if (result.ok) location.hash = '#/'; // the page it was on no longer exists
      return result;
    },
  });
}

// ------------------------------------------------------------ Terminal page

// Unlike the other pages, the terminal and the file manager are not redrawn
// every 2 seconds: that would destroy them. Their routes are "static" (see
// refresh), and leaving the page disposes of what they hold here.
let pageSession = null; // { dispose() } of the terminal or file manager on screen

function disposePage() {
  if (pageSession) {
    pageSession.dispose();
    pageSession = null;
  }
}

// The browser's end of a session: a WebSocket to the server, which pairs it
// with the agent's end (protocol.md §4). The browser sends its login cookie
// and an Origin header by itself, and the server checks both.
function openSocket(sessionId) {
  const scheme = location.protocol === 'https:' ? 'wss://' : 'ws://';
  return new WebSocket(scheme + location.host + '/ui/sessions/' + sessionId);
}

// Overview | Terminal, on both pages of a host (design.md §6).
function hostTabs(host, active) {
  return h('nav', { class: 'tabs' },
    h('a', { href: hostLink(host.id), class: active === 'overview' ? 'active' : null }, 'Overview'),
    isWindows(host) ? null : h('a', { href: terminalHref(host.id), class: active === 'terminal' ? 'active' : null }, 'Terminal'),
    isWindows(host) ? null : h('a', { href: filesHref(host.id), class: active === 'files' ? 'active' : null }, 'Files'),
    // Where there is a screen (D-65): the Screen page is where VNC is started, so it
    // cannot wait for VNC to be running; but a server with no display has none. The tab of
    // the page you are on stays, so it does not vanish under you.
    hasScreen(host) || active === 'screen'
      ? h('a', { href: screenHref(host.id), class: active === 'screen' ? 'active' : null }, 'Screen')
      : null,
    // Only where the agent found a Docker Engine (D-70); the tab of the page you are on stays.
    hasDocker(host) || active === 'docker'
      ? h('a', { href: dockerHref(host.id), class: active === 'docker' ? 'active' : null }, 'Docker')
      : null);
}

// What the Terminal and Files pages say when someone opens them for a Windows
// host anyway (an old link, or the address typed by hand).
function windowsNotice(host) {
  return h('div', { class: 'notice' },
    host.name + ' is a Windows host. It has no terminal or file manager yet: use the VNC button on the Hosts page ' +
    '(it appears when a VNC server runs on the host), or hnctl forward 5900:' + host.name + ':127.0.0.1:5900 with a VNC viewer. ' +
    'See docs/windows.md.');
}

function renderTerminalPage(host) {
  document.title = host.name + ' · headnode';
  const page = [
    h('a', { class: 'back', href: '#/' }, '← Hosts'),
    pageHead(host.name, null),
    h('div', { class: 'status' }, statusText(host)),
    hostTabs(host, 'terminal'),
  ];
  if (isWindows(host)) {
    page.push(windowsNotice(host));
    return page;
  }
  if (!host.online) {
    page.push(h('div', { class: 'notice' },
      'Host offline. Make sure the headnode-agent service is running on this host, then reload this page.'));
    return page;
  }

  const box = h('div', { class: 'terminal' });
  const status = h('span', { class: 'sub' });
  const reconnect = h('button', { class: 'btn', type: 'button', hidden: '' }, 'Reconnect');
  // Escape is kept for the terminal in full screen (vim needs it), see fullscreenButton.
  page.push(box, h('div', { class: 'terminal-bar' }, status, reconnect, fullscreenButton(box, ['Escape'])));

  // xterm measures its box to decide how many columns fit, so the box has to be
  // on the page first. This runs right after refresh() has inserted the nodes.
  queueMicrotask(() => mountTerminal(host, box, status, reconnect));
  return page;
}

// A button that shows the terminal or the remote screen in the browser's own full-screen
// mode: `target` is the box that fills the screen, and the terminal or viewer inside it
// resizes itself (both already follow their box's size). Esc leaves full screen, which
// would be a problem for vim and for a remote desktop, so where the browser allows it
// (Chrome and Edge; hold Esc to leave) the keys in lockKeys are kept for the page
// while the box is full screen. Browsers without full screen get no button.
function fullscreenButton(target, lockKeys) {
  if (!document.fullscreenEnabled) return null;
  const b = button('Full screen', 'btn btn-small fullscreen', async () => {
    if (document.fullscreenElement) {
      await document.exitFullscreen();
      return;
    }
    try {
      await target.requestFullscreen();
    } catch (err) {
      return; // refused: nothing to do
    }
    try {
      await navigator.keyboard.lock(lockKeys);
    } catch (err) { /* no keyboard lock in this browser: Esc leaves full screen as usual */ }
    // The click moved focus to the button: put it back where typing goes.
    const typing = target.querySelector('textarea, canvas');
    if (typing) typing.focus();
  });
  return b;
}

// Given back to the browser as soon as nothing is full screen any more.
document.addEventListener('fullscreenchange', () => {
  if (!document.fullscreenElement && navigator.keyboard && navigator.keyboard.unlock) navigator.keyboard.unlock();
});

// Text from the server that will be written into the terminal: control
// characters are removed, so it cannot carry escape sequences that move the
// cursor or redraw the screen (a close reason comes from the agent).
function plain(text) {
  return String(text || '').replace(/[\u0000-\u001f\u007f]/g, '');
}

function mountTerminal(host, box, status, reconnect) {
  if (!window.Terminal || !window.FitAddon) {
    box.replaceChildren(h('div', { class: 'notice' },
      'The terminal library is missing. Download xterm.js into web/vendor (see web/vendor/README.md) and restart the server.'));
    return;
  }

  const style = getComputedStyle(document.documentElement);
  const term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: style.getPropertyValue('--mono'),
    scrollback: 5000,
    theme: { background: style.getPropertyValue('--terminal-bg').trim() },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(box);
  fit.fit();

  const encoder = new TextEncoder();
  let ws = null;
  let disposed = false;
  const live = () => ws !== null && ws.readyState === WebSocket.OPEN;

  // Keystrokes go to the host as binary frames, resizes as a text frame
  // (protocol.md §4).
  term.onData((data) => { if (live()) ws.send(encoder.encode(data)); });
  // Some input is raw bytes, not text, for example mouse reports in vim.
  term.onBinary((data) => { if (live()) ws.send(Uint8Array.from(data, (c) => c.charCodeAt(0))); });
  term.onResize(({ cols, rows }) => { if (live()) ws.send(JSON.stringify({ type: 'resize', cols, rows })); });
  // Fit the terminal to its box whenever the box changes size (the window was resized).
  const observer = new ResizeObserver(() => fit.fit());
  observer.observe(box);

  async function connect() {
    reconnect.hidden = true;
    status.textContent = 'Connecting…';
    const result = await api('POST', '/api/hosts/' + host.id + '/sessions',
      { kind: 'shell', params: { cols: term.cols, rows: term.rows } });
    if (disposed) return;
    if (!result.ok) {
      term.write('\r\n' + plain(errorText(result)) + '\r\n');
      status.textContent = 'Not connected';
      reconnect.hidden = false;
      return;
    }

    ws = openSocket(result.data.id);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => {
      status.textContent = 'Connected';
      term.focus();
    };
    ws.onmessage = (event) => {
      if (event.data instanceof ArrayBuffer) term.write(new Uint8Array(event.data));
    };
    ws.onclose = (event) => {
      if (disposed) return;
      const why = plain(event.reason);
      term.write('\r\n[session closed' + (why ? ': ' + why : '') + ']\r\n');
      status.textContent = 'Not connected';
      reconnect.hidden = false;
    };
  }
  reconnect.addEventListener('click', () => {
    term.clear();
    connect();
  });

  pageSession = {
    dispose() {
      disposed = true;
      observer.disconnect();
      // Closing the WebSocket is what ends the shell on the host: the server's
      // relay passes the close on and the agent kills the shell.
      if (ws) ws.close(1000);
      term.dispose();
    },
  };
  connect();
}

// ------------------------------------------------------------- Screen page

// The screen of a host (D-44, D-49): noVNC, a VNC viewer written in JavaScript
// (web/vendor/novnc), speaking to a VNC server on the host's own loopback
// through a `tunnel` session. The server lets a browser tunnel to this one port
// only, so a VNC server on the host is all the host needs.
const VNC_TARGET = '127.0.0.1:5900';

function renderScreenPage(host) {
  document.title = host.name + ' · headnode';
  const page = [
    h('a', { class: 'back', href: '#/' }, '← Hosts'),
    pageHead(host.name, null),
    h('div', { class: 'status' }, statusText(host)),
    hostTabs(host, 'screen'),
  ];
  if (!host.online) {
    page.push(h('div', { class: 'notice' },
      'Host offline. Make sure the headnode-agent service is running on this host, then reload this page.'));
    return page;
  }

  const status = h('span', { class: 'sub' }, 'Loading the viewer…');
  const cad = button('Ctrl+Alt+Del', 'btn btn-small', () => {});
  cad.disabled = true; // until connected
  const toggle = button('Disconnect', 'btn btn-small', () => {});

  // The screen is three boxes (D-65). `view` is what noVNC draws into, and it scales the
  // screen to fit `view`. `pan` is a scrolling box around it: when `view` is made bigger
  // than `pan` the picture is zoomed in, and the scrollbars move around it. That is how a
  // computer with several monitors, which is far wider than the window, can be looked at
  // closely. `box` holds both, and is what goes full screen.
  const view = h('div', { class: 'screen-view' });
  const pan = h('div', { class: 'screen-pan' }, view);
  // While Pan is on this sheet is over the screen: dragging it scrolls `pan`, and nothing
  // reaches the remote computer.
  const grab = h('div', { class: 'screen-grab', hidden: '' });
  const tools = {
    fit: button('Fit', 'btn btn-small', () => {}),
    out: button('−', 'btn btn-small', () => {}),
    in: button('+', 'btn btn-small', () => {}),
    actual: button('1:1', 'btn btn-small', () => {}),
    pan: button('Pan', 'btn btn-small', () => {}),
    label: h('span', { class: 'zoom-label' }, 'Fit'),
  };
  tools.fit.title = 'Fit the whole screen in the window';
  tools.out.title = 'Zoom out';
  tools.in.title = 'Zoom in';
  tools.actual.title = 'Actual size: one pixel of the remote screen is one pixel here';
  tools.pan.title = 'Drag the screen around while zoomed in (the scrollbars and the mouse wheel work too)';
  for (const b of [tools.fit, tools.out, tools.in, tools.actual, tools.pan]) b.disabled = true; // until connected
  // Inside the box, not in the bar above it: the bar is not there in full screen.
  const toolbar = h('div', { class: 'screen-tools', hidden: '' }, tools.fit, tools.out, tools.label, tools.in, tools.actual, tools.pan);
  tools.bar = toolbar; // shown by mountScreen while there is a screen to zoom
  const box = h('div', { class: 'screen' }, pan, grab, toolbar);

  // Stop VNC / Start VNC switches the server on the host off or on (D-51), to save
  // its resources. Its label follows the host's real state, which mountScreen
  // keeps asking for: this page is drawn once and the poll does not redraw it.
  const power = button('Stop VNC', 'btn btn-small', () => {});
  power.hidden = true; // until mountScreen knows that this host can be switched
  // In full screen a remote desktop also gets Esc, Tab, Alt and the Windows key.
  const fullscreen = fullscreenButton(box, ['Escape', 'Tab', 'AltLeft', 'AltRight', 'MetaLeft', 'MetaRight']);
  page.push(h('div', { class: 'screen-bar' }, status, h('div', { class: 'screen-actions' }, cad, toggle, power, fullscreen)), box);

  // Built after the box is on the page: noVNC measures it to scale the screen.
  queueMicrotask(() => mountScreen(host, { box, pan, view, grab, tools, status, cad, toggle, power }));
  return page;
}

// Asks for the VNC password (the VNC server's own, not a headnode one). It goes to
// the server for this connection only, and is never stored. Resolves with the
// password, or null if the person cancelled.
function askVncPassword(hostName) {
  return new Promise((resolve) => {
    const password = h('input', { class: 'input', type: 'password', name: 'password', autocomplete: 'off', required: '' });
    let answered = false;
    openDialog({
      title: 'VNC password',
      fields: [
        h('p', { class: 'sub' }, 'The password of the VNC server on ' + hostName + '. It is used for this connection and is never stored.'),
        field('Password', password),
      ],
      submitLabel: 'Connect',
      onSubmit: async () => {
        answered = true;
        resolve(password.value);
        return null;
      },
      onClose: () => { if (!answered) resolve(null); },
    });
  });
}

async function mountScreen(host, ui) {
  const { box, pan, view, grab, tools, status, cad, toggle, power } = ui;
  let disposed = false;
  let rfb = null;
  let ws = null;
  let closeReason = ''; // why the tunnel closed, from the agent: "connect: connection refused"...
  let everConnected = false; // did the screen appear at all in this attempt?
  let attempting = false;    // between "Connect" and the connection ending
  let latest = host;         // the host as the server last described it (see watchHost)
  let wasUp = hasVnc(host);
  let pending = null;        // { enabled, at } while a Start or Stop VNC is in flight
  let watcher = null;
  let sizeWatch = null;      // follows the size of the box the screen is shown in

  pageSession = {
    dispose() {
      disposed = true;
      clearInterval(watcher);
      if (sizeWatch) sizeWatch.disconnect();
      if (rfb) {
        try { rfb.disconnect(); } catch (err) { /* already gone */ }
      }
      if (ws) ws.close(1000);
    },
  };

  // noVNC is a set of ES modules, loaded when this page opens (and not on every
  // page of the dashboard). If the files are not there, say so.
  let RFB;
  try {
    RFB = (await import('/vendor/novnc/core/rfb.js')).default;
  } catch (err) {
    if (disposed) return;
    box.replaceChildren(h('div', { class: 'notice' },
      'The screen viewer library is missing. Download noVNC into web/vendor (see web/vendor/README.md) and restart the server.'));
    status.textContent = '';
    toggle.hidden = true;
    return;
  }
  if (disposed) return;

  // To see what the VNC server offers during login (the "Server security types" lines)
  // when the console shows only a failure, run  localStorage.setItem('headnode-vnc-debug', '1')
  // in the browser console and reload. Off by default: debug logging is very chatty.
  try {
    if (localStorage.getItem('headnode-vnc-debug')) {
      (await import('/vendor/novnc/core/util/logging.js')).initLogging('debug');
    }
  } catch (err) { /* no storage, or an unexpected library layout: carry on without it */ }
  if (disposed) return;

  async function connect() {
    attempting = true;
    everConnected = false;
    toggle.textContent = 'Disconnect';
    cad.disabled = true;
    status.textContent = 'Connecting…';
    closeReason = '';
    const result = await api('POST', '/api/hosts/' + host.id + '/sessions', { kind: 'tunnel', params: { target: VNC_TARGET } });
    if (disposed) return;
    if (!result.ok) {
      ended(plain(errorText(result)));
      return;
    }

    ws = openSocket(result.data.id);
    // Added before noVNC attaches its own handlers, so this runs first and the
    // reason is known by the time noVNC reports the disconnect.
    ws.addEventListener('close', (event) => { closeReason = plain(event.reason); });
    rfb = new RFB(view, ws, {});
    rfb.scaleViewport = true;   // the whole screen, scaled to `view` (which zoom resizes)
    rfb.resizeSession = false;  // never change the host's own resolution
    rfb.background = '#111010';
    // The remote screen can change size (a monitor was plugged in, the resolution was
    // changed): noVNC then changes its canvas, and the zoom has to follow.
    const canvas = view.querySelector('canvas');
    if (canvas) new MutationObserver(layout).observe(canvas, { attributes: true, attributeFilter: ['width', 'height'] });
    rfb.addEventListener('connect', () => {
      everConnected = true;
      status.textContent = 'Connected';
      cad.disabled = false;
      layout();
      rfb.focus();
    });
    rfb.addEventListener('desktopname', (event) => {
      status.textContent = 'Connected: ' + plain(event.detail.name); // text from the host: shown as text
    });
    // The VNC server's password is made by the agent each time VNC is started, and the
    // server hands it only to someone allowed to open this screen (D-64). It is tried
    // once by itself; if it is refused (VNC was restarted a moment ago, say), the
    // person is asked, as for a VNC server headnode does not manage.
    let givenPassword = result.data.vnc_password || '';
    rfb.addEventListener('credentialsrequired', async () => {
      if (givenPassword) {
        const password = givenPassword;
        givenPassword = '';
        rfb.sendCredentials({ password });
        return;
      }
      const password = await askVncPassword(host.name);
      if (disposed || !rfb) return;
      if (password === null) {
        rfb.disconnect();
        return;
      }
      rfb.sendCredentials({ password });
    });
    rfb.addEventListener('securityfailure', (event) => {
      closeReason = plain((event.detail && event.detail.reason) || 'the VNC server refused the password');
    });
    rfb.addEventListener('disconnect', (event) => {
      rfb = null;
      ws = null;
      if (disposed) return;
      layout(); // the buttons that need a screen are switched off again
      // For whoever has to find out why a connection dropped: the browser's console.
      console.info('noVNC disconnected', { clean: event.detail && event.detail.clean, tunnelReason: closeReason, everConnected });
      ended(closeReason, event.detail && event.detail.clean);
    });
  }

  // What the person is told when the connection is over, and why.
  function ended(reason, clean) {
    attempting = false;
    cad.disabled = true;
    toggle.textContent = 'Reconnect';
    if (/refused/i.test(reason)) {
      // Nothing answers on 5900. When headnode can switch the VNC server, that is
      // most likely because it is switched off.
      status.textContent = canSwitchVnc(latest)
        ? 'VNC is switched off on this host. Press Start VNC to turn it on.'
        : 'Nothing is listening on port 5900 of this host. Install a VNC server there and set it to ' +
          'accept connections from this machine only (docs/windows.md), then Reconnect.';
    } else if (reason) {
      status.textContent = 'Disconnected: ' + reason;
    } else if (!everConnected) {
      // The tunnel worked (the VNC server accepted it) but the screen never appeared:
      // the viewer could not log in, or a password was not given. The usual cause is
      // TightVNC 4, whose login (SRP) no browser viewer speaks (D-53).
      status.textContent = 'The viewer could not log in to the VNC server on this host. ' +
        'TightVNC 4 cannot be used from the browser: run the install command on the host again with -ReplaceVnc ' +
        'at the end to swap it for TightVNC 2.8.90 (docs/windows.md). The browser console (F12) has the exact reason.';
    } else {
      status.textContent = clean === false ? 'The connection was lost.' : 'Disconnected.';
    }
  }

  // ---- Start / Stop VNC, and keeping up with the host's real state
  function showPower() {
    power.hidden = !canSwitchVnc(latest);
    if (power.hidden) return;
    const up = hasVnc(latest);
    // A request is over when the host reports what was asked for (or after 20 s).
    if (pending && (pending.enabled === up || Date.now() - pending.at > 20000)) pending = null;
    power.disabled = pending !== null;
    power.textContent = pending ? (pending.enabled ? 'Starting…' : 'Stopping…') : (up ? 'Stop VNC' : 'Start VNC');
    power.classList.toggle('btn-primary', !up && !pending); // Start VNC is the thing to press when there is no screen
  }

  power.addEventListener('click', async () => {
    const enabled = !hasVnc(latest);
    pending = { enabled, at: Date.now() };
    showPower();
    if (!enabled && rfb) rfb.disconnect(); // hang up first, instead of being cut off
    const result = await api('POST', '/api/hosts/' + host.id + '/vnc', { enabled });
    if (disposed) return;
    if (!result.ok) {
      pending = null;
      status.textContent = plain(errorText(result));
      showPower();
    }
  });

  // This page is drawn once, so it asks for the host's state itself, every 2 seconds
  // (like the Hosts page does). That is how the button learns that the VNC server
  // really stopped or started, and how the screen appears by itself after Start VNC.
  async function watchHost() {
    const result = await api('GET', '/api/hosts/' + host.id);
    if (disposed || !result.ok) return;
    latest = result.data;
    const up = hasVnc(latest);
    showPower();
    if (up && !wasUp && !rfb && !attempting) {
      connect(); // it was switched on: show the screen
    } else if (!up && wasUp && canSwitchVnc(latest)) {
      status.textContent = 'VNC is switched off on this host. Press Start VNC to turn it on.';
      cad.disabled = true;
      toggle.textContent = 'Reconnect';
    }
    wasUp = up;
  }
  watcher = setInterval(watchHost, 2000);
  showPower();

  // ---- Zoom and panning (D-65). noVNC scales the screen to fit `view`; `view` is made as
  // big as the zoom asks, inside the scrolling box `pan`. Zooming by the size of the box,
  // and not by scaling the picture with CSS, keeps noVNC's own mapping of the mouse right.
  let zoom = 'fit'; // 'fit', or how many pixels here one pixel of the remote screen takes (1 = actual size)
  let panning = false;

  // The remote screen's size in pixels: noVNC's canvas has it, while a screen is shown.
  function framebuffer() {
    const canvas = rfb && view.querySelector('canvas');
    return canvas && canvas.width ? { w: canvas.width, h: canvas.height } : null;
  }
  const fitScale = (fb) => Math.min(pan.clientWidth / fb.w, pan.clientHeight / fb.h);
  const currentScale = (fb) => (zoom === 'fit' ? fitScale(fb) : zoom);

  // Sizes `view` for the zoom, which makes noVNC scale the screen to match.
  function layout() {
    const fb = framebuffer();
    for (const b of [tools.fit, tools.out, tools.in, tools.actual, tools.pan]) b.disabled = !fb;
    tools.bar.hidden = !fb;
    if (!fb) {
      view.style.width = '100%';
      view.style.height = '100%';
      tools.label.textContent = 'Fit';
      setPanning(false);
      return;
    }
    const scale = currentScale(fb);
    // Rounded down: a pixel too many would show scrollbars when the whole screen fits.
    view.style.width = Math.floor(fb.w * scale) + 'px';
    view.style.height = Math.floor(fb.h * scale) + 'px';
    tools.label.textContent = zoom === 'fit' ? 'Fit' : Math.round(scale * 100) + '%';
  }

  // Zooming in or out by a step. At or below "fit" it is fit: the whole screen is the
  // least that is shown, and the most is 8 times.
  function zoomTo(next) {
    const fb = framebuffer();
    if (!fb) return;
    zoom = next <= fitScale(fb) * 1.001 ? 'fit' : Math.min(next, 8);
    layout();
    if (rfb && !panning) rfb.focus(); // the click moved the keyboard to the button
  }
  tools.fit.addEventListener('click', () => zoomTo(0));
  tools.in.addEventListener('click', () => { const fb = framebuffer(); if (fb) zoomTo(currentScale(fb) * 1.25); });
  tools.out.addEventListener('click', () => { const fb = framebuffer(); if (fb) zoomTo(currentScale(fb) / 1.25); });
  tools.actual.addEventListener('click', () => {
    zoom = 1;
    layout();
    if (rfb && !panning) rfb.focus();
  });

  // Pan: while it is on, dragging scrolls the zoomed screen and nothing is sent to the remote computer.
  function setPanning(on) {
    panning = on;
    grab.hidden = !on;
    tools.pan.classList.toggle('active', on);
    if (!on && rfb) rfb.focus();
  }
  tools.pan.addEventListener('click', () => setPanning(!panning));
  let drag = null;
  grab.addEventListener('pointerdown', (event) => {
    drag = { x: event.clientX, y: event.clientY, left: pan.scrollLeft, top: pan.scrollTop };
    grab.setPointerCapture(event.pointerId);
    grab.classList.add('dragging');
  });
  grab.addEventListener('pointermove', (event) => {
    if (!drag) return;
    pan.scrollLeft = drag.left - (event.clientX - drag.x);
    pan.scrollTop = drag.top - (event.clientY - drag.y);
  });
  const endDrag = () => {
    drag = null;
    grab.classList.remove('dragging');
  };
  grab.addEventListener('pointerup', endDrag);
  grab.addEventListener('pointercancel', endDrag);
  grab.addEventListener('wheel', (event) => {
    event.preventDefault();
    pan.scrollBy(event.deltaX, event.deltaY);
  }, { passive: false });

  // The box changes size with the window, and in full screen.
  sizeWatch = new ResizeObserver(layout);
  sizeWatch.observe(pan);

  // VNC is not running on this host: say what to do, instead of trying a connection that cannot work.
  function showOff() {
    cad.disabled = true;
    toggle.textContent = 'Reconnect';
    status.textContent = canSwitchVnc(latest)
      ? 'VNC is switched off on this host. Press Start VNC to turn it on.'
      : 'No VNC server is running on this host. Install one that accepts connections from this machine only ' +
        '(docs/windows.md, docs/linux-screen.md), then press Reconnect.';
  }

  toggle.addEventListener('click', () => {
    if (rfb) rfb.disconnect(); // ends the session on the host: the tunnel closes with it
    else connect();
  });
  cad.addEventListener('click', () => { if (rfb) rfb.sendCtrlAltDel(); });
  if (hasVnc(host)) connect();
  else showOff();
}

// -------------------------------------------------------------- Files page

function filesHref(hostId) {
  return hostLink(hostId) + '/files';
}

function joinPath(dir, name) {
  return dir === '/' ? '/' + name : dir + '/' + name;
}

function renderFilesPage(host) {
  document.title = host.name + ' · headnode';
  const page = [
    h('a', { class: 'back', href: '#/' }, '← Hosts'),
    pageHead(host.name, null),
    h('div', { class: 'status' }, statusText(host)),
    hostTabs(host, 'files'),
  ];
  if (isWindows(host)) {
    page.push(windowsNotice(host));
    return page;
  }
  if (!host.online) {
    page.push(h('div', { class: 'notice' },
      'Host offline. Make sure the headnode-agent service is running on this host, then reload this page.'));
    return page;
  }
  const box = h('div', { class: 'files' });
  page.push(box);
  // Like the terminal: built once the box is on the page, and never redrawn by the poll.
  queueMicrotask(() => mountFiles(host, box));
  return page;
}

// The file manager talks to the agent over one session WebSocket. It sends a
// JSON request with an id, and the agent answers with the same id (protocol.md
// §4), so several requests can be in flight and each answer finds its caller.
function mountFiles(host, box) {
  let ws = null;
  let disposed = false;
  let readonly = true;  // until the server says otherwise
  let dir = null;       // the folder on show
  let nextId = 1;
  const pending = new Map(); // request id -> { resolve, reject }
  const backStack = [];      // the folders visited before this one, for the Back button
  let openMenu = null;       // an open menu (the folders under a ">", or a row's "more"): { el, owner }
  let entries = [];          // everything the host listed for this folder
  let sortKey = 'name';      // 'name', 'size' or 'date'
  let sortAscending = true;
  let filterText = '';
  let showHidden = false;    // dotfiles are hidden until asked for: a home folder is full of them
  const selected = new Set(); // names ticked in this folder (only ever names that are on screen)
  let baseMessage = '';      // what the message line goes back to after a short "Copied ..."
  let dragItems = null;      // the rows being dragged inside the page, to be moved into a folder

  // The path bar works like the Explorer's address bar. Every folder of the path
  // is a button that goes there, and the ">" after it lists the folders inside
  // it. Clicking the empty part of the bar turns it into a box you can type in.
  const crumbs = h('div', { class: 'crumbs' });
  const crumbsBox = h('div', { class: 'crumbs-wrap' }, crumbs);
  const backButton = iconButton('Back', ICON_BACK, goBack);
  backButton.disabled = true; // nothing to go back to yet
  const reloadButton = iconButton('Refresh', ICON_REFRESH, () => { if (dir) load(dir); });
  const newFolder = iconButton('New folder', ICON_NEW_FOLDER, () => newFolderDialog());
  newFolder.hidden = true; // shown once we know this session may change files
  const uploadButton = iconButton('Upload', ICON_UPLOAD, () => uploadDialog());
  uploadButton.hidden = true;
  const reconnect = h('button', { class: 'btn', type: 'button', hidden: '' }, 'Reconnect');
  const bar = h('div', { class: 'path-bar' }, backButton, crumbsBox, reloadButton, newFolder, uploadButton, reconnect);
  // Narrowing and showing: both only change what is drawn, never what the host is
  // asked, so they are instant even in a folder with thousands of entries.
  const filterInput = h('input', {
    class: 'input', type: 'search', placeholder: 'Filter this folder', autocomplete: 'off', spellcheck: 'false',
    'aria-label': 'Filter this folder',
  });
  filterInput.addEventListener('input', () => {
    filterText = filterInput.value;
    draw();
  });
  const hiddenBox = h('input', { type: 'checkbox' });
  hiddenBox.addEventListener('change', () => {
    showHidden = hiddenBox.checked;
    draw();
  });
  const tools = h('div', { class: 'files-tools' }, filterInput, h('label', { class: 'toggle' }, hiddenBox, 'Hidden files'));

  // Shown while something is ticked: what can be done to all of it at once.
  const selectionText = h('span', { class: 'selection-text' });
  const selectionBar = h('div', { class: 'selection-bar', hidden: '' },
    selectionText,
    button('Move to…', 'btn btn-small', () => transferDialog('move', selectedEntries())),
    button('Copy to…', 'btn btn-small', () => transferDialog('copy', selectedEntries())),
    // For people who may upload: it copies what is ticked to the server first.
    me && me.can_upload ? button('Send to hosts…', 'btn btn-small', () => sendToHosts(selectedEntries())) : null,
    button('Delete', 'btn btn-small btn-danger-text', () => deleteDialog(selectedEntries())));

  const message = h('div', { class: 'files-message sub' });
  const summary = h('div', { class: 'files-summary sub' }); // "Showing 3 of 120", "2 hidden items"
  const listing = h('div');
  box.replaceChildren(bar, tools, selectionBar, message, summary, listing);

  // Dropping files from the computer anywhere on the page uploads them into the
  // folder on show. (Rows dragged inside the page are a different thing: they
  // carry no files, so they never trigger this.)
  const hasFiles = (event) => event.dataTransfer && [...event.dataTransfer.types].includes('Files');
  box.addEventListener('dragover', (event) => {
    if (readonly || dir === null || !hasFiles(event)) return;
    event.preventDefault(); // without this the browser would open the file instead
    box.classList.add('drop-active');
  });
  box.addEventListener('dragleave', (event) => {
    if (!box.contains(event.relatedTarget)) box.classList.remove('drop-active');
  });
  box.addEventListener('drop', (event) => {
    if (!hasFiles(event)) return;
    event.preventDefault();
    box.classList.remove('drop-active');
    if (readonly || dir === null) return;
    uploadDropped([...event.dataTransfer.files]);
  });

  crumbs.addEventListener('click', (event) => {
    // Only a click on the empty part counts: the folder buttons do their own thing.
    if (dir !== null && (event.target === crumbs || event.target.classList.contains('crumb-fill'))) editPath();
  });
  // A click anywhere else, or Escape, closes the folder list.
  const onPointerDown = (event) => {
    // The buttons that open a menu toggle it themselves.
    if (openMenu && !openMenu.el.contains(event.target) && !event.target.closest('.crumb-chevron, .row-more')) closeMenu();
  };
  const onKeyDown = (event) => {
    if (event.key === 'Escape') closeMenu();
  };
  document.addEventListener('mousedown', onPointerDown);
  document.addEventListener('keydown', onKeyDown);

  // Draws "/ > home > sanca >": each name goes to that folder, each ">" lists the
  // folders inside the folder before it, so you can jump sideways in two clicks.
  function drawCrumbs() {
    closeMenu();
    const names = dir === '/' ? [] : dir.split('/').filter(Boolean);
    const parts = [];
    let path = '/';
    const add = (label, target) => {
      const crumb = button(label, 'crumb', () => load(target));
      makeDropTarget(crumb, target); // rows can be dropped on a folder of the path, to move them up
      parts.push(crumb);
      const chevron = h('button', { class: 'crumb-chevron', type: 'button', 'aria-label': 'Folders in ' + target }, icon(ICON_CHEVRON));
      chevron.addEventListener('click', () => showChildren(target, chevron));
      parts.push(chevron);
    };
    add('/', '/');
    for (const name of names) {
      path = joinPath(path, name);
      add(name, path);
    }
    crumbs.replaceChildren(...parts, h('div', { class: 'crumb-fill', title: 'Click to type a path' }));
    crumbs.scrollLeft = crumbs.scrollWidth; // a long path shows its end, where you are
  }

  function closeMenu() {
    if (openMenu) {
      openMenu.el.remove();
      openMenu = null;
    }
  }

  // The folder list under a ">". The names come from the host's disk, so they are
  // text like everything else (D-19).
  async function showChildren(target, chevron) {
    if (openMenu && openMenu.owner === chevron) { // a second click on the same ">" closes it
      closeMenu();
      return;
    }
    closeMenu();
    const menu = h('div', { class: 'menu', role: 'menu' }, h('div', { class: 'menu-note sub' }, 'Loading…'));
    // Right under the ">" that opened it.
    menu.style.left = Math.max(0, chevron.getBoundingClientRect().left - crumbsBox.getBoundingClientRect().left) + 'px';
    crumbsBox.append(menu);
    openMenu = { el: menu, owner: chevron };

    const result = await attempt('list', { path: target });
    if (!openMenu || openMenu.el !== menu) return; // it was closed while we waited
    if (!result.ok) {
      menu.replaceChildren(h('div', { class: 'menu-note danger' }, errorText(result)));
      return;
    }
    const folders = (result.data.entries || []).filter((entry) => entry.is_dir)
      .sort((a, b) => a.name.localeCompare(b.name));
    menu.replaceChildren(...(folders.length === 0
      ? [h('div', { class: 'menu-note sub' }, 'No folders')]
      : folders.map((entry) => button(entry.name, 'menu-item', () => {
        closeMenu();
        load(joinPath(target, entry.name));
      }))));
  }

  // The bar as a text box: type or paste a path, Enter goes there, Escape or
  // clicking away puts the folder buttons back.
  function editPath() {
    closeMenu();
    const input = h('input', {
      class: 'path-edit', value: dir, spellcheck: 'false', autocomplete: 'off', 'aria-label': 'Folder path',
    });
    crumbs.replaceChildren(input);
    input.focus();
    input.select();
    let finished = false;
    const finish = (go) => {
      if (finished) return; // Enter, then the blur that removing the box causes
      finished = true;
      if (go) load(input.value);
      drawCrumbs(); // if the folder cannot be listed, the old path stays
    };
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') {
        event.preventDefault();
        finish(true);
      } else if (event.key === 'Escape') {
        finish(false);
      }
    });
    input.addEventListener('blur', () => finish(false));
  }

  // Back goes to the folder you were in before, not to the parent folder, like
  // the Back button of a browser: from /home, after coming from /home/sanca, it
  // returns to /home/sanca.
  function goBack() {
    const previous = backStack.pop();
    backButton.disabled = backStack.length === 0;
    if (previous) load(previous, false);
  }

  // Send one request and wait for its answer.
  function request(op, fields) {
    return new Promise((resolve, reject) => {
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        reject(new Error('Not connected.'));
        return;
      }
      const id = nextId++;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ id, op, ...fields }));
    });
  }

  // Like request, but never throws: the result has the same shape api() gives
  // (ok, data), so the dialogs and errorText() work with it unchanged.
  async function attempt(op, fields) {
    try {
      const reply = await request(op, fields);
      if (reply.ok) return { ok: true, data: reply };
      return { ok: false, status: 0, data: { error: reply.error || 'The request failed.' } };
    } catch (err) {
      return { ok: false, status: 0, data: { error: err.message } };
    }
  }

  // Shows a folder. record is false when going Back, which must not add to the
  // history it is walking through.
  async function load(target, record = true) {
    // "/etc/" and "/etc" are the same folder.
    const wanted = target.trim().replace(/\/+$/, '') || '/';
    const result = await attempt('list', { path: wanted });
    if (disposed) return;
    if (!result.ok) {
      message.textContent = errorText(result);
      return;
    }
    if (record && dir !== null && dir !== wanted) backStack.push(dir); // reloading the same folder is not a move
    if (dir !== wanted) {
      filterInput.value = ''; // a filter belongs to the folder it was typed in
      filterText = '';
    }
    dir = wanted;
    backButton.disabled = backStack.length === 0;
    drawCrumbs();
    entries = result.data.entries || [];
    selected.clear(); // the listing may have changed under the ticks
    baseMessage = result.data.truncated
      ? 'This folder has more entries than can be shown. Showing the first ' + entries.length + '.'
      : (readonly ? 'Read-only: you can browse and download files here, but not change them.' : '');
    message.textContent = baseMessage;
    draw();
  }

  // What the listing shows now: the hidden-file switch and the filter applied.
  function visibleEntries() {
    const needle = filterText.trim().toLowerCase();
    return entries.filter((entry) =>
      (showHidden || !entry.name.startsWith('.')) && (!needle || entry.name.toLowerCase().includes(needle)));
  }

  // Folders stay first whatever is sorted, like in a file manager. Folder sizes
  // are not real sizes (a folder entry is 4 KB on disk), so folders are ordered
  // by name when sorting by size.
  function sortedEntries(list) {
    const sign = sortAscending ? 1 : -1;
    const byName = (a, b) => a.name.localeCompare(b.name, undefined, { numeric: true });
    return [...list].sort((a, b) => {
      if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
      if (sortKey === 'size' && !a.is_dir) return sign * (a.size - b.size) || byName(a, b);
      if (sortKey === 'date') return sign * (new Date(a.mod_time) - new Date(b.mod_time)) || byName(a, b);
      return sign * byName(a, b);
    });
  }

  function selectedEntries() {
    return entries.filter((entry) => selected.has(entry.name));
  }

  function draw() {
    const visible = visibleEntries();
    // What is ticked is always on screen: an item that a filter hides is not
    // something to delete by accident.
    const shown = new Set(visible.map((entry) => entry.name));
    for (const name of [...selected]) if (!shown.has(name)) selected.delete(name);
    updateSelection();

    const hiddenCount = showHidden ? 0 : entries.filter((entry) => entry.name.startsWith('.')).length;
    const parts = [];
    if (filterText.trim()) parts.push('Showing ' + visible.length + ' of ' + entries.length + ' in this folder');
    if (hiddenCount > 0) parts.push(count(hiddenCount, 'hidden item', 'hidden items') + ' not shown');
    summary.textContent = parts.join(' · ');

    if (visible.length === 0) {
      listing.replaceChildren(h('div', { class: 'empty small' }, entries.length === 0 ? 'This folder is empty.'
        : (filterText.trim() ? 'Nothing here matches the filter.' : 'Only hidden files are here. Turn on "Hidden files" to see them.')));
      return;
    }

    const sorted = sortedEntries(visible);
    const heads = [];
    if (!readonly) {
      const all = h('input', { type: 'checkbox', 'aria-label': 'Select all' });
      all.checked = sorted.every((entry) => selected.has(entry.name));
      all.indeterminate = !all.checked && selected.size > 0;
      all.addEventListener('change', () => {
        if (all.checked) for (const entry of sorted) selected.add(entry.name);
        else selected.clear();
        draw();
      });
      heads.push(h('th', { class: 'col-check' }, all));
    }
    heads.push(
      sortHeader('Name', 'name'),
      sortHeader('Size', 'size'),
      sortHeader('Modified', 'date'),
      h('th', {}, 'Mode'),
      h('th', { class: 'col-act' }, 'Action'));
    listing.replaceChildren(h('table', {},
      h('thead', {}, h('tr', {}, heads)),
      h('tbody', {}, sorted.map(row))));
  }

  // A column title you can click: the first click sorts by it, the next reverses.
  function sortHeader(label, key) {
    const active = sortKey === key;
    const arrow = active ? (sortAscending ? ' ▲' : ' ▼') : '';
    const sortButton = button(label + arrow, 'th-sort', () => {
      if (sortKey === key) sortAscending = !sortAscending;
      else { sortKey = key; sortAscending = true; }
      draw();
    });
    return h('th', { 'aria-sort': active ? (sortAscending ? 'ascending' : 'descending') : null }, sortButton);
  }

  function updateSelection() {
    selectionBar.hidden = readonly || selected.size === 0;
    selectionText.textContent = count(selected.size, 'item', 'items') + ' selected';
    // Keep the "select all" box in the header in step with the ticks in the rows.
    const all = listing.querySelector('thead input[type="checkbox"]');
    if (all) {
      all.checked = selected.size > 0 && selected.size === visibleEntries().length;
      all.indeterminate = selected.size > 0 && !all.checked;
    }
  }

  function row(entry) {
    // Names come from the host's disk, so like everything else they are shown
    // as text (D-19). A folder is a button that opens it.
    const path = joinPath(dir, entry.name);
    const name = entry.is_dir
      ? button(entry.name + '/', 'link-button name', () => load(path))
      : h('span', { class: 'name' }, entry.name);
    const nameCell = h('td', {}, name, entry.is_link ? h('span', { class: 'tag' }, 'link') : null);

    const actions = [];
    // Anyone who can browse can download a file, read-only or not. It is a plain
    // link: the browser's own download manager shows progress and saves the file,
    // and the server streams it from the host (protocol.md §1).
    // The actions are pictures only; the words are in the tooltips.
    // A folder is sent as a zip archive, built as it is sent (protocol.md §4).
    const downloadLabel = entry.is_dir ? 'Download as zip' : 'Download';
    actions.push(h('a', {
      class: 'icon-button',
      href: '/api/hosts/' + host.id + '/download?path=' + encodeURIComponent(path),
      download: '',
      title: downloadLabel,
      'aria-label': downloadLabel,
    }, icon(ICON_DOWNLOAD)));
    actions.push(iconOnly('Copy path', ICON_COPY_PATH, () => copyPathToClipboard(path)));
    if (!readonly) {
      actions.push(
        iconOnly('Rename', ICON_RENAME, () => startRename(entry, nameCell)),
        iconOnly('Delete', ICON_DELETE, () => deleteDialog([entry]), 'danger-text'));
    }
    // Everything else lives in a small menu, so a row does not become a wall of
    // pictures. Properties is a read, so a read-only session has it too.
    const more = iconOnly('More', ICON_MORE, () => showRowMenu(more, entry), 'row-more');
    actions.push(h('span', { class: 'menu-anchor' }, more));

    const cells = [];
    if (!readonly) {
      const tick = h('input', { type: 'checkbox', 'aria-label': 'Select ' + entry.name, checked: selected.has(entry.name) ? '' : null });
      tick.addEventListener('change', () => {
        if (tick.checked) selected.add(entry.name);
        else selected.delete(entry.name);
        tick.closest('tr').classList.toggle('selected', tick.checked);
        updateSelection();
      });
      cells.push(h('td', { class: 'col-check' }, tick));
    }
    cells.push(
      nameCell,
      h('td', { class: 'num sub' }, entry.is_dir ? '' : formatBytes(entry.size)),
      h('td', { class: 'sub', title: new Date(entry.mod_time).toLocaleString() }, relativeTime(entry.mod_time)),
      h('td', { class: 'mono sub' }, entry.mode),
      h('td', { class: 'col-act' }, actions));
    const tr = h('tr', {
      class: selected.has(entry.name) ? 'static selected' : 'static',
      draggable: readonly ? null : 'true',
    }, cells);
    if (!readonly) {
      // Dragging a ticked row takes everything that is ticked with it.
      tr.addEventListener('dragstart', (event) => {
        dragItems = selected.has(entry.name) ? selectedEntries() : [entry];
        event.dataTransfer.effectAllowed = 'move';
        event.dataTransfer.setData('text/plain', dragItems.map((item) => item.name).join('\n'));
      });
      tr.addEventListener('dragend', () => { dragItems = null; });
      if (entry.is_dir) makeDropTarget(tr, path);
    }
    return tr;
  }

  // Something rows can be dropped on to move them there: a folder row, or a
  // folder of the path above the list.
  function makeDropTarget(el, dest) {
    const droppable = () => dragItems !== null && !dragItems.some((item) => joinPath(dir, item.name) === dest);
    el.addEventListener('dragover', (event) => {
      if (!droppable()) return;
      event.preventDefault(); // this is what says "you may drop here"
      event.dataTransfer.dropEffect = 'move';
      el.classList.add('drop-target');
    });
    el.addEventListener('dragleave', () => el.classList.remove('drop-target'));
    el.addEventListener('drop', (event) => {
      if (!droppable()) return;
      event.preventDefault();
      el.classList.remove('drop-target');
      const items = dragItems;
      dragItems = null;
      const problem = moveProblem(dest, items);
      if (problem) {
        message.textContent = problem;
        return;
      }
      runBatch(items, 'Moved', (entry) => attempt('rename', { from: joinPath(dir, entry.name), to: joinPath(dest, entry.name) }));
    });
  }

  // Why items cannot be moved into dest, or null when they can.
  function moveProblem(dest, items) {
    if (dest === dir) return 'These items are already in that folder.';
    const inside = items.find((entry) => entry.is_dir &&
      (dest === joinPath(dir, entry.name) || dest.startsWith(joinPath(dir, entry.name) + '/')));
    return inside ? 'Cannot move "' + inside.name + '" into itself.' : null;
  }

  // The "more" menu of one row.
  function showRowMenu(anchor, entry) {
    if (openMenu && openMenu.owner === anchor) { // a second click closes it
      closeMenu();
      return;
    }
    closeMenu();
    const item = (label, run) => button(label, 'menu-item', () => { closeMenu(); run(); });
    const items = [];
    // A text file opens in the editor: to change it, or only to look, in a
    // read-only session. A folder has nothing to open here.
    if (!entry.is_dir) items.push(item(readonly ? 'View' : 'Edit', () => editorDialog(entry)));
    items.push(item('Properties', () => propertiesDialog(entry)));
    if (!readonly) {
      items.push(
        item('Move to…', () => transferDialog('move', [entry])),
        item('Copy to…', () => transferDialog('copy', [entry])),
        item('Permissions…', () => permissionsDialog(entry)));
    }
    // Only for people who may upload (it creates an upload on the server). A
    // folder is sent as a whole.
    if (me && me.can_upload) items.push(item('Send to hosts…', () => sendToHosts([entry])));
    const menu = h('div', { class: 'menu menu-right', role: 'menu' }, items);
    anchor.parentElement.append(menu);
    openMenu = { el: menu, owner: anchor };
  }

  // Puts a path on the clipboard, to paste into Send file or a shell.
  // navigator.clipboard only exists on https and on localhost, and the dashboard
  // may well be opened over plain http on a trusted network, so there is a
  // fallback that goes through a hidden text box.
  async function copyPathToClipboard(path) {
    let copied = false;
    try {
      await navigator.clipboard.writeText(path);
      copied = true;
    } catch (err) {
      const area = h('textarea', { class: 'clipboard-helper', readonly: '', 'aria-hidden': 'true' });
      area.value = path;
      document.body.append(area);
      area.select();
      try { copied = document.execCommand('copy'); } catch (e) { /* stays false */ }
      area.remove();
    }
    flash(copied ? 'Copied ' + path : 'Could not copy. The path is ' + path);
  }

  // A short note on the message line that goes away by itself.
  let flashTimer = null;
  function flash(text) {
    message.textContent = text;
    clearTimeout(flashTimer);
    flashTimer = setTimeout(() => { message.textContent = baseMessage; }, 4000);
  }

  // Upload to this host, in two steps (D-03): the file goes to the server once,
  // then the host is told to fetch it. The host answers when the file is in
  // place, and until then this waits and shows where it is.
  function uploadDialog() {
    const file = h('input', { class: 'input', type: 'file', name: 'file', multiple: '', required: '' });
    const mode = h('input', {
      class: 'input', name: 'mode', value: '0644', required: '', autocomplete: 'off', pattern: '0?[0-7]{3}',
    });
    const status = h('div', { class: 'sub' });
    openDialog({
      title: 'Upload files',
      fields: [
        h('p', { class: 'sub' }, 'Pick one or more files. They go to the server first, then ' + host.name + ' fetches them into ' +
          dir + ', one after another. If a file with the same name is already there, both are kept: the new one is saved as "name (1)".'),
        field('Files', file),
        field('Mode for all of them (0644 for a normal file, 0755 for a program)', mode),
        status,
      ],
      submitLabel: 'Upload',
      onSubmit: async () => {
        const chosen = [...file.files];
        if (chosen.length === 0) return 'Choose at least one file.';
        // Everything that can be done is done, so a failure in the middle is
        // reported in the message line and the dialog closes: asking again would
        // upload the files that worked a second time.
        await uploadMany(chosen, mode.value.trim(), (text) => { status.textContent = text; });
        return null;
      },
    });
  }

  // Uploads files into the folder on show, one after another, then shows the
  // result on the message line: how many worked, and which did not and why.
  async function uploadMany(files, mode, setStatus) {
    const problems = [];
    for (let i = 0; i < files.length; i++) {
      const prefix = files.length > 1 ? '(' + (i + 1) + ' of ' + files.length + ') ' + files[i].name + ': ' : '';
      const problem = await uploadToHost(files[i], mode, (text) => setStatus(prefix + text));
      if (disposed) return;
      if (problem) problems.push(files[i].name + ': ' + problem);
    }
    await load(dir);
    if (disposed) return;
    message.textContent = problems.length === 0
      ? 'Uploaded ' + count(files.length, 'file', 'files') + '.'
      : 'Uploaded ' + (files.length - problems.length) + ' of ' + files.length + '. Failed: ' + problems.join('; ');
  }

  // Puts one file in the current folder of this host. Returns the problem as text,
  // or null when the file is in place. setStatus shows where it is.
  async function uploadToHost(chosen, mode, setStatus) {
    setStatus('Uploading to the server… 0%');
    const stored = await uploadFile(chosen, (fraction) => {
      setStatus('Uploading to the server… ' + Math.round(fraction * 100) + '%');
    });
    if (!stored.ok) return errorText(stored);

    // The agent replaces whatever is at the destination (D-33), so the name is
    // made free here first, by asking the host.
    const saveAs = await freeName(dir, chosen.name, false);
    setStatus(host.name + ' is fetching the file' + (saveAs === chosen.name ? '…' : ' as "' + saveAs + '"…'));
    const created = await api('POST', '/api/transfers', {
      file_id: stored.data.id,
      agent_ids: [host.id],
      dest_path: joinPath(dir, saveAs),
      mode,
    });
    if (!created.ok) return errorText(created);

    // The host reports when the file is in place, or why it could not be.
    for (let i = 0; i < 900; i++) { // about 15 minutes
      const state = await api('GET', '/api/transfers/' + created.data.id);
      if (!state.ok) return errorText(state);
      const result = state.data.hosts[0];
      if (result.status === 'ok') return null;
      if (result.status === 'error') return 'The host could not save the file: ' + result.error;
      if (disposed) return null;
      if (i === 15) {
        // A host answers within moments unless it is busy with a big file, or
        // its agent is older than file transfers and ignores the request.
        setStatus(host.name + ' has not answered yet. If its agent was started before file ' +
          'transfers existed (the host page shows the agent version, 0.2.0 or newer is needed), ' +
          'it never will: update and restart the agent.');
      }
      await sleep(1000);
    }
    return 'The host is still working on it. Refresh the folder in a while to see the file.';
  }

  // Files dropped from the computer onto the page go into the folder on show,
  // one after another, with the usual mode for a plain file. For another mode,
  // use the Upload button.
  function uploadDropped(files) {
    return uploadMany(files, '0644', (text) => { message.textContent = text; });
  }

  // "Send to hosts": what is picked (files, folders, or both) is copied from this
  // host to the server one item at a time, each becoming an upload (a folder
  // arrives zipped), then the Send dialog opens with them already chosen.
  async function sendToHosts(items) {
    if (items.length === 0) return;
    const staged = [];
    const problems = [];
    for (let i = 0; i < items.length; i++) {
      const prefix = items.length > 1 ? '(' + (i + 1) + ' of ' + items.length + ') ' : '';
      message.textContent = prefix + 'Copying "' + items[i].name + '" from ' + host.name + ' to the server…';
      const result = await api('POST', '/api/hosts/' + host.id + '/pull', { path: joinPath(dir, items[i].name) });
      if (disposed) return;
      if (result.ok) {
        staged.push({ id: result.data.id, name: result.data.name, size: result.data.size, archive: result.data.archive, from: host.name });
      } else {
        problems.push(items[i].name + ': ' + errorText(result));
      }
    }
    message.textContent = baseMessage;
    if (staged.length === 0) {
      messageDialog('Could not copy to the server', problems.join('; '));
      return;
    }
    if (problems.length > 0) {
      message.textContent = 'Could not copy ' + problems.join('; ') + '. The rest is ready to send.';
    }
    sendFileDialog(staged);
  }

  // The editor: the text of a small file in a big box. Saving writes it back, but
  // only if the file is still the version that was opened (protocol.md §4), so a
  // change made on the host meanwhile is never thrown away by accident.
  function editorDialog(entry) {
    const path = joinPath(dir, entry.name);
    const box = h('textarea', {
      class: 'editor', spellcheck: 'false', autocomplete: 'off', wrap: 'off', disabled: '',
      'aria-label': 'Contents of ' + entry.name,
    });
    box.value = 'Loading…';
    if (readonly) box.setAttribute('readonly', '');
    let version = null;     // null until the file has been read: nothing can be saved before
    let original = box.value; // what the box held when it was opened, to tell if it changed
    let crlf = false;       // the file used Windows line ends: a text box hides them, so put them back on save
    const info = h('div', { class: 'sub' }, path);

    openDialog({
      title: (readonly ? 'View ' : 'Edit ') + entry.name,
      wide: true,
      fields: [info, box],
      submitLabel: readonly ? 'Close' : 'Save',
      cancelLabel: readonly ? null : 'Cancel',
      canClose: () => readonly || box.value === original || confirm('Close without saving your changes?'),
      onSubmit: async () => {
        if (readonly) return null;
        if (version === null) return 'The file has not been loaded.';
        if (box.value === original) return null; // nothing to save
        const text = crlf ? box.value.replace(/\r?\n/g, '\r\n') : box.value;
        const result = await attempt('write', { path, content: text, version });
        if (!result.ok) return errorText(result);
        await load(dir);
        return null;
      },
    });

    attempt('read', { path }).then((result) => {
      if (disposed) return;
      if (!result.ok) {
        box.value = '';
        info.textContent = path + ' — ' + errorText(result);
        info.classList.add('danger');
        return;
      }
      const text = result.data.text || '';
      crlf = text.includes('\r\n');
      version = result.data.version;
      box.value = text; // a text box turns CRLF into LF by itself
      original = box.value;
      box.disabled = false;
      box.focus();
    });
  }

  function newFolderDialog() {
    const name = h('input', { class: 'input', name: 'name', required: '', autocomplete: 'off' });
    openDialog({
      title: 'New folder',
      fields: [field('Name', name)],
      submitLabel: 'Create',
      onSubmit: async () => {
        const newName = name.value.trim();
        if (!newName || newName.includes('/')) return 'A name cannot be empty or contain "/".';
        const result = await attempt('mkdir', { path: joinPath(dir, newName) });
        if (!result.ok) return errorText(result);
        load(dir);
        return null;
      },
    });
  }

  // Rename in the row itself: the name becomes a text box. For a file the name
  // is selected without its extension, so typing a new name keeps ".txt", like
  // in a desktop file manager. Enter renames, Escape or clicking away cancels.
  function startRename(entry, nameCell) {
    const input = h('input', {
      class: 'input input-inline', value: entry.name, spellcheck: 'false', autocomplete: 'off', 'aria-label': 'New name',
    });
    // Cancelling puts the old name back into this one cell. It does not redraw the
    // table: a click on another button that caused the blur must still land on it.
    const original = [...nameCell.childNodes];
    const restore = () => nameCell.replaceChildren(...original);
    nameCell.replaceChildren(input);
    input.focus();
    const dot = entry.is_dir ? -1 : entry.name.lastIndexOf('.');
    input.setSelectionRange(0, dot > 0 ? dot : entry.name.length); // ".bashrc" has no extension: dot is 0

    let finished = false;
    const finish = async (commit) => {
      if (finished) return; // Enter, then the blur that redrawing the row causes
      finished = true;
      const newName = input.value.trim();
      if (!commit || newName === entry.name) {
        restore();
        return;
      }
      if (!newName || newName.includes('/')) {
        restore();
        message.textContent = 'A name cannot be empty or contain "/".';
        return;
      }
      const result = await attempt('rename', { from: joinPath(dir, entry.name), to: joinPath(dir, newName) });
      if (disposed) return;
      if (!result.ok) {
        restore();
        message.textContent = 'Could not rename "' + entry.name + '": ' + errorText(result);
        return;
      }
      await load(dir);
    };
    input.addEventListener('keydown', (event) => {
      if (event.key === 'Enter') {
        event.preventDefault();
        finish(true);
      } else if (event.key === 'Escape') {
        finish(false);
      }
    });
    input.addEventListener('blur', () => finish(false));
  }

  // Runs one request per item, one after another. Every item is its own request,
  // so the host reports (and the server audits) each one separately. Whatever
  // fails is listed in the message line, and the rest still happens.
  async function runBatch(items, verb, perform) {
    const failures = [];
    for (const entry of items) {
      const result = await perform(entry);
      if (!result.ok) failures.push(entry.name + ': ' + errorText(result));
    }
    selected.clear();
    await load(dir);
    if (disposed) return;
    message.textContent = failures.length === 0
      ? verb + ' ' + count(items.length, 'item', 'items') + '.'
      : verb + ' ' + (items.length - failures.length) + ' of ' + items.length + '. Failed: ' + failures.join('; ');
  }

  function deleteDialog(items) {
    if (items.length === 0) return;
    const names = items.slice(0, 5).map((entry) => '"' + entry.name + '"').join(', ') +
      (items.length > 5 ? ' and ' + (items.length - 5) + ' more' : '');
    const hasFolder = items.some((entry) => entry.is_dir);
    confirmDialog({
      title: items.length === 1 ? (items[0].is_dir ? 'Delete folder' : 'Delete file') : 'Delete ' + items.length + ' items',
      message: 'Delete ' + names + (hasFolder ? ', and everything inside any folder,' : '') +
        ' from ' + host.name + '? This cannot be undone.',
      confirmLabel: 'Delete',
      onConfirm: async () => {
        await runBatch(items, 'Deleted', (entry) =>
          attempt('delete', { path: joinPath(dir, entry.name), recursive: entry.is_dir }));
        return { ok: true };
      },
    });
  }

  // The first name that is not taken in a folder: "report.pdf", then
  // "report (1).pdf", "report (2).pdf"... It asks the host with `stat`, so it is
  // right even in a folder too big to list completely. Another name that
  // appears in the meantime is still possible, and the agent refuses a move or
  // a copy onto it (a copy never overwrites).
  async function freeName(folder, name, isDir) {
    const taken = async (candidate) => (await attempt('stat', { path: joinPath(folder, candidate) })).ok;
    if (!(await taken(name))) return name;
    const dot = isDir ? -1 : name.lastIndexOf('.');
    const stem = dot > 0 ? name.slice(0, dot) : name;
    const ext = dot > 0 ? name.slice(dot) : '';
    for (let n = 1; n < 1000; n++) {
      const candidate = stem + ' (' + n + ')' + ext;
      if (!(await taken(candidate))) return candidate;
    }
    return name; // not reachable in practice: the host then refuses, and says so
  }

  // A small folder browser for the Move and Copy dialogs: the folder you are in,
  // the folders inside it to step into, and Up. path() is where it stands now.
  function folderPicker(start) {
    let current = start;
    const where = h('div', { class: 'picker-path mono' });
    const list = h('div', { class: 'picker-list' });
    const up = button('Up', 'btn btn-small', () => go(current.slice(0, current.lastIndexOf('/')) || '/'));
    const el = h('div', { class: 'picker' }, h('div', { class: 'picker-bar' }, up, where), list);

    async function go(target) {
      list.replaceChildren(h('div', { class: 'menu-note sub' }, 'Loading…'));
      const result = await attempt('list', { path: target });
      if (!result.ok) {
        list.replaceChildren(h('div', { class: 'menu-note danger' }, errorText(result)));
        return; // stay where we were
      }
      current = target;
      where.textContent = current;
      up.disabled = current === '/';
      const folders = (result.data.entries || []).filter((entry) => entry.is_dir)
        .sort((a, b) => a.name.localeCompare(b.name, undefined, { numeric: true }));
      list.replaceChildren(...(folders.length === 0
        ? [h('div', { class: 'menu-note sub' }, 'No folders here')]
        : folders.map((entry) => button(entry.name + '/', 'menu-item', () => go(joinPath(current, entry.name))))));
    }
    go(start);
    return { el, path: () => current };
  }

  // Move or copy items to a folder picked in the dialog. A move is the `rename`
  // operation with a new folder, so the host refuses to replace anything. A copy
  // gets a free name in the destination, so copying into the same folder gives
  // "name (1)" like a desktop file manager.
  function transferDialog(kind, items) {
    if (items.length === 0) return;
    const moving = kind === 'move';
    const picker = folderPicker(dir);
    const what = items.length === 1 ? '"' + items[0].name + '"' : count(items.length, 'item', 'items');
    openDialog({
      title: (moving ? 'Move ' : 'Copy ') + what,
      fields: [h('p', { class: 'sub' }, 'Pick the destination folder on ' + host.name + '.'), picker.el],
      submitLabel: moving ? 'Move here' : 'Copy here',
      onSubmit: async () => {
        const dest = picker.path();
        if (moving) {
          const problem = moveProblem(dest, items);
          if (problem) return problem;
        }
        await runBatch(items, moving ? 'Moved' : 'Copied', async (entry) => {
          const from = joinPath(dir, entry.name);
          if (moving) return attempt('rename', { from, to: joinPath(dest, entry.name) });
          return attempt('copy', { from, to: joinPath(dest, await freeName(dest, entry.name, entry.is_dir)) });
        });
        return null;
      },
    });
  }

  // What the host knows about one item. A folder's size is added up on the host,
  // which can take a moment, so the window opens at once and fills in.
  function propertiesDialog(entry) {
    const path = joinPath(dir, entry.name);
    const facts = h('dl', { class: 'facts' });
    const show = (rows) => facts.replaceChildren(...rows.flatMap(([label, value]) => [h('dt', {}, label), h('dd', {}, value)]));
    show([['Path', h('span', { class: 'mono' }, path)], ['Details', entry.is_dir ? 'Counting what is inside…' : 'Loading…']]);
    openDialog({ title: 'Properties', fields: [facts], submitLabel: 'Close', cancelLabel: null, onSubmit: async () => null });

    attempt('props', { path }).then((result) => {
      if (disposed) return;
      if (!result.ok) {
        show([['Path', h('span', { class: 'mono' }, path)], ['Error', errorText(result)]]);
        return;
      }
      const p = result.data.props;
      let type = p.is_dir ? 'Folder' : 'File';
      if (p.is_link) type = 'Link' + (p.link_target ? ' to ' + p.link_target : '');
      let size = formatBytes(p.size) + ' (' + p.size.toLocaleString() + ' bytes)';
      if (p.is_dir) {
        size += ' · ' + count(p.files || 0, 'file', 'files') + ', ' + count(p.dirs || 0, 'folder', 'folders');
        if (p.truncated) size = 'At least ' + size + '. This folder is too big to count completely.';
      }
      const rows = [
        ['Name', p.name],
        ['Path', h('span', { class: 'mono' }, path)],
        ['Type', type],
        ['Size', size],
        ['Permissions', h('span', { class: 'mono' }, p.mode + ' (' + p.perm + ')')],
      ];
      if (p.owner || p.group) rows.push(['Owner', (p.owner || '?') + ':' + (p.group || '?')]);
      rows.push(['Modified', new Date(p.mod_time).toLocaleString()]);
      show(rows);
    });
  }

  // chmod: nine boxes and the octal number, kept in step. Only the nine rwx bits:
  // setuid, setgid and sticky cannot be set from here (protocol.md §4).
  function permissionsDialog(entry) {
    // "-rwxr-x--x" or "drwxr-xr-x": the last nine characters are the bits.
    let bits = 0;
    entry.mode.slice(-9).split('').forEach((c, i) => { if (c !== '-') bits |= 1 << (8 - i); });

    const octal = h('input', { class: 'input mono', name: 'mode', required: '', autocomplete: 'off', pattern: '0?[0-7]{3}' });
    const boxes = [];
    const rows = ['Owner', 'Group', 'Others'].map((who, r) =>
      h('tr', {}, h('td', {}, who), ['Read', 'Write', 'Execute'].map((what, c) => {
        const bit = 1 << (8 - (r * 3 + c));
        const box = h('input', { type: 'checkbox', 'aria-label': who + ' ' + what.toLowerCase() });
        box.dataset.bit = String(bit);
        box.checked = (bits & bit) !== 0;
        boxes.push(box);
        box.addEventListener('change', () => { octal.value = fromBoxes(); });
        return h('td', {}, h('label', { class: 'toggle' }, box, what));
      })));
    function fromBoxes() {
      const value = boxes.reduce((sum, box) => sum | (box.checked ? Number(box.dataset.bit) : 0), 0);
      return '0' + value.toString(8).padStart(3, '0');
    }
    octal.value = fromBoxes();
    octal.addEventListener('input', () => {
      if (!/^0?[0-7]{3}$/.test(octal.value.trim())) return; // half typed: leave the boxes alone
      const value = parseInt(octal.value.trim(), 8);
      for (const box of boxes) box.checked = (value & Number(box.dataset.bit)) !== 0;
    });

    openDialog({
      title: 'Permissions of "' + entry.name + '"',
      fields: [
        h('table', { class: 'perm-grid' }, h('tbody', {}, rows)),
        field('Mode (octal)', octal),
        h('p', { class: 'sub' }, 'This changes only this ' + (entry.is_dir ? 'folder, not what is inside it' : 'file') + '.'),
      ],
      submitLabel: 'Apply',
      onSubmit: async () => {
        const mode = octal.value.trim();
        if (!/^0?[0-7]{3}$/.test(mode)) return 'The mode is three octal digits, like 0644 or 0755.';
        const result = await attempt('chmod', { path: joinPath(dir, entry.name), mode });
        if (!result.ok) return errorText(result);
        await load(dir);
        return null;
      },
    });
  }

  async function connect() {
    reconnect.hidden = true;
    message.textContent = 'Connecting…';
    const result = await api('POST', '/api/hosts/' + host.id + '/sessions', { kind: 'files', params: {} });
    if (disposed) return;
    if (!result.ok) {
      message.textContent = errorText(result);
      reconnect.hidden = false;
      return;
    }
    readonly = Boolean(result.data.readonly);
    newFolder.hidden = readonly;
    uploadButton.hidden = readonly;

    ws = openSocket(result.data.id);
    ws.onmessage = (event) => {
      if (typeof event.data !== 'string') return;
      let reply;
      try { reply = JSON.parse(event.data); } catch (err) { return; }
      const waiting = pending.get(reply.id);
      if (!waiting) return;
      pending.delete(reply.id);
      waiting.resolve(reply);
    };
    ws.onopen = async () => {
      message.textContent = '';
      if (dir === null) {
        // First time: start in the agent's home folder.
        const home = await attempt('home');
        await load(home.ok && home.data.path ? home.data.path : '/');
      } else {
        await load(dir, false); // after a reconnect: same folder, not a step in the history
      }
    };
    ws.onclose = () => {
      if (disposed) return;
      for (const waiting of pending.values()) waiting.reject(new Error('Not connected.'));
      pending.clear();
      message.textContent = 'Not connected.';
      reconnect.hidden = false;
    };
  }
  reconnect.addEventListener('click', connect);

  pageSession = {
    dispose() {
      disposed = true;
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
      for (const waiting of pending.values()) waiting.reject(new Error('Not connected.'));
      pending.clear();
      if (ws) ws.close(1000);
    },
  };
  connect();
}

// ------------------------------------------------------------ Docker page

function dockerHref(hostId) {
  return hostLink(hostId) + '/docker';
}

// The Docker tab exists only where it can work (M12, D-70): the host is online, the
// agent's heartbeat says it found a Docker Engine (`ok`) or found one it may not use
// (`no_access`, and then the page says how to fix that), and this person may open a
// shell (the Docker socket is root, so the server asks for the same role). A
// Windows host has none, and an agent older than 0.12.0 sends no `docker` at all.
function hasDocker(host) {
  const state = host.heartbeat && host.heartbeat.docker;
  return Boolean(host.online && host.can_shell && !isWindows(host) && (state === 'ok' || state === 'no_access'));
}

function renderDockerPage(host) {
  document.title = host.name + ' · headnode';
  const page = [
    h('a', { class: 'back', href: '#/' }, '← Hosts'),
    pageHead(host.name, null),
    h('div', { class: 'status' }, statusText(host)),
    hostTabs(host, 'docker'),
  ];
  const state = host.heartbeat && host.heartbeat.docker;
  if (isWindows(host)) {
    page.push(h('div', { class: 'notice' }, 'Docker is only supported on Linux hosts.'));
  } else if (!host.online) {
    page.push(h('div', { class: 'notice' },
      'Host offline. Make sure the headnode-agent service is running on this host, then reload this page.'));
  } else if (!host.can_shell) {
    page.push(h('div', { class: 'notice' },
      'Using Docker needs the same permission as a terminal (an operator of this host\'s group, or an administrator): ' +
      'the Docker socket is root on the host.'));
  } else if (state === 'no_access') {
    page.push(h('div', { class: 'notice' },
      'Docker is installed here, but the agent may not use it. Add the account the agent runs as (the User= line of its ' +
      'systemd unit) to the docker group, then restart the agent:'),
    h('pre', { class: 'docker-command' }, 'sudo usermod -aG docker <user>\nsudo systemctl restart headnode-agent'),
    h('p', { class: 'sub' }, 'Membership of the docker group is root on that machine: a container can mount the whole disk. ' +
      'On a host where you grant it, the agent is effectively root again. You decide that for each host.'));
  } else if (state !== 'ok') {
    page.push(h('div', { class: 'notice' },
      'The agent found no Docker Engine on this host (or its agent is older than 0.12.0). ' +
      'Install Docker and make sure it is running, then reload this page.'));
  } else {
    const box = h('div', { class: 'docker' });
    page.push(box);
    // Like the terminal and the file manager: built once, and never redrawn by the poll.
    queueMicrotask(() => mountDocker(host, box));
  }
  return page;
}

// What a stack button will run, for the dialogs. The agent gives the start of the
// command with the stack (`command`), built from the same labels it will use.
function stackCommand(stack, action, volumes) {
  const down = stack.command + ' down' + (volumes ? ' -v' : '');
  const up = stack.command + ' up -d';
  if (action === 'down') return down;
  if (action === 'up') return up;
  if (action === 'restart') return stack.command + ' restart';
  return down + ' && ' + up; // "recycle": down, then up again
}

// The Docker tab talks to the agent over one session WebSocket, like the file manager:
// a JSON request with an id, and an answer with the same id (protocol.md §4). Every
// answer carries the docker command that was done or tried, and the page shows it.
// Nothing here forces anything: when Docker refuses, its own words are shown.
function mountDocker(host, box) {
  let ws = null;
  let disposed = false;
  let loaded = false;
  let nextId = 1;
  const pending = new Map(); // request id -> { resolve, reject }
  const data = { stacks: [], containers: [], images: [], volumes: [], networks: [] };

  // View state: one tab at a time with contextual filters so large hosts stay readable.
  let activeTab = 'containers';
  let search = '';
  let stateFilter = 'all';     // containers: all | running | stopped
  let stackFilter = '';        // containers: '' | '__none__' | <project>
  let imagesUnused = false;    // images: only unused
  let volumeType = 'all';      // volumes: all | named | anonymous
  let volumesUnused = false;   // volumes: only unused
  const sorts = {
    containers: { key: null, asc: true },
    images: { key: 'size', asc: false },
    volumes: { key: null, asc: true },
  };

  let openMenu = null; // { el, owner }
  function closeMenu() {
    if (openMenu) {
      openMenu.el.remove();
      openMenu = null;
    }
  }
  const onDocClick = (event) => {
    if (openMenu && !openMenu.el.contains(event.target) && !event.target.closest('.docker-more')) closeMenu();
  };
  const onDocKey = (event) => {
    if (event.key === 'Escape') closeMenu();
  };
  document.addEventListener('click', onDocClick);
  document.addEventListener('keydown', onDocKey);

  const summary = h('div', { class: 'docker-summary sub' });
  const message = h('div', { class: 'docker-message' });
  const reconnect = h('button', { class: 'btn', type: 'button', hidden: '' }, 'Reconnect');
  const bar = h('div', { class: 'docker-bar' },
    iconButton('Refresh', ICON_REFRESH, () => reload()),
    button('Prune…', 'btn', () => pruneDialog()),
    reconnect,
    summary);
  const tabsBar = h('div', { class: 'docker-tabs', role: 'tablist', hidden: '' });
  const searchInput = h('input', { class: 'input input-small docker-search', type: 'search', placeholder: 'Filter…' });
  const filterStatus = h('div', { class: 'docker-filter-status sub' });
  const filtersBar = h('div', { class: 'docker-filters', hidden: '' });
  const tableWrap = h('div');
  box.replaceChildren(bar, message, tabsBar, filtersBar, tableWrap);

  searchInput.addEventListener('input', () => {
    search = searchInput.value;
    renderTable();
  });

  function request(op, fields) {
    return new Promise((resolve, reject) => {
      if (!ws || ws.readyState !== WebSocket.OPEN) {
        reject(new Error('Not connected.'));
        return;
      }
      const id = nextId++;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ id, op, ...fields }));
    });
  }

  // Never throws. The result has the shape api() gives, plus the whole answer in
  // data, so a refusal still has its `command`.
  async function attempt(op, fields) {
    try {
      const reply = await request(op, fields);
      return { ok: Boolean(reply.ok), status: 0, data: reply };
    } catch (err) {
      return { ok: false, status: 0, data: { error: err.message } };
    }
  }

  // Docker's own message, and the command that was tried.
  function failureText(result) {
    const command = result.data && result.data.command;
    return errorText(result) + (command ? '   (tried: ' + command + ')' : '');
  }

  // The line under the buttons: what just happened, with its command.
  function showMessage(text, command, bad, output) {
    message.className = 'docker-message' + (bad ? ' danger-text' : ' sub');
    message.replaceChildren(...[
      text,
      command ? h('code', { class: 'mono' }, ' ' + command) : null,
      output ? h('pre', { class: 'docker-pre' }, output) : null,
    ].filter((node) => node !== null));
  }

  // What every change does when Docker answered: a refusal stays in the dialog that
  // asked, so it can be read; success closes the dialog, says what ran, and reloads.
  function afterChange(result, doneText) {
    if (!result.ok) return failureText(result);
    showMessage(doneText || 'Done.', result.data.command, false, result.data.output);
    reload();
    return null;
  }

  async function reload() {
    const ops = ['stacks', 'containers', 'images', 'volumes', 'networks'];
    const results = await Promise.all(ops.map((op) => attempt(op)));
    if (disposed) return;
    const failed = results.find((result) => !result.ok);
    if (failed) showMessage(failureText(failed), null, true);
    ops.forEach((op, i) => {
      if (results[i].ok) data[op] = results[i].data[op] || [];
    });
    if (results.some((result) => result.ok)) {
      loaded = true;
      renderAll();
    }
  }

  // ---- helpers

  const row = (...cells) => h('tr', { class: 'static' }, cells);
  const actions = (...nodes) => h('td', { class: 'col-act' }, h('div', { class: 'docker-actions' }, nodes));
  const list = (items) => (items && items.length > 0 ? items.join(', ') : '—');
  const imageName = (img) => (img.tags && img.tags.length > 0
    ? img.tags.join(', ')
    : '<none> (' + img.id.replace('sha256:', '').slice(0, 12) + ')');

  function containerProjects() {
    const set = new Set();
    data.stacks.forEach((s) => { if (s.project) set.add(s.project); });
    data.containers.forEach((c) => { if (c.project) set.add(c.project); });
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }

  function hasActiveFilter() {
    if (search.trim()) return true;
    if (activeTab === 'containers') return stateFilter !== 'all' || stackFilter !== '';
    if (activeTab === 'images') return imagesUnused;
    if (activeTab === 'volumes') return volumeType !== 'all' || volumesUnused;
    return false;
  }

  function clearFilters() {
    search = '';
    searchInput.value = '';
    if (activeTab === 'containers') { stateFilter = 'all'; stackFilter = ''; }
    if (activeTab === 'images') imagesUnused = false;
    if (activeTab === 'volumes') { volumeType = 'all'; volumesUnused = false; }
    renderFilters();
    renderTable();
  }

  function switchTab(tab) {
    if (activeTab === tab) return;
    activeTab = tab;
    search = '';
    searchInput.value = '';
    renderAll();
  }

  function showStackContainers(project) {
    activeTab = 'containers';
    search = '';
    searchInput.value = '';
    stateFilter = 'all';
    stackFilter = project;
    renderAll();
  }

  function moreButton(items, disabled) {
    const more = iconOnly('More', ICON_MORE, () => {
      if (openMenu && openMenu.owner === more) {
        closeMenu();
        return;
      }
      closeMenu();
      const menu = h('div', { class: 'menu menu-right', role: 'menu' },
        items.filter(Boolean).map((entry) => button(
          entry.label,
          entry.danger ? 'menu-item danger-text' : 'menu-item',
          () => { closeMenu(); entry.run(); })));
      more.parentElement.append(menu);
      openMenu = { el: menu, owner: more };
    }, 'docker-more');
    if (disabled) more.disabled = true;
    return h('span', { class: 'menu-anchor' }, more);
  }

  function sortHeader(tab, label, key, defaultAsc = true) {
    const s = sorts[tab];
    const active = s.key === key;
    const arrow = active ? (s.asc ? ' ▲' : ' ▼') : '';
    const sortBtn = button(label + arrow, 'th-sort', () => {
      if (s.key === key) s.asc = !s.asc;
      else { s.key = key; s.asc = defaultAsc; }
      renderTable();
    });
    return h('th', { 'aria-sort': active ? (s.asc ? 'ascending' : 'descending') : null }, sortBtn);
  }

  function table(headers, rows, empty) {
    if (rows.length === 0) return h('div', { class: 'empty small' }, empty);
    return h('table', {},
      h('thead', {}, h('tr', {}, headers.map((hdr) => (hdr instanceof Node
        ? hdr
        : h('th', { class: hdr === '' ? 'col-act' : null }, hdr))))),
      h('tbody', {}, rows));
  }

  // ---- rendering

  function renderAll() {
    tabsBar.hidden = !loaded;
    filtersBar.hidden = !loaded;
    if (!loaded) return;
    renderSummary();
    renderTabs();
    renderFilters();
    renderTable();
  }

  function renderSummary() {
    const running = data.containers.filter((c) => c.state === 'running').length;
    const imgBytes = data.images.reduce((n, img) => n + (img.size || 0), 0);
    const unusedImgBytes = data.images
      .filter((img) => !img.used_by || img.used_by.length === 0)
      .reduce((n, img) => n + (img.size || 0), 0);
    summary.textContent = [
      running + '/' + data.containers.length + ' running',
      count(data.stacks.length, 'stack', 'stacks'),
      formatBytes(imgBytes) + ' images' + (unusedImgBytes > 0 ? ' (' + formatBytes(unusedImgBytes) + ' unused)' : ''),
    ].join(' · ');
  }

  function renderTabs() {
    const running = data.containers.filter((c) => c.state === 'running').length;
    const tabs = [
      { id: 'containers', label: 'Containers', count: data.containers.length ? running + '/' + data.containers.length : '0' },
      { id: 'stacks', label: 'Stacks', count: String(data.stacks.length) },
      { id: 'images', label: 'Images', count: String(data.images.length) },
      { id: 'volumes', label: 'Volumes', count: String(data.volumes.length) },
      { id: 'networks', label: 'Networks', count: String(data.networks.length) },
    ];
    tabsBar.replaceChildren(...tabs.map((tab) => {
      const btn = h('button', {
        type: 'button',
        role: 'tab',
        'aria-selected': activeTab === tab.id ? 'true' : 'false',
        class: 'docker-tab' + (activeTab === tab.id ? ' active' : ''),
      }, tab.label, h('span', { class: 'num' }, tab.count));
      btn.addEventListener('click', () => switchTab(tab.id));
      return btn;
    }));
  }

  function renderFilters() {
    searchInput.placeholder = 'Filter ' + activeTab + '…';
    searchInput.value = search;
    const extras = [];

    if (activeTab === 'containers') {
      const stateSelect = h('select', { class: 'input input-small', 'aria-label': 'Filter by state' },
        h('option', { value: 'all', selected: stateFilter === 'all' ? '' : null }, 'All states'),
        h('option', { value: 'running', selected: stateFilter === 'running' ? '' : null }, 'Running'),
        h('option', { value: 'stopped', selected: stateFilter === 'stopped' ? '' : null }, 'Stopped'));
      stateSelect.addEventListener('change', () => { stateFilter = stateSelect.value; renderTable(); });
      extras.push(stateSelect);

      const projects = containerProjects();
      if (stackFilter && stackFilter !== '__none__' && !projects.includes(stackFilter)) stackFilter = '';
      if (projects.length > 0) {
        const stackSelect = h('select', { class: 'input input-small', 'aria-label': 'Filter by stack' },
          h('option', { value: '', selected: stackFilter === '' ? '' : null }, 'All stacks'),
          h('option', { value: '__none__', selected: stackFilter === '__none__' ? '' : null }, 'Standalone'),
          ...projects.map((p) => h('option', { value: p, selected: stackFilter === p ? '' : null }, p)));
        stackSelect.addEventListener('change', () => { stackFilter = stackSelect.value; renderTable(); });
        extras.push(stackSelect);
      }
    } else if (activeTab === 'images') {
      const unusedBox = h('input', { type: 'checkbox', checked: imagesUnused ? '' : null });
      unusedBox.addEventListener('change', () => { imagesUnused = unusedBox.checked; renderTable(); });
      extras.push(h('label', { class: 'toggle' }, unusedBox, 'Unused only'));
    } else if (activeTab === 'volumes') {
      const typeSelect = h('select', { class: 'input input-small', 'aria-label': 'Filter by volume type' },
        h('option', { value: 'all', selected: volumeType === 'all' ? '' : null }, 'All types'),
        h('option', { value: 'named', selected: volumeType === 'named' ? '' : null }, 'Named'),
        h('option', { value: 'anonymous', selected: volumeType === 'anonymous' ? '' : null }, 'Anonymous'));
      typeSelect.addEventListener('change', () => { volumeType = typeSelect.value; renderTable(); });
      const unusedBox = h('input', { type: 'checkbox', checked: volumesUnused ? '' : null });
      unusedBox.addEventListener('change', () => { volumesUnused = unusedBox.checked; renderTable(); });
      extras.push(typeSelect, h('label', { class: 'toggle' }, unusedBox, 'Unused only'));
    }

    filtersBar.replaceChildren(searchInput, ...extras, filterStatus);
  }

  function updateFilterStatus(shown, total) {
    if (!hasActiveFilter()) {
      filterStatus.replaceChildren();
      return;
    }
    filterStatus.replaceChildren(
      h('span', { class: 'num' }, shown + ' of ' + total),
      button('Clear', 'link-button', clearFilters));
  }

  function matches(q, ...values) {
    if (!q) return true;
    return values.some((v) => v && String(v).toLowerCase().includes(q));
  }

  function renderTable() {
    closeMenu();
    const q = search.trim().toLowerCase();
    const emptyMsg = (noun) => (hasActiveFilter() ? 'No matching ' + noun + '.' : 'No ' + noun + '.');
    let note = null;
    let content = null;

    if (activeTab === 'containers') {
      let items = data.containers.filter((c) => {
        if (stateFilter === 'running' && c.state !== 'running') return false;
        if (stateFilter === 'stopped' && c.state === 'running') return false;
        if (stackFilter === '__none__' && c.project) return false;
        if (stackFilter && stackFilter !== '__none__' && c.project !== stackFilter) return false;
        return matches(q, c.name, c.image, c.project, c.service, c.state, c.status, (c.ports || []).join(' '));
      });
      const s = sorts.containers;
      if (s.key) {
        items = items.slice().sort((a, b) => {
          const d = s.key === 'name'
            ? a.name.localeCompare(b.name)
            : (a.state === 'running' ? 0 : 1) - (b.state === 'running' ? 0 : 1)
              || a.state.localeCompare(b.state)
              || a.name.localeCompare(b.name);
          return s.asc ? d : -d;
        });
      }
      updateFilterStatus(items.length, data.containers.length);
      content = table(
        [sortHeader('containers', 'Container', 'name'), 'Image', sortHeader('containers', 'State', 'state'), 'Ports', ''],
        items.map(containerRow),
        emptyMsg('containers'));
    } else if (activeTab === 'stacks') {
      note = 'Compose projects, found from the labels of their containers. Click a stack to view its containers.';
      const items = data.stacks.filter((s) => matches(q, s.project, s.working_dir, (s.services || []).join(' ')));
      updateFilterStatus(items.length, data.stacks.length);
      content = table(['Stack', 'Services', 'Containers', ''], items.map(stackRow), emptyMsg('Compose stacks'));
    } else if (activeTab === 'images') {
      let items = data.images.filter((img) => {
        if (imagesUnused && img.used_by && img.used_by.length > 0) return false;
        return matches(q, imageName(img), img.id, (img.used_by || []).join(' '));
      });
      const s = sorts.images;
      if (s.key) {
        items = items.slice().sort((a, b) => {
          const d = s.key === 'size'
            ? (a.size || 0) - (b.size || 0)
            : s.key === 'created'
              ? Date.parse(a.created || 0) - Date.parse(b.created || 0)
              : imageName(a).localeCompare(imageName(b));
          return s.asc ? d : -d;
        });
      }
      updateFilterStatus(items.length, data.images.length);
      content = table(
        [sortHeader('images', 'Image', 'name'), sortHeader('images', 'Size', 'size', false), sortHeader('images', 'Created', 'created', false), 'Used by', ''],
        items.map(imageRow),
        emptyMsg('images'));
    } else if (activeTab === 'volumes') {
      let items = data.volumes.filter((vol) => {
        if (volumeType === 'named' && vol.anonymous) return false;
        if (volumeType === 'anonymous' && !vol.anonymous) return false;
        if (volumesUnused && vol.used_by && vol.used_by.length > 0) return false;
        return matches(q, vol.name, vol.project, vol.mountpoint, (vol.used_by || []).join(' '));
      });
      const s = sorts.volumes;
      if (s.key) {
        items = items.slice().sort((a, b) => {
          const d = s.key === 'name'
            ? a.name.localeCompare(b.name)
            : (a.project || '').localeCompare(b.project || '') || a.name.localeCompare(b.name);
          return s.asc ? d : -d;
        });
      }
      updateFilterStatus(items.length, data.volumes.length);
      content = table(
        [sortHeader('volumes', 'Volume', 'name'), 'Used by', sortHeader('volumes', 'Stack', 'project'), 'Where'],
        items.map(volumeRow),
        emptyMsg('volumes'));
    } else if (activeTab === 'networks') {
      note = 'Containers on the same network reach each other by name.';
      const items = data.networks.filter((net) => matches(
        q, net.name, net.driver,
        (net.containers || []).map((m) => m.name + ' ' + (m.address || '')).join(' ')));
      updateFilterStatus(items.length, data.networks.length);
      content = table(['Network', 'Driver', 'Containers'], items.map(networkRow), emptyMsg('networks'));
    }

    if (note) tableWrap.replaceChildren(h('p', { class: 'sub docker-note' }, note), content);
    else tableWrap.replaceChildren(content);
  }

  function stackRow(stack) {
    const missing = stack.missing_files && stack.missing_files.length > 0;
    const restartBtn = button('Restart', 'btn btn-small', () => stackDialog(stack, 'restart'));
    if (missing) restartBtn.disabled = true;
    const menu = moreButton([
      { label: 'Up', run: () => stackDialog(stack, 'up') },
      { label: 'Down', danger: true, run: () => stackDialog(stack, 'down') },
      { label: 'Down and up again', danger: true, run: () => stackDialog(stack, 'recycle') },
    ], missing);
    return row(
      h('td', {},
        button(stack.project, 'link-button name', () => showStackContainers(stack.project)),
        h('div', { class: 'sub mono' }, stack.working_dir || ''),
        missing ? h('div', { class: 'danger-text' }, 'The compose file ' + stack.missing_files[0] + ' no longer exists on this host.') : null),
      h('td', {}, list(stack.services)),
      h('td', { class: stack.running < stack.total ? 'warn' : null }, stack.running + ' of ' + stack.total + ' running'),
      actions(restartBtn, menu));
  }

  function containerRow(c) {
    const running = c.state === 'running';
    const primary = running
      ? button('Stop', 'btn btn-small', () => containerDialog(c, 'stop'))
      : button('Start', 'btn btn-small', async () => { // harmless, so no dialog: the result shows on the line above
        const result = await attempt('start', { container: c.name });
        if (disposed) return;
        if (result.ok) afterChange(result, 'Started.');
        else showMessage(failureText(result), null, true);
      });
    const menu = moreButton([
      { label: 'Inspect', run: () => inspectDialog(c) },
      running ? { label: 'Restart', run: () => containerDialog(c, 'restart') } : null,
      { label: 'Remove', danger: true, run: () => containerDialog(c, 'remove') },
    ]);
    return row(
      h('td', {}, h('span', { class: 'name' }, c.name),
        c.project ? h('div', { class: 'sub' }, c.project + ' / ' + c.service) : null),
      h('td', {}, h('span', { class: 'mono' }, c.image),
        // Which commit runs here: the delivery question of the course.
        c.revision ? h('div', { class: 'sub mono', title: c.revision }, 'commit ' + c.revision.slice(0, 7)) : null),
      h('td', {}, running ? h('span', {}, h('span', { class: 'dot' }), 'running') : h('span', { class: 'sub' }, c.state),
        h('div', { class: 'sub' }, c.status)),
      h('td', { class: 'mono' }, list(c.ports)),
      actions(button('Logs', 'btn btn-small', () => logsDialog(c)), primary, menu));
  }

  function imageRow(img) {
    const name = imageName(img);
    return row(
      h('td', { class: 'mono' }, name, img.dangling ? h('span', { class: 'tag' }, 'dangling') : null),
      h('td', { class: 'num' }, formatBytes(img.size)),
      h('td', { class: 'sub' }, relativeTime(img.created)),
      h('td', {}, list(img.used_by)),
      actions(button('Remove', 'btn btn-small btn-danger-text', () => imageDialog(img, name))));
  }

  function volumeRow(vol) {
    return row(
      h('td', {}, h('span', { class: 'mono' }, vol.anonymous ? vol.name.slice(0, 12) + '…' : vol.name),
        h('span', { class: 'tag' }, vol.anonymous ? 'anonymous' : 'named')),
      h('td', {}, list(vol.used_by)),
      h('td', {}, vol.project || '—'),
      h('td', { class: 'sub mono' }, vol.mountpoint));
  }

  function networkRow(net) {
    const members = (net.containers || []).map((m) => m.name + (m.address ? ' (' + m.address + ')' : ''));
    return row(h('td', {}, h('span', { class: 'name' }, net.name)), h('td', {}, net.driver), h('td', {}, list(members)));
  }

  // ---- dialogs: each shows the command it will run, and names the object

  const commandLine = (command) => h('pre', { class: 'docker-command' }, '$ ' + command);

  async function copyText(text) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch (err) {
      const area = h('textarea', { class: 'clipboard-helper', readonly: '', 'aria-hidden': 'true' });
      area.value = text;
      document.body.append(area);
      area.select();
      let copied = false;
      try { copied = document.execCommand('copy'); } catch (e) { /* stays false */ }
      area.remove();
      return copied;
    }
  }

  function commandDialog({ title, text, command, confirmLabel, danger, op, fields, doneText }) {
    openDialog({
      title,
      danger,
      submitLabel: confirmLabel,
      fields: [h('p', {}, text), commandLine(command)],
      onSubmit: async () => afterChange(await attempt(op, fields), doneText),
    });
  }

  function containerDialog(c, op) {
    const texts = {
      stop: 'Stop the container "' + c.name + '"? Docker asks it to stop and kills it after 10 seconds.',
      restart: 'Restart the container "' + c.name + '"?',
      remove: 'Remove the container "' + c.name + '"? Its own filesystem is deleted. Its anonymous volumes are not (no -v), ' +
        'and nothing is forced: Docker refuses to remove a running container, so stop it first.',
    };
    const labels = { stop: 'Stop', restart: 'Restart', remove: 'Remove' };
    commandDialog({
      title: labels[op] + ' ' + c.name, text: texts[op], command: 'docker ' + (op === 'remove' ? 'rm' : op) + ' ' + c.name,
      confirmLabel: labels[op], danger: op === 'remove', op, fields: { container: c.name },
      doneText: { stop: 'Stopped.', restart: 'Restarted.', remove: 'Removed.' }[op],
    });
  }

  function imageDialog(img, name) {
    commandDialog({
      title: 'Remove image ' + name,
      text: 'Remove the image "' + name + '" (' + formatBytes(img.size) + ')? Nothing is forced: Docker refuses an image that a container ' +
        'uses, or one with several tags, and says so.',
      command: 'docker rmi ' + img.id, confirmLabel: 'Remove image', danger: true,
      op: 'remove_image', fields: { image: img.id }, doneText: 'Removed.',
    });
  }

  function stackDialog(stack, action) {
    const volumesBox = h('input', { type: 'checkbox' });
    const volumesRow = h('label', { class: 'check' }, volumesBox,
      'Also delete the stack\'s named volumes (-v). Their data is lost.');
    volumesRow.hidden = action !== 'down' && action !== 'recycle';
    const preview = h('pre', { class: 'docker-command' });
    const wantsVolumes = () => !volumesRow.hidden && volumesBox.checked;
    const update = () => { preview.textContent = '$ ' + stackCommand(stack, action, wantsVolumes()); };
    volumesBox.addEventListener('change', update);
    update();

    const texts = {
      down: 'Stop and remove the containers and networks of "' + stack.project + '". Named volumes keep their data unless you tick the box.',
      up: 'Create and start the containers of "' + stack.project + '" from its compose file.',
      restart: 'Restart the containers of "' + stack.project + '".',
      recycle: 'Take "' + stack.project + '" down and bring it up again. Data in a named volume survives, unless you tick the box: ' +
        'that is the test the box is for.',
    };
    const labels = { down: 'Down', up: 'Up', restart: 'Restart', recycle: 'Down and up again' };
    openDialog({
      title: labels[action] + ': ' + stack.project,
      danger: action === 'down' || action === 'recycle',
      submitLabel: labels[action],
      fields: [h('p', {}, texts[action]), volumesRow, preview],
      onSubmit: async () => afterChange(await attempt('stack', { project: stack.project, action, volumes: wantsVolumes() })),
    });
  }

  function pruneDialog() {
    const what = h('select', { class: 'input' },
      h('option', { value: 'containers' }, 'Stopped containers'),
      h('option', { value: 'images' }, 'Unused images'),
      h('option', { value: 'build_cache' }, 'Build cache'));
    const allBox = h('input', { type: 'checkbox' });
    const allRow = h('label', { class: 'check' }, allBox,
      'Every image that no container uses, not only the dangling ones (layers with no tag)');
    const explain = h('p', {});
    const preview = h('pre', { class: 'docker-command' });
    const update = () => {
      allRow.hidden = what.value !== 'images';
      const all = what.value === 'images' && allBox.checked;
      explain.textContent = {
        containers: 'Removes every container that is stopped.',
        images: all ? 'Removes EVERY image that no container uses, tagged or not.' : 'Removes dangling images: layers with no tag, left by rebuilds.',
        build_cache: 'Removes the build cache.',
      }[what.value];
      preview.textContent = '$ ' + {
        containers: 'docker container prune -f',
        images: all ? 'docker image prune -a -f' : 'docker image prune -f',
        build_cache: 'docker builder prune -f',
      }[what.value];
    };
    what.addEventListener('change', update);
    allBox.addEventListener('change', update);
    update();

    openDialog({
      title: 'Prune',
      danger: true,
      submitLabel: 'Prune',
      fields: [field('What to remove', what), allRow, explain, preview],
      onSubmit: async () => {
        const result = await attempt('prune', { what: what.value, all: what.value === 'images' && allBox.checked });
        if (!result.ok) return failureText(result);
        const removed = (result.data.removed || []).length;
        showMessage('Removed ' + count(removed, 'item', 'items') + ', reclaimed ' + formatBytes(result.data.space_reclaimed || 0) + '.', result.data.command, false);
        reload();
        return null;
      },
    });
  }

  // Logs and Inspect are reads: a window with the text and the command behind it.
  function textDialog({ title, load, toolbar }) {
    const command = h('pre', { class: 'docker-command' });
    const pre = h('pre', { class: 'docker-pre' }, 'Loading…');
    const copyBtn = button('Copy', 'btn', async () => {
      const ok = await copyText(pre.textContent);
      copyBtn.textContent = ok ? 'Copied' : 'Copy failed';
      setTimeout(() => { copyBtn.textContent = 'Copy'; }, 1500);
    });
    const run = async () => {
      const result = await load(pre);
      if (disposed) return;
      command.textContent = '$ ' + ((result.data && result.data.command) || '');
      if (result.ok) {
        if (result.text !== undefined) pre.textContent = result.text;
      } else {
        pre.textContent = errorText(result);
      }
      pre.scrollTop = pre.scrollHeight; // logs: the newest line is at the bottom
    };
    const barNodes = toolbar ? toolbar(run, pre, copyBtn) : h('div', { class: 'docker-toolbar' }, copyBtn);
    openDialog({
      title, wide: true, cancelLabel: null, submitLabel: 'Close',
      fields: [barNodes, command, pre],
      onSubmit: async () => null,
    });
    run();
  }

  function logsDialog(c) {
    let rawLogs = '';
    let truncated = false;
    let preEl = null;
    const tail = h('select', { class: 'input input-small' },
      [100, 200, 500, 2000].map((n) => h('option', { value: String(n), selected: n === 200 ? '' : null }, 'Last ' + n + ' lines')));
    const filterInput = h('input', { class: 'input input-small docker-log-filter', type: 'search', placeholder: 'Filter lines…' });
    const matchInfo = h('span', { class: 'sub num' });

    function applyFilter() {
      if (!preEl) return;
      const q = filterInput.value.trim().toLowerCase();
      if (!rawLogs) {
        matchInfo.textContent = '';
        preEl.textContent = '(no output)';
        return;
      }
      if (!q) {
        matchInfo.textContent = '';
        preEl.textContent = rawLogs + (truncated ? '\n… (the beginning was cut to fit)' : '');
        return;
      }
      const lines = rawLogs.split('\n');
      const matched = lines.filter((line) => line.toLowerCase().includes(q));
      matchInfo.textContent = matched.length + ' / ' + lines.length + ' lines';
      preEl.textContent = matched.length > 0 ? matched.join('\n') : '(no matching lines)';
    }

    textDialog({
      title: 'Logs of ' + c.name,
      toolbar: (run, pre, copyBtn) => {
        preEl = pre;
        tail.addEventListener('change', run);
        filterInput.addEventListener('input', applyFilter);
        return h('div', { class: 'docker-toolbar' }, tail, filterInput, button('Refresh', 'btn', run), copyBtn, matchInfo);
      },
      load: async () => {
        const result = await attempt('logs', { container: c.name, tail: Number(tail.value) });
        if (result.ok) {
          rawLogs = result.data.logs || '';
          truncated = Boolean(result.data.truncated);
          applyFilter();
        }
        return result;
      },
    });
  }

  function inspectDialog(c) {
    textDialog({
      title: 'Inspect ' + c.name,
      load: async () => {
        const result = await attempt('inspect', { container: c.name });
        if (result.ok) result.text = '// environment values are hidden\n' + JSON.stringify(result.data.inspect, null, 2);
        return result;
      },
    });
  }

  // ---- the session

  async function connect() {
    reconnect.hidden = true;
    showMessage('Connecting…', null, false);
    const result = await api('POST', '/api/hosts/' + host.id + '/sessions', { kind: 'docker', params: {} });
    if (disposed) return;
    if (!result.ok) {
      showMessage(errorText(result), null, true);
      reconnect.hidden = false;
      return;
    }
    ws = openSocket(result.data.id);
    ws.onmessage = (event) => {
      if (typeof event.data !== 'string') return;
      let reply;
      try { reply = JSON.parse(event.data); } catch (err) { return; }
      const waiting = pending.get(reply.id);
      if (!waiting) return;
      pending.delete(reply.id);
      waiting.resolve(reply);
    };
    ws.onopen = () => {
      showMessage('', null, false);
      reload();
    };
    ws.onclose = () => {
      if (disposed) return;
      for (const waiting of pending.values()) waiting.reject(new Error('Not connected.'));
      pending.clear();
      showMessage('Not connected.', null, true);
      reconnect.hidden = false;
    };
  }
  reconnect.addEventListener('click', connect);

  pageSession = {
    dispose() {
      disposed = true;
      closeMenu();
      document.removeEventListener('click', onDocClick);
      document.removeEventListener('keydown', onDocKey);
      for (const waiting of pending.values()) waiting.reject(new Error('Not connected.'));
      pending.clear();
      if (ws) ws.close(1000);
    },
  };
  connect();
}

// ------------------------------------------------------------- Users page

// The one picture every user has (D-69): there is no upload, so the same file is
// shown for everybody, in the sidebar and on the Profile page.
const PICTURE = '/sadaharu.jpg';

// What a person is called on screen: their display name, or else their username.
const displayName = (user) => user.display_name || user.username;

function renderUsers(users) {
  return [
    pageHead('Users', 'People who can log in to headnode.', button('Add user', 'btn btn-primary', addUserDialog)),
    h('div', { class: 'pill' }, count(users.length, 'user', 'users')),
    h('table', {},
      h('thead', {}, h('tr', {},
        h('th', {}, 'Username'),
        h('th', {}, 'Role'),
        h('th', {}, 'Created'),
        h('th', { class: 'col-act' }, 'Action'))),
      h('tbody', {}, users.map((u) => h('tr', { class: u.disabled ? 'static user-disabled' : 'static' },
        h('td', {},
          h('span', { class: 'name' }, u.username),
          u.id === me.id ? h('span', { class: 'tag' }, 'you') : null,
          u.disabled ? h('span', { class: 'tag' }, 'Disabled') : null,
          // A display name is only a label: the username is what the audit log shows.
          u.display_name ? h('div', { class: 'sub' }, u.display_name) : null),
        h('td', {}, u.is_admin ? 'Administrator' : 'User'),
        h('td', { class: 'sub', title: new Date(u.created_at).toLocaleString() }, relativeTime(u.created_at)),
        // A wrapper inside the cell: "display: flex" on a td itself would stop it being a table cell.
        h('td', { class: 'col-act' }, h('div', { class: 'user-actions' }, ...userActions(u))))))),
    h('p', { class: 'sub note' }, 'Administrators see every host and manage users and groups. Everyone else sees only the hosts in their groups. ' +
      'A disabled user cannot log in, and is logged out at once, shells included; nothing is deleted, so the audit log still names them.'),
  ];
}

// The buttons of a user's row. Nobody can disable or demote themselves, or reset their own
// password here (that asks for the current one, on the Profile page): the server refuses it
// too (D-69), so these are only left out so as not to offer what would be refused.
function userActions(u) {
  if (u.id === me.id) return [h('span', { class: 'sub' }, 'Your Profile page has your own account')];
  const set = (fields) => api('PATCH', '/api/users/' + u.id, fields);
  return [
    button('Reset password', 'btn btn-small', () => resetPasswordDialog(u)),
    button(u.is_admin ? 'Remove admin' : 'Make admin', 'btn btn-small', () => confirmDialog({
      title: (u.is_admin ? 'Remove administrator rights from ' : 'Make ') + u.username + (u.is_admin ? '?' : ' an administrator?'),
      message: u.is_admin
        ? u.username + ' will see only the hosts in their groups, and can no longer manage users, groups or uploads.'
        : u.username + ' will see every host, and can manage users, groups and uploads.',
      confirmLabel: u.is_admin ? 'Remove admin' : 'Make admin',
      onConfirm: () => set({ is_admin: !u.is_admin }),
    })),
    button(u.disabled ? 'Enable' : 'Disable', u.disabled ? 'btn btn-small' : 'btn btn-small danger-text', () => confirmDialog({
      title: (u.disabled ? 'Enable ' : 'Disable ') + u.username + '?',
      message: u.disabled
        ? u.username + ' will be able to log in again, with the password they had.'
        : u.username + ' cannot log in any more, and is logged out now: their browsers, their hnctl tokens, and every shell, ' +
          'file session and tunnel they have open end at once. Nothing is deleted.',
      confirmLabel: u.disabled ? 'Enable' : 'Disable',
      onConfirm: () => set({ disabled: !u.disabled }),
    })),
  ];
}

function resetPasswordDialog(u) {
  const password = h('input', { class: 'input', name: 'password', type: 'password', autocomplete: 'new-password', required: '', minlength: '8', maxlength: '72' });
  openDialog({
    title: 'Reset the password of ' + u.username,
    fields: [
      h('p', {}, 'Choose a new password and give it to ' + u.username + '. All their sessions end now: their browsers, their hnctl tokens and what they have open.'),
      field('New password (8 to 72 characters)', password),
    ],
    submitLabel: 'Reset password',
    onSubmit: async () => {
      const result = await api('POST', '/api/users/' + u.id + '/password', { password: password.value });
      return result.ok ? null : errorText(result);
    },
  });
}

function addUserDialog() {
  const username = h('input', { class: 'input', name: 'username', autocomplete: 'off', required: '', maxlength: '32' });
  const password = h('input', { class: 'input', name: 'password', type: 'password', autocomplete: 'new-password', required: '', minlength: '8', maxlength: '72' });
  const admin = h('input', { type: 'checkbox', name: 'is_admin' });

  openDialog({
    title: 'Add user',
    fields: [
      field('Username', username),
      field('Password (8 to 72 characters)', password),
      h('label', { class: 'check' }, admin, 'Administrator'),
    ],
    submitLabel: 'Add user',
    onSubmit: async () => {
      const result = await api('POST', '/api/users', {
        username: username.value,
        password: password.value,
        is_admin: admin.checked,
      });
      return result.ok ? null : errorText(result);
    },
  });
}

// ------------------------------------------------------------- Profile page

// A person's own account (M11, D-69): display name, password, and the logins that are
// open. Static, like the terminal: it holds forms, and the 2-second poll must not
// redraw them while someone types. It loads and redraws its own list of sessions.
function renderProfile() {
  document.title = 'Profile · headnode';
  const sessionsBox = h('div', {});
  queueMicrotask(() => loadSessions(sessionsBox));
  return [
    pageHead('Profile', 'Your own account.', null),
    h('div', { class: 'profile-head' },
      h('img', { class: 'profile-picture', src: PICTURE, alt: '' }),
      h('div', {},
        h('div', { class: 'name' }, displayName(me)),
        h('div', { class: 'sub' }, me.username + ' · ' + (me.is_admin ? 'Administrator' : 'User')))),
    h('h2', {}, 'Display name'),
    profileNameForm(),
    h('h2', {}, 'Password'),
    profilePasswordForm(() => loadSessions(sessionsBox)),
    h('h2', {}, 'Where you are logged in'),
    sessionsBox,
  ];
}

// A message under a form: plain, or red when it is an error.
function formMessage() {
  const box = h('div', { role: 'status' });
  box.show = (text, isError) => {
    box.textContent = text;
    box.className = isError ? 'form-error' : 'form-ok';
  };
  return box;
}

function profileNameForm() {
  const input = h('input', { class: 'input', name: 'display_name', autocomplete: 'off', maxlength: '64', placeholder: me.username, value: me.display_name || '' });
  const message = formMessage();
  const save = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save');
  const form = h('form', { class: 'profile-form' },
    field('Shown in the sidebar instead of your username (optional). The username itself never changes: the audit log shows it.', input),
    message, save);
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    save.disabled = true;
    const result = await api('PATCH', '/api/me', { display_name: input.value });
    save.disabled = false;
    if (!result.ok) {
      message.show(errorText(result), true);
      return;
    }
    me.display_name = result.data.display_name;
    renderUserBox(); // the sidebar changes at once
    message.show('Saved.', false);
  });
  return form;
}

function profilePasswordForm(reloadSessions) {
  const current = h('input', { class: 'input', name: 'current', type: 'password', autocomplete: 'current-password', required: '' });
  const next = h('input', { class: 'input', name: 'new', type: 'password', autocomplete: 'new-password', required: '', minlength: '8', maxlength: '72' });
  const again = h('input', { class: 'input', name: 'again', type: 'password', autocomplete: 'new-password', required: '', minlength: '8', maxlength: '72' });
  const message = formMessage();
  const save = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Change password');
  const form = h('form', { class: 'profile-form' },
    field('Current password', current),
    field('New password (8 to 72 characters)', next),
    field('New password again', again),
    message, save);
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (next.value !== again.value) {
      message.show('The two new passwords are not the same.', true);
      return;
    }
    save.disabled = true;
    const result = await api('POST', '/api/me/password', { current: current.value, new: next.value });
    save.disabled = false;
    if (!result.ok) {
      message.show(errorText(result), true);
      return;
    }
    form.reset();
    message.show('Password changed. Every other login of yours was signed out.', false);
    reloadSessions(); // they are gone from the list
  });
  return form;
}

// The logins of this user: browsers, and hnctl tokens. Each can be signed out, except the
// one in use (that is Log out), and all the others at once.
async function loadSessions(box) {
  const result = await api('GET', '/api/me/sessions');
  if (!result.ok) {
    box.replaceChildren(h('div', { class: 'form-error' }, errorText(result)));
    return;
  }
  const list = result.data;
  const reload = () => loadSessions(box);
  const others = list.filter((s) => !s.current).length;

  const signOutOthers = button('Sign out everywhere else', 'btn', () => confirmDialog({
    title: 'Sign out everywhere else?',
    message: 'Every other browser and hnctl login of yours ends now. This one stays.',
    confirmLabel: 'Sign out',
    onConfirm: async () => {
      const done = await api('POST', '/api/me/sessions/revoke-others');
      if (done.ok) reload();
      return done;
    },
  }));
  signOutOthers.disabled = others === 0;

  box.replaceChildren(
    h('table', {},
      h('thead', {}, h('tr', {},
        h('th', {}, 'Where'),
        h('th', {}, 'Address'),
        h('th', {}, 'Logged in'),
        h('th', {}, 'Last used'),
        h('th', { class: 'col-act' }, ''))),
      h('tbody', {}, list.map((s) => h('tr', { class: 'static' },
        h('td', {}, s.client === 'cli' ? 'hnctl (command line)' : 'Browser', s.current ? h('span', { class: 'tag' }, 'this one') : null),
        h('td', { class: 'sub' }, s.ip || '—'),
        h('td', { class: 'sub', title: new Date(s.created_at).toLocaleString() }, relativeTime(s.created_at)),
        h('td', { class: 'sub', title: new Date(s.last_used_at).toLocaleString() }, relativeTime(s.last_used_at)),
        h('td', { class: 'col-act' }, s.current ? null : button('Sign out', 'btn btn-small', async (event) => {
          event.target.disabled = true;
          await api('DELETE', '/api/me/sessions/' + s.id);
          reload();
        })))))),
    h('div', { class: 'profile-actions' }, signOutOthers));
}

// ------------------------------------------------------------ Groups pages

function renderGroups(groups) {
  const page = [
    pageHead('Groups', 'A group decides who can see its hosts, and what they can do with them.',
      button('Add group', 'btn btn-primary', addGroupDialog)),
    h('div', { class: 'pill' }, count(groups.length, 'group', 'groups')),
  ];
  if (groups.length === 0) {
    page.push(h('div', { class: 'empty' }, 'No groups yet. Add a group, then add users and hosts to it.'));
    return page;
  }
  page.push(h('table', {},
    h('thead', {}, h('tr', {},
      h('th', {}, 'Group'),
      h('th', {}, 'Members'),
      h('th', {}, 'Hosts'))),
    h('tbody', {}, groups.map((g) => {
      const row = h('tr', {},
        h('td', {}, h('a', { class: 'name', href: '#/groups/' + g.id }, g.name)),
        h('td', { class: 'num' }, String(g.member_count)),
        h('td', { class: 'num' }, String(g.host_count)));
      row.addEventListener('click', () => { location.hash = '#/groups/' + g.id; });
      return row;
    }))));
  return page;
}

function addGroupDialog() {
  const name = h('input', { class: 'input', name: 'name', autocomplete: 'off', required: '', maxlength: '64' });
  openDialog({
    title: 'Add group',
    fields: [field('Name', name)],
    submitLabel: 'Add group',
    onSubmit: async () => {
      const result = await api('POST', '/api/groups', { name: name.value });
      return result.ok ? null : errorText(result);
    },
  });
}

function renderGroup(group) {
  document.title = group.name + ' · headnode';
  const page = [
    h('a', { class: 'back', href: '#/groups' }, '← Groups'),
    pageHead(group.name,
      count(group.member_count, 'member', 'members') + ', ' + count(group.host_count, 'host', 'hosts') + '.',
      button('Add host', 'btn', () => addHostToGroup(group)),
      button('Add member', 'btn btn-primary', () => addMemberDialog(group))),
  ];

  page.push(h('h2', {}, 'Hosts'));
  if (group.hosts.length === 0) {
    page.push(h('div', { class: 'empty small' },
      'No hosts in this group yet. Use "Add host", or move an existing host here from its page.'));
  } else {
    page.push(hostsTable(group.hosts));
  }

  page.push(h('h2', {}, 'Members'));
  if (group.members.length === 0) {
    page.push(h('div', { class: 'empty small' }, 'No members yet. Add a member to let someone see this group\'s hosts.'));
  } else {
    page.push(h('table', {},
      h('thead', {}, h('tr', {},
        h('th', {}, 'User'),
        h('th', {}, 'Role'),
        h('th', { class: 'col-act' }, ''))),
      h('tbody', {}, group.members.map((m) => h('tr', { class: 'static' },
        h('td', {}, h('span', { class: 'name' }, m.username)),
        h('td', {}, roleSelect(group, m)),
        h('td', { class: 'col-act' }, button('Remove', 'link-button danger-text', () => removeMemberDialog(group, m))))))));
  }
  page.push(h('p', { class: 'sub note' },
    'Operators can open terminals and change files on the group\'s hosts. Viewers can see the hosts and read files, nothing more.'));
  return page;
}

function roleSelect(group, member) {
  const select = h('select', { class: 'input input-small', 'aria-label': 'Role of ' + member.username },
    [['operator', 'Operator'], ['viewer', 'Viewer']].map(([value, label]) =>
      h('option', { value, selected: value === member.role ? '' : null }, label)));
  select.addEventListener('change', async () => {
    const result = await api('PUT', '/api/groups/' + group.id + '/members/' + member.user_id, { role: select.value });
    if (!result.ok) messageDialog('Could not change the role', errorText(result));
    lastKey = null;
    refresh(); // shows the saved role, or puts the old one back after an error
  });
  return select;
}

async function addMemberDialog(group) {
  const users = await loadList('/api/users');
  if (!users) return;
  const taken = new Set(group.members.map((m) => m.user_id));
  const candidates = users.filter((u) => !taken.has(u.id));
  if (candidates.length === 0) {
    messageDialog('Add member', 'Every user is already in this group. Add a user first (Admin, then Users).');
    return;
  }

  const user = h('select', { class: 'input', name: 'user' }, candidates.map((u) => h('option', { value: u.id }, u.username)));
  const role = h('select', { class: 'input', name: 'role' },
    h('option', { value: 'viewer' }, 'Viewer'),
    h('option', { value: 'operator' }, 'Operator'));
  openDialog({
    title: 'Add member to ' + group.name,
    fields: [field('User', user), field('Role', role)],
    submitLabel: 'Add member',
    onSubmit: async () => {
      const result = await api('PUT', '/api/groups/' + group.id + '/members/' + user.value, { role: role.value });
      return result.ok ? null : errorText(result);
    },
  });
}

function removeMemberDialog(group, member) {
  confirmDialog({
    title: 'Remove member',
    message: 'Remove "' + member.username + '" from "' + group.name + '"? They will no longer see this group\'s hosts.',
    confirmLabel: 'Remove',
    onConfirm: () => api('DELETE', '/api/groups/' + group.id + '/members/' + member.user_id),
  });
}

// -------------------------------------------------------------- Audit page

// Readable names for the action codes the server writes (server/audit.go).
// An action this table does not know is shown as it is.
const AUDIT_LABELS = {
  login: 'Login',
  login_failed: 'Failed login',
  logout: 'Logout',
  user_create: 'User created',
  group_create: 'Group created',
  member_set: 'Member added or changed',
  member_remove: 'Member removed',
  agent_enroll: 'Host enrolled',
  token_create: 'Enroll token created',
  host_rename: 'Host renamed',
  host_move: 'Host moved',
  host_delete: 'Host deleted',
  session_open: 'Session opened',
  session_close: 'Session closed',
  password_change: 'Password changed',
  password_reset: 'Password reset',
  display_name_set: 'Display name changed',
  session_revoke: 'Login signed out',
  sessions_revoke_others: 'Signed out everywhere else',
  user_disable: 'User disabled',
  user_enable: 'User enabled',
  admin_grant: 'Administrator rights given',
  admin_revoke: 'Administrator rights removed',
  vnc_on: 'VNC server started',
  vnc_off: 'VNC server stopped',
  file_download: 'File downloaded',
  file_upload: 'File uploaded',
  upload_delete: 'Upload deleted',
  file_mkdir: 'Folder created',
  file_rename: 'File renamed',
  file_delete: 'File deleted',
  file_copy: 'File copied',
  file_chmod: 'Permissions changed',
  file_write: 'File edited',
  transfer_start: 'File send started',
  transfer_result: 'File send result',
  docker_start: 'Container started',
  docker_stop: 'Container stopped',
  docker_restart: 'Container restarted',
  docker_remove: 'Container removed',
  docker_remove_image: 'Image removed',
  docker_prune: 'Docker pruned',
  docker_stack: 'Compose stack changed',
};

// What a session is called in the log. The server writes the same two actions
// (session_open, session_close) for every kind of session and puts the kind in
// the detail ("kind=files readonly=false"), so the page reads it from there.
const SESSION_KIND_NAMES = { shell: 'Terminal', files: 'Files', tunnel: 'Tunnel', docker: 'Docker' };

function auditLabel(e) {
  if (e.action === 'session_open' || e.action === 'session_close') {
    const match = /^kind=(\S+)/.exec(e.detail);
    const name = match && SESSION_KIND_NAMES[match[1]];
    if (name) return name + (e.action === 'session_open' ? ' opened' : ' closed');
  }
  return AUDIT_LABELS[e.action] || e.action;
}

const AUDIT_PAGE_SIZE = 200; // the most the server returns in one reply

function renderAudit(entries) {
  const page = [
    pageHead('Audit log', 'Who did what, newest first.'),
    h('div', { class: 'pill' }, entries.length >= AUDIT_PAGE_SIZE
      ? 'Latest ' + AUDIT_PAGE_SIZE + ' entries'
      : count(entries.length, 'entry', 'entries')),
  ];
  if (entries.length === 0) {
    page.push(h('div', { class: 'empty' }, 'Nothing has been recorded yet.'));
    return page;
  }

  page.push(h('table', {},
    h('thead', {}, h('tr', {},
      h('th', { class: 'col-time' }, 'Time'),
      h('th', {}, 'User'),
      h('th', {}, 'Host'),
      h('th', {}, 'Action'),
      h('th', {}, 'Detail'))),
    h('tbody', {}, entries.map((e) => h('tr', { class: 'static' },
      // Absolute time: for a log, "3 hours ago" is not precise enough.
      h('td', { class: 'col-time num sub' }, new Date(e.at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })),
      h('td', {}, auditUser(e)),
      h('td', {}, auditHost(e)),
      h('td', { class: 'name' }, auditLabel(e)),
      // The detail can contain text from agents (a hostname), so it is a text
      // node like everything else (D-19).
      h('td', { class: 'sub detail' }, e.detail))))));
  return page;
}

// Nobody is logged in for a failed login or an enrolling host.
function auditUser(e) {
  if (e.username) return e.username;
  if (e.user_id) return h('span', { class: 'sub' }, 'user #' + e.user_id);
  return h('span', { class: 'sub' }, '—');
}

// A link while the host exists. After it is deleted only its id is left.
function auditHost(e) {
  if (e.host_name) return h('a', { href: hostLink(e.agent_id) }, e.host_name);
  if (e.agent_id) return h('span', { class: 'sub mono', title: 'This host no longer exists' }, e.agent_id);
  return h('span', { class: 'sub' }, '—');
}

// --------------------------------------------------------------- Transfers

// "2 of 3 done, 1 failed, 1 working", for the list and the detail page.
function transferSummary(hosts) {
  const done = hosts.filter((host) => host.status === 'ok').length;
  const failed = hosts.filter((host) => host.status === 'error').length;
  const working = hosts.length - done - failed;
  const parts = [done + ' of ' + hosts.length + ' done'];
  if (failed > 0) parts.push(failed + ' failed');
  if (working > 0) parts.push(working + ' working');
  return parts.join(', ');
}

function renderTransfers(transfers) {
  const send = me && me.can_upload ? button('Send files', 'btn btn-primary', () => sendFileDialog()) : null;
  const page = [
    pageHead('Transfers', 'Send a file to several hosts at once, and follow how each one is doing.', send),
    h('div', { class: 'pill' }, count(transfers.length, 'transfer', 'transfers')),
  ];
  if (transfers.length === 0) {
    page.push(h('div', { class: 'empty' }, 'No transfers yet. Use "Send files" to put files on one or more hosts.'));
    return page;
  }

  page.push(h('table', {},
    h('thead', {}, h('tr', {},
      h('th', {}, 'Started'),
      h('th', {}, 'File'),
      h('th', {}, 'Destination'),
      h('th', {}, 'Hosts'),
      h('th', {}, 'By'))),
    h('tbody', {}, transfers.map((transfer) => {
      const failed = transfer.hosts.some((host) => host.status === 'error');
      const row = h('tr', {},
        h('td', { class: 'sub', title: new Date(transfer.created_at).toLocaleString() }, relativeTime(transfer.created_at)),
        h('td', {},
          h('a', { class: 'name', href: '#/transfers/' + transfer.id }, transfer.file_name),
          h('div', { class: 'sub' }, formatBytes(transfer.file_size))),
        h('td', { class: 'mono sub' }, transfer.dest_path + (transfer.extract ? ' (unpacked)' : '')),
        h('td', {}, h('span', { class: failed ? 'danger' : '' }, transferSummary(transfer.hosts))),
        h('td', { class: 'sub' }, transfer.started_by));
      row.addEventListener('click', () => { location.hash = '#/transfers/' + transfer.id; });
      return row;
    }))));
  return page;
}

// ----------------------------------------------------------------- Uploads

// Every file that was uploaded to the server (admin only). Uploads stay on the
// server's disk until someone deletes them here: nothing cleans them up by
// itself (D-38).
function renderUploads(files) {
  const page = [
    pageHead('Uploads', 'Files uploaded to the server for sending to hosts. They stay on its disk until you delete them.'),
    h('div', { class: 'pill' }, count(files.length, 'file', 'files') + ' · ' + formatBytes(files.reduce((sum, f) => sum + f.size, 0))),
  ];
  if (files.length === 0) {
    page.push(h('div', { class: 'empty' }, 'No uploads. A file appears here when someone uses "Send files" on the Transfers page.'));
    return page;
  }

  page.push(h('table', {},
    h('thead', {}, h('tr', {},
      h('th', {}, 'Uploaded'),
      h('th', {}, 'File'),
      h('th', {}, 'By'),
      h('th', { class: 'col-act' }, ''))),
    h('tbody', {}, files.map((file) => h('tr', { class: 'static' },
      h('td', { class: 'sub', title: new Date(file.created_at).toLocaleString() }, relativeTime(file.created_at)),
      h('td', {},
        h('span', { class: 'name' }, file.name),
        h('div', { class: 'sub' }, formatBytes(file.size))),
      h('td', { class: 'sub' }, file.uploaded_by || 'Deleted user'),
      h('td', { class: 'col-act' }, file.in_use
        // The server refuses too: this only saves a click that would fail.
        ? h('span', { class: 'sub', title: 'A transfer of this file is still running.' }, 'In use')
        : button('Delete', 'link-button danger-text', () => deleteUploadDialog(file))))))));
  return page;
}

function deleteUploadDialog(file) {
  confirmDialog({
    title: 'Delete upload',
    message: 'Delete "' + file.name + '" (' + formatBytes(file.size) + ') from the server? This cannot be undone. ' +
      'Copies already sent to hosts are not touched, and past transfers stay in the list.',
    confirmLabel: 'Delete',
    onConfirm: () => api('DELETE', '/api/files/' + file.id),
  });
}

// One transfer, host by host. This page is redrawn by the poll like the others,
// so the statuses move from "Fetching…" to "Done" by themselves.
function renderTransfer(transfer) {
  document.title = transfer.file_name + ' · headnode';
  const working = transfer.hosts.some((host) => host.status === 'pending');

  const hostRows = transfer.hosts.map((host) => {
    let status;
    if (host.status === 'ok') status = h('span', {}, h('span', { class: 'dot' }), 'Done');
    else if (host.status === 'error') status = h('span', { class: 'danger' }, 'Failed');
    else status = h('span', { class: 'sub' }, 'Fetching…');

    const name = host.host_name
      ? h('a', { class: 'name', href: hostLink(host.agent_id) }, host.host_name)
      : h('span', { class: 'sub', title: host.agent_id }, 'Deleted host');
    return h('tr', { class: 'static' },
      h('td', {}, name),
      h('td', {}, status),
      h('td', { class: 'sub' }, host.error || ''));
  });

  const facts = [
    ['Destination', h('span', { class: 'mono' }, transfer.dest_path)],
    // A zipped folder is unpacked into the destination, with the modes it had.
    transfer.extract ? ['Unpacked', 'The file is a folder, unpacked inside the destination'] : ['Mode', h('span', { class: 'mono' }, transfer.mode)],
    ['Size', formatBytes(transfer.file_size)],
    ['Started', new Date(transfer.created_at).toLocaleString()],
    ['Started by', transfer.started_by],
  ];
  return [
    h('a', { class: 'back', href: '#/transfers' }, '← Transfers'),
    pageHead(transfer.file_name, transferSummary(transfer.hosts) + (working ? '. This page updates by itself.' : '.')),
    h('h2', {}, 'Hosts'),
    h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Host'), h('th', {}, 'Status'), h('th', {}, 'Details'))),
      h('tbody', {}, hostRows)),
    h('h2', {}, 'Details'),
    h('dl', { class: 'facts' }, facts.map(([label, value]) => [h('dt', {}, label), h('dd', {}, value)])),
  ];
}

// "Send file": upload once, then every chosen host fetches it (D-03). Only hosts
// the server says we may write to are listed, and offline ones are shown but
// cannot be picked: a transfer to a host that is not there fails at once.
//
// With staged items ([{id, name, size, from, archive}]) the files are already on
// the server (the file manager's "Send to hosts" copied them there from a host),
// so there is nothing to choose or upload. An item with archive set is a zipped
// folder: it is sent with `extract`, so the hosts get the folder, not the zip.
async function sendFileDialog(staged = []) {
  const hosts = await loadList('/api/hosts');
  if (!hosts) return;
  // Not Windows hosts: file transfers are untested there (paths, modes), see D-47.
  const targets = hosts.filter((host) => host.can_files_write && !isWindows(host));
  if (targets.length === 0) {
    messageDialog('Send files', 'There are no hosts you may send files to. You need the operator role in a group that has hosts.');
    return;
  }
  const hasStaged = staged.length > 0;
  const onlyFolders = hasStaged && staged.every((item) => item.archive);

  const file = h('input', { class: 'input', type: 'file', name: 'file', multiple: '', required: hasStaged ? null : '' });
  const folder = h('input', { class: 'input', name: 'folder', value: '/tmp', required: '', autocomplete: 'off', spellcheck: 'false' });
  const name = h('input', { class: 'input', name: 'name', required: '', autocomplete: 'off', spellcheck: 'false' });
  // One file can be saved under another name. Several files, and a folder, keep
  // their own names, so there is nothing to type for them.
  function syncName() {
    const files = hasStaged ? staged : [...file.files];
    const keepsOwn = files.length > 1 || (files.length === 1 && files[0].archive === true);
    name.disabled = keepsOwn;
    name.required = !keepsOwn;
    name.placeholder = keepsOwn ? (files.length > 1 ? 'Each one keeps its own name' : 'A folder keeps its own name') : '';
    if (keepsOwn) name.value = '';
    else if (files.length === 1) name.value = files[0].name; // start with its own name
  }
  file.addEventListener('change', syncName);
  const stagedBox = h('div', { class: 'staged' });
  function drawStaged() {
    stagedBox.replaceChildren(...staged.map((item) => h('div', {},
      h('span', { class: 'name' }, item.name),
      h('span', { class: 'sub' }, ' ' + (item.archive ? 'folder, unpacked on each host' : formatBytes(item.size)) +
        (item.from ? ' · from ' + item.from : '')))));
  }
  drawStaged();
  const mode = h('input', {
    class: 'input', name: 'mode', value: '0644', required: onlyFolders ? null : '', autocomplete: 'off', pattern: '0?[0-7]{3}',
  });
  const modeField = field('Mode for files (0644 for a normal file, 0755 for a program)', mode);
  modeField.hidden = onlyFolders; // a folder keeps the modes of its files
  syncName();

  const choices = targets.map((host) => ({
    host,
    box: h('input', { type: 'checkbox', value: host.id, disabled: host.online ? null : '' }),
  }));
  const picker = h('div', { class: 'host-picker' }, choices.map(({ host, box }) =>
    h('label', { class: 'check' }, box, host.name,
      h('span', { class: 'sub' }, [host.group_name || 'No group', host.online ? 'online' : 'offline'].join(' · ')))));
  const selectAll = button('Select all online', 'link-button', () => {
    for (const { box } of choices) if (!box.disabled) box.checked = true;
  });
  const status = h('div', { class: 'sub' });

  openDialog({
    title: 'Send to hosts',
    fields: [
      h('p', { class: 'sub' }, hasStaged
        ? 'These are now on the server. Every host you pick fetches them, checks them, and puts them in place. A file with the same name is replaced; a folder that already exists on a host is refused, and nothing on that host changes.'
        : 'The files go to the server once, then every host you pick fetches them, checks them, and puts them in place. A file with the same name is replaced.'),
      hasStaged
        ? h('div', { class: 'field' }, h('span', {}, 'What is sent'), stagedBox)
        : field('Files (one or more; each is sent to the same folder on the same hosts)', file),
      field(onlyFolders || staged.some((item) => item.archive)
        ? 'Folder on each host (must exist; a folder you send is unpacked inside it)'
        : 'Folder on each host (must exist)', folder),
      field('Save as (when sending one file)', name),
      modeField,
      h('div', { class: 'field' }, h('span', {}, 'Hosts'), picker, selectAll),
      status,
    ],
    submitLabel: 'Send',
    onSubmit: async () => {
      const chosen = hasStaged ? [] : [...file.files];
      if (!hasStaged && chosen.length === 0) return 'Choose at least one file.';
      const dir = folder.value.trim().replace(/\/+$/, '') || '/';
      if (!dir.startsWith('/')) return 'The folder must be an absolute path, starting with /.';

      // What is sent: files from the computer (uploaded now), or items that are
      // already on the server.
      const items = hasStaged ? staged.map((item) => ({ staged: item })) : chosen.map((f) => ({ file: f }));
      const nameOf = (item) => (item.file ? item.file.name : item.staged.name);
      const isFolder = (item) => Boolean(item.staged && item.staged.archive);
      const many = items.length > 1;
      const singleName = name.value.trim();
      if (!many && !isFolder(items[0]) && (!singleName || singleName.includes('/'))) {
        return 'The file name cannot be empty or contain "/".';
      }
      const selected = choices.filter(({ box }) => box.checked).map(({ host }) => host.id);
      if (selected.length === 0) return 'Pick at least one host.';

      // One transfer per item, started one after another: each has its own page
      // and its own audit lines, and the same hosts, folder and mode.
      const started = [];
      for (let i = 0; i < items.length; i++) {
        const item = items[i];
        const label = many ? '(' + (i + 1) + ' of ' + items.length + ') ' + nameOf(item) + ': ' : '';
        const fileName = many || isFolder(item) ? nameOf(item) : singleName;

        let fileId;
        let problem = null;
        if (item.staged) {
          fileId = item.staged.id;
        } else {
          status.textContent = label + 'Uploading to the server… 0%';
          const stored = await uploadFile(item.file, (fraction) => {
            status.textContent = label + 'Uploading to the server… ' + Math.round(fraction * 100) + '%';
          });
          if (stored.ok) fileId = stored.data.id;
          else problem = errorText(stored);
        }
        if (!problem) {
          status.textContent = label + 'Starting…';
          // A zipped folder is unpacked into the folder, and keeps its files' modes.
          const body = isFolder(item)
            ? { file_id: fileId, agent_ids: selected, dest_path: dir, extract: true }
            : { file_id: fileId, agent_ids: selected, dest_path: joinPath(dir, fileName), mode: mode.value.trim() };
          const created = await api('POST', '/api/transfers', body);
          if (created.ok) started.push(created.data.id);
          else problem = errorText(created);
        }

        if (problem) {
          if (started.length === 0) return problem;
          // Some are already on their way. Keep only the ones that were not started,
          // so pressing Send again does not send the others twice.
          if (hasStaged) {
            staged.splice(0, i);
            drawStaged();
          } else {
            const rest = new DataTransfer();
            chosen.slice(i).forEach((f) => rest.items.add(f));
            file.files = rest.files;
          }
          syncName(); // so the name box matches what is left
          return count(started.length, 'transfer', 'transfers') + ' started (see the Transfers page). Could not send "' +
            nameOf(item) + '": ' + problem + ' What is still listed was not sent: fix this and press Send again.';
        }
      }

      // One transfer: follow it host by host. Several: the list shows them all.
      location.hash = started.length === 1 ? '#/transfers/' + started[0] : '#/transfers';
      return null;
    },
  });
}

// ------------------------------------------------------------ login & sidebar

let me = null; // the logged-in user from /api/me, or null when logged out

function renderLogin() {
  document.title = 'Log in · headnode';

  const error = h('div', { class: 'form-error', role: 'alert' });
  const username = h('input', { class: 'input', name: 'username', autocomplete: 'username', required: '', autofocus: '' });
  const password = h('input', { class: 'input', name: 'password', type: 'password', autocomplete: 'current-password', required: '' });
  const submit = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Log in');

  const form = h('form', { class: 'login' },
    h('div', { class: 'org' }, h('img', { class: 'org-logo', src: '/headnode-mark.svg', alt: '' }), 'headnode'),
    field('Username', username),
    field('Password', password),
    error,
    submit);

  form.addEventListener('submit', async (event) => {
    event.preventDefault(); // stay on the page: we send the login with fetch
    submit.disabled = true;
    error.textContent = '';
    try {
      const resp = await fetch('/api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: username.value, password: password.value }),
      });
      if (resp.ok) {
        password.value = '';
        me = null;
        routeId++;
        lastKey = null;
        refresh(); // loads the page the URL points at, and /api/me
        return;
      }
      // The server explains itself: wrong password, or "try again in N seconds".
      const body = await resp.json().catch(() => ({}));
      error.textContent = body.error || 'Login failed (HTTP ' + resp.status + ').';
    } catch (err) {
      error.textContent = 'Cannot reach the server.';
    }
    submit.disabled = false;
  });
  return [form];
}

async function loadMe() {
  const resp = await fetch('/api/me');
  me = resp.ok ? await resp.json() : null;
  renderSidebar();
}

// The sidebar: links, and the user block at the bottom. With nobody logged in
// there is nothing to navigate to, so it is hidden.
function renderSidebar() {
  document.body.classList.toggle('logged-out', !me);
  renderNav();
  renderUserBox();
}

function renderNav() {
  const active = currentRoute().nav;
  const link = (href, label, iconPath) =>
    h('a', { href, class: href === active ? 'active' : null }, icon(iconPath), label);

  const items = [link('#/', 'Hosts', ICON_HOSTS)];
  // Not an admin page: anyone who may send files has something to do here.
  if (me && me.can_upload) items.push(link('#/transfers', 'Transfers', ICON_TRANSFERS));
  if (isAdmin()) {
    items.push(
      h('div', { class: 'nav-label' }, 'Admin'),
      link('#/users', 'Users', ICON_USERS),
      link('#/groups', 'Groups', ICON_GROUPS),
      link('#/uploads', 'Uploads', ICON_UPLOADS),
      link('#/audit', 'Audit log', ICON_AUDIT));
  }
  document.getElementById('nav').replaceChildren(...items);
}

function renderUserBox() {
  const box = document.getElementById('userbox');
  closeUserMenu();
  if (!me) {
    box.replaceChildren();
    renderServerLine(); // clears it
    return;
  }
  // Only the user is shown: the placeholder picture every user has (D-69), their
  // display name or else their username, and their role. The block is a button:
  // clicking it opens a small menu with Profile and Log out, so a stray click cannot
  // log anyone out.
  const trigger = h('button', { class: 'user user-button', type: 'button', 'aria-haspopup': 'menu', 'aria-expanded': 'false' },
    h('img', { class: 'avatar', src: PICTURE, alt: '' }),
    h('div', { class: 'user-text' },
      h('div', { class: 'user-name' }, displayName(me)),
      h('div', { class: 'sub' }, me.is_admin ? 'Administrator' : 'User')));
  trigger.addEventListener('click', () => {
    if (userMenu) closeUserMenu();
    else openUserMenu(box, trigger);
  });
  box.replaceChildren(trigger);
  renderServerLine();
}

let userMenu = null; // the open Log out menu, or null

function openUserMenu(box, trigger) {
  const logout = button('Log out', 'menu-item', async () => {
    closeUserMenu();
    await fetch('/api/logout', { method: 'POST' });
    me = null;
    routeId++;
    lastKey = null;
    refresh(); // the server now answers 401, which shows the login form
  });
  const profile = h('a', { class: 'menu-item', href: '#/profile', role: 'menuitem' }, 'Profile');
  profile.addEventListener('click', () => closeUserMenu());
  userMenu = h('div', { class: 'menu user-menu', role: 'menu' }, profile, logout);
  box.append(userMenu);
  trigger.setAttribute('aria-expanded', 'true');
}

function closeUserMenu() {
  if (!userMenu) return;
  userMenu.remove();
  userMenu = null;
  const trigger = document.querySelector('#userbox .user-button');
  if (trigger) trigger.setAttribute('aria-expanded', 'false');
}

// Like the folder list of the file manager: a click anywhere else, or Escape,
// closes it.
document.addEventListener('mousedown', (event) => {
  if (userMenu && !event.target.closest('#userbox')) closeUserMenu();
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') closeUserMenu();
});

// "0.1.0 · up 3h 12m", under "headnode" at the top of the sidebar. The server
// sends its version and the moment it started (in /api/me), and the uptime is
// counted here, so it keeps moving without asking the server again.
function renderServerLine() {
  const line = document.getElementById('server-line');
  if (!line) return;
  if (!me || !me.server_version) {
    line.textContent = '';
    return;
  }
  const sec = Math.max(0, Math.floor((Date.now() - new Date(me.server_started_at)) / 1000));
  line.textContent = me.server_version + ' · up ' + formatDuration(sec);
}

setInterval(renderServerLine, 30000);

// ------------------------------------------------------------------- loop

const view = document.getElementById('view');
const POLL_MS = 2000; // a new host appears within about 2 s (roadmap M1)

let routeId = 0;     // changes on navigation, so a slow reply for the old page is dropped
let lastKey = null;  // what is on screen now, so an unchanged reply does not redraw

// Which page the URL asks for. Ids in the URL are checked against a strict
// pattern before they go into a request path: a host id is "a_" plus hex, a
// group id is digits. Anything else is simply the Hosts page.
// title is the browser tab title. The detail pages replace it with the name of
// the host or group once they have it.
const routes = [
  { pattern: /^#\/hosts\/([A-Za-z0-9_]+)$/, url: (m) => '/api/hosts/' + m[1], render: renderHost, nav: '#/', title: 'Host' },
  // static: drawn once per visit, never by the 2-second poll (see refresh).
  { pattern: /^#\/hosts\/([A-Za-z0-9_]+)\/terminal$/, url: (m) => '/api/hosts/' + m[1], render: renderTerminalPage, nav: '#/', title: 'Terminal', static: true },
  { pattern: /^#\/hosts\/([A-Za-z0-9_]+)\/files$/, url: (m) => '/api/hosts/' + m[1], render: renderFilesPage, nav: '#/', title: 'Files', static: true },
  { pattern: /^#\/hosts\/([A-Za-z0-9_]+)\/screen$/, url: (m) => '/api/hosts/' + m[1], render: renderScreenPage, nav: '#/', title: 'Screen', static: true },
  { pattern: /^#\/hosts\/([A-Za-z0-9_]+)\/docker$/, url: (m) => '/api/hosts/' + m[1], render: renderDockerPage, nav: '#/', title: 'Docker', static: true },
  { pattern: /^#\/users$/, url: () => '/api/users', render: renderUsers, nav: '#/users', title: 'Users' },
  // static: it holds forms, which the 2-second poll must not redraw while someone types.
  { pattern: /^#\/profile$/, url: () => '/api/me', render: renderProfile, nav: '', title: 'Profile', static: true },
  { pattern: /^#\/groups\/(\d+)$/, url: (m) => '/api/groups/' + m[1], render: renderGroup, nav: '#/groups', title: 'Group' },
  { pattern: /^#\/groups$/, url: () => '/api/groups', render: renderGroups, nav: '#/groups', title: 'Groups' },
  { pattern: /^#\/uploads$/, url: () => '/api/files', render: renderUploads, nav: '#/uploads', title: 'Uploads' },
  { pattern: /^#\/audit$/, url: () => '/api/audit?limit=' + AUDIT_PAGE_SIZE, render: renderAudit, nav: '#/audit', title: 'Audit log' },
  { pattern: /^#\/transfers\/(t_[0-9a-f]+)$/, url: (m) => '/api/transfers/' + m[1], render: renderTransfer, nav: '#/transfers', title: 'Transfer' },
  { pattern: /^#\/transfers$/, url: () => '/api/transfers', render: renderTransfers, nav: '#/transfers', title: 'Transfers' },
];

function currentRoute() {
  for (const route of routes) {
    const match = location.hash.match(route.pattern);
    if (match) return { url: route.url(match), render: route.render, nav: route.nav, title: route.title, static: route.static };
  }
  return { url: '/api/hosts', render: renderHosts, nav: '#/', title: 'Hosts' };
}

function messagePage(backHref, backLabel, text) {
  return [h('a', { class: 'back', href: backHref }, backLabel), h('div', { class: 'empty' }, text)];
}

async function refresh() {
  if (document.hidden) return; // nobody is looking, do not poll
  const route = currentRoute();
  const myRoute = routeId;

  // A static page (the terminal) is built once per visit. Do not even ask the
  // server again: a failed poll during a server restart would otherwise
  // replace the page, and the terminal with it.
  if (route.static && lastKey === 'static:' + location.hash) return;

  // What to show is decided in two steps. The key says whether it differs from
  // what is on screen. build() makes the nodes, and only runs when the key
  // changed, because for the terminal page building means starting a terminal.
  let key;
  let build;
  try {
    const resp = await fetch(route.url);
    if (resp.status === 401) {
      // Not logged in, or the session ended. Show the form. Its key never
      // changes, so the 2-second poll leaves it alone and does not wipe what
      // is being typed.
      key = 'login';
      build = renderLogin;
      me = null;
    } else {
      // Ask who we are before drawing: the pages differ for administrators.
      if (!me) await loadMe();

      if (resp.status === 403) {
        key = 'forbidden';
        build = () => messagePage('#/', '← Hosts', 'You do not have access to this page.');
      } else if (resp.status === 404) {
        key = 'not-found';
        build = () => messagePage('#/', '← Hosts', 'Not found.');
      } else if (!resp.ok) {
        throw new Error('HTTP ' + resp.status);
      } else {
        const text = await resp.text();
        // The minute is part of the key because "3 minutes ago" changes without
        // the data changing.
        key = route.static ? 'static:' + location.hash : text + '@' + Math.floor(Date.now() / 60000);
        build = () => {
          document.title = route.title + ' · headnode';
          return route.render(JSON.parse(text));
        };
      }
    }
  } catch (err) {
    // The server may be restarting. Forget who we are, so the next good reply
    // asks /api/me again and the sidebar shows the new start time.
    me = null;
    key = 'error:' + err.message;
    build = () => [h('div', { class: 'notice' }, 'Cannot reach the server (' + err.message + '). Retrying…')];
  }

  if (myRoute !== routeId) return;
  if (key === 'login') renderSidebar(); // hides the sidebar

  if (key === lastKey) return;
  lastKey = key;
  // Redrawing only when something changed also keeps text you selected (to copy
  // an agent id, say) from disappearing every 2 seconds.
  disposePage(); // a page that is replaced takes its terminal or file session with it
  view.replaceChildren(...build());
}

window.addEventListener('hashchange', () => {
  routeId++;
  lastKey = null;
  renderNav(); // moves the highlight to the new page
  refresh();
});
document.addEventListener('visibilitychange', refresh);
setInterval(refresh, POLL_MS);
refresh();
