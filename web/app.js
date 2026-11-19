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
const ICON_AUDIT = 'M3.5 2.5h9v11h-9zM5.5 5.5h5M5.5 8h5M5.5 10.5h3';
const ICON_GROUPS ='M6 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM2 13c.4-2 1.9-3 4-3s3.6 1 4 3M11 7a1.7 1.7 0 1 0 0-3.4M11.5 10c1.6.1 2.5 1 2.8 3';

function button(label, className, onClick) {
  const b = h('button', { class: className, type: 'button' }, label);
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
function openDialog({ title, fields, submitLabel, cancelLabel = 'Cancel', danger = false, onSubmit }) {
  const heading = h('h2', { class: 'dialog-title' }, title);
  const error = h('div', { class: 'form-error', role: 'alert' });
  const submit = h('button', { class: danger ? 'btn btn-danger' : 'btn btn-primary', type: 'submit' }, submitLabel);
  const cancel = cancelLabel ? button(cancelLabel, 'btn', () => dlg.close()) : h('span');
  const form = h('form', {}, heading, fields, error, h('div', { class: 'dialog-actions' }, cancel, submit));
  const dlg = h('dialog', { class: 'dialog' }, form);

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
    lastKey = null;
    refresh();
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
  const page = [
    pageHead('Hosts', 'Manage the machines connected to headnode.', addHost),
    h('div', { class: 'pill' }, count(hosts.length, 'host', 'hosts')),
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
  return h('table', {},
    h('thead', {}, h('tr', {},
      h('th', { class: 'col-host' }, 'Host'),
      h('th', { class: 'col-addr' }, 'Addresses'),
      h('th', { class: 'col-ver' }, 'Version'),
      h('th', { class: 'col-usage' }, 'Usage'),
      h('th', {}, 'Status'))),
    h('tbody', {}, hosts.map(hostRow)));
}

function hostRow(host) {
  const info = host.info || {};
  const ips = info.lan_ips || [];

  const row = h('tr', {},
    h('td', { class: 'col-host' },
      h('a', { class: 'name', href: hostLink(host.id) }, host.name),
      h('div', { class: 'sub' }, [info.platform || host.os, host.group_name].filter(Boolean).join(' · ')),
      // Only admins can see a host that is in no group, so this is a hint for them.
      host.group_id == null ? h('div', {}, h('span', { class: 'tag' }, 'No group')) : null),
    h('td', { class: 'col-addr num' },
      ips.length > 0 ? ips.map((ip) => h('div', {}, ip)) : h('span', { class: 'sub' }, '—')),
    h('td', { class: 'col-ver num' },
      host.version || '—',
      h('div', { class: 'sub' }, info.kernel ? 'Linux ' + info.kernel : '')),
    h('td', { class: 'col-usage num' }, usageSummary(host)),
    h('td', {}, statusText(host)));

  // The whole row is clickable. The name is a real link too, so the page
  // still works from the keyboard.
  row.addEventListener('click', () => { location.hash = hostLink(host.id); });
  return row;
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
  const code = h('div', { class: 'code-block' }, data.install_command);
  const copy = button('Copy command', 'btn', async () => {
    try {
      await navigator.clipboard.writeText(data.install_command);
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
  return [
    h('p', {}, 'Copy the headnode-agent program to the host, then run this command there. ' + where),
    code,
    copy,
    h('p', { class: 'sub' }, 'The token is shown only now and is valid for 24 hours.'),
  ];
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

// ------------------------------------------------------------- Users page

function renderUsers(users) {
  return [
    pageHead('Users', 'People who can log in to headnode.', button('Add user', 'btn btn-primary', addUserDialog)),
    h('div', { class: 'pill' }, count(users.length, 'user', 'users')),
    h('table', {},
      h('thead', {}, h('tr', {},
        h('th', {}, 'Username'),
        h('th', {}, 'Role'),
        h('th', {}, 'Created'))),
      h('tbody', {}, users.map((u) => h('tr', { class: 'static' },
        h('td', {}, h('span', { class: 'name' }, u.username), u.id === me.id ? h('span', { class: 'tag' }, 'you') : null),
        h('td', {}, u.is_admin ? 'Administrator' : 'User'),
        h('td', { class: 'sub', title: new Date(u.created_at).toLocaleString() }, relativeTime(u.created_at)))))),
    h('p', { class: 'sub note' }, 'Administrators see every host and manage users and groups. Everyone else sees only the hosts in their groups.'),
  ];
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
};

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
      h('td', { class: 'name' }, AUDIT_LABELS[e.action] || e.action),
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

// ------------------------------------------------------------ login & sidebar

let me = null; // the logged-in user from /api/me, or null when logged out

function renderLogin() {
  document.title = 'Log in · headnode';

  const error = h('div', { class: 'form-error', role: 'alert' });
  const username = h('input', { class: 'input', name: 'username', autocomplete: 'username', required: '', autofocus: '' });
  const password = h('input', { class: 'input', name: 'password', type: 'password', autocomplete: 'current-password', required: '' });
  const submit = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Log in');

  const form = h('form', { class: 'login' },
    h('div', { class: 'org' }, h('div', { class: 'org-mark' }, 'HN'), 'headnode'),
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
  if (isAdmin()) {
    items.push(
      h('div', { class: 'nav-label' }, 'Admin'),
      link('#/users', 'Users', ICON_USERS),
      link('#/groups', 'Groups', ICON_GROUPS),
      link('#/audit', 'Audit log', ICON_AUDIT));
  }
  document.getElementById('nav').replaceChildren(...items);
}

function renderUserBox() {
  const box = document.getElementById('userbox');
  if (!me) {
    box.replaceChildren();
    return;
  }
  const logout = button('Log out', 'link-button', async () => {
    await fetch('/api/logout', { method: 'POST' });
    me = null;
    routeId++;
    lastKey = null;
    refresh(); // the server now answers 401, which shows the login form
  });
  box.replaceChildren(h('div', { class: 'user' },
    h('div', { class: 'avatar' }, me.username.slice(0, 2).toUpperCase()),
    h('div', { class: 'user-text' },
      h('div', { class: 'user-name' }, me.username),
      h('div', { class: 'sub' }, me.is_admin ? 'Administrator' : 'User')),
    logout));
}

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
  { pattern: /^#\/users$/, url: () => '/api/users', render: renderUsers, nav: '#/users', title: 'Users' },
  { pattern: /^#\/groups\/(\d+)$/, url: (m) => '/api/groups/' + m[1], render: renderGroup, nav: '#/groups', title: 'Group' },
  { pattern: /^#\/groups$/, url: () => '/api/groups', render: renderGroups, nav: '#/groups', title: 'Groups' },
  { pattern: /^#\/audit$/, url: () => '/api/audit?limit=' + AUDIT_PAGE_SIZE, render: renderAudit, nav: '#/audit', title: 'Audit log' },
];

function currentRoute() {
  for (const route of routes) {
    const match = location.hash.match(route.pattern);
    if (match) return { url: route.url(match), render: route.render, nav: route.nav, title: route.title };
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

  let nodes;
  let key;
  try {
    const resp = await fetch(route.url);
    if (resp.status === 401) {
      // Not logged in, or the session ended. Show the form. Its key never
      // changes, so the 2-second poll leaves it alone and does not wipe what
      // is being typed.
      key = 'login';
      nodes = renderLogin();
      me = null;
    } else {
      // Ask who we are before drawing: the pages differ for administrators.
      if (!me) await loadMe();

      if (resp.status === 403) {
        key = 'forbidden';
        nodes = messagePage('#/', '← Hosts', 'You do not have access to this page.');
      } else if (resp.status === 404) {
        key = 'not-found';
        nodes = messagePage('#/', '← Hosts', 'Not found.');
      } else if (!resp.ok) {
        throw new Error('HTTP ' + resp.status);
      } else {
        const text = await resp.text();
        // The minute is part of the key because "3 minutes ago" changes without
        // the data changing.
        key = text + '@' + Math.floor(Date.now() / 60000);
        document.title = route.title + ' · headnode';
        nodes = route.render(JSON.parse(text));
      }
    }
  } catch (err) {
    key = 'error:' + err.message;
    nodes = [h('div', { class: 'notice' }, 'Cannot reach the server (' + err.message + '). Retrying…')];
  }

  if (myRoute !== routeId) return;
  if (key === 'login') renderSidebar(); // hides the sidebar

  if (key === lastKey) return;
  lastKey = key;
  // Redrawing only when something changed also keeps text you selected (to copy
  // an agent id, say) from disappearing every 2 seconds.
  view.replaceChildren(...nodes);
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
