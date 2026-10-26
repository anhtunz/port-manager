// Test cho app.logic.js — logic thuần của UI (F1.2, F4.3).
// Chạy: node --test frontend/test/
const test = require("node:test");
const assert = require("node:assert");
const logic = require("../dist/app.logic.js");

test("parseUiState: rỗng/hỏng → trạng thái mặc định", () => {
  assert.deepStrictEqual(logic.parseUiState(null), {
    search: "", filterState: "all", filterDirty: false, collapsedGroups: [],
  });
  assert.deepStrictEqual(logic.parseUiState(""), logic.parseUiState(null));
  assert.deepStrictEqual(logic.parseUiState("{json sai"), logic.parseUiState(null));
  assert.deepStrictEqual(logic.parseUiState("123"), logic.parseUiState(null));
});

test("parseUiState: đọc đúng field hợp lệ, bỏ field sai kiểu", () => {
  const raw = JSON.stringify({
    search: "auth",
    filterState: "running",
    filterDirty: true,
    collapsedGroups: ["a", 42, "b"],
    extra: "bỏ qua",
  });
  const st = logic.parseUiState(raw);
  assert.strictEqual(st.search, "auth");
  assert.strictEqual(st.filterState, "running");
  assert.strictEqual(st.filterDirty, true);
  assert.deepStrictEqual(st.collapsedGroups, ["a", "b"]);
});

test("parseUiState: filterState lạ → về 'all'", () => {
  const st = logic.parseUiState(JSON.stringify({ filterState: "hack" }));
  assert.strictEqual(st.filterState, "all");
});

test("saveUiState → parseUiState là vòng tròn (round-trip)", () => {
  const st = {
    search: "port 3000",
    filterState: "stuck",
    filterDirty: true,
    collapsedGroups: ["g1", "g2"],
  };
  assert.deepStrictEqual(logic.parseUiState(logic.saveUiState(st)), st);
});

test("matchSearch: q rỗng khớp tất cả", () => {
  const s = { id: "auth-be", name: "Auth BE", ports: [{ port: 8080 }] };
  assert.strictEqual(logic.matchSearch(s, ""), true);
  assert.strictEqual(logic.matchSearch(s, "   "), true);
  assert.strictEqual(logic.matchSearch(s, null), true);
});

test("matchSearch: khớp theo id/tên/cổng/group/branch", () => {
  const s = {
    id: "auth-be",
    name: "Auth BE",
    startCmd: "go run .",
    command: "/usr/local/go/bin/go run .",
    repo: { branch: "feat/login" },
    ports: [{ port: 8080 }, { port: 9090 }],
  };
  assert.strictEqual(logic.matchSearch(s, "auth", "Nhân sự"), true);
  assert.strictEqual(logic.matchSearch(s, "8080"), true);
  assert.strictEqual(logic.matchSearch(s, "9090 8080"), true); // AND nhiều số cổng
  assert.strictEqual(logic.matchSearch(s, "feat/login"), true);
  assert.strictEqual(logic.matchSearch(s, "go run", "Nhân sự"), true);
  assert.strictEqual(logic.matchSearch(s, "nhân sự", "Nhân sự"), true); // group
});

test("matchSearch: nhiều từ khóa là AND — thiếu 1 là không khớp", () => {
  const s = { id: "auth-be", ports: [{ port: 8080 }] };
  assert.strictEqual(logic.matchSearch(s, "auth 9999"), false);
});

test("matchSearch: không phân biệt hoa thường", () => {
  const s = { id: "Auth-BE" };
  assert.strictEqual(logic.matchSearch(s, "AUTH"), true);
});

test("statusMatches: 'running' gộp cả 'partial'", () => {
  assert.strictEqual(logic.statusMatches("running", "running"), true);
  assert.strictEqual(logic.statusMatches("partial", "running"), true);
  assert.strictEqual(logic.statusMatches("stopped", "running"), false);
  assert.strictEqual(logic.statusMatches("starting", "starting"), true);
  assert.strictEqual(logic.statusMatches("stuck", "stuck"), true);
  assert.strictEqual(logic.statusMatches("bất cứ gì", "all"), true);
});

function mockRes() {
  return {
    groups: [
      {
        name: "Nhân sự",
        services: [
          { id: "hr-be", name: "HR BE", status: "running", ports: [{ port: 3000 }], repo: { dirty: 2 } },
          { id: "hr-fe", name: "HR FE", status: "stopped", ports: [{ port: 5173 }], repo: { dirty: 0 } },
        ],
      },
      {
        name: "Cổng vào",
        services: [
          { id: "gw", name: "Gateway", status: "partial", ports: [{ port: 8000 }] },
        ],
      },
    ],
  };
}

test("filterServices: không lọc → giữ nguyên, có số liệu tổng", () => {
  const out = logic.filterServices(mockRes(), { search: "", filterState: "all", filterDirty: false });
  assert.strictEqual(out.length, 2);
  assert.strictEqual(out[0].name, "Nhân sự");
  assert.strictEqual(out[0].total, 2);
  assert.strictEqual(out[0].up, 1); // chỉ hr-be running
  assert.strictEqual(out[0].shown, 2);
  assert.strictEqual(out[1].up, 1); // partial cũng tính là đang chạy
});

