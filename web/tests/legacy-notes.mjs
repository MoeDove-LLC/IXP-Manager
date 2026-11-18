// Real UI against a synthetic local database. SMTP remains disabled.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const exec = promisify(execFile), repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp(path.join(tmpdir(), "legacy-notes-ui-")), input = path.join(dir, "source.json"), archivePath = path.join(dir, "note-sources.json");
const largeID = "9007199254740993", password = "Legacy-note-browser-fixture-123";
const tables = {
  cust_notes: Array.from({ length: 25 }, (_, i) => ({ id: i + 1, customer_id: 999, title: `旧备注 ${i + 1}`, note: i === 0 ? "  原正文\r\n<img src=x onerror=window.__legacyNoteUnsafe=true>\n" : `旧正文 ${i + 1}`, private: i === 0 ? "unknown" : 1, created: "2015-01-01 12:00:00", updated: "2016-02-03" })),
  user: [
    { id: largeID, name: "旧员工大编号", email: "old-address@example.test", password: "NEVER-IMPORT-OLD-PASSWORD", prefs: { notes: { global_notifs: "all", customer_watching: { "999": "2010-01-01" }, note_watching: { "1": "2010-01-02" }, last_read: { "999": "2010-01-03" }, read_upto: "2010-01-04" }, private_key: "NEVER-IMPORT-PRIVATE-PREF" } },
    { id: 7, name: "旧员工七", prefs: { notes: { global_notifs: "default" } } },
    { id: 9, name: "旧员工九", prefs: {} },
  ],
  user_pref: [{ id: 1, user_id: largeID, attribute: "customer-notes.notify", value: "all", ix: "original-index", op: "=", expire: "2019-01-01" }],
};
let child, browser, stderr = "";
try {
  await writeFile(input, JSON.stringify({ tables }));
  await exec(process.env.TEST_MIGRATE_BIN || path.join(repo, ".cache/legacy-notes-ui/migrate"), ["--input", input, "--note-archive", "--source-timezone", "UTC", "--output", archivePath]);
  const sourceBytes = await readFile(archivePath);
  assert.ok(sourceBytes.includes(Buffer.from(largeID))); assert.ok(!sourceBytes.includes(Buffer.from("NEVER-IMPORT")));
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve)); const port = listener.address().port; await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN || path.join(repo, ".cache/legacy-notes-ui/server"), ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web/dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "legacy-note-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(`fixture service stopped: ${stderr}`); if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break; if (i > 100) throw new Error("fixture service startup timeout"); await new Promise(resolve => setTimeout(resolve, 100)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await admin.newPage(), errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "legacy-note-setup", name: "Migration QA", email: "admin@example.test", password, ixpName: "Legacy Notes QA", asn: 64500 } }); assert.equal(setup.status(), 200, await setup.text());
  const call = async (ctx, route, data, method = data ? "POST" : "GET") => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + route, { method, headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(data ? { data } : {}) });
    assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`); return response.json();
  };
  const member = await call(admin, "/resources/members", { name: "当前合成成员甲", asn: 64501, status: "active" });
  const note = await call(admin, "/resources/notes", { name: "当前维护备注", memberId: member.id, body: "CURRENT NOTE BODY", visibility: "internal" });
  const viewer = await call(admin, "/resources/users", { name: "当前只读员工", email: "viewer@example.test", password, role: "viewer" });
  await call(admin, "/resources/users", { name: "普通成员", email: "member@example.test", password, role: "member", memberId: member.id });
  const operator = await call(admin, "/resources/users", { name: "其他员工", email: "other@example.test", password, role: "operator" });
  const login = async email => { const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } }); const r = await ctx.request.post(base + "/api/login", { data: { email, password } }); assert.equal(r.status(), 200, await r.text()); return ctx; };
  await page.goto(base + "/#settings"); await page.getByRole("heading", { name: "旧备注与订阅来源归档", exact: true }).waitFor();
  await page.getByLabel("备注来源 JSON 包", { exact: true }).setInputFiles(archivePath);
  const importing = page.waitForResponse(r => r.url().endsWith("/api/import/note-archive")); await page.getByRole("button", { name: "导入备注来源包", exact: true }).click(); const imported = await importing; assert.equal(imported.status(), 200);
  const result = { batch: (await call(admin, "/notes/legacy")).batches[0] };
  await page.getByText(/已归档 25 条备注、3 个旧账号和 4 条订阅来源/).waitFor();
  assert.equal((await call(admin, "/resources/notes")).items.length, 1); assert.equal((await call(admin, "/notifications")).items.length, 0);
  await page.getByRole("button", { name: "查看旧备注迁移", exact: true }).click(); await page.getByRole("heading", { name: "旧备注与订阅迁移", exact: true }).waitFor();
  await page.getByRole("button", { name: "来源归档", exact: true }).click(); await page.getByRole("article").first().waitFor(); assert.equal(await page.getByRole("article").count(), 20);
  await page.getByRole("button", { name: "下一页", exact: true }).click(); assert.equal(await page.getByRole("article").count(), 5);
  await page.getByRole("button", { name: "上一页", exact: true }).click(); await page.getByRole("article").filter({ has: page.getByText("旧备注 1", { exact: true }) }).getByRole("button", { name: "查看来源详情", exact: true }).click();
  const details = page.getByRole("dialog"); await details.getByText(/原正文/).waitFor(); assert.equal(await page.evaluate(() => window.__legacyNoteUnsafe), undefined); await details.getByRole("button", { name: "关闭", exact: true }).click();
  const download = page.waitForEvent("download"); await page.getByRole("button", { name: "下载规范化归档", exact: true }).click(); const downloaded = await readFile(await (await download).path(), "utf8"); assert.ok(downloaded.includes(largeID)); assert.ok(!downloaded.includes("NEVER-IMPORT"));
  await page.setViewportSize({ width: 390, height: 844 }); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "archive overflowed mobile viewport"); assert.ok((await page.getByLabel("来源批次", { exact: true }).boundingBox()).width >= 200, "archive batch selector collapsed on mobile"); if (process.env.LEGACY_NOTE_ARCHIVE_SCREENSHOT) await page.screenshot({ path: process.env.LEGACY_NOTE_ARCHIVE_SCREENSHOT, fullPage: true, animations: "disabled" }); await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("button", { name: "账号映射", exact: true }).click(); await page.getByLabel("当前批次的旧账号", { exact: true }).waitFor();
  assert.equal(await page.getByLabel("当前员工账号", { exact: true }).inputValue(), "", "old email/name preselected a target user");
  await page.getByLabel("当前批次的旧账号", { exact: true }).selectOption(`legacy-note-user-${largeID}`);
  await page.getByLabel("当前员工账号", { exact: true }).selectOption({ label: "当前只读员工 · viewer@example.test · viewer" });
  const mappingRequest = page.waitForRequest(r => r.url().endsWith("/api/notes/legacy/mappings") && r.method() === "POST");
  await page.getByRole("button", { name: "创建待确认账号映射", exact: true }).click(); assert.equal((await mappingRequest).postDataJSON().sourceUserId, largeID, "source int64 was rounded");
  await page.getByText("账号映射已创建，目标员工需本人审阅并明确接受。当前订阅未改变。", { exact: true }).waitFor();
  const employee = await login("viewer@example.test"), employeePage = await employee.newPage(); employeePage.on("pageerror", error => errors.push(error.message));
  await employeePage.goto(base + "/#security?section=legacy-notes"); const card = employeePage.getByRole("region", { name: "本人待确认迁移" });
  await card.getByLabel("本人选择的通知方式", { exact: true }).waitFor(); assert.equal(await card.getByLabel("本人选择的通知方式", { exact: true }).inputValue(), "none");
  assert.equal(await employeePage.getByRole("button", { name: "账号映射", exact: true }).count(), 0);
  await card.getByText("查看本人规范化来源记录", { exact: true }).click(); assert.ok((await card.locator("pre").innerText()).includes(largeID));
  const beforeUnread = (await call(employee, "/account/note-summary")).unreadCount;
  await card.getByLabel("本人选择的通知方式", { exact: true }).selectOption("watched");
  await card.getByRole("button", { name: "当前合成成员甲 选择", exact: true }).click(); await card.getByRole("button", { name: "当前维护备注 当前合成成员甲 选择", exact: true }).click();
  await card.getByRole("button", { name: "预览本人迁移选择", exact: true }).click(); const review = employeePage.getByRole("dialog"); await review.getByRole("heading", { name: "本人确认订阅迁移", exact: true }).waitFor();
  await review.getByText("viewer@example.test", { exact: true }).waitFor(); assert.equal(await review.getByRole("button", { name: "本人确认接受", exact: true }).isDisabled(), true);
  const current = await call(employee, "/account/note-preferences"); await call(employee, "/account/note-preferences", { revision: current.revision, mode: "all", memberWatches: [], noteWatches: [] }, "PUT");
  await review.getByRole("checkbox").check(); const conflicted = employeePage.waitForResponse(r => r.url().endsWith("/accept")); await review.getByRole("button", { name: "本人确认接受", exact: true }).click(); assert.equal((await conflicted).status(), 409);
  await review.waitFor({ state: "hidden" }); assert.equal(await card.getByLabel("本人选择的通知方式", { exact: true }).inputValue(), "watched"); assert.equal(await card.getByRole("button", { name: "预览本人迁移选择", exact: true }).isDisabled(), true);
  await card.getByRole("button", { name: "重新读取，保留选择", exact: true }).click(); await card.getByRole("button", { name: "预览本人迁移选择", exact: true }).click(); await review.getByRole("heading", { name: "本人确认订阅迁移", exact: true }).waitFor();
  await review.getByRole("button", { name: "返回调整", exact: true }).click();
  await employeePage.clock.install({ time: new Date() }); await card.getByRole("button", { name: "预览本人迁移选择", exact: true }).click(); await review.waitFor(); await employeePage.clock.fastForward(301000); await review.waitFor({ state: "hidden" }); await employeePage.clock.setSystemTime(new Date());
  assert.equal(await card.getByLabel("本人选择的通知方式", { exact: true }).inputValue(), "watched"); await card.getByText(/审阅已过期/).waitFor();
  await card.getByRole("button", { name: "重新读取，保留选择", exact: true }).click(); await card.getByRole("button", { name: "预览本人迁移选择", exact: true }).click(); await review.waitFor();
  await review.getByRole("checkbox").check(); const accepted = employeePage.waitForResponse(r => r.url().endsWith("/accept")); await review.getByRole("button", { name: "本人确认接受", exact: true }).click(); assert.equal((await accepted).status(), 200);
  await card.getByText(/已接受本次选择/).waitFor(); const prefs = await call(employee, "/account/note-preferences"); assert.equal(prefs.mode, "watched"); assert.deepEqual(prefs.memberWatches.map(x => x.id), [member.id]); assert.deepEqual(prefs.noteWatches.map(x => x.id), [note.id]); assert.equal((await call(employee, "/account/note-summary")).unreadCount, beforeUnread); assert.equal((await call(admin, "/notifications")).items.length, 0);
  // Cancellation uses the version shown by the administrator, even if the
  // employee creates another review while the cancellation dialog is open.
  await page.getByLabel("当前批次的旧账号", { exact: true }).selectOption("legacy-note-user-7"); await page.getByRole("button", { name: "创建待确认账号映射", exact: true }).click();
  await page.getByLabel("账号映射 7", { exact: true }).getByRole("button", { name: "取消待确认映射", exact: true }).click(); const cancelDialog = page.getByRole("dialog"); await cancelDialog.waitFor();
  const second = (await call(employee, "/account/note-migrations")).items.find(x => x.mapping.sourceUserId === "7");
  await call(employee, `/account/note-migrations/${second.mapping.id}/review`, { revision: second.mapping.revision, preferenceRevision: prefs.revision, mode: "none", memberWatches: [], noteWatches: [] });
  const staleCancel = page.waitForResponse(r => r.url().endsWith("/cancel")); await cancelDialog.getByRole("button", { name: "确认取消映射", exact: true }).click(); assert.equal((await staleCancel).status(), 409); assert.equal(await cancelDialog.getByRole("button", { name: "确认取消映射", exact: true }).isDisabled(), true);
  await cancelDialog.getByRole("button", { name: "重新读取此映射", exact: true }).click(); await cancelDialog.getByRole("button", { name: "确认取消映射", exact: true }).click(); await cancelDialog.waitFor({ state: "hidden" });
  const third = (await call(admin, "/notes/legacy/mappings", { archiveChecksum: result.batch.checksum, sourceUserId: "9", targetUserId: viewer.id, targetUserCreatedAt: viewer.createdAt })).mapping;
  await employeePage.getByRole("button", { name: "刷新迁移工作区", exact: true }).click(); const thirdCard = employeePage.getByLabel("本人迁移 legacy-note-user-9", { exact: true }); await thirdCard.getByLabel("本人选择的通知方式", { exact: true }).selectOption("all"); await thirdCard.getByRole("button", { name: "预览本人迁移选择", exact: true }).click(); await review.waitFor();
  const freshMapping = (await call(admin, "/notes/legacy")).mappings.find(x => x.id === third.id); await call(admin, `/notes/legacy/mappings/${third.id}/cancel`, { revision: freshMapping.revision });
  await review.getByRole("checkbox").check(); const revoked = employeePage.waitForResponse(r => r.url().endsWith("/accept")); await review.getByRole("button", { name: "本人确认接受", exact: true }).click(); assert.equal((await revoked).status(), 409); await review.waitFor({ state: "hidden" }); assert.equal(await thirdCard.getByLabel("本人选择的通知方式", { exact: true }).inputValue(), "all");
  await thirdCard.getByRole("button", { name: "重新读取，保留选择", exact: true }).click(); await thirdCard.getByText(/此映射已不再待确认/).waitFor(); assert.equal(await thirdCard.getByRole("button", { name: "预览本人迁移选择", exact: true }).isDisabled(), true);
  await employeePage.setViewportSize({ width: 390, height: 844 }); assert.ok(await employeePage.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "migration confirmation overflows mobile viewport"); if (process.env.LEGACY_NOTE_ACCEPT_SCREENSHOT) await employeePage.screenshot({ path: process.env.LEGACY_NOTE_ACCEPT_SCREENSHOT, fullPage: true, animations: "disabled" });
  const ordinary = await login("member@example.test"), ordinaryPage = await ordinary.newPage(), requests = []; ordinaryPage.on("request", r => requests.push(new URL(r.url()).pathname)); await ordinaryPage.goto(base + "/#security?section=legacy-notes"); await ordinaryPage.getByRole("heading", { name: "账号安全", exact: true }).waitFor(); assert.equal(await ordinaryPage.getByRole("button", { name: "旧备注与订阅迁移", exact: true }).count(), 0); assert.ok(!requests.some(p => p.includes("/notes/legacy") || p.includes("/note-migrations")));
  assert.equal((await ordinary.request.get(base + "/api/notes/legacy")).status(), 403); const other = await login(operator.email); assert.equal((await call(other, "/account/note-migrations")).items.length, 0);
  assert.deepEqual(errors, []); console.log("Legacy notes archive, exact source IDs, explicit mapping, self review/accept, CAS, expiry, revocation and mobile boundaries passed");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { child.kill("SIGTERM"); await new Promise(resolve => child.once("exit", resolve)); }
  await rm(dir, { recursive: true, force: true });
}
