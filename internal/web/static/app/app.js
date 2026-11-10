"use strict";
// Console for agent-gateway. Plain JS, no build step. State that matters lives in the URL hash.

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const usd = (micros) => "$" + (Number(micros || 0) / 1e6).toFixed(Math.abs(micros) < 10000 && micros !== 0 ? 4 : 2);
const fmt = (n) => Number(n || 0).toLocaleString();
const when = (t) => (t ? new Date(t).toLocaleString() : "–");
const S = { me: null, cfg: {}, org: null, role: null };

class HTTPError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}

async function api(method, path, body) {
  const r = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-GW-CSRF": "1" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (r.status === 204) return null;
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new HTTPError(r.status, data?.error?.message || r.statusText);
  return data;
}

function toast(msg) {
  const t = document.createElement("div");
  t.className = "toast";
  t.setAttribute("role", "status");
  t.textContent = msg;
  document.body.append(t);
  setTimeout(() => t.remove(), 4000);
}

// region renders the four states of a data-loading area: loading, error, empty, full.
async function region(el, load, render, emptyText) {
  el.innerHTML = '<div class="skeleton"></div><div class="skeleton"></div><div class="skeleton"></div>';
  try {
    const data = await load();
    if (Array.isArray(data) && data.length === 0 && emptyText) {
      el.innerHTML = `<p class="state">${emptyText}</p>`;
      return;
    }
    render(data);
  } catch (e) {
    if (e.status === 401) return showLogin();
    el.innerHTML = `<p class="state error">Could not load this: ${esc(e.message)}. <button class="link" data-retry>Try again</button></p>`;
    $("[data-retry]", el).onclick = () => region(el, load, render, emptyText);
  }
}

function table(cols, rows) {
  return `<div class="table-wrap"><table><thead><tr>${cols.map((c) => `<th class="${c.num ? "num" : ""}">${c.label}</th>`).join("")}</tr></thead><tbody>${rows
    .map((r) => `<tr>${cols.map((c) => `<td class="${c.num ? "num" : ""}">${c.cell(r)}</td>`).join("")}</tr>`)
    .join("")}</tbody></table></div>`;
}

// dialog opens a modal form; onSubmit returns true to close.
function dialog(title, bodyHTML, onSubmit, submitLabel = "Save") {
  const d = $("#dlg");
  d.innerHTML = `<form method="dialog" class="form"><h2>${esc(title)}</h2>${bodyHTML}<p class="state error" hidden data-err></p>
    <div class="actions"><button type="button" data-cancel>Cancel</button><button class="primary" type="submit">${submitLabel}</button></div></form>`;
  const f = $("form", d);
  $("[data-cancel]", d).onclick = () => d.close();
  f.onsubmit = async (ev) => {
    ev.preventDefault();
    const btn = $("button[type=submit]", f);
    btn.disabled = true;
    try {
      if ((await onSubmit(new FormData(f), f)) !== false) d.close();
    } catch (e) {
      const err = $("[data-err]", f);
      err.hidden = false;
      err.textContent = e.message;
    } finally {
      btn.disabled = false;
    }
  };
  d.showModal();
  $("input, select, textarea", f)?.focus();
}

const confirmAction = (msg) => window.confirm(msg);
const pathID = (id) => id.split("/").map(encodeURIComponent).join("/");
const num = (v) => (v === "" || v == null ? 0 : Number(v));
const list = (v) => String(v || "").split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
const rank = { viewer: 1, member: 2, admin: 3, owner: 4 };
const can = (r) => rank[S.role] >= rank[r];

// ---------- auth ----------

function showLogin(msg) {
  document.title = "Sign in · agent-gateway";
  $("#root").innerHTML = `<form class="auth form" id="login"><h1>Sign in</h1>
    ${msg ? `<p class="state">${esc(msg)}</p>` : ""}
    <label>Email<input name="email" type="email" autocomplete="username" required></label>
    <label>Password<input name="password" type="password" autocomplete="current-password" required></label>
    <p class="state error" hidden data-err></p>
    <button class="primary" type="submit">Sign in</button>
    ${S.cfg.sso ? '<a class="btn" href="/api/auth/oidc/login">Sign in with SSO</a>' : ""}
    ${S.cfg.signup ? '<button type="button" class="link" id="to-signup">Create an account</button>' : ""}
    <a href="/" class="muted">Browse models</a></form>`;
  $("#login").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("POST", "/api/auth/login", { email: f.get("email"), password: f.get("password") });
      boot();
    } catch (e) {
      const el = $("[data-err]");
      el.hidden = false;
      el.textContent = e.message;
    }
  };
  if ($("#to-signup")) $("#to-signup").onclick = showSignup;
}

function showSignup() {
  $("#root").innerHTML = `<form class="auth form" id="signup"><h1>Create an account</h1>
    <label>Name<input name="name" autocomplete="name"></label>
    <label>Email<input name="email" type="email" autocomplete="username" required></label>
    <label>Password (10+ characters)<input name="password" type="password" minlength="10" autocomplete="new-password" required></label>
    <p class="state error" hidden data-err></p>
    <button class="primary" type="submit">Create account</button>
    <button type="button" class="link" id="to-login">I have an account</button></form>`;
  $("#to-login").onclick = () => showLogin();
  $("#signup").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("POST", "/api/auth/signup", Object.fromEntries(f));
      boot();
    } catch (e) {
      const el = $("[data-err]");
      el.hidden = false;
      el.textContent = e.message;
    }
  };
}

// ---------- shell ----------

