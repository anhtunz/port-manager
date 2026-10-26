// app.logic.js — logic thuần của UI (F1.2, F4.3): parse/lưu trạng thái giao
// diện, tìm kiếm, lọc theo trạng thái. KHÔNG đụng DOM, KHÔNG gọi API.
//
// index.html nạp file này TRƯỚC app.js → app.js lấy qua window.uiLogic.
// Có guard module.exports để chạy được bằng `node --test frontend/test/`
// (Node không có window).
(function (global) {
  "use strict";

  // parseUiState: đọc chuỗi JSON từ localStorage → trạng thái hợp lệ. Bất kỳ
  // lỗi/thiếu field nào → về mặc định, không bao giờ ném exception.
  function parseUiState(raw) {
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

  // saveUiState: trạng thái → chuỗi JSON cho localStorage (chỉ lấy field hợp lệ).
  function saveUiState(s) {
    return JSON.stringify({
      search: typeof s.search === "string" ? s.search : "",
      filterState: s.filterState || "all",
      filterDirty: s.filterDirty === true,
      collapsedGroups: Array.isArray(s.collapsedGroups) ? s.collapsedGroups : [],
    });
  }

  // matchSearch: q rỗng → khớp tất cả; nhiều từ khóa (ngăn cách khoảng trắng)
  // phải khớp TẤT CẢ (AND) trong: id, tên, group, số cổng, lệnh start, branch,
  // command.
  function matchSearch(s, q, groupName) {
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
  function statusMatches(st, filter) {
    if (filter === "all") return true;
    if (filter === "running") return st === "running" || st === "partial";
    return st === filter;
  }

  // filterServices: trả cấu trúc phục vụ render — giữ NGUYÊN mọi service của
  // group, gắn cờ visible cho từng dòng (dòng ẩn được gắn class hidden ngay lúc
  // dựng HTML nên re-render 3s không làm mất bộ lọc). Số liệu tổng (total/up)
  // tính trên TOÀN BỘ group, không tính trên phần đã lọc.
  function filterServices(res, ui) {
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
          matchSearch(s, q, g.name) &&
            statusMatches(s.status, fs) &&
            (!dirtyOnly || (s.repo && s.repo.dirty > 0))
        );
        if (visible) shown += 1;
        return { svc: s, visible };
      });
      return { name: g.name, total: rows.length, up, shown, rows };
    });
  }

  // logLineLevel: đoán mức độ 1 dòng log → "error" | "warn" | "". Heuristic
  // cho các dạng hay gặp trong repo: zerolog/zap JSON ({"level":"error"}),
  // chữ ERROR/FATAL/PANIC/[error]/err!, lỗi mạng (connection refused,
  // EADDRINUSE...). Ranh giới [^a-z] để "errors"/"stderr" không khớp "error"/"err".
  const LOG_ERR_RE = new RegExp(
    [
      '"level"\\s*:\\s*"(error|fatal|panic|dpanic)"',
      "(^|[^a-z])(error|err|fatal|panic|exception|traceback|critical)([^a-z]|$)",
      "\\b(econnrefused|eaddrinuse|enotfound|etimedout|epipe)\\b",
      "connection refused|address already in use|segmentation fault|unhandled rejection|uncaught",
    ].join("|"),
    "i"
  );
  const LOG_WARN_RE = /"level"\s*:\s*"warn(ing)?"|(^|[^a-z])(warn|warning|deprecated)([^a-z]|$)/i;

  function logLineLevel(line) {
    const s = String(line || "");
    if (LOG_ERR_RE.test(s)) return "error";
    if (LOG_WARN_RE.test(s)) return "warn";
    return "";
  }

  // filterLogLines: tách nội dung log thành dòng + lọc. opts.query: nhiều từ
  // khóa cách nhau bởi khoảng trắng, phải khớp TẤT CẢ (AND, không phân biệt
  // hoa/thường) — dùng để tìm mã lỗi (500, ECONNREFUSED...). opts.errorsOnly:
  // chỉ giữ dòng level "error". Trả { lines: [{text, level, no}], total, shown };
  // `no` là số thứ tự dòng trong nội dung gốc (đếm từ 1) để biết dòng ở đâu
  // khi đang lọc. Dòng rỗng cuối (tail log kết thúc bằng "\n") bị bỏ.
  function filterLogLines(content, opts) {
    const q = String((opts && opts.query) || "").trim().toLowerCase();
    const errorsOnly = !!(opts && opts.errorsOnly);
    const toks = q ? q.split(/\s+/) : [];
    const all = String(content || "").split("\n");
    if (all.length && all[all.length - 1] === "") all.pop();
    const lines = [];
    all.forEach((text, i) => {
      const level = logLineLevel(text);
      if (errorsOnly && level !== "error") return;
      if (toks.length) {
        const low = text.toLowerCase();
        if (!toks.every((t) => low.indexOf(t) >= 0)) return;
      }
      lines.push({ text, level, no: i + 1 });
    });
    return { lines, total: all.length, shown: lines.length };
  }

  // ---------------------------------------------------------------------------
  // Chạy ngầm (v0.7.0): hàm thuần cho banner lý do thoát + modal LaunchAgent.
  // Nhận đúng JSON của App.LastExit() / App.AgentStatus() (xem internal/api).
  // ---------------------------------------------------------------------------

  // fmtTime: RFC3339 → "dd/mm/yyyy HH:MM:SS" theo giờ máy. Chuỗi không parse
  // được trả nguyên văn (không ném) để UI vẫn hiện được gì đó.
  function fmtTime(at) {
    if (!at) return "";
    const d = new Date(at);
    if (isNaN(d.getTime())) return String(at);
    const p = (n) => String(n).padStart(2, "0");
    return p(d.getDate()) + "/" + p(d.getMonth() + 1) + "/" + d.getFullYear() +
      " " + p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
  }

  // exitBanner: thông tin lần thoát trước → nội dung banner, hoặc null nếu
  // không có gì đáng báo (lần đầu chạy / lần trước thoát sạch). Chỉ "abrupt"
  // (chết không kịp ghi gì — kill -9, crash, mất điện) và "signal" (SIGTERM/
  // SIGINT/SIGHUP) mới hiện, vì đó là lúc người dùng thắc mắc "sao app tắt".
  function exitBanner(info) {
    if (!info || !info.kind || info.kind === "none" || info.kind === "quit") return null;
    const when = info.at ? " lúc " + fmtTime(info.at) : "";
    const pid = info.pid ? " (pid " + info.pid + ")" : "";
    if (info.kind === "signal") {
      return {
        kind: "signal",
        title: "Lần trước Port Manager bị dừng bởi tín hiệu " + (info.signal || "?") + when + ".",
        text: info.detail || "",
      };
    }
    return {
      kind: "abrupt",
      title: "Lần trước Port Manager bị tắt đột ngột" + pid + when + ".",
      text: info.detail || "App không kịp ghi lý do — thường do kill -9 từ bên ngoài hoặc crash.",
    };
  }

  // agentSummary: một dòng tóm tắt + tone để tô màu: "ok" đang chạy ngầm,
  // "warn" đã cài nhưng launchd chưa giữ process, "off" chưa cài.
  function agentSummary(st) {
    if (!st) return { tone: "off", label: "Không lấy được trạng thái." };
    if (!st.installed) return { tone: "off", label: "Chưa cài chạy ngầm — app chỉ chạy khi bạn tự mở." };
    if (st.loaded && st.pid > 0) {
      const me = st.thisIsAgent ? " — chính là tiến trình này" : " — KHÔNG phải tiến trình này";
      return { tone: "ok", label: "Đã cài, launchd đang giữ process pid " + st.pid + me + "." };
    }
    if (st.loaded) return { tone: "warn", label: "Đã cài và đã nạp vào launchd nhưng chưa có process (đang khởi động lại?)." };
    return { tone: "warn", label: "Đã cài plist nhưng chưa nạp vào launchd — bấm Cài lại hoặc đăng nhập lại." };
  }

  // agentRows: các cặp nhãn/giá trị hiện trong modal (bỏ dòng rỗng).
  function agentRows(st) {
    if (!st) return [];
    const rows = [
      { k: "Label", v: st.label },
      { k: "Tiến trình này", v: st.thisIsAgent ? "do launchd quản lý (chạy ngầm)" : "mở tay (make run / Dock)" },
      { k: "Plist", v: st.plistPath },
      { k: "Binary", v: st.binPath },
      { k: "Log stdout", v: st.logOutPath },
      { k: "Log stderr", v: st.logErrPath },
    ];
    if (!st.canInstall && st.cannotInstallReason) {
      rows.push({ k: "Không cài được", v: st.cannotInstallReason });
    }
    return rows.filter((r) => r.v);
  }

  // installLabel: nhãn nút cài theo trạng thái — đã cài thì là "cài lại" (bàn
  // giao sang bản binary mới sau khi rebuild).
  function installLabel(st) {
    if (st && st.installed) return "Cài lại (bàn giao)";
    return "Cài & chạy ngầm";
  }

  const uiLogic = {
    parseUiState: parseUiState,
    saveUiState: saveUiState,
    matchSearch: matchSearch,
    statusMatches: statusMatches,
    filterServices: filterServices,
    logLineLevel: logLineLevel,
    filterLogLines: filterLogLines,
    fmtTime: fmtTime,
    exitBanner: exitBanner,
    agentSummary: agentSummary,
    agentRows: agentRows,
    installLabel: installLabel,
  };

  global.uiLogic = uiLogic;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = uiLogic;
  }
})(typeof window !== "undefined" ? window : globalThis);
