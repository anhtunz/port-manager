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
        { id: "auth-be", name: "auth-service BE (Go · Gin · gRPC)", ports: [{ port: 3000, open: true }, { port: 9090, open: true }], status: "running", pid: 17210, command: "/private/var/folders/.../exe/server", managedByApp: false, startCmd: "make run", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/auth-service/backend", branch: "master", detached: false, isWorktree: false, dirty: 2, ahead: 0, behind: 0, upstream: "origin/master" } },
        { id: "wm-be", name: "warehouse-management BE", ports: [{ port: 3001, open: true }, { port: 9092, open: true }], status: "running", pid: 75197, command: "/private/var/folders/.../exe/main", managedByApp: false, startCmd: "make run-be", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/warehouse-management/backend", branch: "feat/wm-grpc", detached: false, isWorktree: true, dirty: 0, ahead: 1, behind: 0, upstream: "origin/feat/wm-grpc" } },
        { id: "notification-be", name: "notification-service BE", ports: [{ port: 3004, open: false }, { port: 9091, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make dev", isCompose: false },
        { id: "assistant-be", name: "assistant-service BE (chatbox AI)", ports: [{ port: 3005, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make run", isCompose: false },
        { id: "telegram-emulator-be", name: "telegram-emulator (Bot API giả)", ports: [{ port: 3010, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make backend", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/telegram-emulator", branch: "main", detached: false, isWorktree: false, dirty: 11, ahead: 0, behind: 0 } },
        { id: "facility-be", name: "facility BE (fm-backend)", ports: [{ port: 6868, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make run-be", isCompose: false },
        { id: "base-be", name: "base BE", ports: [{ port: 8090, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make dev", isCompose: false },
      ],
    },
    {
      name: "Frontend",
      services: [
        { id: "auth-fe", name: "auth-frontend (React admin)", ports: [{ port: 3003, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "portal-fe", name: "portal-frontend", ports: [{ port: 8000, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/auth-service/portal-frontend", branch: "master", detached: false, isWorktree: false, dirty: 0, ahead: 0, behind: 3, upstream: "origin/master" } },
        { id: "base-fe", name: "base FE", ports: [{ port: 8091, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "facility-fe", name: "facility FE", ports: [{ port: 6869, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "wm-fe", name: "warehouse-management FE (umi max)", ports: [{ port: 6789, open: true }], status: "running", pid: 4632, command: "node /Users/tunztunzz/Documents/smatec/warehouse-management/frontend/node_modules/.bin/max dev", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "telegram-emulator-fe", name: "telegram-emulator web UI (Vite dev)", ports: [{ port: 3011, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make frontend", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/telegram-emulator", branch: "main", detached: false, isWorktree: false, dirty: 11, ahead: 0, behind: 0 } },
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

  // Chip branch — bấm mở modal chi tiết repo / chuyển nhánh
  let repoChip = "";
  if (s.repo) {
    const dirty = s.repo.dirty > 0 ? `<span class="chip-dot">●${s.repo.dirty}</span>` : "";
    const wt = s.repo.isWorktree ? `<span class="chip-wt">wt</span>` : "";
    repoChip = `
      <button class="branch-chip" data-act="repo" data-id="${esc(s.id)}"
              title="Chi tiết repo — xem/chuyển nhánh">⎇ ${esc(s.repo.branch)} ${dirty}${wt}</button>`;
  }

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
        <div class="svc-sub">${esc(s.id)} ${repoChip}</div>
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
// Repo / branch
// ---------------------------------------------------------------------------
function basename(p) {
  return String(p || "").split("/").filter(Boolean).pop() || p;
}

function mockRepoDetails(id) {
  const svc = findService(id);
  if (!svc || !svc.repo) return { error: "service không có git repo" };
  const r = svc.repo;
  return {
    view: r,
    branches: [r.branch, "master", "main", "update/git-branch-switch"].filter((b, i, a) => a.indexOf(b) === i),
    worktrees: [
      { path: r.topLevel, branch: r.isWorktree ? "master" : r.branch, detached: false, current: !r.isWorktree },
      ...(r.isWorktree ? [{ path: r.topLevel + "-wt", branch: r.branch, detached: false, current: true }] : []),
    ],
  };
}

async function repoModal(id) {
  let d;
  try {
    d = API ? await API.RepoDetails(id) : mockRepoDetails(id);
  } catch (e) {
    showError("Lỗi đọc repo: " + (e && e.message ? e.message : e));
    return;
  }
  if (d.error) {
    showError("Repo: " + d.error);
    return;
  }
  const v = d.view;
  const svc = findService(id);
  const running = svc && svc.status !== "stopped";

  const branches = (d.branches || [])
    .map((b) => {
      const isCur = b === v.branch && !v.detached;
      return `
      <div class="branch-row">
        <span class="mono">${esc(b)}${isCur ? ' <span class="cur-mark">← hiện tại</span>' : ""}</span>
        ${isCur ? "" : `<button class="btn primary sm" data-act="switch" data-id="${esc(id)}" data-branch="${esc(b)}">Chuyển</button>`}
      </div>`;
    })
    .join("") || `<div class="muted">Không có branch local nào</div>`;

  const worktrees = (d.worktrees || [])
    .map(
      (w) => `
      <div class="wt-row mono">
        <span>${w.current ? "📍 " : "　"}${esc(w.path)}</span>
        <span class="wt-branch">${esc(w.branch)}</span>
      </div>`
    )
    .join("");

  const upstream =
    v.upstream
      ? ` (theo <code>${esc(v.upstream)}</code>${v.ahead ? `, ahead ${v.ahead}` : ""}${v.behind ? `, behind ${v.behind}` : ""})`
      : "";

  modalBusy = true;
  openModal(`
    <h3>Repo — ${esc(basename(v.topLevel))}${v.isWorktree ? ' <span class="badge mini">worktree</span>' : ""}</h3>
    <div class="body">
      <div><code>${esc(v.topLevel)}</code></div>
      <div style="margin-top:6px">Nhánh: <b>${esc(v.branch)}</b>${upstream}</div>
      <div>${v.dirty > 0 ? `<span class="dirty-note">● ${v.dirty} thay đổi chưa commit</span>` : "Làm việc sạch ✓"}</div>
      ${running ? `<div class="warn-note">⚠️ Service đang chạy — sau khi chuyển nhánh hãy restart service để chạy code mới.</div>` : ""}
    </div>
    <h4>Chuyển nhánh (local)</h4>
    <div class="branch-list">${branches}</div>
    <div class="custom-branch">
      <input id="branch-input" class="branch-input mono" placeholder="nhánh khác: local hoặc origin/..." />
      <button class="btn primary sm" data-act="switch" data-id="${esc(id)}" data-branch-input="1">Chuyển</button>
    </div>
    <h4>Các worktree của repo</h4>
    <div class="wt-list">${worktrees || '<div class="muted">—</div>'}</div>
    <div class="modal-actions" style="margin-top:12px">
      <button class="btn ghost" id="m-close">Đóng</button>
    </div>`);
  document.getElementById("m-close").onclick = closeModal;
  const input = document.getElementById("branch-input");
  if (input) input.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      const val = input.value.trim();
      if (val) doSwitch(id, val);
    }
  });
}

async function doSwitch(id, branch) {
  if (!branch || !API) return;
  const svc = findService(id);
  const repo = svc && svc.repo;
  const ok = await confirmModal({
    title: `Chuyển nhánh — ${id}`,
    body:
      `<code>${esc(repo ? repo.branch : "?")}</code> → <code>${esc(branch)}</code>` +
      (svc && svc.status !== "stopped" ? `<br><br>⚠️ Service đang chạy — cần restart service để chạy code mới.` : "") +
      (repo && repo.dirty > 0 ? `<br>⚠️ Repo có ${repo.dirty} thay đổi chưa commit — git sẽ từ chối nếu đụng chạm.` : ""),
    okText: "Chuyển",
    danger: false,
  });
  if (!ok) return;
  try {
    await API.SwitchBranch(id, branch);
    closeModal();
    await refresh();
  } catch (e) {
    showError("Chuyển nhánh lỗi:\n" + (e && e.message ? e.message : e));
  }
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
  else if (act === "repo") repoModal(btn.dataset.id);
  else if (act === "switch") {
    const val = btn.dataset.branchInput
      ? (document.getElementById("branch-input") || {}).value
      : btn.dataset.branch;
    doSwitch(btn.dataset.id, (val || "").trim());
  }
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
