import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, mkdir, readFile, writeFile, rm } from "node:fs/promises";
import { createHash } from "node:crypto";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "contact-export-")), artifacts = process.env.CONTACT_EXPORT_ARTIFACTS || path.join(dir, "artifacts");
await mkdir(artifacts, { recursive: true });
const password = "Contact-export-synthetic-password", groupName = "运营联系人、含标点 & 多组", formulaName = '=HYPERLINK("https://invalid.example.test","示例")';
const result = { result: "RUNNING", checks: [], screenshots: [] }, errors = [], pages = [];
let child, browser, log = "";
const check = value => { console.log("PASS " + value); result.checks.push(value); };
try {
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port; await new Promise(resolve => listener.close(resolve)); const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "contact-export-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", data => { log = (log + data).slice(-32000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(log); if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break; if (i > 200) throw new Error("server startup timeout"); await new Promise(resolve => setTimeout(resolve, 50)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext(), viewer = await browser.newContext({ viewport: { width: 390, height: 844 } }), memberContext = await browser.newContext({ viewport: { width: 390, height: 844 } });
  assert.equal((await admin.request.post(base + "/api/setup", { data: { setupToken: "contact-export-setup", name: "Contact Export QA", email: "admin@example.test", password, ixpName: "Contact Export QA", asn: 64500 } })).status(), 200);
  const api = async (ctx, route, data) => { const session = await (await ctx.request.get(base + "/api/session")).json(); const r = await ctx.request.fetch(base + "/api" + route, { method: data === undefined ? "GET" : "POST", headers: { "X-CSRF-Token": session.csrfToken }, data }); assert.ok(r.ok(), `${route}: ${r.status()} ${await r.text()}`); return r.json(); };
  const today = new Date().toISOString().slice(0, 10);
  const current = await api(admin, "/resources/members", { name: "当前成员 " + "长名称".repeat(8), asn: 64501, status: "active", joinedAt: today, billingEmail: "BILLING-EXCLUDED@example.test" });
  const leaving = await api(admin, "/resources/members", { name: "今日离开的待接入成员", asn: 64502, status: "pending", joinedAt: "2020-01-01", leftAt: today });
  const suspended = await api(admin, "/resources/members", { name: "暂停但仍在期", asn: 64503, status: "suspended", joinedAt: "2020-01-01" });
  const unknown = await api(admin, "/resources/members", { name: "缺加入日期", asn: 64504, status: "active" });
  const future = await api(admin, "/resources/members", { name: "未来成员", asn: 64505, status: "active", joinedAt: "2099-01-01" });
  const groups = [];
  for (const value of [{ name: groupName, type: "ROLE", active: false, limitedTo: 3 }, { name: groupName, type: "SOCIAL" }, { name: "财务联络", type: "ROLE" }]) groups.push(await api(admin, "/resources/contact-groups", value));
  const contact = await api(admin, "/resources/contacts", { name: formulaName, memberId: current.id, position: "负责人", email: "same@example.test", phone: "+861234567890", mobile: "0012345678", notes: 'first line, "quoted"\nsecond line' });
  const second = await api(admin, "/resources/contacts", { name: "另一个联系人，同邮箱", memberId: leaving.id, email: "same@example.test" });
  const third = await api(admin, "/resources/contacts", { name: "暂停成员联系人", memberId: suspended.id, email: "third@example.test" });
  const noDate = await api(admin, "/resources/contacts", { name: "EXCLUDED-MISSING-DATE", memberId: unknown.id, email: "missing-date@example.test" });
  const later = await api(admin, "/resources/contacts", { name: "EXCLUDED-FUTURE", memberId: future.id, email: "future@example.test" });
  for (const [c, g] of [[contact, groups[0]], [contact, groups[2]], [second, groups[1]], [third, groups[2]], [noDate, groups[0]], [later, groups[0]]]) await api(admin, "/resources/contact-group-memberships", { name: "Synthetic group membership", contactId: c.id, groupId: g.id });
  for (const [ctx, role] of [[viewer, "viewer"], [memberContext, "member"]]) {
    const email = `${role}-contacts@example.test`;
    await api(admin, "/resources/users", { name: role + " contact reader", email, password, role, ...(role === "member" ? { memberId: current.id } : {}) });
    assert.equal((await ctx.request.post(base + "/api/login", { data: { email, password } })).status(), 200);
  }
  const page = await viewer.newPage(); pages.push(["viewer", page]); page.setDefaultTimeout(12000); page.on("pageerror", e => errors.push(e.message));
  let exportRequests = 0; page.on("request", r => { if (new URL(r.url()).pathname === "/api/contact-export") exportRequests++; });
  await page.goto(base + "/#contacts"); await page.getByRole("button", { name: "按联系组导出", exact: true }).waitFor(); assert.equal(exportRequests, 0);
  await page.getByRole("button", { name: "按联系组导出", exact: true }).click(); const dialog = page.getByRole("dialog");
  await dialog.getByLabel("联系组名称", { exact: true }).waitFor();
  assert.equal(await dialog.getByLabel("联系组名称", { exact: true }).inputValue(), ""); assert.equal(await dialog.getByRole("button", { name: "下载联系人文件", exact: true }).isDisabled(), true);
  await dialog.getByLabel("联系组名称", { exact: true }).selectOption(groupName);
  await dialog.getByText("将合并匹配的 2 个组。同一联系人加入多个匹配组时只导出一次。", { exact: true }).waitFor();
  const download = async () => { const event = page.waitForEvent("download"); await dialog.getByRole("button", { name: "下载联系人文件", exact: true }).click(); const file = await event; return readFile(await file.path()); };
  const raw = await download(), rows = JSON.parse(raw);
  assert.equal(rows.length, 2); assert.deepEqual(Object.keys(rows[0]), ["name", "position", "email", "phone", "mobile", "notes", "cust_status"]);
  const first = rows.find(r => r.name === formulaName); assert.equal(first.phone, "+861234567890"); assert.equal(first.mobile, "0012345678"); assert.equal(first.notes, 'first line, "quoted"\nsecond line');
  assert.deepEqual(rows.map(r => r.cust_status).sort(), ["Normal", "Not Connected"].sort());
  assert.ok(!raw.includes(Buffer.from("EXCLUDED")));
  const direct = await (await viewer.request.get(base + "/api/contact-export?" + new URLSearchParams({ name: groupName }))).body(); assert.deepEqual(raw, direct);
  result.jsonSHA256 = createHash("sha256").update(raw).digest("hex"); await writeFile(path.join(artifacts, "contacts.json"), raw, { mode: 0o600 });
  check("readonly staff opens lazy export, explicitly selects duplicate-name union, and downloads exact seven-field JSON with raw phones/notes and inclusive leave date");

  await dialog.getByLabel("成员范围", { exact: true }).selectOption(current.id); await dialog.getByLabel("下载格式", { exact: true }).selectOption("csv");
  const csv = await download(), text = csv.toString("utf8");
  assert.ok(text.startsWith('"name","position","email","phone","mobile","notes","cust_status"\r\n'));
  assert.ok(text.includes('"text: =HYPERLINK(""https://invalid.example.test"",""示例"")"'));
  assert.ok(text.includes('"text: +861234567890","text: 0012345678"'));
  assert.ok(text.includes('"first line, ""quoted""\nsecond line"'));
  await dialog.getByRole("status").filter({ hasText: "已下载 1 条联系人" }).waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  const box = await dialog.boundingBox(); assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= 390 && box.y + box.height <= 844);
  await page.screenshot({ path: path.join(artifacts, "contact-export-390.png"), fullPage: true, animations: "disabled" }); result.screenshots.push("contact-export-390.png");
  result.csvSHA256 = createHash("sha256").update(csv).digest("hex"); await writeFile(path.join(artifacts, "contacts.csv"), csv, { mode: 0o600 });
  check("actual CSV download preserves quoted multiline columns and protects formula/phone text; member filter and 390px dialog fit");

  await dialog.getByLabel("分组匹配方式", { exact: true }).selectOption("type"); assert.equal(await dialog.getByLabel("联系组类型", { exact: true }).inputValue(), "");
  await dialog.getByLabel("联系组类型", { exact: true }).selectOption("ROLE"); await dialog.getByLabel("成员范围", { exact: true }).selectOption(""); await dialog.getByLabel("下载格式", { exact: true }).selectOption("json");
  const typed = JSON.parse(await download()); assert.equal(typed.length, 2); assert.deepEqual(typed.map(r => r.cust_status).sort(), ["Normal", "Suspended"].sort());
  await page.route("**/api/contact-export?options=1", route => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "synthetic selector refresh failure" }) }));
  await dialog.getByRole("button", { name: "刷新筛选资料", exact: true }).click(); await dialog.getByRole("alert").filter({ hasText: "synthetic selector refresh failure" }).waitFor();
  assert.equal(await dialog.getByRole("button", { name: "下载联系人文件", exact: true }).isDisabled(), true);
  await page.unroute("**/api/contact-export?options=1");
  const fresh = page.waitForResponse(r => r.url().endsWith("/api/contact-export?options=1")); await dialog.getByRole("button", { name: "刷新筛选资料", exact: true }).click(); assert.equal((await fresh).status(), 200);
  await dialog.getByRole("button", { name: "下载联系人文件", exact: true }).waitFor();
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some(b => b.textContent.trim() === "下载联系人文件" && !b.disabled));
  check("type selection unions multiple groups without status filtering; failed refresh disables export until a successful explicit reload");

  const own = await memberContext.newPage(); pages.push(["member", own]); own.setDefaultTimeout(12000); own.on("pageerror", e => errors.push(e.message));
  await own.goto(base + "/#contacts"); await own.getByRole("heading", { name: "联系人", exact: true }).waitFor();
  assert.equal(await own.getByRole("button", { name: "按联系组导出", exact: true }).count(), 0);
  assert.equal((await memberContext.request.get(base + "/api/contact-export?options=1")).status(), 403);
  assert.equal((await memberContext.request.get(base + "/api/contact-export?type=ROLE&format=csv")).status(), 403);
  assert.deepEqual(errors, []); check("member browser has no staff group-export surface and cannot read selector metadata or group downloads");
  result.result = "PASS";
} catch (error) { result.result = "FAIL"; result.error = error.stack || String(error); throw error; }
finally {
  for (const [name, page] of pages) if (!page.isClosed() && result.result !== "PASS") { await page.screenshot({ path: path.join(artifacts, "failure-" + name + ".png"), fullPage: true }).catch(() => {}); await writeFile(path.join(artifacts, "failure-" + name + ".txt"), await page.locator("body").innerText().catch(() => "")).catch(() => {}); }
  await writeFile(path.join(artifacts, "results.json"), JSON.stringify({ ...result, pageErrors: errors }, null, 2) + "\n"); await writeFile(path.join(artifacts, "server.log"), log);
  if (browser) await browser.close();
  if (child && child.exitCode === null) { const exit = new Promise(resolve => child.once("exit", resolve)); child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000); await exit; clearTimeout(timer); }
  await rm(dir, { recursive: true, force: true });
}
