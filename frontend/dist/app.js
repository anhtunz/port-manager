// SMATEC Port Manager — frontend vanilla JS, nhúng qua go:embed.
// Mở ngoài app Wails (file://) → chạy MOCK với dữ liệu mẫu để xem giao diện.
//
// v0.6.0: tìm kiếm/bộ lọc (F1.2), bật/tắt nhóm & toàn bộ (F1.1), dòng lỗi cuối
// (F1.3), ẩn cổng lạ (F1.4), mở Terminal (F1.5), đối chiếu mirror (F2.1),
// chip sức khoẻ (F2.2), lịch sử chạy (F3.3), thêm/sửa service (F3.4),
// phím tắt (F4.1) và giữ trạng thái giao diện (F4.3).

const API = (window.go && window.go.main && window.go.main.App) ? window.go.main.App : null;
const POLL_MS = 3000;

let lastRes = null;
let pollTimer = null;
let modalBusy = false;
let logTimer = null;

// Trạng thái giao diện (F1.2 + F4.3) — lưu/đọc localStorage key cố định.
const UI_KEY = "port-manager-ui-v1";
let uiState = { search: "", filterState: "all", filterDirty: false, collapsedGroups: [] };

// Kết quả probe sức khoẻ (F2.2) — key "<id>:<port>" → HealthEntry.
// Sau mỗi lần quét gọi HealthCheckAll đúng 1 lần (có cờ chống chồng),
// KHÔNG gọi trong ticker 1 giây.
let healthMap = {};
let healthBusy = false;
let lastHealthAt = 0; // mốc throttle probe sức khoẻ (30s/lần) — xem runHealth()

// ---------------------------------------------------------------------------
// Logic thuần (F1.2, F4.3) — không đụng DOM, export qua window.uiLogic để test
// được bằng node --test. Nếu app.logic.js đã nạp trước (window.uiLogic có sẵn)
// thì dùng của nó; chưa có thì bản dưới đây làm bản dự phòng.
// ---------------------------------------------------------------------------
function pureParseUiState(raw) {
  const d = { search: "", filterState: "all", filterDirty: false, collapsedGroups: [] };
  if (!raw) return d;
  try {
    const o = JSON.parse(raw);
    if (typeof o.search === "string") d.search = o.search;
    if (typeof o.filterState === "string" &&
        ["all", "running", "starting", "stuck", "stopped"].indexOf(o.filterState) >= 0) {
      d.filterState = o.filterState;
    }
    d.filterDirty = o.filterDirty === true;
    if (Array.isArray(o.collapsedGroups)) {
      d.collapsedGroups = o.collapsedGroups.filter((x) => typeof x === "string");
    }
  } catch (e) {
    // localStorage hỏng / JSON sai → bỏ qua, dùng trạng thái mặc định.
  }
  return d;
}

function pureSaveUiState(s) {
  return JSON.stringify({
    search: typeof s.search === "string" ? s.search : "",
    filterState: s.filterState || "all",
    filterDirty: s.filterDirty === true,
    collapsedGroups: Array.isArray(s.collapsedGroups) ? s.collapsedGroups : [],
  });
}