const NAV = [
  ["usage", "Usage"], ["logs", "Request log"], ["keys", "API keys"], ["models", "Models"],
  ["providers", "Providers", "admin"], ["members", "Members"], ["settings", "Settings"],
  ["audit", "Audit log", "admin"], ["playground", "Playground"],
];
const PLATFORM = [
  ["platform/usage", "Platform usage"], ["platform/orgs", "Organizations"], ["platform/providers", "Public providers"],
  ["platform/models", "Public models"], ["platform/users", "Users"], ["platform/mcp", "MCP servers"], ["platform/audit", "Platform audit"],
];

async function boot() {
  try {
    S.cfg = await fetch("/api/auth/config").then((r) => r.json());
    const me = await api("GET", "/api/auth/me");
    S.me = me.user;
    S.orgs = me.orgs;
  } catch (e) {
    return e.status === 401 ? showLogin() : ($("#root").innerHTML = `<p class="state error page">Gateway unreachable: ${esc(e.message)}</p>`);
  }
  const saved = sessionStorage.getItem("org");
  const pick = S.orgs.find((o) => o.id === saved) || S.orgs[0];
  S.org = pick?.id || null;
  S.role = pick?.role || (S.me.operator ? "owner" : null);
  $("#root").innerHTML = `<header class="top">
      <button class="menu-btn" id="menu" aria-expanded="false" aria-controls="side">Menu</button>
      <a class="brand" href="#/usage">agent-gateway</a>
      ${S.orgs.length ? `<label class="inline"><span class="sr">Organization</span><select id="org">${S.orgs.map((o) => `<option value="${esc(o.id)}" ${o.id === S.org ? "selected" : ""}>${esc(o.name)}</option>`).join("")}</select></label>` : ""}
      <span class="spacer"></span>
      <a href="/" class="muted">Catalog</a>
      <a href="#/account" class="muted">${esc(S.me.email)}</a>
      <button id="logout">Sign out</button>
    </header>
    <div class="shell"><nav class="side" id="side" aria-label="Main"></nav><main id="main" tabindex="-1"></main></div>`;
  $("#logout").onclick = async () => { await api("POST", "/api/auth/logout"); showLogin(); };
  $("#menu").onclick = () => {
    const open = $("#side").classList.toggle("open");
    $("#menu").setAttribute("aria-expanded", open);
  };
  if ($("#org")) $("#org").onchange = (e) => {
    S.org = e.target.value;
    S.role = S.orgs.find((o) => o.id === S.org).role;
    sessionStorage.setItem("org", S.org);
    route();
  };
  window.onhashchange = route;
  route();
}

function navHTML(cur) {
  const link = ([k, label, min]) => (min && !can(min) ? "" : `<a href="#/${k}" ${cur === k ? 'aria-current="page"' : ""}>${label}</a>`);
  let h = S.org ? NAV.map(link).join("") : '<p class="muted">You are not in an organization yet.</p>';
  if (S.me.operator) h += `<div class="group">Platform</div>` + PLATFORM.map(link).join("");
  return h;
}

