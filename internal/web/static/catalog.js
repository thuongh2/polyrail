"use strict";
const $ = (s) => document.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const price = (p) => (Number(p) === 0 ? "free" : "$" + Number(p).toFixed(Number(p) < 1 ? 3 : 2));
let models = [];
let sort = { key: "id", dir: 1 };

function sortValue(m) {
  if (sort.key === "input" || sort.key === "output") return Number(m.pricing[sort.key]);
  return sort.key === "context" ? m.context_length || 0 : m[sort.key];
}

function render() {
  const q = $("#q").value.trim().toLowerCase();
  const kind = $("#kind").value;
  const rows = models
    .filter((m) => (!kind || m.kind === kind) && (!q || (m.id + " " + m.name + " " + (m.description || "")).toLowerCase().includes(q)))
    .sort((a, b) => {
      const va = sortValue(a), vb = sortValue(b);
      return (va > vb ? 1 : va < vb ? -1 : 0) * sort.dir;
    });
  const params = new URLSearchParams();
  if (q) params.set("q", q);
  if (kind) params.set("kind", kind);
  history.replaceState(null, "", params.toString() ? "?" + params : location.pathname);
  if (!models.length) {
    $("#models").innerHTML = '<p class="state">No models are published yet. An operator adds them in the console under Platform, Models.</p>';
    return;
  }
  if (!rows.length) {
    $("#models").innerHTML = '<p class="state">No model matches this search.</p>';
    return;
  }
  const th = (k, label, cls = "") => `<th class="${cls}" aria-sort="${sort.key === k ? (sort.dir > 0 ? "ascending" : "descending") : "none"}"><button class="sort" data-k="${k}">${label}</button></th>`;
  $("#models").innerHTML = `<div class="table-wrap"><table><thead><tr>${th("id", "Model")}${th("kind", "Kind")}${th("context", "Context", "num")}${th("input", "Input / 1M", "num")}${th("output", "Output / 1M", "num")}</tr></thead><tbody>${rows
    .map((m) => `<tr><td><b>${esc(m.name || m.id)}</b><br><code>${esc(m.id)}</code>${m.description ? `<br><span class="muted">${esc(m.description)}</span>` : ""}</td><td>${esc(m.kind)}</td><td class="num">${m.context_length ? Number(m.context_length).toLocaleString() : "–"}</td><td class="num">${price(m.pricing.input)}</td><td class="num">${price(m.pricing.output)}</td></tr>`)
    .join("")}</tbody></table></div>`;
  document.querySelectorAll("button.sort").forEach((b) => b.addEventListener("click", () => {
    sort = { key: b.dataset.k, dir: sort.key === b.dataset.k ? -sort.dir : 1 };
    render();
  }));
}

async function load() {
  const p = new URLSearchParams(location.search);
  $("#q").value = p.get("q") || "";
  $("#kind").value = p.get("kind") || "";
  $("#snippet").textContent = `curl ${location.origin}/v1/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_KEY" -H "Content-Type: application/json" \\
  -d '{"model": "vendor/model", "messages": [{"role": "user", "content": "Hello"}]}'`;
  try {
    const r = await fetch("/api/public/models");
    if (!r.ok) throw new Error(r.status);
    models = (await r.json()).data;
    render();
  } catch (e) {
    $("#models").innerHTML = `<p class="state error">Could not load the model list (${esc(e.message)}). Reload the page to try again.</p>`;
  }
}
$("#q").addEventListener("input", render);
$("#kind").addEventListener("change", render);
load();
