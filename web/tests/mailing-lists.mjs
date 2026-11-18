// Synthetic account preferences and local downloads only; no Mailman or SMTP.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "mailing-browser-"));
const password = "Mailing-browser-synthetic-password";
let child, browser, stderr = "";
try {
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port; await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "mailing-browser-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", data => { stderr = (stderr + data).slice(-4000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(stderr); if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break; if (i > 200) throw new Error("server startup timeout"); await new Promise(resolve => setTimeout(resolve, 50)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "mailing-browser-setup", name: "Mailing QA", email: "admin@example.test", password, ixpName: "Mailing QA", asn: 64500 } }); assert.equal(setup.status(), 200);
  const api = async (ctx, route, data, method = data ? "POST" : "GET") => {
    const session = await (await ctx.request.get(base + "/api/session")).json(); const headers = { "X-CSRF-Token": session.csrfToken };
    if (route.startsWith("/resources/") && method === "PUT") { const old = await (await ctx.request.get(base + "/api" + route)).json(); headers["If-Match"] = `"${old._etag}"`; }
    const response = await ctx.request.fetch(base + "/api" + route, { method, headers, data });
    assert.ok(response.ok(), `${method} ${route}: ${response.status()} ${await response.text()}`); return response.json();
  };
  const page = await admin.newPage(), errors = [];
  page.setDefaultTimeout(12000); page.on("pageerror", e => errors.push(e.message));
  let adminGets = 0; page.on("request", request => { if (request.method() === "GET" && new URL(request.url()).pathname === "/api/mailing-lists") adminGets++; });
  await page.goto(base + "/#settings"); await page.getByLabel("IX-F 导出格式", { exact: true }).waitFor();
  await page.getByLabel("IX-F 导出格式", { exact: true }).selectOption("0.7");
  assert.equal(adminGets, 0, "mailing configuration loaded before opening its lazy card");
  await page.getByRole("button", { name: "管理邮件列表", exact: true }).click();
  await page.getByRole("button", { name: "新增邮件列表", exact: true }).click();
  const archive = "https://archive.example.test/" + "long-path-".repeat(70);
  await page.getByLabel("列表标识", { exact: true }).fill("operations");
  await page.getByLabel("列表名称", { exact: true }).fill("运营通知");
  await page.getByLabel("列表邮箱", { exact: true }).fill("operations@example.test");
  await page.getByLabel("邮件归档 URL", { exact: true }).fill(archive);
  await page.getByLabel("列表说明", { exact: true }).fill("用于合成运维公告，不连接任何外部邮件列表。");
  await page.getByRole("button", { name: "创建邮件列表", exact: true }).click();
  await page.getByText("列表定义已保存；这不会向外部邮件列表发送同步请求。", { exact: true }).waitFor();
  await page.locator('[data-mailing-definition="operations"]').waitFor();
  assert.equal(await page.getByLabel("IX-F 导出格式", { exact: true }).inputValue(), "0.7");
  let list = (await api(admin, "/mailing-lists")).items[0];
  assert.equal(typeof list.revision, "number");
  console.log("PASS lazy admin create, exact numeric revision and preserved IX-F version selection");

  await page.goto(base + "/#security"); await page.getByRole("button", { name: "邮件列表偏好", exact: true }).click();
  const selfCard = page.locator('[data-mailing-list="operations"]');
  await selfCard.getByLabel("希望如何处理 运营通知", { exact: true }).selectOption("subscribed");
  await selfCard.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
  await page.getByText("运营通知：偏好已保存；外部邮件列表尚未确认同步。", { exact: true }).waitFor();
  assert.equal((await api(admin, "/account/mailing-lists")).items[0].desired, "subscribed");
  await selfCard.getByLabel("希望如何处理 运营通知", { exact: true }).selectOption("unsubscribed");
  list = (await api(admin, "/mailing-lists")).items[0];
  await api(admin, `/mailing-lists/${list.id}`, { revision: list.revision, name: list.name, description: "并发更新后的说明", archiveUrl: list.archiveUrl, enabled: true }, "PUT");
  await selfCard.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "你的选择已保留" }).waitFor();
  assert.equal(await selfCard.getByLabel("希望如何处理 运营通知", { exact: true }).inputValue(), "unsubscribed");
  assert.equal(await selfCard.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).isDisabled(), true);
  await page.getByRole("button", { name: "重新读取并核对（保留选择）", exact: true }).click();
  await selfCard.getByText("并发更新后的说明", { exact: true }).waitFor();
  await selfCard.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
  await page.getByText("运营通知：偏好已保存；外部邮件列表尚未确认同步。", { exact: true }).waitFor();
  assert.equal((await api(admin, "/account/mailing-lists")).items[0].desired, "unsubscribed");
  console.log("PASS self preference and explicit conflict reload preserve selection without automatic retry");

  const member = await api(admin, "/resources/members", { name: "Mailing member", asn: 64501, status: "active" });
  const contexts = [];
  let viewer, viewerRecord;
  for (const role of ["viewer", "operator", "member"]) {
    const record = await api(admin, "/resources/users", { name: `${role} fixture`, email: `${role}@example.test`, password, role, ...(role === "member" ? { memberId: member.id } : {}) });
    const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } }); contexts.push(ctx);
    assert.equal((await ctx.request.post(base + "/api/login", { data: { email: `${role}@example.test`, password } })).status(), 200);
    const own = await ctx.newPage(); own.setDefaultTimeout(12000); own.on("pageerror", e => errors.push(e.message));
    await own.goto(base + "/#security?section=mailing-lists");
    await own.getByLabel("希望如何处理 运营通知", { exact: true }).selectOption("subscribed");
    await own.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
    await own.getByText("运营通知：偏好已保存；外部邮件列表尚未确认同步。", { exact: true }).waitFor();
    assert.equal((await ctx.request.get(base + "/api/mailing-lists")).status(), 403);
    assert.equal((await ctx.request.get(base + `/api/mailing-lists/${list.id}/export`)).status(), 403);
    const self = await api(ctx, "/account/mailing-lists"); assert.equal(self.email, `${role}@example.test`); assert.equal(JSON.stringify(self).includes("admin@example.test"), false);
    assert.ok(await own.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${role} mobile overflow`);
    await own.goto(base + "/#settings"); assert.equal(await own.getByRole("button", { name: "管理邮件列表", exact: true }).count(), 0);
    if (role === "viewer") { viewer = ctx; viewerRecord = record; }
    await own.close();
  }
  console.log("PASS all browser roles own preferences, admin-only definitions/export and 390px self page");

  await api(admin, `/resources/users/${viewerRecord.id}`, { name: "viewer fixture", email: "viewer-new@example.test", role: "viewer" }, "PUT");
  await viewer.clearCookies(); assert.equal((await viewer.request.post(base + "/api/login", { data: { email: "viewer-new@example.test", password } })).status(), 200);
  const changed = await viewer.newPage(); changed.setDefaultTimeout(12000); changed.on("pageerror", e => errors.push(e.message));
  await changed.goto(base + "/#security?section=mailing-lists");
  await changed.getByText("viewer-new@example.test", { exact: true }).waitFor();
  await changed.getByText(/账号邮箱已改变/).waitFor();
  assert.equal(await changed.getByLabel("希望如何处理 运营通知", { exact: true }).inputValue(), "unset");
  assert.equal(await changed.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).isDisabled(), true);
  await changed.getByLabel("希望如何处理 运营通知", { exact: true }).selectOption("subscribed");
  await changed.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
  await changed.getByText("运营通知：偏好已保存；外部邮件列表尚未确认同步。", { exact: true }).waitFor();
  console.log("PASS changed email requires explicit new choice without inheriting old subscription");

  await page.goto(base + "/#settings"); await page.getByRole("button", { name: "管理邮件列表", exact: true }).click();
  await page.getByRole("button", { name: "编辑 运营通知", exact: true }).click();
  assert.equal(await page.getByLabel("列表标识", { exact: true }).isDisabled(), true); assert.equal(await page.getByLabel("列表邮箱", { exact: true }).isDisabled(), true);
  await page.getByLabel("列表说明", { exact: true }).fill("保留的管理员编辑");
  list = (await api(admin, "/mailing-lists")).items[0];
  await api(admin, `/mailing-lists/${list.id}`, { revision: list.revision, name: "并发新名称", description: "服务器新说明", archiveUrl: list.archiveUrl, enabled: true }, "PUT");
  await page.getByRole("button", { name: "保存列表定义", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "编辑内容已保留" }).waitFor();
  assert.equal(await page.getByLabel("列表说明", { exact: true }).inputValue(), "保留的管理员编辑");
  await page.getByRole("button", { name: "重新读取并核对（保留编辑）", exact: true }).click();
  await page.getByText("服务器当前定义：并发新名称 · 启用", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "保存列表定义", exact: true }).isDisabled(), true);
  await page.getByRole("button", { name: "已核对当前定义，保留我的编辑", exact: true }).click();
  await page.getByRole("button", { name: "保存列表定义", exact: true }).click();
  await page.getByText("列表定义已保存；这不会向外部邮件列表发送同步请求。", { exact: true }).waitFor();
  const downloading = page.waitForEvent("download"); await page.getByRole("button", { name: "导出 运营通知 意图", exact: true }).click();
  const download = await downloading, exported = JSON.parse(await readFile(await download.path(), "utf8"));
  assert.equal(exported.externalApplied, false); assert.equal(exported.version, 1); assert.equal(exported.list.id, list.id);
  assert.ok(exported.subscribed.includes("viewer-new@example.test")); assert.ok(!exported.subscribed.includes("viewer@example.test")); assert.ok(exported.unsubscribed.includes("viewer@example.test")); assert.ok(exported.unsubscribed.includes("admin@example.test"));
  console.log("PASS admin CAS review, immutable key/email and actual intent JSON download with retired email");

  await page.route("**/api/mailing-lists", async route => { if (route.request().method() === "GET") await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "synthetic refresh failure" }) }); else await route.continue(); });
  await page.getByRole("button", { name: "刷新邮件列表", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "synthetic refresh failure" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "导出 运营通知 意图", exact: true }).isDisabled(), true);
  await page.unroute("**/api/mailing-lists"); await page.getByRole("button", { name: "重新读取并核对（保留编辑）", exact: true }).click();
  await page.getByRole("button", { name: "编辑 运营通知", exact: true }).click();
  await page.getByRole("checkbox", { name: "启用此邮件列表", exact: true }).uncheck();
  await page.getByRole("button", { name: "保存列表定义", exact: true }).click();
  await page.getByText("列表定义已保存；这不会向外部邮件列表发送同步请求。", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "导出 运营通知 意图", exact: true }).isDisabled(), true);
  await changed.reload(); await changed.getByText("此列表不接受新的订阅偏好，仍可明确选择退订。", { exact: true }).waitFor();
  assert.equal(await changed.getByLabel("希望如何处理 运营通知", { exact: true }).locator('option[value="subscribed"]').isDisabled(), true);
  await changed.getByLabel("希望如何处理 运营通知", { exact: true }).selectOption("unsubscribed");
  await changed.getByRole("button", { name: "保存 运营通知 偏好", exact: true }).click();
  await changed.getByText("运营通知：偏好已保存；外部邮件列表尚未确认同步。", { exact: true }).waitFor();
  assert.equal((await api(viewer, "/account/mailing-lists")).items[0].desired, "unsubscribed");
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "admin mobile overflow");
  if (process.env.MAILING_SCREENSHOT) await page.screenshot({ path: process.env.MAILING_SCREENSHOT, fullPage: true, animations: "disabled" });
  assert.deepEqual(errors, []);
  console.log("PASS failed refresh fails closed, disabled list retains withdrawal and admin mobile layout");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { const exit = new Promise(resolve => child.once("exit", resolve)); child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000); await exit; clearTimeout(timer); }
  await rm(dir, { recursive: true, force: true });
}