function route() {
  const path = location.hash.replace(/^#\/?/, "").split("?")[0] || (S.org ? "usage" : S.me.operator ? "platform/orgs" : "account");
  $("#side").innerHTML = navHTML(path);
  $("#side").classList.remove("open");
  const main = $("#main");
  const ob = `/api/orgs/${encodeURIComponent(S.org)}`;
  const views = {
    usage: () => usageView(main, ob, true), logs: () => logsView(main, ob), keys: () => keysView(main, ob),
    models: () => modelsView(main, ob, can("admin")), providers: () => providersView(main, ob), members: () => membersView(main, ob),
    settings: () => settingsView(main, ob), audit: () => auditView(main, ob), playground: () => playgroundView(main, ob),
    account: () => accountView(main),
    "platform/usage": () => usageView(main, "/api/admin", false), "platform/orgs": () => orgsView(main),
    "platform/providers": () => providersView(main, "/api/admin"), "platform/models": () => modelsView(main, "/api/admin", true),
    "platform/users": () => usersView(main), "platform/mcp": () => mcpView(main), "platform/audit": () => auditView(main, "/api/admin"),
  };
  const v = views[path];
  if (!v || (!path.startsWith("platform/") && path !== "account" && !S.org) || (path.startsWith("platform/") && !S.me.operator)) {
    main.innerHTML = `<h1>Not found</h1><p class="state">This page does not exist or you do not have access. <a href="#/">Go to the start</a>.</p>`;
    return;
  }
  v();
  main.focus({ preventScroll: true });
}

// ---------- usage ----------

function barChart(daily, field, fmtFn) {
  const W = 720, H = 160, pad = 18;
  const max = Math.max(...daily.map((d) => d[field]), 0);
  const bw = (W - pad) / daily.length;
  const bars = daily.map((d, i) => {
    const h = max ? ((H - pad * 2) * d[field]) / max : 0;
    return `<rect x="${(i * bw + 2).toFixed(1)}" y="${(H - pad - h).toFixed(1)}" width="${Math.max(bw - 4, 1).toFixed(1)}" height="${h.toFixed(1)}"><title>${d.key}: ${fmtFn(d[field])}</title></rect>`;
  });
  const first = daily[0]?.key || "", last = daily.at(-1)?.key || "";
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="${field} per day, peak ${fmtFn(max)}">${bars.join("")}
    <text x="0" y="${H - 2}">${first}</text><text x="${W}" y="${H - 2}" text-anchor="end">${last}</text><text x="0" y="10">peak ${fmtFn(max)}</text></svg>`;
}

function usageView(main, base, isOrg) {
  const days = new URLSearchParams(location.hash.split("?")[1]).get("days") || "30";
  main.innerHTML = `<h1>${isOrg ? "Usage" : "Platform usage"}</h1>
    <div class="row"><label class="shrink">Period<select id="days">${["7", "30", "90"].map((d) => `<option ${d === days ? "selected" : ""} value="${d}">Last ${d} days</option>`).join("")}</select></label>
    <label class="shrink">Chart<select id="metric"><option value="cost_usd">Cost</option><option value="requests">Requests</option><option value="out_tokens">Output tokens</option></select></label></div>
    <div id="u"></div>`;
  $("#days").onchange = (e) => (location.hash = `#/${isOrg ? "usage" : "platform/usage"}?days=${e.target.value}`);
  const el = $("#u");
  region(el, async () => {
    const [u, org, keys] = await Promise.all([
      api("GET", `${base}/usage?days=${days}`),
      isOrg ? api("GET", base) : null,
      isOrg ? api("GET", `${base}/keys`) : [],
    ]);
    return { u, org, keys };
  }, ({ u, org, keys }) => {
    const t = u.total;
    const keyName = Object.fromEntries(keys.map((k) => [k.id, k.name]));
    const draw = () => {
      const m = $("#metric").value;
      $("#chart").innerHTML = barChart(u.daily, m, m === "cost_usd" ? (v) => "$" + v.toFixed(2) : fmt);
    };
    el.innerHTML = `<div class="stats">
        <div class="stat"><b>${fmt(t.requests)}</b><span>requests</span></div>
        <div class="stat"><b>$${t.cost_usd.toFixed(2)}</b><span>cost</span></div>
        <div class="stat"><b>${fmt(t.in_tokens)} / ${fmt(t.out_tokens)}</b><span>tokens in / out</span></div>
        <div class="stat"><b>${t.requests ? ((100 * t.errors) / t.requests).toFixed(1) : "0"}%</b><span>errors</span></div>
        <div class="stat"><b>${fmt(t.avg_latency_ms)} ms</b><span>average latency</span></div>
        ${org ? `<div class="stat"><b>${org.prepaid ? usd(org.balance_micros) : "unlimited"}</b><span>credit balance</span></div>` : ""}
      </div><div id="chart"></div>
      <h2>By model</h2>${u.models.length ? usageTable(u.models, (k) => `<code>${esc(k)}</code>`) : '<p class="state">No requests in this period. Send one from the Playground or with an API key.</p>'}
      ${isOrg ? `<h2>By API key</h2>${u.keys.length ? usageTable(u.keys, (k) => esc(keyName[k] || k)) : '<p class="state">No key usage in this period.</p>'}` : ""}`;
    $("#metric").onchange = draw;
    draw();
  });
}

function usageTable(rows, name) {
  return table([
    { label: "Name", cell: (r) => name(r.key) },
    { label: "Requests", num: 1, cell: (r) => fmt(r.requests) },
    { label: "Errors", num: 1, cell: (r) => fmt(r.errors) },
    { label: "Tokens in", num: 1, cell: (r) => fmt(r.in_tokens) },
    { label: "Tokens out", num: 1, cell: (r) => fmt(r.out_tokens) },
    { label: "Cache hits", num: 1, cell: (r) => fmt(r.cache_hits) },
    { label: "Avg latency", num: 1, cell: (r) => fmt(r.avg_latency_ms) + " ms" },
    { label: "Cost", num: 1, cell: (r) => "$" + r.cost_usd.toFixed(4) },
  ], rows);
}

// ---------- logs ----------

function logsView(main, base) {
  main.innerHTML = `<h1>Request log</h1><p class="muted">Newest first. Prompts and completions are not stored.</p><div class="row"><button class="shrink" id="refresh">Refresh</button></div><div id="l"></div>`;
  const load = () => region($("#l"), () => api("GET", `${base}/logs?limit=200`), (logs) => {
    $("#l").innerHTML = table([
      { label: "Time", cell: (l) => when(l.at) },
      { label: "Model", cell: (l) => `<code>${esc(l.model)}</code>` },
      { label: "Provider", cell: (l) => esc(l.provider || "–") },
      { label: "Status", cell: (l) => `<span class="tag ${l.status < 400 ? "ok" : "bad"}">${l.status}</span>${l.error ? ` <span class="muted">${esc(l.error)}</span>` : ""}` },
      { label: "In", num: 1, cell: (l) => fmt(l.in_tokens) },
      { label: "Out", num: 1, cell: (l) => fmt(l.out_tokens) },
      { label: "Cost", num: 1, cell: (l) => usd(l.cost_micros) },
      { label: "Latency", num: 1, cell: (l) => fmt(l.latency_ms) + " ms" },
      { label: "Mode", cell: (l) => [l.stream && "stream", l.cached && "cached"].filter(Boolean).join(", ") || "–" },
    ], logs);
  }, "No requests yet. They appear here within a few seconds of being served.");
  $("#refresh").onclick = load;
  load();
}

// ---------- keys ----------

function keysView(main, base) {
  main.innerHTML = `<h1>API keys</h1><p class="muted">Use a key as <code>Authorization: Bearer sk-gw-…</code> against <code>${esc(location.origin)}/v1</code>.</p>
    ${can("member") ? '<div class="row"><button class="primary shrink" id="new">Create key</button></div>' : ""}<div id="new-secret"></div><div id="k"></div>`;
  const load = () => region($("#k"), () => api("GET", `${base}/keys`), (keys) => {
    $("#k").innerHTML = table([
      { label: "Name", cell: (k) => esc(k.name) },
      { label: "Key", cell: (k) => `<code>${esc(k.prefix)}…</code>` },
      { label: "Limits", cell: (k) => [k.rpm && `${k.rpm} RPM`, k.tpm && `${fmt(k.tpm)} TPM`, k.monthly_budget_micros && `${usd(k.monthly_budget_micros)}/month`].filter(Boolean).join(", ") || "none" },
      { label: "Models", cell: (k) => (k.models?.length ? k.models.map((m) => `<code>${esc(m)}</code>`).join(" ") : "all") },
      { label: "Expires", cell: (k) => (k.expires_at ? when(k.expires_at) : "never") },
      { label: "Status", cell: (k) => (k.revoked ? '<span class="tag bad">revoked</span>' : '<span class="tag ok">active</span>') },
      { label: "", cell: (k) => (!k.revoked && (can("admin") || k.created_by === S.me.id) ? `<button class="danger" data-revoke="${esc(k.id)}">Revoke</button>` : "") },
    ], keys);
    $$("[data-revoke]").forEach((b) => (b.onclick = async () => {
      if (!confirmAction("Revoke this key? Apps using it stop working within seconds. This cannot be undone.")) return;
      await api("PATCH", `${base}/keys/${b.dataset.revoke}`, { revoked: true }).catch((e) => toast(e.message));
      load();
    }));
  }, "No API keys yet. Create one to call the API.");
  if ($("#new")) $("#new").onclick = () => dialog("Create API key", `
      <label>Name<input name="name" placeholder="production backend" required></label>
      <div class="row"><label>Requests per minute<input name="rpm" type="number" min="0" placeholder="0 = no limit"></label>
      <label>Tokens per minute<input name="tpm" type="number" min="0" placeholder="0 = no limit"></label></div>
      <div class="row"><label>Monthly budget (USD)<input name="budget" type="number" min="0" step="0.01" placeholder="0 = no limit"></label>
      <label>Expires in days<input name="exp" type="number" min="0" placeholder="0 = never"></label></div>
      <label>Allowed models (comma separated, empty = all)<input name="models" placeholder="openai/gpt-4o-mini"></label>`,
    async (f) => {
      const r = await api("POST", `${base}/keys`, {
        name: f.get("name"), rpm: num(f.get("rpm")), tpm: num(f.get("tpm")), monthly_budget_usd: num(f.get("budget")),
        expires_in_days: num(f.get("exp")), models: list(f.get("models")),
      });
      $("#new-secret").innerHTML = `<p class="secret"><b>Copy this key now. It will not be shown again.</b><br><code id="sec">${esc(r.secret)}</code> <button id="copy">Copy</button></p>`;
      $("#copy").onclick = () => navigator.clipboard.writeText(r.secret).then(() => toast("Key copied"));
      load();
    }, "Create");
  load();
}

// ---------- providers ----------

const PROVIDER_TYPES = ["openai", "anthropic", "gemini", "mistral", "groq", "cohere", "ollama", "cerebras", "perplexity", "openrouter", "nebius", "parasail", "sgl"];

function providersView(main, base) {
  const platform = base === "/api/admin";
  main.innerHTML = `<h1>${platform ? "Public providers" : "Private providers"}</h1>
    <p class="muted">${platform ? "Available to every organization's models." : "Only this organization's models can use these. Bring your own keys or self-hosted endpoints."}
    Any OpenAI-compatible API (vLLM, DeepSeek, Together, LM Studio…) works as type <code>openai</code> with a base URL.</p>
    <div class="row"><button class="primary shrink" id="new">Add provider</button></div><div id="p"></div>`;
  const form = (p = {}) => `
    <div class="row"><label>Name<input name="name" value="${esc(p.name)}" required></label>
    <label>Type<select name="type">${(platform ? PROVIDER_TYPES : PROVIDER_TYPES.slice(0, 3).concat("cohere")).map((t) => `<option ${t === p.type ? "selected" : ""}>${t}</option>`).join("")}</select></label></div>
    <label>Base URL (optional for hosted APIs)<input name="base_url" value="${esc(p.base_url)}" placeholder="https://api.example.com"></label>
    <label>API keys, one per line ${p.id ? "(leave empty to keep the stored keys; required when the URL or type changes)" : ""}<textarea name="keys" autocomplete="off" spellcheck="false"></textarea></label>
    <div class="row"><label>Timeout seconds<input name="timeout" type="number" min="1" value="${esc(p.timeout_seconds || 120)}"></label>
    <label class="inline shrink"><input type="checkbox" name="enabled" ${p.enabled !== false ? "checked" : ""}> Enabled</label></div>`;
  const save = (p) => async (f) => {
    const typed = list(f.get("keys")).filter((k) => !k.includes(","));
    const keys = typed.length ? typed.map((v) => ({ value: v, weight: 1 })) : (p.keys || []).map((k) => ({ id: k.id, weight: k.weight, models: k.models }));
    const body = { name: f.get("name"), type: f.get("type"), base_url: f.get("base_url"), keys, timeout_seconds: num(f.get("timeout")), enabled: f.get("enabled") === "on" };
    await api(p.id ? "PUT" : "POST", `${base}/providers${p.id ? "/" + p.id : ""}`, body);
    toast("Provider saved");
    load();
  };
  const load = () => region($("#p"), () => api("GET", `${base}/providers`), (ps) => {
    $("#p").innerHTML = table([
      { label: "Name", cell: (p) => `${esc(p.name)}<br><code class="muted">${esc(p.id)}</code>` },
      { label: "Type", cell: (p) => esc(p.type) },
      { label: "Base URL", cell: (p) => (p.base_url ? `<code>${esc(p.base_url)}</code>` : "default") },
      { label: "Keys", num: 1, cell: (p) => p.keys.length },
      { label: "Status", cell: (p) => (p.enabled ? '<span class="tag ok">enabled</span>' : '<span class="tag">disabled</span>') },
      { label: "", cell: (p) => `<button data-edit="${esc(p.id)}">Edit</button> <button class="danger" data-del="${esc(p.id)}">Delete</button>` },
    ], ps);
    $$("[data-edit]").forEach((b) => (b.onclick = () => { const p = ps.find((x) => x.id === b.dataset.edit); dialog("Edit provider", form(p), save(p)); }));
    $$("[data-del]").forEach((b) => (b.onclick = async () => {
      if (!confirmAction("Delete this provider? Models routed only to it stop working.")) return;
      await api("DELETE", `${base}/providers/${b.dataset.del}`).catch((e) => toast(e.message));
      load();
    }));
  }, "No providers yet. Add one with its API key, then create models that route to it.");
  $("#new").onclick = () => dialog("Add provider", form(), save({}));
  load();
}

// ---------- models ----------

function modelsView(main, base, manage) {
  const platform = base === "/api/admin";
  main.innerHTML = `<h1>${platform ? "Public models" : "Models"}</h1>
    <p class="muted">${platform ? "Listed in the public catalog and usable by every organization." : "Public models plus this organization's private ones."} Prices are USD per 1M tokens.</p>
    ${manage ? `<div class="row"><button class="primary shrink" id="new">Add ${platform ? "public" : "private"} model</button></div>` : ""}<div id="m"></div>`;
  const load = () => region($("#m"), async () => [await api("GET", `${base}/models`), manage ? await api("GET", `${base}/providers`) : []], ([ms, ps]) => {
    if (!ms.length) {
      $("#m").innerHTML = `<p class="state">No models yet. ${manage ? "Add a provider first, then a model that routes to it." : ""}</p>`;
    } else {
      $("#m").innerHTML = table([
        { label: "Model", cell: (m) => `<b>${esc(m.name || m.id)}</b><br><code>${esc(m.id)}</code>` },
        { label: "Kind", cell: (m) => esc(m.kind) },
        { label: "Owner", cell: (m) => esc(m.owner) },
        { label: "Context", num: 1, cell: (m) => (m.context_length ? fmt(m.context_length) : "–") },
        { label: "Input", num: 1, cell: (m) => "$" + m.pricing.input },
        { label: "Output", num: 1, cell: (m) => "$" + m.pricing.output },
        { label: "Routes to", cell: (m) => (m.targets ? m.targets.map((t) => `${esc(ps.find((p) => p.id === t.provider_id)?.name || t.provider_id)}: <code>${esc(t.model)}</code> ×${t.weight || 1}`).join("<br>") : "–") },
        { label: "", cell: (m) => (m.targets ? `${m.enabled ? "" : '<span class="tag">disabled</span> '}<button data-edit="${esc(m.id)}">Edit</button> <button class="danger" data-del="${esc(m.id)}">Delete</button>` : "") },
      ], ms);
    }
    const form = (m = { targets: [{}] }) => `
      <div class="row"><label>Model id (vendor/name)<input name="id" value="${esc(m.id)}" ${m.id ? "readonly" : ""} required pattern="[^/\\s]+/[^\\s]+"></label>
      <label>Display name<input name="name" value="${esc(m.name)}"></label></div>
      <label>Description<input name="description" value="${esc(m.description)}"></label>
      <div class="row"><label>Kind<select name="kind"><option ${m.kind === "chat" ? "selected" : ""}>chat</option><option ${m.kind === "embedding" ? "selected" : ""}>embedding</option></select></label>
      <label>Context length<input name="ctx" type="number" min="0" value="${esc(m.context_length || "")}"></label></div>
      <div class="row"><label>Input price, USD per 1M<input name="in" type="number" min="0" step="0.0001" value="${m.pricing ? esc(m.pricing.input) : ""}" required></label>
      <label>Output price, USD per 1M<input name="out" type="number" min="0" step="0.0001" value="${m.pricing ? esc(m.pricing.output) : ""}" required></label></div>
      <fieldset class="form"><legend>Routing targets (first healthy target by weight serves; the rest are failover)</legend>
      ${[0, 1, 2].map((i) => { const t = m.targets[i] || {}; return `<div class="row">
        <label>Provider<select name="p${i}"><option value="">–</option>${ps.map((p) => `<option value="${esc(p.id)}" ${p.id === t.provider_id ? "selected" : ""}>${esc(p.name)} (${esc(p.type)})</option>`).join("")}</select></label>
        <label>Upstream model<input name="u${i}" value="${esc(t.model)}"></label>
        <label class="shrink">Weight<input name="w${i}" type="number" min="0" step="0.1" value="${esc(t.weight || 1)}"></label></div>`; }).join("")}
      </fieldset>
      <label class="inline"><input type="checkbox" name="enabled" ${m.enabled !== false ? "checked" : ""}> Enabled</label>`;
    const save = (m) => async (f) => {
      const targets = [0, 1, 2].filter((i) => f.get("p" + i) && f.get("u" + i)).map((i) => ({ provider_id: f.get("p" + i), model: f.get("u" + i), weight: num(f.get("w" + i)) || 1 }));
      const id = m.id || f.get("id");
      await api(m.id ? "PUT" : "POST", m.id ? `${base}/models/${pathID(id)}` : `${base}/models`, {
        id, name: f.get("name"), description: f.get("description"), kind: f.get("kind"), context_length: num(f.get("ctx")),
        input_price_micros: Math.round(num(f.get("in")) * 1e6), output_price_micros: Math.round(num(f.get("out")) * 1e6), targets, enabled: f.get("enabled") === "on",
      });
      toast("Model saved");
      load();
    };
    if ($("#new")) $("#new").onclick = () => (ps.length ? dialog("Add model", form(), save({})) : toast("Add a provider first"));
    $$("[data-edit]").forEach((b) => (b.onclick = () => { const m = ms.find((x) => x.id === b.dataset.edit && x.targets); dialog("Edit model", form(m), save(m)); }));
    $$("[data-del]").forEach((b) => (b.onclick = async () => {
      if (!confirmAction(`Delete model ${b.dataset.del}? Requests for it will fail.`)) return;
      await api("DELETE", `${base}/models/${pathID(b.dataset.del)}`).catch((e) => toast(e.message));
      load();
    }));
  });
  load();
}

// ---------- members ----------

function membersView(main, base) {
  main.innerHTML = `<h1>Members</h1>${can("admin") ? '<div class="row"><button class="primary shrink" id="new">Add member</button></div>' : ""}<div id="mm"></div>`;
  const roles = ["viewer", "member", "admin", "owner"];
  const load = () => region($("#mm"), () => api("GET", `${base}/members`), (ms) => {
    $("#mm").innerHTML = table([
      { label: "Email", cell: (m) => esc(m.email) + (m.user_id === S.me.id ? ' <span class="muted">(you)</span>' : "") },
      { label: "Name", cell: (m) => esc(m.name) },
      { label: "Role", cell: (m) => (can("admin") ? `<label class="inline"><span class="sr">Role of ${esc(m.email)}</span><select data-role="${esc(m.user_id)}">${roles.map((r) => `<option ${r === m.role ? "selected" : ""}>${r}</option>`).join("")}</select></label>` : esc(m.role)) },
      { label: "", cell: (m) => (can("admin") ? `<button class="danger" data-rm="${esc(m.user_id)}">Remove</button>` : "") },
    ], ms);
    $$("[data-role]").forEach((s) => (s.onchange = async () => {
      await api("PUT", `${base}/members/${s.dataset.role}`, { role: s.value }).then(() => toast("Role updated")).catch((e) => toast(e.message));
      load();
    }));
    $$("[data-rm]").forEach((b) => (b.onclick = async () => {
      if (!confirmAction("Remove this member from the organization?")) return;
      await api("DELETE", `${base}/members/${b.dataset.rm}`).catch((e) => toast(e.message));
      load();
    }));
  });
  if ($("#new")) $("#new").onclick = () => dialog("Add member", `
      <label>Email<input name="email" type="email" required></label>
      <label>Role<select name="role">${roles.slice(0, S.role === "owner" ? 4 : 3).map((r) => `<option ${r === "member" ? "selected" : ""}>${r}</option>`).join("")}</select></label>
      <label>Initial password for a new user (optional; without one they sign in with SSO)<input name="password" type="password" minlength="10" autocomplete="new-password"></label>`,
    async (f) => { await api("POST", `${base}/members`, { email: f.get("email"), role: f.get("role"), password: f.get("password") }); load(); }, "Add");
  load();
}

// ---------- settings ----------

function settingsView(main, base) {
  main.innerHTML = `<h1>Settings</h1><div id="st"></div>`;
  region($("#st"), () => api("GET", base), (o) => {
    const g = o.guardrails || {};
    const ro = can("admin") ? "" : "disabled";
    $("#st").innerHTML = `<form class="form" id="sf">
      <label>Organization name<input name="name" value="${esc(o.name)}" ${ro}></label>
      <p class="muted">Billing: ${o.prepaid ? `prepaid, balance ${usd(o.balance_micros)} (granted ${usd(o.credits_micros)}, spent ${usd(o.spent_micros)})` : `no credit limit, spent ${usd(o.spent_micros)}`}.</p>
      <h2>Cost control</h2>
      <label>Max output tokens per request (0 = no cap)<input name="cap" type="number" min="0" value="${o.max_tokens_cap || 0}" ${ro}></label>
      <h2>Guardrails</h2>
      <label>Max input characters per request (0 = no limit)<input name="maxin" type="number" min="0" value="${g.max_input_chars || 0}" ${ro}></label>
      <label>Blocked patterns, one regular expression per line<textarea name="patterns" ${ro}>${esc((g.blocked_patterns || []).join("\n"))}</textarea></label>
      <label class="inline"><input type="checkbox" name="pii" ${g.mask_pii ? "checked" : ""} ${ro}> Mask emails and phone numbers before they reach the provider</label>
      ${can("admin") ? '<div class="actions"><button class="primary" type="submit">Save settings</button></div>' : ""}
      </form>
      ${can("owner") ? `<h2>Danger zone</h2><button class="danger" id="del">Delete organization</button>` : ""}`;
    $("#sf").onsubmit = async (ev) => {
      ev.preventDefault();
      const f = new FormData(ev.target);
      try {
        await api("PATCH", base, { name: f.get("name"), max_tokens_cap: num(f.get("cap")),
          guardrails: { max_input_chars: num(f.get("maxin")), blocked_patterns: String(f.get("patterns")).split("\n").map((s) => s.trim()).filter(Boolean), mask_pii: f.get("pii") === "on" } });
        toast("Settings saved");
      } catch (e) { toast(e.message); }
    };
    if ($("#del")) $("#del").onclick = async () => {
      if (prompt(`Type the organization name (${o.name}) to delete it with all its keys, models and providers.`) !== o.name) return;
      await api("DELETE", base).then(() => { sessionStorage.removeItem("org"); boot(); }).catch((e) => toast(e.message));
    };
  });
}

// ---------- audit ----------

function auditView(main, base) {
  main.innerHTML = `<h1>${base === "/api/admin" ? "Platform audit" : "Audit log"}</h1><div id="a"></div>`;
  region($("#a"), () => api("GET", `${base}/audit`), (es) => {
    $("#a").innerHTML = table([
      { label: "Time", cell: (e) => when(e.at) },
      { label: "Actor", cell: (e) => esc(e.actor) },
      { label: "Action", cell: (e) => `<code>${esc(e.action)}</code>` },
      { label: "Target", cell: (e) => `<code>${esc(e.target)}</code>` },
      { label: "Detail", cell: (e) => esc(e.detail) },
    ], es);
  }, "Nothing recorded yet. Changes to keys, models, providers, members and settings appear here.");
}

// ---------- playground ----------

function playgroundView(main, base) {
  main.innerHTML = `<h1>Playground</h1>
    <p class="muted">Calls the real API with one of your keys. The key stays in this tab and is not saved.</p>
    <form class="form" id="pg">
      <div class="row"><label>API key<input name="key" type="password" placeholder="sk-gw-…" autocomplete="off" required></label>
      <label>Model<select name="model" id="pgm"></select></label></div>
      <label>System prompt<input name="system" placeholder="optional"></label>
      <label>Message<textarea name="msg" required>Say hello in five words.</textarea></label>
      <label class="inline"><input type="checkbox" name="stream" checked> Stream the answer</label>
      <div class="actions"><button class="primary" type="submit">Send</button></div>
    </form><h2>Response</h2><pre id="out" aria-live="polite">–</pre>`;
  api("GET", `${base}/models`).then((ms) => {
    const chat = ms.filter((m) => m.kind === "chat" && m.enabled);
    $("#pgm").innerHTML = chat.length ? chat.map((m) => `<option>${esc(m.id)}</option>`).join("") : "<option value=''>No chat models</option>";
  }).catch((e) => toast(e.message));
  $("#pg").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const out = $("#out");
    const messages = [];
    if (f.get("system")) messages.push({ role: "system", content: f.get("system") });
    messages.push({ role: "user", content: f.get("msg") });
    const stream = f.get("stream") === "on";
    out.textContent = "…";
    const t0 = performance.now();
    try {
      const r = await fetch("/v1/chat/completions", {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: "Bearer " + f.get("key") },
        body: JSON.stringify({ model: f.get("model"), messages, stream }),
      });
      if (!r.ok) throw new Error((await r.json()).error?.message || r.status);
      const prov = r.headers.get("x-gateway-provider");
      if (!stream) {
        const j = await r.json();
        out.textContent = j.choices?.[0]?.message?.content ?? JSON.stringify(j, null, 2);
        out.textContent += `\n\n— ${j.provider || prov || ""} · ${j.usage?.prompt_tokens ?? 0} in / ${j.usage?.completion_tokens ?? 0} out · ${Math.round(performance.now() - t0)} ms`;
        return;
      }
      out.textContent = "";
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop();
        for (const line of lines) {
          if (!line.startsWith("data: ") || line === "data: [DONE]") continue;
          const j = JSON.parse(line.slice(6));
          if (j.error) throw new Error(j.error.message);
          out.textContent += j.choices?.[0]?.delta?.content || "";
        }
      }
      out.textContent += `\n\n— streamed in ${Math.round(performance.now() - t0)} ms`;
    } catch (e) {
      out.textContent = "Error: " + e.message;
    }
  };
}

