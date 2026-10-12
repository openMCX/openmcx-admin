//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// OpenMCX Admin console (slice E1). Calls the /v1 API with the session cookie; every
// request carries X-MCX-Admin, which the server requires for writes.
"use strict";

const $ = (id) => document.getElementById(id);
const state = { me: null, config: null, revision: 0, view: "dashboard", timer: null, upload: null, users: [] };

async function api(method, path, { body, ifMatch, type } = {}) {
  const headers = { "X-MCX-Admin": "1" };
  if (ifMatch !== undefined) headers["If-Match"] = `"${ifMatch}"`;
  if (type) headers["Content-Type"] = type;
  let resp;
  try {
    resp = await fetch(path, { method, headers, body, credentials: "same-origin", cache: "no-store" });
  } catch (_) {
    return { status: 0, body: { error: "MCX Admin is unreachable" } };
  }
  const text = await resp.text();
  let parsed = null;
  try { parsed = text ? JSON.parse(text) : null; } catch (_) { parsed = { error: text.slice(0, 200) }; }
  if (resp.status === 401 && state.me) signedOut("Your session ended. Sign in again.");
  return { status: resp.status, etag: (resp.headers.get("ETag") || "").replaceAll('"', ""), body: parsed };
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") node.className = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else if (v !== undefined && v !== null && v !== false) node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c !== null && c !== undefined) node.append(c.nodeType ? c : String(c));
  return node;
}

const pill = (text, kind) => el("span", { class: "pill " + kind }, text);
const statusKind = { active: "ok", pending: "info", suspended: "warn", retired: "idle" };
const when = (iso) => {
  const d = new Date(iso);
  const today = new Date().toDateString() === d.toDateString();
  return today ? d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
               : d.toLocaleDateString([], { month: "short", day: "numeric" }) + " " + d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
};
const reason = (r) => (r.body && (r.body.error || r.body.reason)) || `status ${r.status}`;

let toastTimer;
function toast(text) {
  const t = $("toast");
  t.textContent = text;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 3000);
}

function message(title, text, url) {
  $("msg-title").textContent = title;
  $("msg-text").textContent = text;
  $("msg-url").hidden = $("msg-copy").hidden = !url;
  $("msg-url").textContent = url || "";
  $("dlg-msg").showModal();
}

function confirmAction(title, text, yes) {
  return new Promise((resolve) => {
    const d = $("dlg-confirm");
    $("cf-title").textContent = title;
    $("cf-text").textContent = text;
    $("cf-yes").textContent = yes;
    d.addEventListener("close", () => resolve(d.returnValue === "yes"), { once: true });
    d.returnValue = "";
    d.showModal();
  });
}

async function copy(text, node) {
  try { await navigator.clipboard.writeText(text); toast("Copied"); }
  catch (_) {
    const r = document.createRange(); r.selectNodeContents(node);
    const s = getSelection(); s.removeAllRanges(); s.addRange(r); toast("Selected: press Ctrl+C to copy");
  }
}

// ---- Sign-in ----

function signedOut(text) {
  state.me = null;
  clearInterval(state.timer);
  $("app").hidden = true;
  $("signin").hidden = false;
  $("signin-msg").hidden = !text;
  $("signin-msg").textContent = text || "";
}

async function boot() {
  for (const slot of document.querySelectorAll("[data-logo]")) slot.append($("logo-tpl").content.cloneNode(true));
  const params = new URLSearchParams(location.search);
  const failed = { failed: "Sign-in failed: check the user name and password.", expired: "The sign-in took too long. Try again.",
                   unavailable: "The IdMS is unreachable. Try again shortly." }[params.get("signin")];
  if (params.has("signin")) history.replaceState(null, "", location.pathname + location.hash);
  const me = await api("GET", "/v1/me");
  if (me.status === 401) return signedOut(failed);
  if (me.status === 403) return signedOut("This account has no configuration management rights. Sign in as an administrator.");
  if (me.status !== 200) return signedOut("MCX Admin is not available: " + reason(me));
  state.me = me.body;
  $("me-login").textContent = me.body.login;
  $("me-id").textContent = me.body.mcptt_id;
  for (const d of document.querySelectorAll("[data-domain]")) d.textContent = me.body.domain;
  $("signin").hidden = true;
  $("app").hidden = false;
  await loadConfig();
  show((location.hash || "#dashboard").slice(1));
}

async function loadConfig() {
  const r = await api("GET", "/v1/config");
  if (r.status === 200) {
    state.config = r.body;
    state.revision = Number(r.etag);
    $("user-count").textContent = r.body.users.length;
    $("group-count").textContent = (r.body.groups || []).length;
  }
}

$("logout").addEventListener("click", async () => {
  await api("POST", "/ui/logout");
  signedOut("You are signed out.");
});

// ---- Navigation ----