// matchSearch: q rỗng → khớp tất cả; nhiều từ khóa (ngăn cách khoảng trắng)
// phải khớp TẤT CẢ (AND) trong: id, tên, group, số cổng, lệnh start, branch.
function pureMatchSearch(s, q, groupName) {
  const t = String(q || "").trim().toLowerCase();
  if (!t) return true;
  const hay = [
    s.id,
    s.name,
    groupName,
    s.startCmd,
    s.command,
    s.repo && s.repo.branch,
    (s.ports || []).map((p) => p.port).join(" "),
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return t.split(/\s+/).every((tok) => hay.indexOf(tok) >= 0);
}

// statusMatches: bộ lọc "running" bao gồm cả "partial" (Một phần — cũng đang chạy).
function pureStatusMatches(st, filter) {
  if (filter === "all") return true;
  if (filter === "running") return st === "running" || st === "partial";
  return st === filter;
}

// filterServices: trả cấu trúc phục vụ render — giữ NGUYÊN mọi service của
// group, gắn cờ visible cho từng dòng (dòng ẩn được gắn class hidden ngay lúc
// dựng HTML nên re-render 3s không làm mất bộ lọc). Số liệu tổng (total/up)
// tính trên TOÀN BỘ group, không tính trên phần đã lọc.
function pureFilterServices(res, ui) {
  const q = ui && typeof ui.search === "string" ? ui.search : "";
  const fs = (ui && ui.filterState) || "all";
  const dirtyOnly = !!(ui && ui.filterDirty);
  const groups = (res && res.groups) || [];
  return groups.map((g) => {
    let up = 0;
    let shown = 0;
    const rows = (g.services || []).map((s) => {
      if (s.status === "running" || s.status === "partial" || s.status === "starting" || s.status === "stuck") up += 1;
      const visible = Boolean(
        pureMatchSearch(s, q, g.name) &&
          pureStatusMatches(s.status, fs) &&
          (!dirtyOnly || (s.repo && s.repo.dirty > 0))
      );
      if (visible) shown += 1;
      return { svc: s, visible };
    });
    return { name: g.name, total: rows.length, up, shown, rows };
  });
}

if (!window.uiLogic) {
  window.uiLogic = {
    parseUiState: pureParseUiState,
    saveUiState: pureSaveUiState,
    matchSearch: pureMatchSearch,
    statusMatches: pureStatusMatches,
    filterServices: pureFilterServices,
  };
}
const uiLogic = window.uiLogic;

// ---------------------------------------------------------------------------
// MOCK (chỉ dùng khi mở file:// — không có bindings Wails)
// ---------------------------------------------------------------------------
// ago: thời điểm trong quá khứ (ms) — dùng cho dữ liệu MOCK.
const ago = (ms) => Date.now() - ms;

const MOCK = {
  workspaceRoot: "/Users/tunztunzz/Documents/smatec",
  scannedAt: Date.now(),
  error: "",  groups: [
    {
      name: "Backend Go",
      services: [
        { id: "auth-be", name: "auth-service BE (Go · Gin · gRPC)", ports: [{ port: 3000, open: true }, { port: 9090, open: true }], status: "running", pid: 17210, startedAt: ago(2 * 3600e3), hasLog: true, command: "/private/var/folders/.../exe/server", managedByApp: false, startCmd: "make run", isCompose: false, autoRestart: true, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/auth-service/backend", branch: "master", detached: false, isWorktree: false, dirty: 2, ahead: 0, behind: 0, upstream: "origin/master" } },
        { id: "wm-be", name: "warehouse-management BE", ports: [{ port: 3001, open: true }, { port: 9092, open: true }], status: "running", pid: 75197, startedAt: ago(26 * 3600e3), hasLog: true, command: "/private/var/folders/.../exe/main", managedByApp: false, startCmd: "make run-be", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/warehouse-management/backend", branch: "feat/wm-grpc", detached: false, isWorktree: true, dirty: 0, ahead: 1, behind: 0, upstream: "origin/feat/wm-grpc" } },
        { id: "notification-be", name: "notification-service BE", ports: [{ port: 3004, open: false }, { port: 9091, open: false }], status: "starting", pid: 0, command: "", managedByApp: true, managedPid: 54321, managedSince: ago(25e3), hasLog: true, startCmd: "make dev", isCompose: false, autoRestart: true, lastError: `{"level":"error","msg":"connect tcp 127.0.0.1:5672: connection refused"}` },
        { id: "assistant-be", name: "assistant-service BE (chatbox AI)", ports: [{ port: 3005, open: false }], status: "stuck", pid: 0, command: "", managedByApp: true, managedPid: 54399, managedSince: ago(9 * 60e3), hasLog: true, startCmd: "make run", isCompose: false, lastError: `{"level":"error","msg":"listen tcp :3005: bind: address already in use"}` },
        { id: "telegram-emulator-be", name: "telegram-emulator (Bot API giả)", ports: [{ port: 3010, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make backend", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/telegram-emulator", branch: "main", detached: false, isWorktree: false, dirty: 11, ahead: 0, behind: 0 } },
        { id: "facility-be", name: "facility BE (fm-backend)", ports: [{ port: 6868, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make run-be", isCompose: false },
        { id: "base-be", name: "base BE", ports: [{ port: 8090, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make dev", isCompose: false, lastError: `{"level":"error","msg":"migration failed: duplicate column name"}` },
      ],
    },
    {
      name: "Frontend",
      services: [
        { id: "auth-fe", name: "auth-frontend (React admin)", ports: [{ port: 3003, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "portal-fe", name: "portal-frontend", ports: [{ port: 8000, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/auth-service/portal-frontend", branch: "master", detached: false, isWorktree: false, dirty: 0, ahead: 0, behind: 3, upstream: "origin/master" } },
        { id: "base-fe", name: "base FE", ports: [{ port: 8091, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "facility-fe", name: "facility FE", ports: [{ port: 6869, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "wm-fe", name: "warehouse-management FE (umi max)", ports: [{ port: 6789, open: true }], status: "running", pid: 4632, startedAt: ago(45 * 60e3), hasLog: true, command: "node /Users/tunztunzz/Documents/smatec/warehouse-management/frontend/node_modules/.bin/max dev", managedByApp: false, startCmd: "npm run dev", isCompose: false },
        { id: "telegram-emulator-fe", name: "telegram-emulator web UI (Vite dev)", ports: [{ port: 3011, open: false }], status: "stopped", pid: 0, command: "", managedByApp: false, startCmd: "make frontend", isCompose: false, repo: { topLevel: "/Users/tunztunzz/Documents/smatec/telegram-emulator", branch: "main", detached: false, isWorktree: false, dirty: 11, ahead: 0, behind: 0 } },
      ],
    },
    {
      name: "Hạ tầng (docker)",
      services: [
        { id: "infra_postgres", name: "PostgreSQL (docker-shared)", ports: [{ port: 5432, open: true }], status: "running", pid: 0, startedAt: ago(3 * 24 * 3600e3), command: "com.docker.backend", managedByApp: false, startCmd: "docker compose up -d postgres", isCompose: true },
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
    { port: 20128, pid: 3528, command: "node /Users/tunztunzz/.local/bin/some-dev-tool.mjs", cwd: "assistant-service/frontend", startedAt: Date.now() - 3600_000 },
    { port: 8088, pid: 14825, command: "com.docker.backend", container: "birthday-nginx", project: "others/birthday", startedAt: Date.now() - 6 * 86400_000 },
  ],
};

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function errMsg(e) {
  return e && e.message ? e.message : String(e);
}

function fmtTime(ms) {
  return new Date(ms).toLocaleTimeString("vi-VN");
}

// fmtEventTime: thời điểm sự kiện lịch sử (RFC3339) → dạng ngắn dễ đọc.
function fmtEventTime(at) {
  const d = new Date(at);
  if (isNaN(d.getTime())) return String(at || "");
  return d.toLocaleString("vi-VN");
}

// fmtDur định dạng khoảng thời gian đã trôi qua: 45s · 12m 30s · 2h 15m · 3d 4h.
function fmtDur(ms) {
  if (!ms || ms < 0) ms = 0;
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m " + (s % 60) + "s";
  const h = Math.floor(m / 60);
  if (h < 24) return h + "h " + (m % 60) + "m";
  const d = Math.floor(h / 24);
  return d + "d " + (h % 24) + "h";
}

function statusMeta(st) {
  if (st === "running") return { label: "Đang chạy", cls: "ok" };
  if (st === "partial") return { label: "Một phần", cls: "warn" };
  if (st === "starting") return { label: "Đang khởi động", cls: "info" };
  if (st === "stuck") return { label: "Kẹt", cls: "err" };
  return { label: "Tắt", cls: "off" };
}

// isUp: service đang có process (đang chạy / một phần / đang khởi động / kẹt).
// "stopped" mới là tắt thật.
function isUp(st) {
  return st === "running" || st === "partial" || st === "starting" || st === "stuck";
}

// portChip: chip cổng — bấm để mở http://localhost:<port> trên trình duyệt.
// Cổng đang tắt thì không bấm được (không có gì để mở).
function portChip(port, open) {
  if (!open) {
    return `<span class="port closed" title="Cổng ${port} đang tắt">${port}</span>`;
  }
  return `<button class="port open" data-act="open" data-port="${port}" title="Mở http://localhost:${port}">${port}</button>`;
}

// healthChip: chip sức khoẻ HTTP (F2.2) đặt cạnh chip cổng. Bỏ trống khi chưa
// probe hoặc state "skipped" (cổng gRPC / service compose — hợp đồng DOM).
function healthChip(id, port) {
  const e = healthMap[id + ":" + port];
  if (!e || e.state === "skipped") return "";
  if (e.state === "ok") {
    return `<span class="health health-ok" title="HTTP ${e.code} · ${e.latencyMs}ms">OK</span>`;
  }
  if (e.state === "http_error") {
    return `<span class="health health-http_error" title="${esc(e.detail || "HTTP " + e.code)}">HTTP ${e.code || "?"}</span>`;
  }
  return `<span class="health health-no_response" title="${esc(e.detail || "không có phản hồi")}">không phản hồi</span>`;
}

function findService(id) {
  const src = lastRes || MOCK;
  for (const g of src.groups) {
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

// showOpError: lỗi từ các hành động nhóm/toàn bộ/cấu hình. KHÔNG dùng showError
// vì render() gọi hideError() mỗi vòng quét 3s sẽ xoá ngay (R15 trong plan).
// Banner riêng, tự ẩn sau 8s; banner này nằm ngoài #content nên render() không đụng tới.
let opErrTimer = null;
function showOpError(msg) {
  let bar = document.getElementById("op-error");
  if (!bar) {
    bar = document.createElement("div");
    bar.id = "op-error";
    bar.className = "banner error";
    const content = document.getElementById("content");
    content.parentNode.insertBefore(bar, content);
  }
  bar.textContent = typeof msg === "string" ? msg : JSON.stringify(msg);
  bar.classList.remove("hidden");
  clearTimeout(opErrTimer);
  opErrTimer = setTimeout(() => bar.classList.add("hidden"), 8000);
}

// ---------------------------------------------------------------------------
// Render
// ---------------------------------------------------------------------------
function serviceRow(s, hidden) {
  const st = statusMeta(s.status);
  const ports = s.ports.map((p) => portChip(p.port, p.open) + healthChip(s.id, p.port)).join("");
  const badges = [];
  if (s.isCompose) badges.push(`<span class="badge mini">docker</span>`);
  if (s.managedByApp) badges.push(`<span class="badge mini">app</span>`);
  if (s.autoRestart) badges.push(`<span class="badge mini" title="auto_restart: true — app tự bật lại khi crash">↻</span>`);

  // Chip branch — bấm mở modal chi tiết repo / chuyển nhánh
  let repoChip = "";
  if (s.repo) {
    const dirty = s.repo.dirty > 0 ? `<span class="chip-dot">●${s.repo.dirty}</span>` : "";
    const wt = s.repo.isWorktree ? `<span class="chip-wt">wt</span>` : "";
    repoChip = `
      <button class="branch-chip" data-act="repo" data-id="${esc(s.id)}"
              title="Chi tiết repo — xem/chuyển nhánh">⎇ ${esc(s.repo.branch)} ${dirty}${wt}</button>`;
  }

  // Dòng lỗi cuối (F1.3): dữ liệu do App.Scan điền (lastError). Rỗng → không render.
  const lastError = s.lastError
    ? `<span class="last-error" title="${esc(s.lastError)}">⛔ ${esc(s.lastError)}</span>`
    : "";

  // Chip thời gian: lúc bắt đầu chạy + đã chạy được bao lâu (cập nhật mỗi giây)
  // managedSince: lúc app bật service — dùng khi chưa có listener để lấy thời
  // điểm bắt đầu (service đang khởi động / kẹt).
  const startedMs = s.startedAt || s.managedSince || 0;
  let upChip = "";
  if (startedMs) {
    upChip =
      `<div class="uptime" data-started="${startedMs}" title="Bắt đầu chạy lúc ${esc(fmtTime(startedMs))}">` +
      `▶ <span class="uptime-start">${esc(fmtTime(startedMs))}</span> · ` +
      `<span class="uptime-el"></span></div>`;
  }

  const proc =
    (s.pid !== 0
      ? `<div class="proc">pid <b>${s.pid}</b> · ${esc(s.command || "?")}</div>`
      : s.managedPid
        ? `<div class="proc">pid <b>${s.managedPid}</b> · app đang giữ · chưa lên cổng</div>`
        : s.startCmd
          ? `<div class="proc">start: ${esc(s.startCmd)}</div>`
          : "") + upChip;

  // Vùng nút phụ (cột 2): Terminal (F1.5) + Sửa/Lịch sử (F3.4, F3.3) — không
  // đặt vào .actions để không tràn ngang ở cửa sổ hẹp (grid 5 cột cố định).
  const rowTools = `
    <div class="row-tools">
      <button class="btn ghost sm terminal" data-act="terminal" data-id="${esc(s.id)}"
              title="Mở Terminal.app ngay tại thư mục của service">▸ Terminal</button>
      <button class="btn ghost sm" data-act="edit" data-id="${esc(s.id)}"
              title="Sửa service trong ports.yaml">Sửa</button>
      <button class="btn ghost sm" data-act="history" data-id="${esc(s.id)}"
              title="Lịch sử chạy của service">Lịch sử</button>
    </div>`;

  // "lên" = đang có process: đang chạy / một phần / đang khởi động / kẹt.
  // Trước đây chỉ "running"/"partial" mới có nút Tắt → service kẹt lúc khởi
  // động (chưa lên cổng) không có đường nào để tắt.
  const up = isUp(s.status);
  const actions = [];
  if (up) {
    actions.push(`<button class="btn danger sm" data-act="stop" data-id="${esc(s.id)}">Tắt</button>`);
    actions.push(`<button class="btn warn sm" data-act="restart" data-id="${esc(s.id)}" title="Tắt rồi bật lại">🔄 Restart</button>`);
  } else {
    actions.push(`<button class="btn primary sm" data-act="start" data-id="${esc(s.id)}">Bật</button>`);
  }
  if (s.managedByApp || s.hasLog) {
    actions.push(`<button class="btn ghost sm" data-act="log" data-id="${esc(s.id)}">Log</button>`);
  }

  return `
    <div class="row${hidden ? " hidden" : ""}">
      <div><span class="pill ${st.cls}">${st.label}</span></div>
      <div>
        <div class="svc-name">${esc(s.name)} ${badges.join(" ")}</div>
        <div class="svc-sub">${esc(s.id)} ${repoChip}</div>
        ${lastError}
        ${rowTools}
      </div>
      <div class="ports">${ports}</div>
      <div>${proc}</div>
      <div class="actions">${actions.join("")}</div>
    </div>`;
}

// groupCard: một group = .card-head (tiêu đề + số liệu tổng + nút bật/tắt nhóm)
// + các dòng service. Bộ lọc được áp NGAY LÚC DỰNG HTML (class hidden trên
// .row) nên re-render 3s sau không làm mất bộ lọc; số liệu tổng (up/total)
// luôn tính trên toàn bộ group — không đổi khi đang lọc.
function groupCard(g) {
  const collapsed = uiState.collapsedGroups.indexOf(g.name) >= 0;
  const rows = g.rows
    .map((r) => serviceRow(r.svc, !r.visible || collapsed))
    .join("");
  const emptyNote =
    g.shown === 0 && !collapsed
      ? `<div class="muted" style="padding:6px 4px">Không service nào khớp bộ lọc (tổng ${g.total} service)</div>`
      : "";
  const collapsedNote = collapsed
    ? `<div class="muted" style="padding:6px 4px">— đã thu gọn, bấm tiêu đề để mở —</div>`
    : "";
  return `
    <section class="card">
      <div class="card-head">
        <h2 data-group="${esc(g.name)}" title="Bấm để thu gọn / mở group">${esc(g.name)}
          <span class="muted">(${g.up}/${g.total} đang chạy${g.shown !== g.total ? ` · hiện ${g.shown}` : ""})</span></h2>
        <div class="group-actions">
          <button class="btn group-start" data-act="group-start" data-group="${esc(g.name)}"
                  title="Bật lần lượt service của group theo thứ tự tier">▶ Bật nhóm</button>
          <button class="btn group-stop" data-act="group-stop" data-group="${esc(g.name)}"
                  title="Tắt các service của group (thứ tự ngược)">■ Tắt nhóm</button>
        </div>
      </div>
      ${rows}
      ${emptyNote}
      ${collapsedNote}
    </section>`;
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

  const parts = uiLogic.filterServices(lastRes, uiState).map(groupCard);

  const unknown = lastRes.unknown || [];
  const q = uiState.search || "";
  const unknownBody = unknown.length
    ? unknown
        .map((u) => {
          // Cổng lạ cũng chịu bộ lọc tìm kiếm (gõ số cổng để tìm nhanh).
          const visible = uiLogic.matchSearch(
            { id: String(u.port), name: "Cổng " + u.port, ports: [{ port: u.port }], command: u.command },
            q,
            u.container
          );
          // Nguồn gốc: container đang publish cổng, hoặc thư mục làm việc
          // của process. Không có nó thì chỉ hiện command → không biết của ai.
          let src = "";
          if (u.container) {
            src =
              `<div class="svc-sub">🐳 container ${esc(u.container)}` +
              (u.project ? ` · ${esc(u.project)}` : "") +
              `</div>`;
          } else if (u.cwd) {
            src = `<div class="svc-sub">📁 ${esc(u.cwd)}</div>`;
          }
          let up = "";
          if (u.startedAt) {
            up =
              `<div class="uptime" data-started="${u.startedAt}">` +
              `▶ <span class="uptime-start">${esc(fmtTime(u.startedAt))}</span> · ` +
              `<span class="uptime-el"></span></div>`;
          }
          // Cổng do container giữ: pid lsof thấy là docker backend — kill pid đó
          // là tắt cả Docker Desktop. Chỉ cho dừng đúng container.
          const action = u.container
            ? `<button class="btn danger sm" data-act="stopc" data-port="${u.port}" data-name="${esc(u.container)}">Tắt container</button>`
            : `<button class="btn danger sm" data-act="kill" data-port="${u.port}" data-pid="${u.pid}" data-cmd="${esc(u.command)}">Tắt process</button>`;
          // Nút Ẩn (F1.4): ghi cổng vào unknown_ignore_ports của ports.yaml.
          const ignore = `<button class="btn ghost sm ignore-port" data-act="ignore-port" data-port="${u.port}"
                  title="Thêm vào unknown_ignore_ports trong ports.yaml — cổng sẽ không còn hiện ở đây">Ẩn</button>`;
          return `
      <div class="row${visible ? "" : " hidden"}">
        <div><span class="pill warn">Lạ</span></div>
        <div><div class="svc-name">Cổng ${u.port}</div><div class="svc-sub">pid ${u.pid}</div>${src}</div>
        <div class="ports">${portChip(u.port, true)}</div>
        <div><div class="proc" title="${esc(u.command)}">${esc(u.command)}</div>${up}</div>
        <div class="actions">${action}${ignore}</div>
      </div>`;
        })
        .join("")
    : `<div class="muted" style="padding:8px 4px">Không có cổng lạ ✓</div>`;

  const unknownHidden = unknown.length > 0 && unknown.every(
    (u) => !uiLogic.matchSearch(
      { id: String(u.port), name: "Cổng " + u.port, ports: [{ port: u.port }], command: u.command },
      q,
      u.container
    )
  );

  parts.push(`
    <section class="card${unknownHidden ? " hidden" : ""}">
      <h2>Cổng lạ — không nằm trong cấu hình (${unknown.length})</h2>
      ${unknownBody}
    </section>`);

  // Chỉ thay DOM khi nội dung thực sự đổi — nếu innerHTML bị ghi lại mỗi
  // 3s thì nút bấm bị hủy giữa chừng (không click được) và con trỏ nhảy.
  const html = parts.join("");
  const content = document.getElementById("content");
  if (content.dataset.rendered !== html) {
    content.innerHTML = html;
    content.dataset.rendered = html;
  }
  updateUptime();
}

// updateUptime cập nhật phần "đã chạy" của mọi dòng service (mỗi giây).
// data-started là unix milli của lúc process/container bắt đầu.
function updateUptime() {
  const now = Date.now();
  document.querySelectorAll(".uptime[data-started]").forEach((el) => {
    const started = Number(el.getAttribute("data-started"));
    if (!started) return;
    const target = el.querySelector(".uptime-el");
    if (target) target.textContent = fmtDur(now - started);
  });
}

// ---------------------------------------------------------------------------
// Modal — 1 overlay dùng chung; #svc-modal, #history-modal, #agent-modal là
// panel riêng trong overlay (không có stack): mở panel này thì ẩn panel kia
// và xoá #modal.
// ---------------------------------------------------------------------------
const PANELS = ["svc-modal", "history-modal", "agent-modal"];

function openModal(html) {
  hidePanels();
  document.getElementById("modal").innerHTML = html;
  document.getElementById("overlay").classList.remove("hidden");
}

function hidePanels() {
  PANELS.forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.classList.add("hidden");
  });
}

function showPanel(name) {
  // Đóng modal #modal (nếu đang mở) mà không ẩn overlay, rồi mở panel mới.
  clearInterval(logTimer);
  logTimer = null;
  modalBusy = false;
  document.getElementById("modal").innerHTML = "";
  PANELS.forEach((id) => {
    document.getElementById(id).classList.toggle("hidden", id !== name);
  });
  document.getElementById("overlay").classList.remove("hidden");
}

function closeModal() {
  clearInterval(logTimer);
  logTimer = null;
  // Confirm đang chờ mà modal bị đóng (Escape, bấm nền...) → coi như Huỷ.
  // Không resolve thì Promise treo vĩnh viễn, caller await mãi không xong.
  if (confirmResolve) {
    const r = confirmResolve;
    confirmResolve = null;
    r(false);
  }
  document.getElementById("overlay").classList.add("hidden");
  document.getElementById("modal").innerHTML = "";
  hidePanels();
  modalBusy = false;
}

// confirmResolve: resolver của confirm đang mở — closeModal cần nó để không
// bỏ lại Promise không bao giờ resolve.
let confirmResolve = null;

function confirmModal({ title, body, okText, danger }) {
  return new Promise((resolve) => {
    modalBusy = true;
    confirmResolve = resolve;
    openModal(`
      <h3>${esc(title)}</h3>
      <div class="body">${body}</div>
      <div class="modal-actions">
        <button class="btn ghost" id="m-cancel">Huỷ</button>
        <button class="btn ${danger ? "danger" : "primary"}" id="m-ok">${esc(okText)}</button>
      </div>`);
    document.getElementById("m-ok").onclick = () => { confirmResolve = null; closeModal(); resolve(true); };
    document.getElementById("m-cancel").onclick = () => { confirmResolve = null; closeModal(); resolve(false); };
  });
}

// ---------------------------------------------------------------------------
// Log realtime — poll mỗi giây, tự cuộn xuống đáy khi người dùng không cuộn lên
// ---------------------------------------------------------------------------
const MOCK_LOG_LINES = [
  "info  - Umi v4.0.86",
  "info  - Compiling in 1.2s",
  "info  - webpack compiled successfully",
  "wait  - compiling...",
  "info  - 3 modules have been added",
  `{"level":"error","time":"2026-10-25T09:12:01+07:00","msg":"connect tcp 127.0.0.1:5672: connection refused"}`,
  "warn  - [deprecated] option `foo` will be removed in v5",
  "info  - Built in 842ms",
  "ERROR 500 GET /api/orders: context deadline exceeded",
];
const mockLogBuf = {};

function mockLog(id) {
  const buf = (mockLogBuf[id] = mockLogBuf[id] || []);
  if (buf.length < 4) {
    buf.push(...MOCK_LOG_LINES.slice(0, 4));
  } else if (buf.length < 200) {
    buf.push(MOCK_LOG_LINES[buf.length % MOCK_LOG_LINES.length]);
  }
  return buf.join("\n");
}

// copyToClipboard: ưu tiên clipboard native của wails (CopyText) — trong
// WKWebView navigator.clipboard không chắc dùng được (scheme wails:// không
// phải secure context, hoặc ném "Document is not focused"). Mock/file:// thì
// thử navigator.clipboard, hỏng nữa thì rơi về execCommand.
async function copyToClipboard(text) {
  if (API && typeof API.CopyText === "function") {
    await API.CopyText(text);
    return;
  }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch (e) {
      // rơi xuống execCommand
    }
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  const ok = document.execCommand("copy");
  ta.remove();
  if (!ok) throw new Error("trình duyệt không cho phép copy");
}

// Fallback khi app.logic.js không nạp được: không lọc, không tô màu.
function filterLogLinesFallback(content) {
  const all = String(content || "").split("\n");
  if (all.length && all[all.length - 1] === "") all.pop();
  return { lines: all.map((text, i) => ({ text, level: "", no: i + 1 })), total: all.length, shown: all.length };
}

async function logModal(id) {
  const svc = findService(id);
  modalBusy = true;
  openModal(`
    <h3>Log — ${esc(svc ? svc.name : id)}</h3>
    <div class="log-bar">
      <input class="search-input log-filter" id="log-filter" type="search" spellcheck="false"
        placeholder="Lọc: mã lỗi / từ khóa, cách nhau bởi dấu cách…" />
      <button class="btn ghost sm" id="log-errors" title="Chỉ hiện dòng lỗi (error / fatal / panic / connection refused…)">Chỉ lỗi</button>
      <button class="btn ghost sm" id="log-copy" title="Copy phần đang bôi đen; không bôi đen thì copy toàn bộ dòng đang hiện">Copy</button>
      <button class="btn ghost sm" id="log-bottom" title="Cuộn xuống cuối log">↓ Cuối</button>
      <button class="btn ghost sm" id="m-close">Đóng</button>
    </div>
    <div class="log-meta">
      <span class="log-state" id="log-state">● đang theo dõi</span>
      <span class="log-count mono" id="log-count"></span>
    </div>
    <div class="log-body" id="log-body"><pre id="log-pre"></pre></div>`);
  document.getElementById("m-close").onclick = closeModal;

  const body = document.getElementById("log-body");
  const pre = document.getElementById("log-pre");
  const stateEl = document.getElementById("log-state");
  const countEl = document.getElementById("log-count");
  const filterEl = document.getElementById("log-filter");
  const errBtn = document.getElementById("log-errors");
  const copyBtn = document.getElementById("log-copy");
  const filterLines = uiLogic.filterLogLines || filterLogLinesFallback;

  // pinned = người dùng đang ở đáy → log mới tự cuộn xuống theo.
  // paused = đang bôi đen chữ trong log → tạm không ghi đè DOM (ghi đè là
  // mất vùng chọn, không kịp ⌘C). Bỏ chọn thì tick kế tiếp cập nhật lại.
  let pinned = true;
  let paused = false;
  let raw = null;
  const filter = { query: "", errorsOnly: false };

  const toBottom = () => {
    body.scrollTop = body.scrollHeight;
  };
  const syncState = () => {
    if (!stateEl) return;
    if (paused) stateEl.textContent = "○ đang bôi đen — tạm dừng cập nhật, bỏ chọn để tiếp";
    else if (!pinned) stateEl.textContent = "○ đã cuộn lên — log mới không tự cuộn";
    else stateEl.textContent = "● đang theo dõi";
    stateEl.classList.toggle("paused", paused);
  };
  const syncPinned = () => {
    pinned = body.scrollHeight - body.scrollTop - body.clientHeight < 24;
    body.classList.toggle("not-bottom", !pinned);
    syncState();
  };
  body.addEventListener("scroll", syncPinned);
  document.getElementById("log-bottom").onclick = () => {
    toBottom();
    syncPinned();
  };

  const selectionInLog = () => {
    const sel = window.getSelection ? window.getSelection() : null;
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return "";
    if (!pre.contains(sel.anchorNode) && !pre.contains(sel.focusNode)) return "";
    return sel.toString();
  };

  const render = () => {
    const r = filterLines(raw || "", filter);
    const filtering = Boolean(filter.query || filter.errorsOnly);
    if (!r.total) {
      pre.textContent = raw === null ? "" : "(log rỗng)";
    } else if (!r.shown) {
      pre.innerHTML = `<span class="log-none">(không có dòng nào khớp bộ lọc)</span>`;
    } else {
      pre.innerHTML = r.lines
        .map((l) => (l.level ? `<span class="log-${l.level}">${esc(l.text)}</span>` : esc(l.text)))
        .join("\n");
    }
    const nErr = r.lines.filter((l) => l.level === "error").length;
    countEl.textContent = filtering
      ? `${r.shown}/${r.total} dòng`
      : `${r.total} dòng${nErr ? ` · ${nErr} lỗi` : ""}`;
    if (pinned) toBottom();
  };

  filterEl.oninput = () => {
    filter.query = filterEl.value;
    render();
  };
  filterEl.onkeydown = (ev) => {
    // Escape trong ô lọc: xoá bộ lọc trước, không đóng modal.
    if (ev.key === "Escape" && filterEl.value) {
      ev.stopPropagation();
      filterEl.value = "";
      filter.query = "";
      render();
    }
  };
  errBtn.onclick = () => {
    filter.errorsOnly = !filter.errorsOnly;
    errBtn.classList.toggle("active", filter.errorsOnly);
    render();
  };

  // mousedown preventDefault để bấm nút không làm mất vùng đang bôi đen.
  copyBtn.onmousedown = (ev) => ev.preventDefault();
  copyBtn.onclick = async () => {
    const selected = selectionInLog();
    const text = selected || filterLines(raw || "", filter).lines.map((l) => l.text).join("\n");
    if (!text) return;
    const label = copyBtn.textContent;
    try {
      await copyToClipboard(text);
      copyBtn.textContent = selected ? "✓ Đã copy vùng chọn" : "✓ Đã copy";
    } catch (e) {
      copyBtn.textContent = "✗ " + errMsg(e);
    }
    setTimeout(() => { copyBtn.textContent = label; }, 1500);
  };

  let pending = false;
  const tick = async () => {
    let content;
    try {
      content = API ? await API.GetServiceLog(id) : mockLog(id);
    } catch (e) {
      content = "Lỗi đọc log: " + (e && e.message ? e.message : e);
    }
    if (content === raw) return;
    if (selectionInLog()) {
      pending = true;
      paused = true;
      syncState();
      return;
    }
    pending = false;
    paused = false;
    raw = content;
    render();
    syncState();
  };

  // Bỏ chọn → cập nhật ngay phần log đã dồn lại, không đợi tick kế tiếp.
  const onSelChange = () => {
    if (document.getElementById("log-pre") !== pre) {
      document.removeEventListener("selectionchange", onSelChange);
      return;
    }
    if (paused && !selectionInLog()) {
      paused = false;
      syncState();
      if (pending) tick();
    }
  };
  document.addEventListener("selectionchange", onSelChange);

  await tick();
  toBottom();
  syncPinned();
  filterEl.focus();

  clearInterval(logTimer);
  logTimer = setInterval(tick, 1000);
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
  const running = svc && isUp(svc.status);

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
      (svc && isUp(svc.status) ? `<br><br>⚠️ Service đang chạy — cần restart service để chạy code mới.` : "") +
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
// Actions — từng service (giữ nguyên hành vi cũ)
// ---------------------------------------------------------------------------
async function doStart(id) {
  if (!API) return;
  try {
    await API.StartService(id);
  } catch (e) {
    showOpError("Bật " + id + " lỗi:\n" + errMsg(e));
  }
  await refresh();
}

async function doStop(id) {
  const s = findService(id);
  if (!s) return;
  const body = s.isCompose
    ? `Sẽ chạy <code>docker compose stop</code> — container được giữ, không mất dữ liệu.`
    : s.pid
      ? `Sẽ tắt process:<br><code>pid ${s.pid} — ${esc(s.command || "?")}</code>`
      : s.managedPid
        ? `Sẽ tắt process do app đang giữ:<br><code>pid ${s.managedPid}</code><br><br>` +
          `Service chưa lên cổng nên không có listener để kill — app tắt được nhờ process group.`
        : `Không tìm thấy process nào đang chạy.`;
  const ok = await confirmModal({ title: `Tắt ${s.name}?`, body, okText: "Tắt", danger: true });
  if (!ok) return;
  if (!API) return; // chế độ mock — chỉ xem giao diện
  try {
    await API.StopService(id);
  } catch (e) {
    showOpError("Tắt " + id + " lỗi:\n" + errMsg(e));
  }
  await refresh();
}

// Restart: tắt service rồi bật lại. Dành cho service kẹt trong lúc khởi động
// (process còn sống nhưng chưa lên cổng) hoặc khi cần chạy lại code mới.
async function doRestart(id) {
  const s = findService(id);
  if (!s) return;
  const body = s.isCompose
    ? `Sẽ <code>docker compose stop</code> rồi <code>up -d</code> lại.`
    : `Sẽ tắt process hiện tại rồi bật lại:<br><code>${esc(s.startCmd || "?")}</code>` +
      (s.managedPid
        ? `<br>pid <b>${s.managedPid}</b> (do app quản lý)`
        : s.pid
          ? `<br>pid <b>${s.pid}</b>`
          : "");
  const ok = await confirmModal({ title: `Restart ${s.name}?`, body, okText: "Restart", danger: false });
  if (!ok) return;
  if (!API) return; // chế độ mock — chỉ xem giao diện
  try {
    await API.RestartService(id);
  } catch (e) {
    showOpError("Restart " + id + " lỗi:\n" + errMsg(e));
  }
  await refresh();
}

// openPort mở cổng trên trình duyệt. Trong app Wails gọi binding OpenURL
// (mở bằng trình duyệt mặc định của hệ điều hành); ngoài app (chế độ mock)
// thì dùng window.open.
async function openPort(port) {
  if (!port) return;
  const url = `http://localhost:${port}`;
  if (!API) {
    window.open(url, "_blank");
    return;
  }
  try {
    await API.OpenURL(url);
  } catch (e) {
    showOpError("Mở trình duyệt lỗi:\n" + errMsg(e));
  }
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
    showOpError("Kill lỗi:\n" + errMsg(e));
  }
  await refresh();
}

// Cổng lạ do container giữ: pid mà lsof thấy là docker backend — kill pid đó
// là tắt cả Docker Desktop, nên chỉ cho dừng đúng container.
async function doStopUnknownContainer(name, port) {
  const ok = await confirmModal({
    title: `Dừng container ở cổng ${port}?`,
    body: `<code>${esc(name)}</code><br><br>Container này đang publish cổng ${port} ra máy host.`,
    okText: "Dừng container",
    danger: true,
  });
  if (!ok) return;
  if (!API) return;
  try {
    await API.StopUnknownContainer(name);
  } catch (e) {
    showOpError("Dừng container lỗi:\n" + errMsg(e));
  }
  await refresh();
}

// ---------------------------------------------------------------------------
// Actions — cấp nhóm và toàn bộ workspace (F1.1)
// Chế độ mock: đổi trạng thái trong MOCK để xem giao diện phản hồi.
// Lỗi binding chỉ trả error tổng hợp → hiện qua showOpError (bền trước vòng
// quét 3s); trạng thái thật từng service do refresh() phản ánh.
// ---------------------------------------------------------------------------
function mockSetGroup(group, up) {
  const g = MOCK.groups.find((x) => x.name === group);
  if (!g) return `không tìm thấy group ${JSON.stringify(group)}`;
  g.services.forEach((s) => {
    s.status = up ? "running" : "stopped";
    s.ports.forEach((p) => { p.open = up; });
    if (!up) { s.pid = 0; s.startedAt = 0; }
    else if (!s.startedAt) s.startedAt = Date.now();
  });
  return null;
}

async function doStartGroup(group, btn) {
  if (!group) return;
  if (btn) btn.disabled = true;
  let err = null;
  try {
    if (API) await API.StartGroup(group);
    else { await new Promise((r) => setTimeout(r, 400)); err = mockSetGroup(group, true); }
  } catch (e) {
    err = e;
  }
  await refresh();
  if (btn) btn.disabled = false;
  if (err) showOpError(`Bật nhóm "${group}" lỗi:\n` + errMsg(err));
}

async function doStopGroup(group, btn) {
  if (!group) return;
  const ok = await confirmModal({
    title: `Tắt nhóm "${group}"?`,
    body: `Sẽ tắt lần lượt các service của nhóm này (thứ tự ngược thứ tự khởi động).`,
    okText: "Tắt nhóm",
    danger: true,
  });
  if (!ok) return;
  if (btn) btn.disabled = true;
  let err = null;
  try {
    if (API) await API.StopGroup(group);
    else { await new Promise((r) => setTimeout(r, 400)); err = mockSetGroup(group, false); }
  } catch (e) {
    err = e;
  }
  await refresh();
  if (btn) btn.disabled = false;
  if (err) showOpError(`Tắt nhóm "${group}" lỗi:\n` + errMsg(err));
}

async function doStartAll(btn) {
  const ok = await confirmModal({
    title: "Bật toàn bộ workspace?",
    body: `Sẽ bật các group theo thứ tự <b>hạ tầng → Backend Go → Frontend</b>, chờ cổng lên từng tier (tối đa ~20s mỗi tier) — có thể mất tới khoảng 1 phút.`,
    okText: "Bật toàn bộ",
    danger: false,
  });
  if (!ok) return;
  if (btn) btn.disabled = true;
  let err = null;
  try {
    if (API) await API.StartAll();
    else { await new Promise((r) => setTimeout(r, 500)); for (const g of MOCK.groups) mockSetGroup(g.name, true); }
  } catch (e) {
    err = e;
  }
  await refresh();
  if (btn) btn.disabled = false;
  if (err) showOpError("Bật toàn bộ workspace lỗi:\n" + errMsg(err));
}

async function doStopAll(btn) {
  const ok = await confirmModal({
    title: "Tắt toàn bộ workspace?",
    body: `Sẽ tắt <b>tất cả service</b> của workspace theo thứ tự ngược (Frontend → Backend → hạ tầng). Các cổng lạ không bị đụng tới.`,
    okText: "Tắt toàn bộ",
    danger: true,
  });
  if (!ok) return;
  if (btn) btn.disabled = true;
  let err = null;
  try {
    if (API) await API.StopAll();
    else { await new Promise((r) => setTimeout(r, 500)); for (const g of MOCK.groups) mockSetGroup(g.name, false); }
  } catch (e) {
    err = e;
  }
  await refresh();
  if (btn) btn.disabled = false;
  if (err) showOpError("Tắt toàn bộ workspace lỗi:\n" + errMsg(err));
}

// ---------------------------------------------------------------------------
// Actions — ẩn cổng lạ (F1.4) và mở Terminal (F1.5)
// ---------------------------------------------------------------------------
async function doHideUnknownPort(port) {
  const ok = await confirmModal({
    title: `Ẩn cổng ${port}?`,
    body: `Sẽ thêm <code>${port}</code> vào <code>unknown_ignore_ports</code> trong <code>ports.yaml</code> — cổng này sẽ không còn hiện trong danh sách cổng lạ (file vẫn giữ nguyên comment/định dạng).`,
    okText: "Ẩn cổng",
    danger: false,
  });
  if (!ok) return;
  let err = null;
  try {
    if (API) await API.HideUnknownPort(port);
    else MOCK.unknown = MOCK.unknown.filter((u) => u.port !== port);
  } catch (e) {
    err = e;
  }
  await refresh();
  if (err) showOpError(`Ẩn cổng ${port} lỗi:\n` + errMsg(err));
}

// doTerminal: frontend KHÔNG tự tính đường dẫn — chỉ gửi id, backend tự resolve
// (cfg.Service(id) → serviceDir → ResolveDir → mở Terminal.app tại đó).
async function doTerminal(id) {
  if (!API) { showOpError("Chế độ mock không mở Terminal — chạy app thật để dùng."); return; }
  try {
    await API.OpenTerminal(id);
  } catch (e) {
    showOpError("Mở Terminal lỗi:\n" + errMsg(e));
  }
}

// ---------------------------------------------------------------------------
// Chip sức khoẻ (F2.2) — sau mỗi lần quét gọi HealthCheckAll đúng 1 lần
// (fire-and-forget, có cờ chống chồng); KHÔNG nằm trong ticker 1 giây.
// Xong thì render() lại để gắn chip — render() không trigger refresh nên không vòng lặp.
// ---------------------------------------------------------------------------
function mockHealth() {
  const map = {};
  for (const g of MOCK.groups) {
    for (const s of g.services) {
      for (const p of s.ports) {
        if (!p.open) continue;
        if (s.isCompose) {
          map[s.id + ":" + p.port] = { id: s.id, port: p.port, state: "skipped", code: 0, latencyMs: 0, detail: "service compose (docker)" };
        } else if (s.id === "auth-be" && p.port === 9090) {
          // (mock) giả lập 1 cổng trả lỗi để xem chip health-http_error
          map[s.id + ":" + p.port] = { id: s.id, port: p.port, state: "http_error", code: 503, latencyMs: 8, detail: "(mock) HTTP 503 Service Unavailable" };
        } else {
          map[s.id + ":" + p.port] = { id: s.id, port: p.port, state: "ok", code: 200, latencyMs: 5 + (p.port % 40), detail: "" };
        }
      }
    }
  }
  return map;
}

async function runHealth(force) {
  if (healthBusy) return;
  // Throttle: probe mỗi 30s tối đa một lần. refresh() chạy mỗi 3s — nếu gọi
  // probe theo refresh là đánh ssh/port liên tục vô nghĩa (backend đọc cache
  // được nhưng request vẫn bắn đi mỗi vòng). Nút "Probe" bấm tay → force.
  const now = Date.now();
  if (!force && now - lastHealthAt < 30e3) return;
  lastHealthAt = now;
  healthBusy = true;
  try {
    const entries = API ? await API.HealthCheckAll() : mockHealth();
    const map = {};
    (entries || []).forEach((e) => { map[e.id + ":" + e.port] = e; });
    healthMap = map;
    render();
  } catch (e) {
    showOpError("Probe sức khoẻ lỗi:\n" + errMsg(e));
  } finally {
    healthBusy = false;
  }
}

// ---------------------------------------------------------------------------
// Bảng đối chiếu mirror (F2.1) — panel #mirror-panel nằm NGOÀI #content nên
// render() 3s không xoá được; nội dung điền trực tiếp, không qua render().
// ---------------------------------------------------------------------------
function portOwners() {
  const owners = {};
  const src = lastRes || MOCK;
  for (const g of src.groups || []) {
    for (const s of g.services || []) {
      for (const p of s.ports || []) {
        if (!owners[p.port]) owners[p.port] = { service: s.id, group: g.name };
      }
    }
  }
  return owners;
}

function mockMirror() {
  return {
    checkedAt: new Date().toISOString(),
    mismatches: [
      { port: 6381, inAgents: false, inPortsYaml: true, note: "6381 chỉ có trong ports.yaml (infra-fm-redis) — chưa có trong AGENTS.md mục 10.1" },
      { port: 8088, inAgents: true, inPortsYaml: false, note: "8088 có trong AGENTS.md (hạ tầng) nhưng chưa khai trong ports.yaml" },
    ],
  };
}

async function openMirror() {
  const panel = document.getElementById("mirror-panel");
  const body = document.getElementById("mirror-body");
  const note = document.getElementById("mirror-note");
  panel.classList.remove("hidden");
  panel.scrollIntoView({ behavior: "smooth", block: "start" });
  body.innerHTML = `<tr><td colspan="6" class="muted">Đang đối chiếu…</td></tr>`;
  note.classList.add("hidden");
  let rep;
  try {
    rep = API ? await API.MirrorReport() : mockMirror();
  } catch (e) {
    body.innerHTML = `<tr><td colspan="6" class="muted">Lỗi đối chiếu: ${esc(errMsg(e))}</td></tr>`;
    return;
  }
  const owners = portOwners();
  const yn = (b) => (b ? `<span class="yes">✓</span>` : `<span class="no">✗</span>`);
  const rows = (rep.mismatches || [])
    .map((m) => {
      const o = owners[m.port] || { service: "—", group: "—" };
      return `<tr class="mismatch">
        <td class="mono">${m.port}</td>
        <td>${esc(o.service)}</td>
        <td>${esc(o.group)}</td>
        <td>${yn(m.inAgents)}</td>
        <td>${yn(m.inPortsYaml)}</td>
        <td>${esc(m.note || "")}</td>
      </tr>`;
    })
    .join("");
  body.innerHTML =
    rows ||
    `<tr><td colspan="6" class="muted">Không có lệch ✓</td></tr>`;
  const mm = rep.mismatches ? rep.mismatches.length : 0;
  // Ghi chú từ backend (vd "Không đọc được AGENTS.md: ...") đặt TRƯỚC — nếu
  // thiếu note thì bảng rỗng "0 lệch" dễ bị hiểu nhầm là mọi thứ đều ổn.
  note.textContent =
    (rep.note ? rep.note + " · " : "") +
    `Đối chiếu lúc ${fmtEventTime(rep.checkedAt)} — ${mm} cổng lệch.`;
  note.classList.remove("hidden");
}

function closeMirror() {
  document.getElementById("mirror-panel").classList.add("hidden");
}

// ---------------------------------------------------------------------------
// Lịch sử chạy (F3.3) — modal #history-modal
// ---------------------------------------------------------------------------
const KIND_LABELS = {
  start: "Bật",
  stop: "Tắt",
  restart: "Restart",
  crash: "Crash",
  stuck: "Kẹt",
  fail: "Lỗi",
  edit: "Sửa cấu hình",
};

function mockHistory() {
  const ev = (ms, kind, detail) => ({ at: new Date(Date.now() - ms).toISOString(), kind, detail });
  return [
    ev(3 * 3600e3, "start", "bật bởi app (mock)"),
    ev(2.6 * 3600e3, "stop", "dừng có chủ ý"),
    ev(2 * 3600e3, "start", ""),
    ev(30 * 60e3, "crash", "thoát với code 1"),
    ev(29 * 60e3, "restart", "tắt rồi bật lại"),
  ];
}

async function historyModal(id) {
  const svc = findService(id);
  document.getElementById("history-title").textContent = `Lịch sử chạy — ${svc ? svc.name : id}`;
  const body = document.getElementById("history-body");
  body.innerHTML = `<tr><td colspan="3" class="muted">Đang đọc…</td></tr>`;
  showPanel("history-modal");
  let evs;
  try {
    evs = API ? await API.History(id) : mockHistory(id);
  } catch (e) {
    body.innerHTML = `<tr><td colspan="3" class="muted">Lỗi đọc lịch sử: ${esc(errMsg(e))}</td></tr>`;
    return;
  }
  body.innerHTML = (evs && evs.length)
    ? evs
        .map((ev) => {
          const label = KIND_LABELS[ev.kind] || ev.kind;
          return `<tr>
            <td class="mono">${esc(fmtEventTime(ev.at))}</td>
            <td><span class="kind-${esc(ev.kind)}">${esc(label)}</span></td>
            <td>${esc(ev.detail || "")}</td>
          </tr>`;
        })
        .join("")
    : `<tr><td colspan="3" class="muted">Chưa có sự kiện nào được ghi</td></tr>`;
}

// ---------------------------------------------------------------------------
// Thêm/sửa service (F3.4) — modal #svc-modal
// ---------------------------------------------------------------------------

function currentGroupNames() {
  const src = lastRes || MOCK;
  return (src.groups || []).map((g) => g.name);
}

function showSvcError(msg) {
  const el = document.getElementById("svc-error");
  el.textContent = typeof msg === "string" ? msg : JSON.stringify(msg);
  el.classList.remove("hidden");
}

function hideSvcError() {
  document.getElementById("svc-error").classList.add("hidden");
}

// showEditWarn: cảnh báo trong modal (KHÔNG dùng errbar — render() xoá errbar
// mỗi vòng quét). Backend xoá field khi giá trị gửi sang rỗng → phải cảnh báo.
function showEditWarn(text) {
  let el = document.getElementById("svc-edit-warn");
  if (!el) {
    el = document.createElement("div");
    el.id = "svc-edit-warn";
    el.className = "warn-note";
    const form = document.getElementById("svc-form");
    form.parentNode.insertBefore(el, form);
  }
  el.textContent = text;
  el.classList.remove("hidden");
}

function hideEditWarn() {
  const el = document.getElementById("svc-edit-warn");
  if (el) el.classList.add("hidden");
}

// relDirOf: dir tương đối workspace suy ra từ repo.topLevel (dùng để điền sẵn
// form Sửa — ServiceView không mang dir gốc trong ports.yaml).
function relDirOf(svc) {
  if (!svc || !svc.repo || !svc.repo.topLevel) return "";
  const root = (lastRes || MOCK).workspaceRoot || "";
  if (root && svc.repo.topLevel.indexOf(root + "/") === 0) {
    return svc.repo.topLevel.slice(root.length + 1);
  }
  return "";
}

function openSvcModal(svc) {
  hideSvcError();
  hideEditWarn();

  // Select group: các group đang có trong lần quét gần nhất.
  const sel = document.getElementById("svc-group");
  const names = currentGroupNames();
  sel.innerHTML = names.map((n) => `<option value="${esc(n)}">${esc(n)}</option>`).join("");

  if (svc) {
    // Sửa: điền sẵn những gì ServiceView có. id bị khoá (đổi id = tạo service
    // mới, service cũ vẫn còn → trùng lặp).
    document.getElementById("svc-title").textContent = "Sửa service — " + svc.id;
    document.getElementById("svc-id").value = svc.id;
    document.getElementById("svc-id").disabled = true;
    document.getElementById("svc-name").value = svc.name || "";
    const g = (lastRes || MOCK).groups.find((x) => (x.services || []).some((s) => s.id === svc.id));
    if (g && names.indexOf(g.name) < 0) {
      sel.innerHTML += `<option value="${esc(g.name)}">${esc(g.name)}</option>`;
    }
    sel.value = g ? g.name : names[0] || "";
    document.getElementById("svc-ports").value = (svc.ports || []).map((p) => p.port).join(", ");
    document.getElementById("svc-start").value = svc.startCmd || "";
    const dir = relDirOf(svc);
    document.getElementById("svc-dir").value = dir;
    document.getElementById("svc-repo-dir").value = svc.repoDir || "";
    document.getElementById("svc-compose-dir").value = "";
    document.getElementById("svc-compose-file").value = "";
    document.getElementById("svc-compose-service").value = "";
    document.getElementById("svc-health-path").value = svc.healthPath || "";
    document.getElementById("svc-health-skip-ports").value = (svc.healthSkipPorts || []).join(", ");
    document.getElementById("svc-auto-restart").checked = !!svc.autoRestart;
    // Những ô không biết giá trị cũ: để trống khi Lưu sẽ XOÁ field đó khỏi
    // ports.yaml (backend setString rỗng = removeKey) → cảnh báo rõ trong modal.
    // repo_dir + health_* giờ ServiceView mang theo (backend điền) nên biết
    // giá trị cũ — chỉ dir và compose_* là vẫn phải cảnh báo.
    const unknowns = [];
    if (!dir) unknowns.push(svc.repo ? "dir" : "dir (service không có thông tin repo để suy ra)");
    if (svc.isCompose) unknowns.push("compose_dir, compose_file, compose_service");
    if (unknowns.length) {
      showEditWarn(
        "⚠️ Đang SỬA service có sẵn. Các ô không điền lại sẽ bị XOÁ khỏi ports.yaml" +
          ` (hiện không biết giá trị cũ của: ${unknowns.join(", ")})` +
          ". Hãy điền đủ trước khi bấm Lưu."
      );
    }
  } else {
    document.getElementById("svc-title").textContent = "Thêm service";
    document.getElementById("svc-id").value = "";
    document.getElementById("svc-id").disabled = false;
    document.getElementById("svc-name").value = "";
    sel.value = names[0] || "";
    document.getElementById("svc-ports").value = "";
    document.getElementById("svc-start").value = "";
    document.getElementById("svc-dir").value = "";
    document.getElementById("svc-repo-dir").value = "";
    document.getElementById("svc-compose-dir").value = "";
    document.getElementById("svc-compose-file").value = "";
    document.getElementById("svc-compose-service").value = "";
    document.getElementById("svc-health-path").value = "";
    document.getElementById("svc-health-skip-ports").value = "";
    document.getElementById("svc-auto-restart").checked = false;
  }
  showPanel("svc-modal");
}

function parsePortList(text) {
  return String(text || "")
    .split(/[\s,;]+/)
    .map((t) => parseInt(t, 10))
    .filter((n) => Number.isInteger(n) && n > 0 && n < 65536);
}

function mockSaveService(spec) {
  for (const g of MOCK.groups) {
    const i = g.services.findIndex((s) => s.id === spec.id);
    if (i >= 0) {
      if (g.name !== spec.group) {
        g.services.splice(i, 1);
        break;
      }
      g.services[i] = Object.assign({}, g.services[i], {
        name: spec.name,
        ports: spec.ports.map((p) => ({ port: p, open: false })),
        startCmd: spec.start,
        autoRestart: spec.autoRestart,
      });
      return;
    }
  }
  let g = MOCK.groups.find((x) => x.name === spec.group);
  if (!g) {
    g = { name: spec.group, services: [] };
    MOCK.groups.push(g);
  }
  g.services.push({
    id: spec.id,
    name: spec.name,
    ports: spec.ports.map((p) => ({ port: p, open: false })),
    status: "stopped",
    pid: 0,
    command: "",
    managedByApp: false,
    startCmd: spec.start,
    isCompose: !!spec.composeService,
    autoRestart: spec.autoRestart,
  });
}

async function saveSvc() {
  const spec = {
    id: document.getElementById("svc-id").value.trim(),
    name: document.getElementById("svc-name").value.trim(),
    group: document.getElementById("svc-group").value,
    ports: parsePortList(document.getElementById("svc-ports").value),
    dir: document.getElementById("svc-dir").value.trim(),
    start: document.getElementById("svc-start").value.trim(),
    repoDir: document.getElementById("svc-repo-dir").value.trim(),
    composeDir: document.getElementById("svc-compose-dir").value.trim(),
    composeFile: document.getElementById("svc-compose-file").value.trim(),
    composeService: document.getElementById("svc-compose-service").value.trim(),
    healthPath: document.getElementById("svc-health-path").value.trim(),
    healthSkipPorts: parsePortList(document.getElementById("svc-health-skip-ports").value),
    autoRestart: document.getElementById("svc-auto-restart").checked,
  };
  // Validate phía frontend — lỗi hiển thị TRONG modal (errbar bị render() xoá).
  if (!spec.id) return showSvcError("Thiếu id của service.");
  if (!spec.name) return showSvcError("Thiếu tên hiển thị (name).");
  if (!spec.group) return showSvcError("Thiếu group — hãy quét lại để có danh sách group.");
  if (!spec.ports.length) return showSvcError("Thiếu cổng — nhập ít nhất 1 số cổng hợp lệ (vd: 3000, 9090).");
  if (!spec.composeService && !spec.dir) return showSvcError("Thiếu thư mục (dir) — vd: auth-service/backend.");

  const saveBtn = document.getElementById("svc-save");
  saveBtn.disabled = true;
  try {
    if (API) {
      await API.SaveService(spec);
      // Nạp lại ports.yaml sau khi ghi để config trong RAM khớp đĩa; lỗi nạp
      // lại không hủy kết quả ghi → vẫn đóng modal và quét lại.
      let reloadErr = null;
      try {
        await API.ReloadConfig();
      } catch (e) {
        reloadErr = e;
      }
      closeModal();
      await refresh();
      if (reloadErr) showOpError("Đã lưu nhưng nạp lại config lỗi:\n" + errMsg(reloadErr));
    } else {
      await new Promise((r) => setTimeout(r, 300));
      mockSaveService(spec);
      closeModal();
      await refresh();
    }
  } catch (e) {
    // Không có file config để ghi / validate backend từ chối → hiện trong modal.
    showSvcError("Lưu service lỗi:\n" + errMsg(e));
  } finally {
    saveBtn.disabled = false;
  }
}

async function suggestPort() {
  const input = document.getElementById("svc-ports");
  try {
    const p = API ? await API.SuggestPort() : 3006;
    if (!p) {
      showSvcError("Không còn cổng trống nào trong bảng quy hoạch (AGENTS.md mục 10.1).");
      return;
    }
    const cur = parsePortList(input.value);
    if (cur.indexOf(p) < 0) {
      input.value = cur.length ? cur.join(", ") + ", " + p : String(p);
    }
    hideSvcError();
  } catch (e) {
    showSvcError("Gợi ý cổng lỗi:\n" + errMsg(e));
  }
}

// ---------------------------------------------------------------------------
// Phím tắt (F4.1) + sự kiện từ menu native
// ---------------------------------------------------------------------------
function focusSearch() {
  const el = document.getElementById("search");
  if (!el) return;
  el.scrollIntoView({ block: "nearest" });
  el.focus();
  el.select();
}

// ---------------------------------------------------------------------------
// Giữ trạng thái giao diện (F4.3) — localStorage "port-manager-ui-v1"
// ---------------------------------------------------------------------------
function persistUi() {
  try {
    localStorage.setItem(UI_KEY, uiLogic.saveUiState(uiState));
  } catch (e) {
    // localStorage có thể bị chặn (WKWebView) — im lặng, app vẫn chạy.
  }
}

function toggleGroupCollapsed(name) {
  const i = uiState.collapsedGroups.indexOf(name);
  if (i >= 0) uiState.collapsedGroups.splice(i, 1);
  else uiState.collapsedGroups.push(name);
  persistUi();
  render();
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
    // Sau mỗi lần quét: probe sức khoẻ đúng 1 lần (nền, không await — F2.2).
    runHealth();
  } catch (e) {
    showError("Quét lỗi: " + (e && e.message ? e.message : e));
  }
}

function schedulePoll() {
  clearTimeout(pollTimer);
  const on = document.getElementById("auto").checked;
  if (!on) return;
  pollTimer = setTimeout(async () => {
    try {
      if (!modalBusy) await refresh();
    } catch (e) {
      // không để exception làm chết chuỗi poll
    }
    schedulePoll();
  }, POLL_MS);
}

// ---------------------------------------------------------------------------
// Chạy ngầm (v0.7.0) — modal #agent-modal (LaunchAgent) + banner #exit-banner
// (lý do app thoát lần trước). Hàm thuần ở app.logic.js (agentSummary,
// agentRows, installLabel, exitBanner).
// ---------------------------------------------------------------------------
function mockAgentStatus() {
  return {
    label: "vn.smatec.port-manager",
    plistPath: "~/Library/LaunchAgents/vn.smatec.port-manager.plist",
    binPath: "~/.port-manager/bin/port-manager",
    logOutPath: "~/.port-manager/logs/agent.out.log",
    logErrPath: "~/.port-manager/logs/agent.err.log",
    installed: false,
    loaded: false,
    pid: 0,
    thisIsAgent: false,
    canInstall: false,
    cannotInstallReason: "chế độ mock — không có backend để cài",
  };
}

// agentStatus: bản mới nhất lấy từ API.AgentStatus() — confirm cài/gỡ dùng
// để đổi lời cảnh báo (vd: gỡ khi chính tiến trình này do launchd quản lý).
let agentStatus = null;

async function openAgentModal() {
  document.getElementById("agent-error").classList.add("hidden");
  document.getElementById("agent-status").innerHTML = `<div class="muted">Đang kiểm tra launchctl…</div>`;
  showPanel("agent-modal");
  await loadAgentStatus();
}

async function loadAgentStatus() {
  try {
    agentStatus = API ? await API.AgentStatus() : mockAgentStatus();
  } catch (e) {
    agentStatus = null;
    document.getElementById("agent-status").innerHTML =
      `<div class="muted">Lỗi lấy trạng thái: ${esc(errMsg(e))}</div>`;
    return;
  }
  renderAgentModal(agentStatus);
}

function renderAgentModal(st) {
  const sum = uiLogic.agentSummary(st);
  const rows = uiLogic.agentRows(st)
    .map((r) => `<div class="agent-kv"><span class="agent-k">${esc(r.k)}</span><code class="agent-v">${esc(r.v)}</code></div>`)
    .join("");
  document.getElementById("agent-status").innerHTML =
    `<div class="agent-summary agent-${esc(sum.tone)}">${esc(sum.label)}</div>${rows}`;

  const install = document.getElementById("agent-install");
  install.textContent = uiLogic.installLabel(st);
  install.disabled = !(st && st.canInstall);
  install.title = st && !st.canInstall
    ? (st.cannotInstallReason || "Không cài được")
    : "Chép binary hiện tại vào ~/.port-manager/bin rồi đăng ký LaunchAgent";
  document.getElementById("agent-uninstall").disabled = !(st && st.installed);
}

function showAgentError(msg) {
  const el = document.getElementById("agent-error");
  el.textContent = msg;
  el.classList.remove("hidden");
}

// doAgentInstall: confirm → API.AgentInstall() → backend tự thoát sau ~1.5s để
// launchd mở lại bản ngầm (bàn giao). confirmModal dùng #modal nên panel agent
// bị ẩn trong lúc hỏi → huỷ/lỗi phải mở lại panel.
async function doAgentInstall() {
  const st = agentStatus;
  const ok = await confirmModal({
    title: st && st.installed ? "Cài lại chạy ngầm" : "Cài chạy ngầm",
    body: `<p>App sẽ chép binary hiện tại vào <code>~/.port-manager/bin</code>, đăng ký LaunchAgent
             <code>vn.smatec.port-manager</code> rồi <b>tự thoát và mở lại ngầm</b> sau ~2 giây.</p>
           <p class="muted">Từ đó app tự mở khi đăng nhập và tự mở lại khi bị tắt đột ngột.
             Sau mỗi lần <code>make build</code> cần cài lại để bàn giao sang binary mới.</p>`,
    okText: st && st.installed ? "Cài lại" : "Cài",
  });
  if (!ok) { await openAgentModal(); return; }
  if (!API) { await openAgentModal(); showAgentError("Chế độ mock — không có backend để cài."); return; }
  try {
    await API.AgentInstall();
  } catch (e) {
    await openAgentModal();
    showAgentError("Cài chạy ngầm lỗi:\n" + errMsg(e));
    return;
  }
  modalBusy = true; // chặn poll vẽ lại trong lúc chờ app thoát
  openModal(`<h3>Đã cài chạy ngầm</h3>
    <div class="body"><p>Port Manager sẽ thoát và tự mở lại ở chế độ ngầm trong vài giây…</p>
    <p class="muted">Nếu cửa sổ không tự mở lại, bấm icon trên menu bar hoặc mở app từ Dock.</p></div>`);
}

async function doAgentUninstall() {
  const st = agentStatus;
  const extra = st && st.thisIsAgent
    ? `<p>⚠️ <b>Tiến trình này đang do launchd quản lý</b> — gỡ xong app sẽ bị dừng (SIGTERM).
         Mở lại bằng tay từ Dock hoặc <code>make run</code>.</p>`
    : "";
  const ok = await confirmModal({
    title: "Gỡ chạy ngầm",
    body: `<p>Xoá LaunchAgent và binary trong <code>~/.port-manager/bin</code>. App sẽ không còn tự mở
             khi đăng nhập / tự mở lại khi bị tắt.</p>${extra}`,
    okText: "Gỡ",
    danger: true,
  });
  if (!ok) { await openAgentModal(); return; }
  if (!API) { await openAgentModal(); showAgentError("Chế độ mock — không có gì để gỡ."); return; }
  try {
    await API.AgentUninstall();
  } catch (e) {
    await openAgentModal();
    showAgentError("Gỡ chạy ngầm lỗi:\n" + errMsg(e));
    return;
  }
  await openAgentModal();
}

// showExitBanner: chỉ hiện khi lần trước thoát bất thường (uiLogic.exitBanner
// trả null cho none/quit). Không tự ẩn — người dùng bấm "Đã hiểu".
function showExitBanner(info) {
  const b = uiLogic.exitBanner(info);
  if (!b) return;
  document.getElementById("exit-banner-title").textContent = b.title;
  document.getElementById("exit-banner-detail").textContent = b.text;
  document.getElementById("exit-banner").classList.remove("hidden");
}

function dismissExitBanner() {
  document.getElementById("exit-banner").classList.add("hidden");
}

// ---------------------------------------------------------------------------
// Listener ủy quyền duy nhất cho mọi button[data-act]
// ---------------------------------------------------------------------------
document.addEventListener("click", (ev) => {
  // Thu gọn / mở group: bấm vào tiêu đề group (h2[data-group]).
  const head = ev.target.closest("h2[data-group]");
  if (head) {
    toggleGroupCollapsed(head.dataset.group);
    return;
  }
  const btn = ev.target.closest("button[data-act]");
  if (!btn) return;
  const act = btn.dataset.act;
  if (act === "start") doStart(btn.dataset.id);
  else if (act === "stop") doStop(btn.dataset.id);
  else if (act === "restart") doRestart(btn.dataset.id);
  else if (act === "log") logModal(btn.dataset.id);
  else if (act === "open") openPort(Number(btn.dataset.port));
  else if (act === "kill") doKillUnknown(Number(btn.dataset.port), Number(btn.dataset.pid), btn.dataset.cmd);
  else if (act === "stopc") doStopUnknownContainer(btn.dataset.name, Number(btn.dataset.port));
  else if (act === "repo") repoModal(btn.dataset.id);
  else if (act === "switch") {
    const val = btn.dataset.branchInput
      ? (document.getElementById("branch-input") || {}).value
      : btn.dataset.branch;
    doSwitch(btn.dataset.id, (val || "").trim());
  }
  // --- v0.6.0 ---
  else if (act === "group-start") doStartGroup(btn.dataset.group, btn);
  else if (act === "group-stop") doStopGroup(btn.dataset.group, btn);
  else if (act === "start-all") doStartAll(btn);
  else if (act === "stop-all") doStopAll(btn);
  else if (act === "ignore-port") doHideUnknownPort(Number(btn.dataset.port));
  else if (act === "terminal") doTerminal(btn.dataset.id);
  else if (act === "health") runHealth(true);
  else if (act === "mirror") openMirror();
  else if (act === "add-service") openSvcModal(null);
  else if (act === "reload-config") doReloadConfig(btn);
  else if (act === "edit") {
    const svc = findService(btn.dataset.id);
    if (!svc) {
      showOpError("Không tìm thấy service " + btn.dataset.id + " — có thể vừa bị xoá khỏi ports.yaml, hãy quét lại.");
      return;
    }
    openSvcModal(svc);
  }
  else if (act === "history") historyModal(btn.dataset.id);
  // --- v0.7.0 chạy ngầm ---
  else if (act === "agent") openAgentModal();
  else if (act === "agent-refresh") loadAgentStatus();
  else if (act === "agent-install") doAgentInstall();
  else if (act === "agent-uninstall") doAgentUninstall();
  else if (act === "dismiss-exit") dismissExitBanner();
});

// doReloadConfig (F3.4): nạp lại ports.yaml sau khi user sửa tay file.
async function doReloadConfig(btn) {
  if (btn) btn.disabled = true;
  let err = null;
  try {
    if (API) await API.ReloadConfig();
    else err = "chế độ mock — không có config để nạp lại";
  } catch (e) {
    err = e;
  }
  await refresh();
  if (btn) btn.disabled = false;
  if (err) showOpError("Nạp lại config lỗi:\n" + errMsg(err));
}

document.getElementById("btn-refresh").onclick = async () => { await refresh(); schedulePoll(); };
document.getElementById("auto").onchange = schedulePoll;

// Bộ lọc (F1.2): mọi thay đổi → cập nhật uiState → lưu localStorage → render ngay.
document.getElementById("search").addEventListener("input", (ev) => {
  uiState.search = ev.target.value;
  persistUi();
  render();
});
document.getElementById("filter-state").addEventListener("change", (ev) => {
  uiState.filterState = ev.target.value;
  persistUi();
  render();
});
document.getElementById("filter-dirty").addEventListener("change", (ev) => {
  uiState.filterDirty = ev.target.checked;
  persistUi();
  render();
});

// Form thêm/sửa service (F3.4).
const svcSaveBtn = document.getElementById("svc-save");
if (svcSaveBtn) svcSaveBtn.onclick = saveSvc;
const svcSuggestBtn = document.getElementById("svc-suggest-port");
if (svcSuggestBtn) svcSuggestBtn.onclick = suggestPort;
const svcCloseBtn = document.getElementById("svc-close");
if (svcCloseBtn) svcCloseBtn.onclick = closeModal;

// Modal lịch sử (F3.3).
const historyCloseBtn = document.getElementById("history-close");
if (historyCloseBtn) historyCloseBtn.onclick = closeModal;

// Modal chạy ngầm (v0.7.0) — các nút hành động đi qua listener data-act.
const agentCloseBtn = document.getElementById("agent-close");
if (agentCloseBtn) agentCloseBtn.onclick = closeModal;

// Panel mirror (F2.1) — nút riêng của panel.
const mirrorRefreshBtn = document.getElementById("mirror-refresh");
if (mirrorRefreshBtn) mirrorRefreshBtn.onclick = openMirror;
const mirrorCloseBtn = document.getElementById("mirror-close");
if (mirrorCloseBtn) mirrorCloseBtn.onclick = closeMirror;

// Phím tắt: Cmd/Ctrl+R quét lại, Cmd/Ctrl+F / Cmd/Ctrl+K nhảy vào ô tìm kiếm,
// Escape đóng modal/panel. (Menu native cũng bắt Cmd+R/Cmd+F — khi menu xử lý
// thì keydown không tới được webview; handler này là đường dự phòng + mock.)
document.addEventListener("keydown", (ev) => {
  if (ev.key === "Escape") {
    closeModal();
    closeMirror();
    return;
  }
  if (!(ev.metaKey || ev.ctrlKey)) return;
  // Khi app thật (Wails): menu native đã giữ Cmd+R/Cmd+F — để nó xử lý, còn
  // đây preventDefault + tự refresh sẽ phóng ĐÔI request quét mỗi lần bấm.
  // Plan F4.1 chốt: Cmd+R/Cmd+F của menu native, FE chỉ phụ trách Cmd+K;
  // đường dự phòng dưới đây chỉ chạy ở chế độ mock (không có menu native).
  if (API) return;
  const k = (ev.key || "").toLowerCase();
  if (k === "r") {
    ev.preventDefault();
    refresh();
  } else if (k === "k") {
    ev.preventDefault();
    focusSearch();
  }
});

// Chỉ đóng khi cả mousedown lẫn mouseup đều trên nền overlay. Kéo chuột bôi
// đen log rồi thả ra ngoài modal thì target của click là overlay (tổ tiên
// chung) → không có guard này modal đóng giữa lúc đang chọn chữ.
let overlayPressOnBg = false;
document.getElementById("overlay").addEventListener("mousedown", (ev) => {
  overlayPressOnBg = ev.target.id === "overlay";
});
document.getElementById("overlay").addEventListener("click", (ev) => {
  const onBg = overlayPressOnBg && ev.target.id === "overlay";
  overlayPressOnBg = false;
  if (onBg && modalBusy) closeModal();
});

// Sự kiện từ menu ứng dụng native (main.go emit "menu:action") — guard khi
// window.runtime không có (chế độ mock / mở file ngoài Wails).
try {
  if (window.runtime && typeof window.runtime.EventsOn === "function") {
    window.runtime.EventsOn("menu:action", (name) => {
      if (name === "refresh") refresh();
      else if (name === "search") focusSearch();
      else if (name === "start-all" || name === "stop-all") refresh(); // backend đã chạy xong
      else if (name === "agent") openAgentModal(); // menu Chạy ngầm → Trạng thái… / sau khi gỡ
    });
  }
} catch (e) {
  /* bỏ qua */
}

(async function init() {
  // F4.3: đọc trạng thái giao diện đã lưu rồi áp lên các ô điều khiển.
  try {
    uiState = uiLogic.parseUiState(localStorage.getItem(UI_KEY));
  } catch (e) {
    uiState = uiLogic.parseUiState(null);
  }
  document.getElementById("search").value = uiState.search || "";
  document.getElementById("filter-state").value = uiState.filterState || "all";
  document.getElementById("filter-dirty").checked = uiState.filterDirty === true;

  document.getElementById("ver").textContent = API ? await API.Version() : "mock";
  document.getElementById("mockbar").classList.toggle("hidden", !!API);
  // v0.7.0: lần trước app tắt bất thường (kill -9 / signal) → banner giải thích.
  // Lỗi lấy LastExit không được chặn phần còn lại của init.
  if (API && typeof API.LastExit === "function") {
    try {
      showExitBanner(await API.LastExit());
    } catch (e) {
      /* không có banner cũng không sao */
    }
  }
  await refresh();
  schedulePoll();
  // Ticker 1s cho phần "đã chạy" — không cần quét lại toàn bộ, không gọi health.
  setInterval(updateUptime, 1000);
})();
