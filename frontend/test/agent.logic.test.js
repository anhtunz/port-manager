// Test cho phần chạy ngầm (v0.7.0) trong app.logic.js: banner lý do thoát +
// trạng thái LaunchAgent. Chạy: node --test frontend/test/
const test = require("node:test");
const assert = require("node:assert");
const logic = require("../dist/app.logic.js");

// --- exitBanner -------------------------------------------------------------

test("exitBanner: none/quit/rỗng → null (không có gì để báo)", () => {
  assert.strictEqual(logic.exitBanner(null), null);
  assert.strictEqual(logic.exitBanner(undefined), null);
  assert.strictEqual(logic.exitBanner({}), null);
  assert.strictEqual(logic.exitBanner({ kind: "none" }), null);
  assert.strictEqual(logic.exitBanner({ kind: "quit", at: "2026-10-25T09:00:00+07:00" }), null);
});

test("exitBanner: abrupt → tiêu đề 'tắt đột ngột' kèm pid + detail", () => {
  const b = logic.exitBanner({
    kind: "abrupt",
    at: "2026-10-25T09:12:01+07:00",
    pid: 4242,
    detail: "có thể bị kill -9",
  });
  assert.strictEqual(b.kind, "abrupt");
  assert.match(b.title, /tắt đột ngột/);
  assert.match(b.title, /pid 4242/);
  assert.match(b.title, /lúc \d{2}\/\d{2}\/2026 \d{2}:\d{2}:\d{2}/);
  assert.strictEqual(b.text, "có thể bị kill -9");
});

test("exitBanner: abrupt không có detail → có câu gợi ý mặc định", () => {
  const b = logic.exitBanner({ kind: "abrupt" });
  assert.match(b.title, /tắt đột ngột\.$/);
  assert.match(b.text, /kill -9/);
});

test("exitBanner: signal → nêu tên tín hiệu", () => {
  const b = logic.exitBanner({ kind: "signal", signal: "SIGTERM", detail: "launchctl bootout?" });
  assert.strictEqual(b.kind, "signal");
  assert.match(b.title, /tín hiệu SIGTERM/);
  assert.strictEqual(b.text, "launchctl bootout?");
  assert.match(logic.exitBanner({ kind: "signal" }).title, /tín hiệu \?/);
});

// --- fmtTime ---------------------------------------------------------------

test("fmtTime: RFC3339 → dd/mm/yyyy HH:MM:SS; chuỗi lạ trả nguyên", () => {
  assert.strictEqual(logic.fmtTime(""), "");
  assert.strictEqual(logic.fmtTime(null), "");
  assert.strictEqual(logic.fmtTime("không phải ngày"), "không phải ngày");
  const s = logic.fmtTime(new Date(2026, 9, 25, 9, 5, 7).toISOString());
  assert.strictEqual(s, "25/10/2026 09:05:07");
});

// --- agentSummary / agentRows / installLabel --------------------------------

const base = {
  label: "vn.smatec.port-manager",
  plistPath: "/u/Library/LaunchAgents/vn.smatec.port-manager.plist",
  binPath: "/u/.port-manager/bin/port-manager",
  logOutPath: "/u/.port-manager/logs/agent.out.log",
  logErrPath: "/u/.port-manager/logs/agent.err.log",
  installed: false,
  loaded: false,
  pid: 0,
  thisIsAgent: false,
  canInstall: true,
  cannotInstallReason: "",
};

test("agentSummary: chưa cài → off", () => {
  const s = logic.agentSummary(base);
  assert.strictEqual(s.tone, "off");
  assert.match(s.label, /Chưa cài/);
  assert.strictEqual(logic.agentSummary(null).tone, "off");
});

test("agentSummary: đã cài + launchd giữ pid → ok, phân biệt tiến trình này", () => {
  const me = logic.agentSummary({ ...base, installed: true, loaded: true, pid: 77, thisIsAgent: true });
  assert.strictEqual(me.tone, "ok");
  assert.match(me.label, /pid 77/);
  assert.match(me.label, /chính là tiến trình này/);
  const other = logic.agentSummary({ ...base, installed: true, loaded: true, pid: 78 });
  assert.match(other.label, /KHÔNG phải tiến trình này/);
});

test("agentSummary: đã cài nhưng chưa nạp / chưa có process → warn", () => {
  assert.strictEqual(logic.agentSummary({ ...base, installed: true }).tone, "warn");
  assert.match(logic.agentSummary({ ...base, installed: true }).label, /chưa nạp/);
  assert.strictEqual(logic.agentSummary({ ...base, installed: true, loaded: true }).tone, "warn");
});

test("agentRows: bỏ dòng rỗng, thêm lý do khi không cài được", () => {
  const rows = logic.agentRows({ ...base, logOutPath: "", logErrPath: "" });
  const keys = rows.map((r) => r.k);
  assert.deepStrictEqual(keys, ["Label", "Tiến trình này", "Plist", "Binary"]);
  assert.match(rows[1].v, /mở tay/);

  const noInst = logic.agentRows({ ...base, canInstall: false, cannotInstallReason: "đang chạy từ go run" });
  assert.strictEqual(noInst[noInst.length - 1].k, "Không cài được");
  assert.strictEqual(noInst[noInst.length - 1].v, "đang chạy từ go run");

  assert.match(logic.agentRows({ ...base, thisIsAgent: true })[1].v, /launchd/);
  assert.deepStrictEqual(logic.agentRows(null), []);
});

test("installLabel: đã cài → cài lại (bàn giao)", () => {
  assert.strictEqual(logic.installLabel(base), "Cài & chạy ngầm");
  assert.strictEqual(logic.installLabel(null), "Cài & chạy ngầm");
  assert.strictEqual(logic.installLabel({ ...base, installed: true }), "Cài lại (bàn giao)");
});