function show(view) {
  if (!$("v-" + view)) view = "dashboard";
  state.view = view;
  for (const v of document.querySelectorAll(".view")) v.hidden = v.id !== "v-" + view;
  for (const a of document.querySelectorAll("#menu a")) {
    if (a.dataset.view === view) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
  }
  clearInterval(state.timer);
  if (view === "dashboard") { refreshStatus(); state.timer = setInterval(() => { if (!document.hidden) refreshStatus(); }, 5000); }
  if (view === "users") loadUsers();
  if (view === "activation") fillActivationUsers();
  if (view === "history") loadHistory();
  if (view === "groups") loadGroups();
  if (view === "templates") loadTemplates();
  if (view === "audit") loadAudit();
}

$("menu").addEventListener("click", (e) => {
  const a = e.target.closest("a[data-view]");
  if (!a) return;
  e.preventDefault();
  history.replaceState(null, "", "#" + a.dataset.view);
  show(a.dataset.view);
});

// ---- Dashboard ----

async function refreshStatus() {
  const r = await api("GET", "/v1/status");
  if (r.status !== 200) { $("banner").hidden = false; $("banner").textContent = "Status unavailable: " + reason(r); return; }
  $("banner").hidden = true;
  const s = r.body;
  state.revision = s.revision;
  $("st-domain").textContent = s.domain;
  $("st-time").textContent = "checked " + new Date(s.checked_at).toLocaleTimeString();
  const behind = s.delivery.filter((d) => d.applied_revision < s.revision || d.failed > 0).length;
  $("st-rev").textContent = `revision ${s.revision} · ` + (behind ? `${behind} consumer(s) behind` : "applied everywhere");
  $("st-active").textContent = s.users.active;
  $("st-inactive").replaceChildren(
    s.users.suspended ? pill(`${s.users.suspended} suspended`, "warn") : "",
    s.users.retired ? " " : "", s.users.retired ? pill(`${s.users.retired} retired`, "idle") : "");
  const presence = await api("GET", "/v1/presence");
  $("st-registered").textContent = presence.status === 200 && presence.body.sip_core ? presence.body.registered : "–";
  $("st-pending-note").textContent = s.users.pending ? `${s.users.pending} user(s) waiting for activation` : "devices at the SIP core";
  $("st-links").textContent = s.pending_activations;
  const up = s.services.filter((x) => x.up).length;
  $("st-up").textContent = s.services.length ? `${up} / ${s.services.length}` : "–";
  $("st-upbar").style.width = s.services.length ? `${(100 * up) / s.services.length}%` : "0";
  $("st-upbar").style.background = up === s.services.length ? "var(--ok)" : "var(--warn)";
  $("st-services").replaceChildren(...(s.services.length ? s.services.flatMap((x) => [
    el("div", {}, x.name),
    el("div", { class: "m" }, x.up ? `${x.latency_ms} ms` : (x.detail || "")),
    el("div", {}, x.up ? pill("up", "ok") : pill("down", "bad")),
  ]) : [el("div", { class: "muted" }, "No services configured to check."), el("div"), el("div")]));
  $("st-delivery").replaceChildren(...s.delivery.flatMap((d) => [
    el("div", {}, { userdb: "User database (Diameter)", sip: "SIP credentials" }[d.consumer] || d.consumer),
    el("div", { class: "m" }, `rev ${d.applied_revision}`),
    el("div", {}, d.failed ? pill(`${d.failed} failed`, "bad") : d.pending ? pill(`${d.pending} pending`, "warn") : pill("applied", "ok")),
  ]));
  $("st-activity").replaceChildren(...(s.activity.length ? s.activity.map((a) =>
    el("li", {}, el("time", { datetime: a.created_at }, when(a.created_at)), el("span", {}, el("b", {}, a.actor), " ", activityText(a)))) :
    [el("li", {}, el("span", { class: "muted" }, "No activity yet."))]));
}

// One audit entry in words: a commit by its reason, other actions by name and target.
function activityText(a) {
  if (a.action === "commit") {
    let why = "";
    try { why = JSON.parse(a.detail).reason || ""; } catch (_) { /* no detail */ }
    return `${why || "changed the configuration"} · revision ${a.revision}`;
  }
  const words = { "activation-issued": "issued an activation link for", activated: "activated a device as",
                  "password-link": "issued a password link for", "sign-in-refused": "was refused at" }[a.action];
  return `${words || a.action.replaceAll("-", " ")} ${a.target || ""}`;
}

// ---- Users ----