test("filterServices: lọc theo trạng thái — total/up vẫn tính trên toàn group", () => {
  const out = logic.filterServices(mockRes(), { search: "", filterState: "stopped", filterDirty: false });
  assert.strictEqual(out[0].total, 2);
  assert.strictEqual(out[0].shown, 1);
  assert.strictEqual(out[0].rows[0].visible, false);
  assert.strictEqual(out[0].rows[1].visible, true);
});

test("filterServices: lọc dirty chỉ tính repo.dirty > 0", () => {
  const out = logic.filterServices(mockRes(), { search: "", filterState: "all", filterDirty: true });
  assert.strictEqual(out[0].shown, 1);
  assert.strictEqual(out[0].rows[0].visible, true);
  assert.strictEqual(out[0].rows[1].visible, false);
  // Service không có repo → không khớp dirty
  assert.strictEqual(out[1].rows[0].visible, false);
});

test("filterServices: tìm kiếm theo cổng + group name", () => {
  const out = logic.filterServices(mockRes(), { search: "5173", filterState: "all", filterDirty: false });
  assert.strictEqual(out[0].shown, 1);
  assert.strictEqual(out[0].rows[1].visible, true);
  const out2 = logic.filterServices(mockRes(), { search: "nhân sự", filterState: "all", filterDirty: false });
  assert.strictEqual(out2[0].shown, 2);
  assert.strictEqual(out2[1].shown, 0);
});

test("filterServices: res rỗng/lệch → trả mảng rỗng, không ném lỗi", () => {
  assert.deepStrictEqual(logic.filterServices(null, {}), []);
  assert.deepStrictEqual(logic.filterServices({}, null), []);
});

// --- Log: nhận diện dòng lỗi + lọc ---------------------------------------

test("logLineLevel: nhận ra các dạng lỗi hay gặp", () => {
  const errs = [
    `{"level":"error","msg":"connect tcp 127.0.0.1:5672: connection refused"}`,
    `{"level":"fatal","msg":"db down"}`,
    "ERROR 500 GET /api/orders: context deadline exceeded",
    "2026/10/25 09:12:01 [error] upstream timed out",
    "npm ERR! code ELIFECYCLE",
    "panic: runtime error: invalid memory address",
    "Error: listen EADDRINUSE: address already in use :::3005",
    "Traceback (most recent call last):",
    "err: something broke",
  ];
  errs.forEach((l) => assert.strictEqual(logic.logLineLevel(l), "error", l));
});

test("logLineLevel: warn + dòng thường, không khớp nhầm errors/stderr", () => {
  assert.strictEqual(logic.logLineLevel(`{"level":"warn","msg":"slow query"}`), "warn");
  assert.strictEqual(logic.logLineLevel("WARN  - [deprecated] option foo"), "warn");
  assert.strictEqual(logic.logLineLevel("info  - webpack compiled successfully"), "");
  assert.strictEqual(logic.logLineLevel("compiled with 0 errors"), "");
  assert.strictEqual(logic.logLineLevel("redirect stderr to file"), "");
  assert.strictEqual(logic.logLineLevel(""), "");
  assert.strictEqual(logic.logLineLevel(null), "");
});

const LOG = [
  "info  - start",
  `{"level":"error","msg":"connect tcp 127.0.0.1:5672: connection refused"}`,
  "warn  - deprecated",
  "ERROR 500 GET /api/orders",
  "info  - done",
].join("\n") + "\n";

test("filterLogLines: không lọc → giữ nguyên, bỏ dòng rỗng cuối, đánh số dòng", () => {
  const r = logic.filterLogLines(LOG, {});
  assert.strictEqual(r.total, 5);
  assert.strictEqual(r.shown, 5);
  assert.deepStrictEqual(r.lines.map((l) => l.no), [1, 2, 3, 4, 5]);
  assert.deepStrictEqual(r.lines.map((l) => l.level), ["", "error", "warn", "error", ""]);
});

test("filterLogLines: errorsOnly chỉ giữ level error, giữ số dòng gốc", () => {
  const r = logic.filterLogLines(LOG, { errorsOnly: true });
  assert.strictEqual(r.total, 5);
  assert.strictEqual(r.shown, 2);
  assert.deepStrictEqual(r.lines.map((l) => l.no), [2, 4]);
});

test("filterLogLines: query nhiều từ = AND, không phân biệt hoa/thường, kết hợp errorsOnly", () => {
  assert.strictEqual(logic.filterLogLines(LOG, { query: "500" }).shown, 1);
  assert.strictEqual(logic.filterLogLines(LOG, { query: "REFUSED tcp" }).shown, 1);
  assert.strictEqual(logic.filterLogLines(LOG, { query: "info" }).shown, 2);
  assert.strictEqual(logic.filterLogLines(LOG, { query: "info", errorsOnly: true }).shown, 0);
  assert.strictEqual(logic.filterLogLines(LOG, { query: "   " }).shown, 5);
});

test("filterLogLines: nội dung rỗng/null → total 0, không ném lỗi", () => {
  assert.deepStrictEqual(logic.filterLogLines("", {}), { lines: [], total: 0, shown: 0 });
  assert.deepStrictEqual(logic.filterLogLines(null, null), { lines: [], total: 0, shown: 0 });
});
