// SMATEC Port Manager — frontend vanilla JS, nhúng qua go:embed.
// Mở ngoài app Wails (file://) → chạy MOCK với dữ liệu mẫu để xem giao diện.

const API = (window.go && window.go.main && window.go.main.App) ? window.go.main.App : null;
const POLL_MS = 3000;

let lastRes = null;
let pollTimer = null;
let modalBusy = false;

// ---------------------------------------------------------------------------
// MOCK (chỉ dùng khi mở file:// — không có bindings Wails)
// ---------------------------------------------------------------------------
const MOCK = {
  workspaceRoot: "/Users/tunztunzz/Documents/smatec",
  scannedAt: Date.now(),
  error: "",
  groups: [
    {
      name: "Backend Go",
      services: [
        { id: "auth-be", name: "auth-service BE (Go · Gin · gRPC)", ports: [{ port: 3000, open: true }, { port: 9090, open: true }], status: "running", pid: 17210, command: "/private/var/folders/.../exe/server", managedByApp: false, startCmd: "make run", isCompose: false },
        { id: "wm-be", name: "warehouse-management BE", ports: [{ port: 3001, open: true }, { port: 9092, open: true }], status: "running", pid: 75197, command: "/private/var/folders/.../exe/main", managedByApp: false, startCmd: "make run-be", isCompose: false },
        { id: "notification-be", name: "notification-service BE", ports: [{ port: 3004, open: false }, { port: 9091, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make dev", isCompose: false },
        { id: "assistant-be", name: "assistant-service BE (chatbox AI)", ports: [{ port: 3005, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make run", isCompose: false },
        { id: "telegram-emulator-be", name: "telegram-emulator (Bot API giả)", ports: [{ port: 3010, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make backend", isCompose: false },
        { id: "facility-be", name: "facility BE (fm-backend)", ports: [{ port: 6868, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make run-be", isCompose: false },
        { id: "base-be", name: "base BE", ports: [{ port: 8090, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make dev", isCompose: false },
      ],
    },
    {
      name: "Frontend",
      services: [
        { id: "auth-fe", name: "auth-frontend (React admin)", ports: [{ port: 3003, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "portal-fe", name: "portal-frontend", ports: [{ port: 8000, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "base-fe", name: "base FE", ports: [{ port: 8091, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "facility-fe", name: "facility FE", ports: [{ port: 6869, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "wm-fe", name: "warehouse-management FE (umi max)", ports: [{ port: 6789, open: true }], status: "running", pid: 4632, command: "node /Users/tunztunzz/Documents/smatec/warehouse-management/frontend/node_modules/.bin/max dev", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "telegram-emulator-fe", name: "telegram-emulator web UI (Vite dev)", ports: [{ port: 3011, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make frontend", isCompose: false },
      ],
    },
    {
      name: "Hạ tầng (docker)",
      services: [
        { id: "infra-postgres", name: "PostgreSQL (docker-shared)", ports: [{ port: 5432, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d postgres", isCompose: true },
        { id: "infra-redis", name: "Redis (docker-shared)", ports: [{ port: 6379, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d redis", isCompose: true },
        { id: "infra-rabbitmq", name: "RabbitMQ (docker-shared)", ports: [{ port: 5672, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d rabbitmq", isCompose: true },
        { id: "infra-mailpit", name: "Mailpit (SMTP :1025 · UI :8025)", ports: [{ port: 1025, open: true }, { port: 8025, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d mailpit", isCompose: true },
        { id: "infra-rustfs", name: "RustFS (S3 :9000 · console :9001)", ports: [{ port: 9000, open: true }, { port: 9001, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d rustfs", isCompose: true },
        { id: "infra-wm-nginx", name: "wm-frontend-nginx (preview dist WM)", ports: [{ port: 8080, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d frontend", isCompose: true },
        { id: "infra-fm-redis", name: "fm-redis (facility dev)", ports: [{ port: 6381, open: true }], status: "running", pid: 0, command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d redis", isCompose: true },
      ],
    },
  ],
  unknown: [
    { port: 20128, pid: 3528, command: "node /Users/tunztunzz/.local/bin/some-dev-tool.mjs" },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function fmtTime(ms) {
  return new Date(ms).toLocaleTimeString("vi-VN");
}

function statusMeta(st) {
  if (st === "running") return { label: "Đang chạy", cls: "ok" };
  if (st === "partial") return { label: "Một phần", cls: "warn" };
  return { label: "Tắt", cls: "off" };
}

function findService(id) {
  for (const g of lastRes.groups) {
    const s = g.services.find((x) => x.id === id);
    if (s) return s;
  }
  return null;
}

function showError(msg) {
  const bar = document.getElementById("errbar");
  bar.textContent = typeof msg === "string" ? msg : JSON.stringify(msg);
  bar.classList.remove("hidden");
}

function hideError() {
  document.getElementById("errbar").classList.add("hidden");
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------
function serviceRow(s) {
  const st = statusMeta(s.status);
  const ports = s.ports
    .map((p) => `<span class="port ${p.open ? "open" : "closed"}">${p.port}</span>`)
    .join("");
  const badges = [];
  if (s.isCompose) badges.push(`<span class="badge mini">docker</span>`);
  if (s.managedByApp) badges.push(`<span class="badge mini">app</span>`);

  const proc =
    s.pid !== 0
      ? `<div class="proc">pid <b>${s.pid}</b> · ${esc(s.command || "?")}</div>`
      : s.startCmd
        ? `<div class="proc">start: ${esc(s.startCmd)}</div>`
        : "";

  const actions = [];
  if (s.status === "stopped" || s.status === "partial") {
    actions.push(`<button class="btn primary sm" data-act="start" data-id="${esc(s.id)}">Bật</button>`);
  }
  if (s.status !== "stopped") {
    actions.push(`<button class="btn danger sm" data-act="stop" data-id="${esc(s.id)}">Tắt</button>`);
  }
  if (s.managedByApp) {
    actions.push(`<button class="btn ghost sm" data-act="log" data-id="${esc(s.id)}">Log</button>`);
  }

  return `
    <div class="row">
      <div><span class="pill ${st.cls}">${st.label}</span></div>
      <div>
        <div class="svc-name">${esc(s.name)} ${badges.join(" ")}</div>
        <div class="svc-sub">${esc(s.id)}</div>
      </div>
      <div class="ports">${ports}</div>
      <div>${proc}</div>
      <div class="actions">${actions.join("")}</div>
    </div>`;
}

function render() {
  if (!lastRes) return;
  hideError();
  if (lastRes.error) {
    showError("Lỗi quét: " + lastRes.error);
    return;
  }
  document.getElementById("scanned").textContent =
    "Quét lúc " + fmtTime(lastRes.scannedAt) + (lastRes.workspaceRoot ? " · " + lastRes.workspaceRoot : "");

  const parts = lastRes.groups.map(
    (g) => `
    <section class="card">
      <h2>${esc(g.name)}</h2>
      ${g.services.map(serviceRow).join("")}
    </section>`
  );

  const unknown = lastRes.unknown || [];
  const unknownBody = unknown.length
    ? unknown
        .map(
          (u) => `
      <div class="row">
        <div><span class="pill warn">Lạ</span></div>
        <div><div class="svc-name">Cổng ${u.port}</div><div class="svc-sub">pid ${u.pid}</div></div>
        <div></div>
        <div><div class="proc">${esc(u.command)}</div></div>
        <div class="actions"><button class="btn danger sm" data-act="kill" data-port="${u.port}" data-pid="${u.pid}" data-cmd="${esc(u.command)}">Tắt process</button></div>
      </div>`
        )
        .join("")
    : `<div class="muted" style="padding:8px 4px">Không có cổng lạ ✓</div>`;

  parts.push(`
    <section class="card">
      <h2>Cổng lạ — không nằm trong cấu hình (${unknown.length})</h2>
      ${unknownBody}
    </section>`);

  document.getElementById("content").innerHTML = parts.join("");
}

// ---------------------------------------------------------------------------
// Modal
// ---------------------------------------------------------------------------
function openModal(html) {
  document.getElementById("modal").innerHTML = html;
  document.getElementById("overlay").classList.remove("hidden");
}

function closeModal() {
  document.getElementById("overlay").classList.add("hidden");
  document.getElementById("modal").innerHTML = "";
  modalBusy = false;
}

function confirmModal({ title, body, okText, danger }) {
  return new Promise((resolve) => {
    modalBusy = true;
    openModal(`
      <h3>${esc(title)}</h3>
      <div class="body">${body}</div>
      <div class="modal-actions">
        <button class="btn ghost" id="m-cancel">Huỷ</button>
        <button class="btn ${danger ? "danger" : "primary"}" id="m-ok">${esc(okText)}</button>
      </div>`);
    document.getElementById("m-ok").onclick = () => { closeModal(); resolve(true); };
    document.getElementById("m-cancel").onclick = () => { closeModal(); resolve(false); };
  });
}

async function logModal(id) {
  let content;
  try {
    content = API ? await API.GetServiceLog(id) : "(mock) log chỉ có với service do app bật.";
  } catch (e) {
    content = "Lỗi đọc log: " + e;
  }
  modalBusy = true;
  openModal(`
    <h3>Log — ${esc(id)}</h3>
    <pre>${esc(content) || "(log rỗng)"}</pre>
    <div class="modal-actions" style="margin-top:12px">
      <button class="btn ghost" id="m-close">Đóng</button>
    </div>`);
  document.getElementById("m-close").onclick = closeModal;
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------
async function doStart(id) {
  if (!API) return;
  try {
    await API.StartService(id);
  } catch (e) {
    showError("Bật " + id + " lỗi:\n" + (e && e.message ? e.message : e));
  }
  await refresh();
}

async function doStop(id) {
  const s = findService(id);
  if (!s) return;
  const body = s.isCompose
    ? `Sẽ chạy <code>docker compose stop</code> — container được giữ, không mất dữ liệu.`
    : `Sẽ tắt process:<br><code>pid ${s.pid} — ${esc(s.command || "?")}</code>`;
  const ok = await confirmModal({ title: `Tắt ${s.name}?`, body, okText: "Tắt", danger: true });
  if (!ok) return;
  try {
    await API.StopService(id);
  } catch (e) {
    showError("Tắt " + id + " lỗi:\n" + (e && e.message ? e.message : e));
  }
  await refresh();
}

async function doKillUnknown(port, pid, cmd) {
  const ok = await confirmModal({
    title: `Tắt process lạ ở cổng ${port}?`,
    body: `<code>pid ${pid} — ${esc(cmd)}</code><br><br>Process này không nằm trong danh sách service đã cấu hình.`,
    okText: "Tắt process",
    danger: true,
  });
  if (!ok) return;
  try {
    await API.KillUnknown(port, pid);
  } catch (e) {
    showError("Kill lỗi:\n" + (e && e.message ? e.message : e));
  }
  await refresh();
}

// ---------------------------------------------------------------------------
// Polling
// ---------------------------------------------------------------------------
async function refresh() {
  try {
    const res = API ? await API.Scan() : MOCK;
    if (res && res.scannedAt === undefined) res.scannedAt = Date.now();
    if (!API) res.scannedAt = Date.now();
    lastRes = res;
    render();
  } catch (e) {
    showError("Quét lỗi: " + (e && e.message ? e.message : e));
  }
}

function schedulePoll() {
  clearTimeout(pollTimer);
  const on = document.getElementById("auto").checked;
  if (!on) return;
  pollTimer = setTimeout(async () => {
    if (!modalBusy) await refresh();
    schedulePoll();
  }, POLL_MS);
}

// ---------------------------------------------------------------------------
// Init
// ---------------------------------------------------------------------------
document.addEventListener("click", (ev) => {
  const btn = ev.target.closest("button[data-act]");
  if (!btn) return;
  const act = btn.dataset.act;
  if (act === "start") doStart(btn.dataset.id);
  else if (act === "stop") doStop(btn.dataset.id);
  else if (act === "log") logModal(btn.dataset.id);
  else if (act === "kill") doKillUnknown(Number(btn.dataset.port), Number(btn.dataset.pid), btn.dataset.cmd);
});

document.getElementById("btn-refresh").onclick = async () => { await refresh(); schedulePoll(); };
document.getElementById("auto").onchange = schedulePoll;
document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") closeModal(); });
document.getElementById("overlay").addEventListener("click", (ev) => {
  if (ev.target.id === "overlay" && modalBusy) closeModal();
});

(async function init() {
  document.getElementById("ver").textContent = API ? await API.Version() : "mock";
  document.getElementById("mockbar").classList.toggle("hidden", !!API);
  await refresh();
  schedulePoll();
})();
