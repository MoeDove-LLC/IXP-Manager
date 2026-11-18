// Entirely synthetic offline bundle + isolated Go service + real browser.
// No production files, old credentials, external providers or device changes.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { createHash } from "node:crypto";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, readFile, writeFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright");
const exec = promisify(execFile), repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp("/tmp/moedove-patch-ui-"), sourceRoot = path.join(dir, "old-disk"), output = path.join(dir, "patch.tar.gz");
const tables = {
  cust: [{ id: 1, name: "Current member A", autsys: 64501, status: 1 }, { id: 2, name: "Current member B", autsys: 64502, status: 1 }],
  location: [{ id: 1, name: "Fixture location" }], cabinet: [{ id: 1, name: "Fixture rack", locationid: 1 }],
  patch_panel: [{ id: 1, cabinet_id: 1, name: "Legacy panel", port_prefix: "F", location_notes: "STAFF PANEL NOTE" }, { id: 2, cabinet_id: 1, name: "Review panel", port_prefix: "R" }],
  patch_panel_port: [{ id: 1, patch_panel_id: 1, number: 1, state: 7, customer_id: 1, private_notes: "STAFF CURRENT NOTE", assigned_at: "2020-01-02", loa_code: "SYNTHETIC-OLD-LOA-NEVER-GRANTED" }, { id: 2, patch_panel_id: 1, number: 2, state: 1, duplex_master_id: 1 }, { id: 3, patch_panel_id: 2, number: 1, state: 999 }],
  patch_panel_port_history: Array.from({ length: 25 }, (_, i) => ({ id: i + 7, patch_panel_port_id: 1, number: 1, state: 3, cust_id: 999, customer: "Deleted historical tenant", switchport: "retired-switch::xe99", private_notes: `STAFF HISTORICAL NOTE ${i}`, ceased_at: "2019-01-01", created_at: "2019-01-01" })),
  patch_panel_port_file: [], patch_panel_port_history_file: [],
};
const contents = new Map();
function attachment(table, id, parent, parentID, name, content, privateFile, existingKey) {
  const key = existingKey || createHash("sha256").update(`old-port-99-${name}`).digest("hex");
  contents.set(key, content);
  tables[table].push({ id, [parent]: parentID, name, type: name.endsWith("pdf") ? "application/pdf" : "text/plain", uploaded_at: "2020-01-02 03:04:05", uploaded_by: "old-uploader-not-current-user", size: Buffer.byteLength(content), is_private: privateFile, storage_location: key });
  return key;
}
const shared = attachment("patch_panel_port_file", 1, "patch_panel_port_id", 1, "member-current.txt", "CURRENT MEMBER CONTENT", false);
attachment("patch_panel_port_file", 2, "patch_panel_port_id", 1, "staff-physical.txt", "STAFF PHYSICAL CONTENT", true);
attachment("patch_panel_port_file", 4, "patch_panel_port_id", 2, "slave-unowned.txt", "NO RAW CUSTOMER GRANT", false);
attachment("patch_panel_port_history_file", 3, "patch_panel_port_history_id", 7, "history-note.txt", "CURRENT MEMBER CONTENT", false, shared);
attachment("patch_panel_port_history_file", 5, "patch_panel_port_history_id", 7, "old-authorization.pdf", "%PDF-1.4\nOLD ATTACHMENT NOT A NEW LOA", false);
attachment("patch_panel_port_file", 6, "patch_panel_port_id", 3, "review-current.txt", "UNRESOLVED SOURCE ONLY", false);
for (const [key, content] of contents) { const file = path.join(sourceRoot, "files", "ppp", key[0], key[1], key); await mkdir(path.dirname(file), { recursive: true }); await writeFile(file, content, { mode: 0o600 }); }
const input = path.join(dir, "source.json"); await writeFile(input, JSON.stringify({ tables }), { mode: 0o600 });
let child, browser, stderr = "";
try {
  await exec(process.env.GO_BIN || "/tmp/moedove-toolchain/go/bin/go", ["run", "./cmd/ixp-migrate", "--input", input, "--patch-bundle", "--patch-files-root", sourceRoot, "--source-timezone", "UTC", "--output", output], { cwd: repo, timeout: 60000, env: { ...process.env, GOCACHE: process.env.GOCACHE || "/tmp/moedove-go-cache", GOPATH: process.env.GOPATH || "/tmp/moedove-gopath" } });
  const socket = net.createServer(); await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve)); const port = socket.address().port; await new Promise((resolve) => socket.close(resolve));
  const base = `http://127.0.0.1:${port}`, password = "Patch-browser-fixture-password-123";
  child = spawn(process.env.TEST_SERVER_BIN || "/tmp/moedove-patch-migration-server", ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web", "dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "patch-ui-bootstrap" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", (chunk) => { stderr = (stderr + chunk).slice(-4000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(`fixture service stopped: ${stderr}`); if (await fetch(base + "/api/setup").then((r) => r.ok).catch(() => false)) break; if (i > 100) throw new Error("fixture service did not start"); await new Promise((resolve) => setTimeout(resolve, 100)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox"] });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 }, timezoneId: "Asia/Shanghai" }), page = await admin.newPage(), errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "patch-ui-bootstrap", name: "Patch QA", email: "admin@example.test", password, ixpName: "MoeDove Patch QA", asn: 64500 } }); assert.equal(setup.status(), 200, await setup.text());
  const api = async (context, target, data, method = data ? "POST" : "GET") => { const session = await (await context.request.get(base + "/api/session")).json(); const response = await context.request.fetch(base + "/api" + target, { method, headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(data ? { data } : {}) }); const result = await response.json(); assert.ok(response.ok(), `${target}: ${response.status()} ${JSON.stringify(result)}`); return result; };
  await page.goto(base + "/#settings"); await page.getByRole("heading", { name: "离线配线迁移包", exact: true }).waitFor();
  await page.locator("#patch-bundle-file").setInputFiles(output);
  const dialog = page.getByRole("dialog"); await dialog.getByRole("heading", { name: "确认导入离线配线包", exact: true }).waitFor();
  assert.equal(await dialog.getByRole("button", { name: "确认导入配线包", exact: true }).isDisabled(), true);
  assert.equal((await api(admin, "/patching/legacy")).batches.length, 0);
  await page.locator("#patch-bundle-ack").check(); const imported = page.waitForResponse((response) => response.url().endsWith("/api/import/patching"));
  await dialog.getByRole("button", { name: "确认导入配线包", exact: true }).click(); const response = await imported, result = await response.json(); assert.equal(response.status(), 200, JSON.stringify(result));
  await dialog.waitFor({ state: "hidden" }); await page.getByRole("status", { name: "配线导入结果" }).waitFor();
  assert.equal(result.loaAuthorizationImported, false); assert.equal(result.patching.reviewPanels, 1); assert.equal(result.patching.histories, 25); assert.equal(result.patching.attachments, 6);
  assert.ok(!(await page.locator("body").innerText()).includes("SYNTHETIC-OLD-LOA-NEVER-GRANTED"));
  const archive = await api(admin, "/patching/legacy"), docs = (await api(admin, "/documents")).items;
  const currentDoc = docs.find((item) => item.legacySourceId === "legacy-patch-current-file-1"), historyDoc = docs.find((item) => item.legacySourceId === "legacy-patch-history-file-3"), privateDoc = docs.find((item) => item.legacySourceId === "legacy-patch-current-file-2");
  assert.ok(currentDoc && historyDoc && privateDoc);
  await page.getByRole("button", { name: "查看遗留配线归档", exact: true }).click(); await page.getByRole("heading", { name: "遗留配线归档", exact: true }).waitFor();
  await page.getByRole("article").filter({ hasText: "Review panel" }).first().waitFor();
  await page.getByRole("button", { name: "分配快照", exact: true }).click(); assert.equal(await page.getByRole("article").count(), 20); await page.getByRole("button", { name: "下一页", exact: true }).click(); assert.equal(await page.getByRole("article").count(), 5);
  await page.getByRole("article").first().getByRole("button", { name: "查看来源详情", exact: true }).click();
  await dialog.getByText("原终止日期 · 仅日期", { exact: true }).waitFor(); assert.ok((await dialog.innerText()).includes("2019-01-01")); assert.ok((await dialog.innerText()).includes("Deleted historical tenant"));
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: "附件归档", exact: true }).click();
  await page.getByRole("article").filter({ hasText: "member-current.txt" }).getByText("· 当前分配成员可读", { exact: true }).waitFor();
  await page.getByRole("article").filter({ hasText: "history-note.txt" }).getByText("· 仅工作人员", { exact: true }).waitFor();
  await page.getByRole("article").filter({ hasText: "old-authorization.pdf" }).getByText("遗留只读", { exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 }); await page.screenshot({ path: "/tmp/moedove-legacy-patch-mobile.png", fullPage: true, animations: "disabled" });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "legacy archive overflowed narrow viewport");
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(base + `/#documents?id=${currentDoc.id}`); await page.getByRole("button", { name: `查看文本 ${currentDoc.name}`, exact: true }).waitFor();
  for (const name of [`编辑文件信息 ${currentDoc.name}`, `替换内容 ${currentDoc.name}`, `删除文件 ${currentDoc.name}`, "上传文件", "新建目录"]) assert.equal(await page.getByRole("button", { name, exact: true }).count(), 0, `readonly control visible: ${name}`);
  await page.getByRole("button", { name: `查看文本 ${currentDoc.name}`, exact: true }).click(); assert.equal(await dialog.getByLabel("文档文本内容").inputValue(), "CURRENT MEMBER CONTENT"); assert.equal(await dialog.getByLabel("文档文本内容").getAttribute("readonly"), ""); assert.equal(await dialog.getByRole("button", { name: "保存文本", exact: true }).count(), 0); await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  const ports = (await api(admin, "/patching/ports")).items, port1 = ports.find((item) => item.id === "legacy-patch-ports-1");
  await page.goto(base + `/#patching?id=${port1.id}`); await page.getByRole("heading", { name: `${port1.name} · 配线详情`, exact: true }).waitFor(); assert.equal((await api(admin, "/patching/loas")).items.length, 0);
  await page.setViewportSize({ width: 390, height: 844 }); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "current patch toolbar overflowed narrow viewport"); await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("button", { name: "申请连接", exact: true }).click(); await dialog.getByLabel("更新关联资料", { exact: true }).check();
  assert.equal(await dialog.locator("fieldset").getByText(currentDoc.name, { exact: true }).count(), 0); assert.equal(await dialog.locator("fieldset").getByText(historyDoc.name, { exact: true }).count(), 0); await dialog.getByRole("button", { name: "取消", exact: true }).click();
  for (const [name, email, role, memberId] of [["Member A", "a@example.test", "member", "legacy-members-1"], ["Member B", "b@example.test", "member", "legacy-members-2"], ["Viewer", "viewer@example.test", "viewer", ""]]) await api(admin, "/resources/users", { name, email, role, password, ...(memberId ? { memberId } : {}) });
  const member = await browser.newContext({ viewport: { width: 390, height: 844 } }), memberPage = await member.newPage(), memberRequests = [];
  memberPage.on("pageerror", (error) => errors.push(error.message)); memberPage.on("request", (request) => memberRequests.push(new URL(request.url()).pathname));
  const login = await member.request.post(base + "/api/login", { data: { email: "a@example.test", password } }); assert.equal(login.status(), 200, await login.text());
  await memberPage.goto(base + "/#patching?view=legacy"); await memberPage.getByRole("heading", { name: "我的配线与授权", exact: true }).waitFor(); assert.ok(!memberRequests.includes("/api/patching/legacy")); assert.equal(await memberPage.getByRole("button", { name: "遗留归档与待核对", exact: true }).count(), 0);
  await memberPage.goto(base + `/#documents?id=${currentDoc.id}`); await memberPage.getByRole("button", { name: `查看文本 ${currentDoc.name}`, exact: true }).waitFor();
  await memberPage.getByRole("region", { name: "遗留文档说明" }).getByText(currentDoc.name, { exact: true }).waitFor();
  assert.ok(!(await memberPage.locator("body").innerText()).includes("STAFF")); assert.ok(!(await memberPage.locator("body").innerText()).includes("history-note"));
  const download = memberPage.waitForEvent("download"); await memberPage.getByRole("button", { name: `下载 ${currentDoc.name}`, exact: true }).click(); assert.equal(await readFile(await (await download).path(), "utf8"), "CURRENT MEMBER CONTENT");
  assert.equal((await member.request.get(base + "/api/patching/legacy")).status(), 403); assert.equal((await member.request.get(base + `/api/documents/${historyDoc.id}/download`)).status(), 404);
  assert.equal((await member.request.get(base + `/api/documents/${privateDoc.id}/download`)).status(), 404);
  await memberPage.screenshot({ path: "/tmp/moedove-legacy-patch-member-mobile.png", fullPage: true, animations: "disabled" }); assert.ok(await memberPage.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "member document view overflowed narrow viewport");
  const viewer = await browser.newContext(), viewerPage = await viewer.newPage(); await viewer.request.post(base + "/api/login", { data: { email: "viewer@example.test", password } }); await viewerPage.goto(base + "/#patching?view=legacy"); await viewerPage.getByRole("heading", { name: "遗留配线归档", exact: true }).waitFor(); assert.equal(await viewerPage.getByRole("link", { name: "在设置中导入离线配线迁移包", exact: true }).count(), 0);
  const currentPort = (await api(admin, "/patching/ports")).items.find((item) => item.id === port1.id);
  await api(admin, `/patching/ports/${port1.id}/transition`, { action: "release", revision: currentPort.revision, reason: "End synthetic member allocation" });
  await memberPage.reload(); await memberPage.getByText("此文档已删除，或你已失去访问权限。", { exact: true }).waitFor(); assert.equal((await api(member, "/documents")).items.length, 0); assert.equal((await member.request.get(base + `/api/documents/${currentDoc.id}/download`)).status(), 404);
  const second = await browser.newContext(); await second.request.post(base + "/api/login", { data: { email: "b@example.test", password } }); assert.equal((await api(second, "/documents")).items.length, 0);
  await page.goto(base + "/#patching?view=legacy"); await page.getByRole("heading", { name: "遗留配线归档", exact: true }).waitFor(); await page.getByRole("button", { name: "附件归档", exact: true }).click(); await page.getByRole("article").filter({ hasText: "member-current.txt" }).getByText("· 仅工作人员", { exact: true }).waitFor();
  assert.deepEqual(errors, []); assert.equal(archive.histories.length, 25);
  console.log("legacy patch upload/result, staff review pagination, immutable documents, member allocation ACL, old LOA separation and 390px layout passed");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { child.kill("SIGTERM"); await Promise.race([new Promise((resolve) => child.once("exit", resolve)), new Promise((resolve) => setTimeout(resolve, 5000))]); if (child.exitCode === null) { child.kill("SIGKILL"); await new Promise((resolve) => child.once("exit", resolve)); } }
  await rm(dir, { recursive: true, force: true });
  console.log("legacy patch isolated browser, service and synthetic attachment directory cleaned up");
}