async function loadUsers() {
  const q = new URLSearchParams({ q: $("q").value, status: $("st").value, limit: "1000" });
  const [r, presence] = await Promise.all([api("GET", "/v1/users?" + q), api("GET", "/v1/presence")]);
  if (r.status !== 200) return toast("Users unavailable: " + reason(r));
  const seen = presence.status === 200 ? presence.body.users : {};
  const device = (login) => {
    const p = seen[login] || {};
    const signIn = p.last_sign_in ? `signed in ${when(p.last_sign_in)}` : "";
    if (p.registered === true) return el("div", {}, pill("registered", "ok"), el("div", { class: "muted" }, [p.user_agent, signIn].filter(Boolean).join(" · ")));
    if (p.registered === false) return el("div", {}, pill("not registered", "idle"), signIn ? el("div", { class: "muted" }, signIn) : null);
    return el("span", { class: "muted" }, signIn || "–");
  };
  state.revision = Number(r.etag);
  state.users = r.body.users;
  $("users-total").textContent = `${r.body.total} user${r.body.total === 1 ? "" : "s"}`;
  $("users").tBodies[0].replaceChildren(...r.body.users.map((u) => {
    const suspended = u.status === "suspended";
    return el("tr", {},
      el("td", {}, el("a", { href: "#users", class: "link", onclick: (e) => { e.preventDefault(); userDetail(u.login); } }, el("b", {}, u.login))),
      el("td", {}, u.display_name || ""), el("td", { class: "mono" }, u.mcptt_id),
      el("td", {}, u.template || ""), el("td", {}, pill(u.status, statusKind[u.status] || "idle")), el("td", {}, device(u.login)),
      el("td", {}, el("div", { class: "acts" },
        u.status === "retired" ? null : el("button", { class: "small" + (suspended ? "" : " danger"), onclick: () => setStatus(u, suspended ? "restore" : "suspend") }, suspended ? "Restore" : "Suspend"),
        u.status === "active" ? el("button", { class: "small", onclick: () => passwordLink(u) }, "Password link") : null,
        u.status === "active" ? el("button", { class: "small", onclick: () => { history.replaceState(null, "", "#activation"); show("activation"); $("act-user").value = u.login; } }, "Activate device") : null)));
  }));
}
let searchTimer;
$("q").addEventListener("input", () => { clearTimeout(searchTimer); searchTimer = setTimeout(loadUsers, 250); });
$("st").addEventListener("change", loadUsers);

async function setStatus(u, action) {
  const ok = await confirmAction(action === "suspend" ? `Suspend ${u.login}?` : `Restore ${u.login}?`,
    action === "suspend" ? "They can no longer sign in, register or be authorised; their devices stop working." :
      "They can sign in and use their devices again.", action === "suspend" ? "Suspend" : "Restore");
  if (!ok) return;
  const r = await api("POST", `/v1/users/${encodeURIComponent(u.login)}/${action}`, { ifMatch: state.revision });
  if (r.status === 412) { toast("Someone else changed the configuration. The list is reloaded; try again."); return loadUsers(); }
  if (r.status !== 200) return toast(`Not changed: ${reason(r)}`);
  toast(`${u.login} ${action === "suspend" ? "suspended" : "restored"} · revision ${r.body.revision}`);
  await loadConfig();
  loadUsers();
}

async function passwordLink(u) {
  const r = await api("POST", `/v1/users/${encodeURIComponent(u.login)}/password-link?purpose=reset`);
  if (r.status !== 200) return toast("No link: " + reason(r));
  message(`Password link for ${u.login}`,
    `Give this one-time link to ${u.login}. It expires ${new Date(r.body.expires * 1000).toLocaleString()}. It is shown only now.`, r.body.url);
}
$("msg-copy").addEventListener("click", () => copy($("msg-url").textContent, $("msg-url")));

// ---- User detail ----

async function userDetail(login) {
  const r = await api("GET", `/v1/users/${encodeURIComponent(login)}`);
  if (r.status !== 200) return toast("User unavailable: " + reason(r));
  const u = r.body.user;
  $("dt-title").textContent = u.display_name ? `${u.login} (${u.display_name})` : u.login;
  const rows = [["Status", u.status], ["Template", u.template || "none"], ["MC ID", u.mc_id], ["MCPTT ID", u.mcptt_id],
    ["SIP URI", u.sip_uri], ["MCData ID", u.mcdata_id || "none"], ["MCVideo ID", u.mcvideo_id || "none"],
    ["Client IDs", (u.client_ids || []).join(", ")], ["Groups", (u.groups || []).join(", ") || "none"], ["Email", u.email || ""]];
  $("dt-ids").replaceChildren(...rows.flatMap(([k, v]) => [el("dt", {}, k), el("dd", { class: k.includes("ID") || k === "SIP URI" ? "mono" : "" }, v)]));
  $("dt-profile").textContent = JSON.stringify(r.body.effective_profile || {}, null, 2);
  $("dt-revoke").onclick = () => revokeDevices(u.login);
  $("dlg-detail").showModal();
}

async function revokeDevices(login) {
  $("dlg-detail").close();
  if (!await confirmAction(`Revoke ${login}'s devices?`,
    "Their devices stop registering at once. They activate a device again with a new activation code.", "Revoke devices")) return;
  const r = await api("POST", `/v1/users/${encodeURIComponent(login)}/revoke-devices`);
  toast(r.status === 200 ? `${login}'s devices revoked` : "Not revoked: " + reason(r));
}