// ---------- account ----------

function accountView(main) {
  main.innerHTML = `<h1>Account</h1><p>${esc(S.me.email)}${S.me.operator ? ' <span class="tag ok">operator</span>' : ""}</p>
    <form class="form" id="pw"><h2>Change password</h2>
    <label>Current password<input name="current" type="password" autocomplete="current-password"></label>
    <label>New password (10+ characters)<input name="new" type="password" minlength="10" autocomplete="new-password" required></label>
    <div class="actions"><button class="primary" type="submit">Change password</button></div></form>`;
  $("#pw").onsubmit = async (ev) => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      await api("POST", "/api/auth/password", { current: f.get("current"), new: f.get("new") });
      toast("Password changed. Other sessions were signed out.");
      ev.target.reset();
    } catch (e) { toast(e.message); }
  };
}

// ---------- platform ----------

function orgsView(main) {
  main.innerHTML = `<h1>Organizations</h1><div class="row"><button class="primary shrink" id="new">Create organization</button></div><div id="o"></div>`;
  const load = () => region($("#o"), () => api("GET", "/api/orgs?limit=500"), (os) => {
    $("#o").innerHTML = table([
      { label: "Name", cell: (o) => `${esc(o.name)}<br><code class="muted">${esc(o.id)}</code>` },
      { label: "Billing", cell: (o) => (o.prepaid ? "prepaid" : "unlimited") },
      { label: "Credits granted", num: 1, cell: (o) => usd(o.credits_micros) },
      { label: "Created", cell: (o) => when(o.created_at) },
      { label: "MCP access", cell: (o) => esc((o.mcp_servers || []).join(", ") || "none") },
      { label: "", cell: (o) => `<button data-credit="${esc(o.id)}">Add credits</button> <button data-bill="${esc(o.id)}" data-prepaid="${o.prepaid}">${o.prepaid ? "Make unlimited" : "Make prepaid"}</button> <button data-mcp="${esc(o.id)}">MCP access</button>` },
    ], os);
    $$("[data-mcp]").forEach((b) => (b.onclick = () => {
      const o = os.find((x) => x.id === b.dataset.mcp);
      dialog("MCP access", `<label>MCP server names this organization may use (comma separated, * for all, empty for none)<input name="names" value="${esc((o.mcp_servers || []).join(", "))}"></label>`,
        async (f) => { await api("PATCH", `/api/orgs/${o.id}`, { mcp_servers: list(f.get("names")) }); toast("MCP access updated"); load(); });
    }));
    $$("[data-credit]").forEach((b) => (b.onclick = () => dialog("Add credits", `<label>Amount in USD (negative to deduct)<input name="usd" type="number" step="0.01" required></label><label>Note<input name="note" placeholder="invoice 2026-11"></label>`,
      async (f) => { await api("POST", `/api/orgs/${b.dataset.credit}/credits`, { usd: num(f.get("usd")), note: f.get("note") }); toast("Credits updated"); load(); })));
    $$("[data-bill]").forEach((b) => (b.onclick = async () => {
      await api("PATCH", `/api/orgs/${b.dataset.bill}`, { prepaid: b.dataset.prepaid !== "true" }).catch((e) => toast(e.message));
      load();
    }));
  }, "No organizations yet.");
  $("#new").onclick = () => dialog("Create organization", `<label>Name<input name="name" required></label>
      <label>Owner email (an existing user, optional)<input name="owner" type="email"></label>
      <label class="inline"><input type="checkbox" name="prepaid"> Prepaid (requests need credits)</label>`,
    async (f) => { await api("POST", "/api/orgs", { name: f.get("name"), owner_email: f.get("owner"), prepaid: f.get("prepaid") === "on" }); load(); }, "Create");
  load();
}

