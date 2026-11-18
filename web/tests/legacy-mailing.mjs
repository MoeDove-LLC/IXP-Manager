// Run only against a private synthetic Go server; no SMTP or external service.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, readFile, writeFile, stat, rm } from "node:fs/promises";
import { createHash } from "node:crypto";
import { chromium } from "playwright";

const run = promisify(execFile), dir = await mkdtemp(path.join(tmpdir(), "legacy-mailing-ui-"));
const artifacts = process.env.LEGACY_MAILING_ARTIFACTS || path.join(dir, "artifacts");
await mkdir(artifacts, { recursive: true });
const password = "Legacy-mailing-synthetic-password", sourceID = "9007199254740993", memberSourceID = "9223372036854775806";
const viewerEmail = `viewer-${"longidentity".repeat(3)}@${"longdomain".repeat(4)}.example.test`;
const memberEmail = "member-migration@example.test", privateValue = "UNKNOWN-RAW-PRIVATE-SOURCE";
const archiveURL = "https://archive.example.test/" + "long-path-".repeat(65);
const evidence = { result: "RUNNING", checks: [], screenshots: [], responses: [] };
const check = message => { evidence.checks.push(message); console.log("PASS " + message); };
const hash = data => createHash("sha256").update(data).digest("hex");
let child, browser, serverLog = "", base;
const pages = [], errors = [];
try {
  const memberPrefs = JSON.stringify({ mailinglist: { memberin: 1, memberout: 0 }, secret: "STRING-PREF-EXCLUDED" });
  const rawSource = `{"tables":{"user":[
    {"id":${sourceID},"name":"旧 Viewer 来源","email":${JSON.stringify(viewerEmail)},"password":"PASSWORD-EXCLUDED","prefs":{"mailinglist":{"optin":"1","optout":"0","unknown":"${privateValue}","blocked":"1","pending":"1"},"secret":"NON-MAILING-EXCLUDED","notes":{"global_notifs":"NOTES-EXCLUDED"}}},
    {"id":${memberSourceID},"name":"旧 Member 来源","email":"old-member@example.test","prefs":${JSON.stringify(memberPrefs)}},
    {"id":3,"email":"NON-SOURCE-IDENTITY-EXCLUDED","prefs":[]}],
    "user_pref":[{"id":9007199254740995,"user_id":${memberSourceID},"attribute":"mailinglist.historic","value":"1","ix":null,"op":"=","expire":123},
    {"id":9,"user_id":${memberSourceID},"attribute":"customer-notes.notify","value":"HISTORICAL-SECRET-EXCLUDED"}]}}`;
  const input = path.join(dir, "legacy-tables.json"), output = path.join(dir, "mailing-sources.json");
  await writeFile(input, rawSource, { mode: 0o600 });
  const converted = await run(process.env.TEST_MIGRATE_BIN, ["--mailing-archive", "--input", input, "--output", output], { timeout: 15000 });
  await writeFile(path.join(artifacts, "converter.log"), converted.stdout + converted.stderr);
  const archiveBytes = await readFile(output), archive = JSON.parse(archiveBytes);
  assert.equal((await stat(output)).mode & 0o777, 0o600);
  assert.deepEqual(archive.users.map(u => u.legacyId).sort(), [memberSourceID, sourceID].sort());
  assert.ok(archive.choices.some(c => c.format === "user_pref" && c.sourceKey === "historic"));
  assert.ok(archive.choices.some(c => c.format === "user.prefs" && c.sourceKey === "optout" && c.value === "0"));
  assert.ok(archiveBytes.includes(Buffer.from(privateValue)));
  for (const value of ["PASSWORD-EXCLUDED", "NON-MAILING-EXCLUDED", "NOTES-EXCLUDED", "STRING-PREF-EXCLUDED", "NON-SOURCE-IDENTITY-EXCLUDED", "HISTORICAL-SECRET-EXCLUDED"]) assert.ok(!archiveBytes.includes(Buffer.from(value)), value);
  evidence.converter = { sourceSHA256: hash(rawSource), archiveSHA256: hash(archiveBytes), checksum: archive.checksum, users: archive.users.length, choices: archive.choices.length };
  await writeFile(path.join(artifacts, "converter-archive.json"), archiveBytes, { mode: 0o600 });
  check("real Go converter retains precise int64 identity, both historical formats and opt-out, excluding unrelated secrets");

  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port; await new Promise(resolve => listener.close(resolve)); base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "legacy-mailing-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", data => { serverLog = (serverLog + data).slice(-32000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(serverLog); if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break; if (i > 200) throw new Error("server startup timeout"); await new Promise(resolve => setTimeout(resolve, 50)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  assert.equal((await admin.request.post(base + "/api/setup", { data: { setupToken: "legacy-mailing-setup", name: "Legacy Mailing QA", email: "admin@example.test", password, ixpName: "Legacy Mailing QA", asn: 64500 } })).status(), 200);
  const request = async (ctx, route, data, method = data === undefined ? "GET" : "POST") => {
    const session = await (await ctx.request.get(base + "/api/session")).json(), headers = { "X-CSRF-Token": session.csrfToken };
    if (route.startsWith("/resources/") && method === "PUT") { const old = await (await ctx.request.get(base + "/api" + route)).json(); headers["If-Match"] = `"${old._etag}"`; }
    return ctx.request.fetch(base + "/api" + route, { method, headers, data });
  };
  const api = async (ctx, route, data, method) => { const r = await request(ctx, route, data, method); assert.ok(r.ok(), `${route}: ${r.status()} ${await r.text()}`); return r.json(); };
  const page = async (context, name) => { const p = await context.newPage(); p.setDefaultTimeout(12000); p.on("pageerror", e => errors.push(name + ": " + e.message)); pages.push([name, p]); return p; };
  const screenshot = async (p, name) => {
    await p.evaluate(() => document.fonts.ready);
    assert.ok(await p.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${name}: horizontal overflow`);
    if (p.url().endsWith("section=legacy-mailing")) {
      const heading = await p.getByRole("heading", { name: "旧邮件订阅迁移", exact: true, includeHidden: true }).boundingBox();
      assert.ok(heading && heading.height <= 64, `${name}: action buttons squeeze the title into a vertical column`);
    }
    if (await p.getByRole("dialog").count()) {
      const dialog = await p.getByRole("dialog").boundingBox();
      assert.ok(dialog && dialog.x >= 0 && dialog.y >= 0 && dialog.x + dialog.width <= 390 && dialog.y + dialog.height <= 844, `${name}: review dialog exceeds mobile viewport`);
    }
    await p.screenshot({ path: path.join(artifacts, name + ".png"), fullPage: true, animations: "disabled" }); evidence.screenshots.push(name + ".png");
  };
  const memberRecord = await api(admin, "/resources/members", { name: "Legacy source member", asn: 64501, status: "active" });
  const viewerRecord = await api(admin, "/resources/users", { name: "Viewer target", email: viewerEmail, password, role: "viewer" });
  const memberUser = await api(admin, "/resources/users", { name: "Member target", email: memberEmail, password, role: "member", memberId: memberRecord.id });
  const viewer = await browser.newContext({ viewport: { width: 390, height: 844 } }), member = await browser.newContext({ viewport: { width: 390, height: 844 } });
  for (const [ctx, email] of [[viewer, viewerEmail], [member, memberEmail]]) assert.equal((await ctx.request.post(base + "/api/login", { data: { email, password } })).status(), 200);
  const definitions = {};
  for (const key of ["optin", "optout", "unknown", "blocked", "pending", "memberin", "memberout", "historic"]) {
    definitions[key] = await api(admin, "/mailing-lists", { key, name: "迁移列表 " + key, email: `${key}-${"longlist".repeat(4)}@${"longdomain".repeat(4)}.example.test`, description: "合成迁移验证", archiveUrl: archiveURL, enabled: true });
  }
  const setPreference = async (ctx, key, subscribed) => { const v = await api(ctx, "/account/mailing-lists"), item = v.items.find(i => i.id === definitions[key].id); return api(ctx, `/account/mailing-lists/${item.id}`, { email: v.email, listRevision: item.listRevision, preferenceRevision: item.preferenceRevision, subscribed }, "PUT"); };
  const desired = async (ctx, key) => (await api(ctx, "/account/mailing-lists")).items.find(i => i.id === definitions[key].id).desired;
  await setPreference(viewer, "blocked", false);
  const adminPage = await page(admin, "admin"), viewerPage = await page(viewer, "viewer"), memberPage = await page(member, "member");
  await adminPage.goto(base + "/#settings");
  await adminPage.getByLabel("IX-F 导出格式", { exact: true }).selectOption("0.7");
  await adminPage.getByLabel("邮件订阅来源 JSON（最大 16 MiB）", { exact: true }).setInputFiles(output);
  await adminPage.getByRole("button", { name: "导入邮件订阅来源", exact: true }).click();
  await adminPage.getByRole("status").filter({ hasText: "已归档" }).waitFor();
  assert.equal(await adminPage.getByLabel("IX-F 导出格式", { exact: true }).inputValue(), "0.7");
  assert.deepEqual((await api(viewer, "/account/mailing-migrations")).items, [], "same email must not imply mapping");
  assert.equal(await desired(viewer, "optin"), "unset"); assert.equal(await desired(viewer, "blocked"), "unsubscribed");
  await adminPage.getByRole("button", { name: "管理邮件列表", exact: true }).click();
  await adminPage.locator('[data-mailing-definition="optin"]').waitFor();
  assert.equal(await adminPage.getByLabel("IX-F 导出格式", { exact: true }).inputValue(), "0.7");
  await adminPage.getByRole("button", { name: "查看邮件订阅迁移", exact: true }).click();
  const adminSection = adminPage.getByRole("region", { name: "管理员旧邮件订阅映射", exact: true });
  await adminSection.getByLabel("来源批次", { exact: true }).selectOption(archive.checksum);
  await adminSection.getByRole("button", { name: "下载原始来源 JSON", exact: true }).waitFor();
  const archived = await api(admin, `/mailing-lists/legacy/archives/${archive.checksum}`); assert.equal(archived.readOnly, true);
  const wireBytes = await (await admin.request.get(base + `/api/mailing-lists/legacy/archives/${archive.checksum}?download=1`)).body();
  const downloading = adminPage.waitForEvent("download"); await adminSection.getByRole("button", { name: "下载原始来源 JSON", exact: true }).click();
  const downloaded = await readFile(await (await downloading).path()); assert.deepEqual(downloaded, wireBytes);
  assert.ok(downloaded.includes(Buffer.from(`"id":${sourceID}`))); assert.ok(downloaded.includes(Buffer.from(`"id":${memberSourceID}`)));
  evidence.downloadSHA256 = hash(downloaded); await writeFile(path.join(artifacts, "downloaded-source.json"), downloaded, { mode: 0o600 });
  check("admin import is source-only; original archive download preserves raw int64 bytes; existing IX-F and mailing settings remain usable");

  const bind = async (id, target, keys, snapshot = false) => {
    await adminSection.getByLabel("旧账号", { exact: true }).selectOption(id);
    if (snapshot) assert.equal(await adminSection.getByLabel("当前账号", { exact: true }).inputValue(), "", "equal email must not auto-select a target");
    await adminSection.getByLabel("当前账号", { exact: true }).selectOption(JSON.stringify([target.id, target.createdAt]));
    for (const key of keys) { const list = definitions[key]; await adminSection.getByLabel(`旧列表 ${key}`, { exact: true }).selectOption(JSON.stringify([list.id, list.createdAt, list.revision])); }
    if (snapshot) { await adminPage.setViewportSize({ width: 390, height: 844 }); await screenshot(adminPage, "admin-mapping-390"); }
    await adminSection.getByRole("button", { name: "创建待本人确认的映射", exact: true }).click();
    await adminSection.getByText("映射已建立，等待目标账号本人选择并确认。", { exact: true }).waitFor();
    return (await api(admin, "/mailing-lists/legacy")).mappings.at(-1);
  };
  const mapping = await bind(sourceID, viewerRecord, ["optin", "optout", "unknown", "blocked"], true);
  const memberMapping = await bind(memberSourceID, memberUser, ["memberin", "memberout", "historic"]);
  const pendingCard = (p, id) => p.locator(`[data-mailing-migration="${id}"]`).filter({ has: p.getByText("待本人确认", { exact: true }) }).last();
  const refreshSelf = async p => {
    const response = p.waitForResponse(r => new URL(r.url()).pathname === "/api/account/mailing-migrations" && r.request().method() === "GET");
    await p.getByRole("button", { name: "重新读取本人迁移", exact: true }).click();
    assert.equal((await response).status(), 200);
    await p.waitForFunction(() => [...document.querySelectorAll("button")].some(b => b.textContent.trim() === "重新读取本人迁移" && !b.disabled));
  };
  for (const [ctx, p, id, other] of [[viewer, viewerPage, sourceID, memberSourceID], [member, memberPage, memberSourceID, sourceID]]) {
    let globalRequests = 0; p.on("request", r => { if (new URL(r.url()).pathname.startsWith("/api/mailing-lists/legacy")) globalRequests++; });
    await p.goto(base + "/#security?section=legacy-mailing"); await pendingCard(p, id).waitFor();
    const view = await api(ctx, "/account/mailing-migrations");
    assert.equal(view.items.length, 1); assert.equal(view.items[0].mapping.sourceUserId, id);
    const ownText = JSON.stringify(view); for (const secret of [privateValue, "旧 Viewer 来源", "old-member@example.test", other]) assert.ok(!ownText.includes(secret));
    assert.equal(await p.getByRole("region", { name: "管理员旧邮件订阅映射", exact: true }).count(), 0);
    assert.equal(globalRequests, 0);
    for (const route of ["/mailing-lists/legacy", `/mailing-lists/legacy/archives/${archive.checksum}`, `/mailing-lists/legacy/archives/${archive.checksum}?download=1`]) assert.equal((await request(ctx, route)).status(), 403);
    assert.ok(!(await p.locator("body").innerText()).includes(privateValue));
  }
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("checkbox", { name: "选择迁移 unknown", exact: true }).isDisabled(), true);
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("checkbox", { name: "选择迁移 blocked", exact: true }).isDisabled(), true);
  check("member/viewer only see their explicit mappings; unknown source values and historical labels remain private; existing current preference is unavailable for overwrite");

  const review = async (p, id, keys) => { const card = pendingCard(p, id); for (const key of keys) await card.getByRole("checkbox", { name: `选择迁移 ${key}`, exact: true }).check(); await card.getByRole("button", { name: "审阅所选迁移", exact: true }).click(); await p.getByRole("dialog").waitFor(); return p.getByRole("dialog"); };
  const accept = async (p, dialog) => { await dialog.getByRole("checkbox", { name: "我已核对当前邮箱及所选订阅、退订方向。", exact: true }).check(); const response = p.waitForResponse(r => r.url().endsWith("/accept") && r.request().method() === "POST"); await dialog.getByRole("button", { name: "确认接受所选偏好", exact: true }).click(); const result = await response; evidence.responses.push({ action: "accept", status: result.status() }); return result; };
  let dialog = await review(viewerPage, sourceID, ["optin"]);
  assert.equal(await dialog.getByRole("button", { name: "确认接受所选偏好", exact: true }).isDisabled(), true);
  assert.equal(await desired(viewer, "optin"), "unset");
  await screenshot(viewerPage, "viewer-review-390");
  assert.equal((await accept(viewerPage, dialog)).status(), 200);
  await viewerPage.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(await desired(viewer, "optin"), "subscribed"); assert.equal(await desired(viewer, "optout"), "unset"); assert.equal(await desired(viewer, "blocked"), "unsubscribed");
  check("self checkbox → review → explicit acknowledgment → acceptance changes only the selected opt-in, preserving unselected and existing choices");
  await adminSection.getByRole("button", { name: "刷新映射资料", exact: true }).click();
  await adminSection.locator(`[data-mailing-mapping="${mapping.id}"]`).getByText("已接受", { exact: true }).waitFor();
  await adminSection.getByLabel("旧账号", { exact: true }).selectOption(sourceID);
  await adminSection.getByLabel("当前账号", { exact: true }).selectOption(JSON.stringify([viewerRecord.id, viewerRecord.createdAt]));
  await adminSection.getByLabel("旧列表 optin", { exact: true }).selectOption(JSON.stringify([definitions.optin.id, definitions.optin.createdAt, definitions.optin.revision]));
  const duplicate = adminPage.waitForResponse(r => r.url().endsWith("/mappings") && r.request().method() === "POST");
  await adminSection.getByRole("button", { name: "创建待本人确认的映射", exact: true }).click(); assert.equal((await duplicate).status(), 409);
  await adminSection.getByRole("alert").filter({ hasText: "已保留选择" }).waitFor();
  assert.equal(await adminSection.getByRole("button", { name: "创建待本人确认的映射", exact: true }).isDisabled(), true);
  await adminSection.getByRole("button", { name: "刷新映射资料", exact: true }).click();
  await adminSection.getByLabel("旧列表 optin", { exact: true }).selectOption("");
  await bind(sourceID, viewerRecord, ["optout"]);
  await refreshSelf(viewerPage);
  dialog = await review(viewerPage, sourceID, ["optout"]); assert.equal((await accept(viewerPage, dialog)).status(), 200);
  await viewerPage.getByRole("dialog").waitFor({ state: "hidden" }); assert.equal(await desired(viewer, "optout"), "unsubscribed");
  dialog = await review(memberPage, memberSourceID, ["memberin", "memberout", "historic"]); assert.equal((await accept(memberPage, dialog)).status(), 200);
  await memberPage.getByRole("dialog").waitFor({ state: "hidden" });
  for (const [key, value] of [["memberin", "subscribed"], ["memberout", "unsubscribed"], ["historic", "subscribed"]]) assert.equal(await desired(member, key), value);
  await screenshot(memberPage, "member-self-390");
  check("accepted keys reject remapping with real 409; unselected opt-out maps later; member accepts JSON and user_pref choices with explicit directions");

  const pending = await bind(sourceID, viewerRecord, ["pending"]);
  await refreshSelf(viewerPage);
  dialog = await review(viewerPage, sourceID, ["pending"]);
  const oldList = definitions.pending;
  definitions.pending = await api(admin, `/mailing-lists/${oldList.id}`, { revision: oldList.revision, name: oldList.name, description: "并发列表变更", archiveUrl: archiveURL, enabled: true }, "PUT");
  assert.equal((await accept(viewerPage, dialog)).status(), 409);
  await viewerPage.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("button", { name: "审阅所选迁移", exact: true }).isDisabled(), true);
  assert.equal(await desired(viewer, "pending"), "unset");
  await pendingCard(viewerPage, sourceID).getByRole("button", { name: "重新读取并核对", exact: true }).click();
  dialog = await review(viewerPage, sourceID, ["pending"]);
  await adminSection.getByRole("button", { name: "刷新映射资料", exact: true }).click();
  await adminSection.locator(`[data-mailing-mapping="${pending.id}"]`).getByRole("button", { name: "取消待确认映射", exact: true }).click();
  await adminPage.getByRole("dialog").getByRole("button", { name: "确认取消映射", exact: true }).click();
  await adminPage.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal((await accept(viewerPage, dialog)).status(), 409);
  await viewerPage.getByRole("dialog").waitFor({ state: "hidden" }); assert.equal(await desired(viewer, "pending"), "unset");
  check("actual list revision conflict and administrator cancellation invalidate the open review; failed acceptance never writes the old choice");

  await bind(sourceID, viewerRecord, ["pending"]);
  await refreshSelf(viewerPage);
  dialog = await review(viewerPage, sourceID, ["pending"]);
  const newEmail = "viewer-changed@example.test";
  await api(admin, `/resources/users/${viewerRecord.id}`, { name: viewerRecord.name, email: newEmail, role: "viewer" }, "PUT");
  const changedEmailResponse = await accept(viewerPage, dialog); assert.ok([401, 409].includes(changedEmailResponse.status()));
  await viewer.clearCookies(); assert.equal((await viewer.request.post(base + "/api/login", { data: { email: newEmail, password } })).status(), 200);
  await viewerPage.reload();
  await pendingCard(viewerPage, sourceID).waitFor();
  await viewerPage.getByText(`当前账号邮箱：${newEmail}`, { exact: true }).waitFor();
  assert.equal(await desired(viewer, "pending"), "unset");
  await viewerPage.route("**/api/account/mailing-migrations", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "synthetic refresh failure" }) }));
  await viewerPage.getByRole("button", { name: "重新读取本人迁移", exact: true }).click();
  await viewerPage.getByRole("alert").filter({ hasText: "synthetic refresh failure" }).waitFor();
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("button", { name: "审阅所选迁移", exact: true }).isDisabled(), true);
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("checkbox", { name: "选择迁移 pending", exact: true }).isDisabled(), true);
  await viewerPage.unroute("**/api/account/mailing-migrations");
  await refreshSelf(viewerPage);
  assert.equal(await pendingCard(viewerPage, sourceID).getByRole("checkbox", { name: "选择迁移 pending", exact: true }).isEnabled(), true);
  dialog = await review(viewerPage, sourceID, ["pending"]);
  await dialog.getByText(`当前邮箱：${newEmail}`, { exact: true }).waitFor();
  await dialog.getByRole("button", { name: "返回选择", exact: true }).click();
  await screenshot(viewerPage, "viewer-self-390");
  await viewerPage.getByRole("button", { name: "当前邮件列表偏好", exact: true }).click();
  await viewerPage.getByLabel("希望如何处理 迁移列表 optin", { exact: true }).waitFor();
  await screenshot(viewerPage, "current-mailing-long-url-390");
  await adminPage.route("**/api/mailing-lists/legacy", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "synthetic admin refresh failure" }) }));
  await adminSection.getByRole("button", { name: "刷新映射资料", exact: true }).click();
  await adminSection.getByRole("alert").filter({ hasText: "synthetic admin refresh failure" }).waitFor();
  assert.equal(await adminSection.getByRole("button", { name: "下载原始来源 JSON", exact: true }).isDisabled(), true);
  assert.equal(await adminSection.getByRole("button", { name: "创建待本人确认的映射", exact: true }).isDisabled(), true);
  check("email change invalidates reviewed consent; failed self/admin refresh disables mutations/download; 390px long IDs/emails/URLs fit");
  assert.deepEqual(errors, []); evidence.result = "PASS";
} catch (error) {
  evidence.result = "FAIL"; evidence.error = error.stack || String(error); throw error;
} finally {
  for (const [name, p] of pages) if (!p.isClosed() && evidence.result !== "PASS") {
    await p.screenshot({ path: path.join(artifacts, "failure-" + name + ".png"), fullPage: true }).catch(() => {});
    await writeFile(path.join(artifacts, "failure-" + name + ".txt"), await p.locator("body").innerText().catch(() => "")).catch(() => {});
  }
  await writeFile(path.join(artifacts, "results.json"), JSON.stringify({ ...evidence, pageErrors: errors }, null, 2) + "\n");
  await writeFile(path.join(artifacts, "server.log"), serverLog);
  if (browser) await browser.close();
  if (child && child.exitCode === null) { const exit = new Promise(resolve => child.once("exit", resolve)); child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000); await exit; clearTimeout(timer); }
  await rm(dir, { recursive: true, force: true });
}