// ---- Audit ----

let auditEntries = [];
async function loadAudit() {
  const r = await api("GET", "/v1/audit?latest=300");
  if (r.status !== 200) return toast("Audit unavailable: " + reason(r));
  auditEntries = r.body.audit;
  renderAudit();
}
function renderAudit() {
  const q = $("audit-q").value.toLowerCase();
  const list = auditEntries.filter((a) => !q || `${a.actor} ${a.action} ${a.target}`.toLowerCase().includes(q));
  $("audit-rows").replaceChildren(...(list.length ? list.map((a) => el("tr", {},
    el("td", { class: "mono" }, when(a.created_at)), el("td", {}, a.actor), el("td", {}, a.action.replaceAll("-", " ")),
    el("td", { class: "mono" }, a.target || ""), el("td", { class: "mono" }, a.revision || ""),
    el("td", { class: "mono muted" }, a.detail && a.detail !== "{}" ? a.detail.slice(0, 160) : ""))) :
    [el("tr", {}, el("td", { colspan: "6", class: "muted" }, "Nothing matches."))]));
}
$("audit-q").addEventListener("input", renderAudit);

// ---- New user ----

function randomUUID() {
  if (crypto.randomUUID) return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40; b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

function newUserIDs() {
  const login = $("nu-login").value.trim(), d = state.config.system.domain;
  const ids = { mc_id: `sip:${login}.mc@${d}`, mcptt_id: `sip:${login}.mcptt@${d}`, sip_uri: `sip:${login}@${d}` };
  if ($("nu-mcdata").checked) ids.mcdata_id = `sip:${login}.mcdata@${d}`;
  if ($("nu-mcvideo").checked) ids.mcvideo_id = `sip:${login}.mcvideo@${d}`;
  return ids;
}
function showIDs() {
  $("nu-ids").textContent = $("nu-login").value ? Object.values(newUserIDs()).join("  ") : "";
}
for (const id of ["nu-login", "nu-mcdata", "nu-mcvideo"]) $(id).addEventListener("input", showIDs);

$("new-user").addEventListener("click", async () => {
  await loadConfig();
  $("form-user").reset();
  $("nu-error").hidden = true;
  $("nu-template").replaceChildren(el("option", { value: "" }, "(none)"),
    ...Object.keys(state.config.templates || {}).sort().map((t) => el("option", { value: t }, t)));
  $("nu-groups").replaceChildren(...(state.config.groups || []).map((g) =>
    el("label", {}, el("input", { type: "checkbox", value: g.id }), g.display_name || g.id)));
  showIDs();
  $("dlg-user").showModal();
});

$("form-user").addEventListener("submit", async (e) => {
  if (e.submitter && e.submitter.value !== "save") return;
  e.preventDefault();
  const login = $("nu-login").value.trim();
  if (state.config.users.some((u) => u.login === login)) {
    $("nu-error").hidden = false; $("nu-error").textContent = `A user ${login} exists already.`; return;
  }
  const user = { login, status: "active", ...newUserIDs(), client_ids: [`urn:uuid:${randomUUID()}`],
    groups: [...$("nu-groups").querySelectorAll("input:checked")].map((c) => c.value) };
  if ($("nu-name").value.trim()) user.display_name = $("nu-name").value.trim();
  if ($("nu-email").value.trim()) user.email = $("nu-email").value.trim();
  if ($("nu-template").value) user.template = $("nu-template").value;
  const r = await api("PUT", `/v1/users/${encodeURIComponent(login)}`, { body: JSON.stringify(user), type: "application/json", ifMatch: state.revision });
  if (r.status !== 200) {
    const detail = Array.isArray(r.body && r.body.detail) ? ": " + r.body.detail.map((p) => `${p.field} ${p.reason}`).join("; ") : "";
    $("nu-error").hidden = false;
    $("nu-error").textContent = (r.status === 412 ? "Someone else changed the configuration; try again." : reason(r)) + detail;
    await loadConfig();
    return;
  }
  $("dlg-user").close();
  toast(`${login} created · revision ${r.body.revision}`);
  await loadConfig();
  loadUsers();
});

// ---- Groups ----

const localUsers = () => (state.config ? state.config.users : []).filter((u) => u.status !== "retired")
  .sort((a, b) => a.login.localeCompare(b.login));
const members = (g) => Array.isArray(g.members) ? g.members : [];

async function loadGroups() {
  await loadConfig();
  const byID = Object.fromEntries(localUsers().map((u) => [u.mcptt_id, u.login]));
  const groups = [...(state.config.groups || [])].sort((a, b) => a.id.localeCompare(b.id));
  $("groups").tBodies[0].replaceChildren(...(groups.length ? groups.map((g) => {
    const names = members(g).map((m) => byID[m] || m);
    const services = [pill("MCPTT", "info"), g.mcdata ? pill("MCData", "info") : null, g.mcvideo ? pill("MCVideo", "info") : null];
    const locked = g.regrouped || g.regrouped_by || (g.temporary_constituents || []).length;
    return el("tr", {},
      el("td", {}, el("b", {}, g.id.replace(/^sip:/, "").split("@")[0]), el("div", { class: "mono muted" }, g.id)),
      el("td", {}, g.type || "prearranged"),
      el("td", {}, el("b", {}, members(g).length), " ", el("span", { class: "muted" }, names.slice(0, 4).join(", ") + (names.length > 4 ? ", …" : ""))),
      el("td", {}, el("div", { class: "acts" }, services)),
      el("td", {}, g.protect_media || g.protect_floor ? pill([g.protect_media && "media", g.protect_floor && "floor"].filter(Boolean).join(" + "), "ok") : el("span", { class: "muted" }, "none")),
      el("td", {}, locked ? el("span", { class: "muted" }, "regrouped") : el("div", { class: "acts" },
        el("button", { class: "small", onclick: () => openGroup(g) }, "Edit"),
        el("button", { class: "small danger", onclick: () => deleteGroup(g) }, "Delete"))));
  }) : [el("tr", {}, el("td", { colspan: "6", class: "muted" }, "No groups yet."))]));
}

let editing = null;
function openGroup(g) {
  editing = g ? structuredClone(g) : null;
  $("form-group").reset();
  $("gr-error").hidden = true;
  $("gr-title").textContent = g ? "Edit group" : "New group";
  $("gr-name").value = g ? g.id.replace(/^sip:/, "").split("@")[0] : "";
  $("gr-name").disabled = !!g;
  $("gr-mcvideo").checked = !!(g && g.mcvideo);
  $("gr-mcdata").checked = !!(g && g.mcdata);
  $("gr-sds").checked = g && g.mcdata ? !!(g.mcdata.sds_supported && g.mcdata.allow_sds) : true;
  $("gr-fd").checked = !!(g && g.mcdata && g.mcdata.fd_supported && g.mcdata.allow_fd);
  $("gr-mcdata-options").hidden = !$("gr-mcdata").checked;
  // The group's rights for every member: on for a new group.
  $("gr-emergency").checked = g ? !!g.allow_emergency_call : true;
  $("gr-imminent").checked = g ? !!g.allow_imminent_peril : true;
  $("gr-alert").checked = g ? !!g.allow_emergency_alert : true;
  $("gr-transmit").checked = g && g.mcdata ? !!g.mcdata.transmit_all : true;
  $("gr-media").checked = !!(g && g.protect_media);
  $("gr-floor").checked = !!(g && g.protect_floor);
  const chosen = new Set(g ? members(g) : []);
  $("gr-members").replaceChildren(...localUsers().map((u) =>
    el("label", { "data-text": `${u.login} ${u.display_name || ""}`.toLowerCase() },
      el("input", { type: "checkbox", value: u.mcptt_id, checked: chosen.has(u.mcptt_id) }), u.login,
      u.display_name ? el("span", { class: "muted" }, ` ${u.display_name}`) : "")));
  // Members of other systems are kept as they are.
  const foreign = [...chosen].filter((m) => !localUsers().some((u) => u.mcptt_id === m));
  $("gr-members").dataset.foreign = JSON.stringify(foreign);
  groupCount();
  $("dlg-group").showModal();
}
function groupID() { return `sip:${$("gr-name").value.trim()}@${state.config.system.domain}`; }
function groupCount() {
  $("gr-id").textContent = $("gr-name").value ? groupID() : "";
  $("gr-count").textContent = `${$("gr-members").querySelectorAll("input:checked").length} selected`;
}
$("gr-name").addEventListener("input", groupCount);
$("gr-mcdata").addEventListener("change", () => { $("gr-mcdata-options").hidden = !$("gr-mcdata").checked; });
$("gr-members").addEventListener("change", groupCount);
$("gr-filter").addEventListener("input", () => {
  const q = $("gr-filter").value.toLowerCase();
  for (const l of $("gr-members").children) l.hidden = !!q && !l.dataset.text.includes(q);
});
$("new-group").addEventListener("click", async () => { await loadConfig(); openGroup(null); });

$("form-group").addEventListener("submit", async (e) => {
  if (e.submitter && e.submitter.value !== "save") return;
  e.preventDefault();
  const id = editing ? editing.id : groupID();
  if (!editing && (state.config.groups || []).some((g) => g.id === id)) {
    $("gr-error").hidden = false; $("gr-error").textContent = `A group ${id} exists already.`; return;
  }
  const chosen = [...$("gr-members").querySelectorAll("input:checked")].map((c) => c.value);
  const g = editing || { id };
  g.members = [...chosen, ...JSON.parse($("gr-members").dataset.foreign || "[]")];
  g.protect_media = $("gr-media").checked;
  g.protect_floor = $("gr-floor").checked;
  // TS 24.481 rules for every member: <allow-MCPTT-emergency-call>, <allow-imminent-peril-call>,
  // <allow-MCPTT-emergency-alert> (and the MCVideo ones in the MCVideo part).
  const rights = (target) => {
    for (const [key, box] of [["allow_emergency_call", "gr-emergency"], ["allow_imminent_peril", "gr-imminent"], ["allow_emergency_alert", "gr-alert"]]) {
      if ($(box).checked) target[key] = true; else delete target[key];
    }
  };
  rights(g);
  if ($("gr-mcvideo").checked && !g.mcvideo) {
    // A new MCVideo part: the members who have MCVideo.
    g.mcvideo = { members: localUsers().filter((u) => chosen.includes(u.mcptt_id) && u.mcvideo_id).map((u) => u.mcvideo_id) };
  } else if (!$("gr-mcvideo").checked) {
    delete g.mcvideo;
  }
  if (g.mcvideo) rights(g.mcvideo);
  if ($("gr-mcdata").checked) {
    // A new MCData part: the members who have MCData; its other settings stay as they are.
    if (!g.mcdata) g.mcdata = { members: localUsers().filter((u) => chosen.includes(u.mcptt_id) && u.mcdata_id).map((u) => u.mcdata_id) };
    g.mcdata.sds_supported = g.mcdata.allow_sds = $("gr-sds").checked;
    g.mcdata.fd_supported = g.mcdata.allow_fd = $("gr-fd").checked;
    // <mcdata-allow-transmit-data-in-this-group> for every member; a list of single members is kept.
    if ($("gr-transmit").checked) g.mcdata.transmit_all = true; else delete g.mcdata.transmit_all;
  } else {
    delete g.mcdata;
  }
  const r = await api("PUT", `/v1/groups/${encodeURIComponent(id)}`, { body: JSON.stringify(g), type: "application/json", ifMatch: state.revision });
  if (r.status !== 200) {
    const detail = Array.isArray(r.body && r.body.detail) ? ": " + r.body.detail.map((p) => `${p.record} ${p.field} ${p.reason}`).join("; ") : "";
    $("gr-error").hidden = false;
    $("gr-error").textContent = (r.status === 412 ? "Someone else changed the configuration; try again." : reason(r)) + detail;
    await loadConfig();
    return;
  }
  $("dlg-group").close();
  toast(`${id} saved · revision ${r.body.revision}`);
  loadGroups();
});

async function deleteGroup(g) {
  if (!await confirmAction(`Delete ${g.id}?`, `Its ${members(g).length} members lose the group; their devices are notified, and calls to it are refused.`, "Delete")) return;
  const r = await api("DELETE", `/v1/groups/${encodeURIComponent(g.id)}`, { ifMatch: state.revision });
  if (r.status !== 200) return toast("Not deleted: " + reason(r));
  toast(`${g.id} deleted · revision ${r.body.revision}`);
  loadGroups();
}

// ---- Templates ----

async function loadTemplates() {
  const r = await api("GET", "/v1/templates");
  if (r.status !== 200) return toast("Templates unavailable: " + reason(r));
  state.revision = Number(r.etag);
  $("templates").tBodies[0].replaceChildren(...(r.body.templates.length ? r.body.templates.map((t) => el("tr", {},
    el("td", {}, el("b", {}, t.name)),
    el("td", { class: "mono" }, Object.keys(t.profile || {}).join(", ") || "none"),
    el("td", {}, t.users),
    el("td", {}, el("div", { class: "acts" },
      el("button", { class: "small", onclick: () => openTemplate(t) }, "Edit"),
      el("button", { class: "small danger", disabled: t.users > 0, title: t.users ? "In use" : "", onclick: () => deleteTemplate(t) }, "Delete"))))) :
    [el("tr", {}, el("td", { colspan: "4", class: "muted" }, "No templates yet. Users then have only their own rights."))]));
}

function openTemplate(t) {
  $("form-template").reset();
  $("tp-error").hidden = true;
  $("tp-title").textContent = t ? `Edit template ${t.name}` : "New template";
  $("tp-name").value = t ? t.name : "";
  $("tp-name").disabled = !!t;
  const profile = t ? t.profile : { private_call: { allow: true } };
  $("tp-profile").value = JSON.stringify(profile, null, 2);
  const call = profile.group_call || {}, priv = profile.private_call || {};
  for (const box of $("tp-rights").querySelectorAll("input")) {
    const right = box.dataset.right;
    box.checked = right === "private_emergency" ? !!(priv.emergency && priv.emergency_any) : !!call[right];
  }
  $("dlg-template").showModal();
}
$("new-template").addEventListener("click", () => openTemplate(null));

$("form-template").addEventListener("submit", async (e) => {
  if (e.submitter && e.submitter.value !== "save") return;
  e.preventDefault();
  let profile;
  try { profile = JSON.parse($("tp-profile").value); } catch (err) {
    $("tp-error").hidden = false; $("tp-error").textContent = "The rights are not valid JSON: " + err.message; return;
  }
  if (!profile || typeof profile !== "object" || Array.isArray(profile)) {
    $("tp-error").hidden = false; $("tp-error").textContent = "The rights must be a JSON object of profile sections."; return;
  }
  // The checkboxes: <allow-emergency-group-call> and the others in group_call, the emergency private call in
  // private_call; the same in mcvideo when the template has MCVideo rights.
  const apply = (target) => {
    target.group_call = target.group_call || {};
    for (const box of $("tp-rights").querySelectorAll("input")) {
      const right = box.dataset.right;
      if (right === "private_emergency") {
        target.private_call = target.private_call || {};
        if (box.checked) { target.private_call.emergency = target.private_call.emergency_any = true; }
        else { delete target.private_call.emergency; delete target.private_call.emergency_any; }
      } else if (box.checked) target.group_call[right] = true;
      else delete target.group_call[right];
    }
    if (!Object.keys(target.group_call).length) delete target.group_call;
  };
  apply(profile);
  if (profile.mcvideo && typeof profile.mcvideo === "object") apply(profile.mcvideo);
  const name = $("tp-name").value.trim();
  const r = await api("PUT", `/v1/templates/${encodeURIComponent(name)}`, { body: JSON.stringify(profile), type: "application/json", ifMatch: state.revision });
  if (r.status !== 200) {
    const detail = Array.isArray(r.body && r.body.detail) ? ": " + r.body.detail.map((p) => `${p.record} ${p.field} ${p.reason}`).join("; ") : "";
    $("tp-error").hidden = false; $("tp-error").textContent = reason(r) + detail; return;
  }
  $("dlg-template").close();
  toast(`Template ${name} saved · revision ${r.body.revision}`);
  loadTemplates();
});

async function deleteTemplate(t) {
  if (!await confirmAction(`Delete template ${t.name}?`, "No user has it. This makes a new revision.", "Delete")) return;
  const r = await api("DELETE", `/v1/templates/${encodeURIComponent(t.name)}`, { ifMatch: state.revision });
  if (r.status !== 200) return toast("Not deleted: " + reason(r));
  toast(`Template ${t.name} deleted`);
  loadTemplates();
}

// ---- Import ----

const drop = $("drop");
drop.addEventListener("dragover", (e) => { e.preventDefault(); drop.classList.add("over"); });
drop.addEventListener("dragleave", () => drop.classList.remove("over"));
drop.addEventListener("drop", (e) => { e.preventDefault(); drop.classList.remove("over"); if (e.dataTransfer.files[0]) validate(e.dataTransfer.files[0]); });
$("file").addEventListener("change", () => { if ($("file").files[0]) validate($("file").files[0]); });

const uploadType = (f) => f.name.toLowerCase().endsWith(".csv") ? "text/csv"
  : "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";

async function validate(file) {
  $("file-name").textContent = `${file.name} · ${(file.size / 1024).toFixed(1)} KB`;
  const r = await api("POST", "/v1/uploads?mode=validate", { body: file, type: uploadType(file) });
  if (r.status !== 200 && r.status !== 422) { resetUpload(); return toast("Not checked: " + reason(r)); }
  const res = { ...r.body, problems: r.body.problems || [], changes: r.body.changes || [] };
  state.upload = { file, revision: res.revision, clean: r.status === 200 && res.changes.length > 0 };
  $("pv-base").textContent = `against revision ${res.revision}`;
  const kinds = { "user-added": ["new", "ok"], "user-changed": ["changed", "info"], "user-removed": ["removed", "warn"],
                  "group-added": ["new group", "ok"], "group-changed": ["group", "info"], "group-removed": ["group removed", "warn"] };
  const rows = [
    ...res.problems.map((p) => el("tr", {}, el("td", {}, pill("error", "bad")),
      el("td", {}, p.line ? `${p.sheet} line ${p.line}` : p.sheet), el("td", {}, `${p.column}: ${p.reason}`))),
    ...res.changes.slice(0, 300).map((c) => {
      const [label, kind] = kinds[c.kind] || [c.kind, "idle"];
      const detail = c.field ? `${c.field}${c.after !== undefined ? ": " + JSON.stringify(c.after) : ""}` : "";
      return el("tr", {}, el("td", {}, pill(label, kind)), el("td", {}, c.record), el("td", { class: "mono" }, detail));
    })];
  if (res.changes.length > 300) rows.push(el("tr", {}, el("td", { colspan: "3", class: "muted" }, `and ${res.changes.length - 300} more changes`)));
  if (!rows.length) rows.push(el("tr", {}, el("td", { colspan: "3", class: "muted" }, "The file changes nothing.")));
  $("pv-rows").replaceChildren(...rows);
  const count = (k) => res.changes.filter((c) => c.kind === k).length;
  $("pv-summary").replaceChildren(
    el("dt", {}, "File"), el("dd", {}, file.name),
    el("dt", {}, "Rows"), el("dd", {}, `${res.users} users, ${res.groups} groups`),
    el("dt", {}, "New users"), el("dd", {}, count("user-added")),
    el("dt", {}, "Changed users"), el("dd", {}, new Set(res.changes.filter((c) => c.kind === "user-changed").map((c) => c.record)).size),
    el("dt", {}, "Errors"), el("dd", {}, res.problems.length ? pill(`${res.problems.length}`, "bad") : "none",
      res.problems.length ? " fix the file and choose it again" : ""));
  $("apply").disabled = !state.upload.clean;
  $("apply").textContent = state.upload.clean ? "Apply changes" : "Apply";
  $("discard").disabled = false;
}

function resetUpload() {
  state.upload = null;
  $("file").value = "";
  $("file-name").textContent = "no file chosen";
  $("pv-base").textContent = "";
  $("pv-rows").replaceChildren(el("tr", {}, el("td", { colspan: "3", class: "muted" }, "Choose a file to see what it changes.")));
  $("pv-summary").replaceChildren(el("dt", {}, "File"), el("dd", {}, "none"));
  $("apply").disabled = $("discard").disabled = true;
}
$("discard").addEventListener("click", resetUpload);

$("apply").addEventListener("click", async () => {
  const u = state.upload;
  if (!u) return;
  $("apply").disabled = true;
  const r = await api("POST", "/v1/uploads?mode=apply", { body: u.file, type: uploadType(u.file), ifMatch: u.revision });
  if (r.status === 412) { toast("The configuration changed since the check. Checking again."); return validate(u.file); }
  if (r.status !== 200) { $("apply").disabled = false; return toast("Not applied: " + reason(r)); }
  const revs = r.body.revisions || [];
  toast(revs.length ? `Applied as revision${revs.length > 1 ? "s" : ""} ${revs.join(", ")}` : "Nothing to apply");
  resetUpload();
  loadConfig();
});

// ---- Activation ----

function fillActivationUsers() {
  const keep = $("act-user").value;
  const users = (state.config ? state.config.users : []).filter((u) => u.status === "active").sort((a, b) => a.login.localeCompare(b.login));
  $("act-user").replaceChildren(...users.map((u) => el("option", { value: u.login }, u.display_name ? `${u.login} (${u.display_name})` : u.login)));
  if (keep) $("act-user").value = keep;
  if (!state.me.activation) { $("act-issue").disabled = true; $("act-none").textContent = "Activation is not configured on this server."; }
}

$("act-issue").addEventListener("click", async () => {
  const login = $("act-user").value;
  if (!login) return;
  const r = await api("POST", `/v1/users/${encodeURIComponent(login)}/activation`);
  if (r.status !== 200) return toast("No link: " + reason(r));
  $("act-qr").src = "data:image/png;base64," + r.body.qr_png;
  $("act-login").textContent = login;
  $("act-expires").textContent = new Date(r.body.expires * 1000).toLocaleString();
  $("act-url").textContent = r.body.url;
  $("act-result").hidden = false;
  $("act-none").hidden = true;
});
$("act-copy").addEventListener("click", () => copy($("act-url").textContent, $("act-url")));

// ---- History ----

async function loadHistory() {
  const r = await api("GET", "/v1/revisions");
  if (r.status !== 200) return toast("History unavailable: " + reason(r));
  const list = [...r.body.revisions].sort((a, b) => b.revision - a.revision);
  const latest = list.length ? list[0].revision : 0;
  $("hist-rows").replaceChildren(...list.map((v) => el("tr", {},
    el("td", { class: "mono" }, v.revision), el("td", { class: "mono" }, when(v.created_at)), el("td", {}, v.actor),
    el("td", {}, v.source), el("td", {}, v.reason, v.changes ? el("span", { class: "muted" }, ` · ${v.changes.length} change${v.changes.length === 1 ? "" : "s"}`) : ""),
    el("td", {}, el("div", { class: "acts" },
      el("a", { class: "button small", href: `/v1/revisions/${v.revision}/export` }, "Export"),
      v.revision < latest ? el("button", { class: "small", onclick: () => rollback(v.revision, latest) }, "Roll back to this") : null)))));
}

async function rollback(n, latest) {
  const preview = await api("POST", `/v1/revisions/${n}/rollback?preview=1`);
  if (preview.status !== 200) return toast("No preview: " + reason(preview));
  const count = preview.body.changes.length;
  if (!await confirmAction(`Roll back to revision ${n}?`,
    `This makes a new revision with the configuration of revision ${n}: ${count} change${count === 1 ? "" : "s"} from revision ${latest}.`, "Roll back")) return;
  const r = await api("POST", `/v1/revisions/${n}/rollback`, { ifMatch: preview.body.revision });
  if (r.status !== 200) return toast("Not rolled back: " + reason(r));
  toast(`Rolled back · revision ${r.body.revision}`);
  await loadConfig();
  loadHistory();
}

boot();