function usersView(main) {
  main.innerHTML = `<h1>Users</h1><div id="us"></div>`;
  const load = () => region($("#us"), () => api("GET", "/api/admin/users"), (us) => {
    $("#us").innerHTML = table([
      { label: "Email", cell: (u) => esc(u.email) },
      { label: "Name", cell: (u) => esc(u.name) },
      { label: "Role", cell: (u) => (u.operator ? '<span class="tag ok">operator</span>' : "user") + (u.disabled ? ' <span class="tag bad">disabled</span>' : "") },
      { label: "Created", cell: (u) => when(u.created_at) },
      { label: "", cell: (u) => (u.id === S.me.id ? "" : `<button data-op="${esc(u.id)}" data-v="${!u.operator}">${u.operator ? "Remove operator" : "Make operator"}</button> <button class="danger" data-dis="${esc(u.id)}" data-v="${!u.disabled}">${u.disabled ? "Enable" : "Disable"}</button>`) },
    ], us);
    $$("[data-op]").forEach((b) => (b.onclick = async () => { await api("PATCH", `/api/admin/users/${b.dataset.op}`, { operator: b.dataset.v === "true" }).catch((e) => toast(e.message)); load(); }));
    $$("[data-dis]").forEach((b) => (b.onclick = async () => { await api("PATCH", `/api/admin/users/${b.dataset.dis}`, { disabled: b.dataset.v === "true" }).catch((e) => toast(e.message)); load(); }));
  });
  load();
}

function mcpView(main) {
  main.innerHTML = `<h1>MCP servers</h1>
    <p class="muted">Tools from these servers are offered to models when a request sends <code>x-gateway-mcp-clients: name</code> (or <code>*</code>). Run a returned tool call with <code>POST /v1/mcp/tool/execute</code>.</p>
    <div class="row"><button class="primary shrink" id="new">Add MCP server</button></div><div id="mc"></div>`;
  const load = () => region($("#mc"), () => api("GET", "/api/admin/mcp"), ({ servers, connected }) => {
    const state = Object.fromEntries((connected || []).map((c) => [c.config?.id || c.name, c]));
    if (!servers.length) {
      $("#mc").innerHTML = '<p class="state">No MCP servers. Add an HTTP or SSE server URL.</p>';
      return;
    }
    $("#mc").innerHTML = table([
      { label: "Name", cell: (m) => esc(m.name) },
      { label: "Transport", cell: (m) => esc(m.type) },
      { label: "URL", cell: (m) => `<code>${esc(m.url)}</code>` },
      { label: "Tools", cell: (m) => { const c = state[m.id]; return c ? (c.tools || []).map((t) => `<code>${esc(t.name)}</code>`).join(" ") || "none" : (m.enabled ? '<span class="tag bad">not connected</span>' : "–"); } },
      { label: "Allowed", cell: (m) => esc((m.tools || []).join(", ") || "none") },
      { label: "", cell: (m) => `<button class="danger" data-del="${esc(m.id)}">Delete</button>` },
    ], servers);
    $$("[data-del]").forEach((b) => (b.onclick = async () => { if (confirmAction("Delete this MCP server?")) { await api("DELETE", `/api/admin/mcp/${b.dataset.del}`).catch((e) => toast(e.message)); load(); } }));
  });
  $("#new").onclick = () => dialog("Add MCP server", `<div class="row"><label>Name<input name="name" required pattern="[A-Za-z0-9_-]+"></label>
      <label>Transport<select name="type"><option>http</option><option>sse</option></select></label></div>
      <label>URL<input name="url" type="url" required></label>
      <label>Allowed tools (comma separated, * for all)<input name="tools" value="*"></label>
      <label class="inline"><input type="checkbox" name="enabled" checked> Enabled</label>`,
    async (f) => { await api("POST", "/api/admin/mcp", { name: f.get("name"), type: f.get("type"), url: f.get("url"), tools: list(f.get("tools")), enabled: f.get("enabled") === "on" }); toast("Saved. It connects within a few seconds."); setTimeout(load, 2500); }, "Add");
  load();
}

boot();
