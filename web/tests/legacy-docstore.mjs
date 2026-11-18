// Synthetic offline source, private loopback server and real Chromium only.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, readFile, writeFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
const exec = promisify(execFile), repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp(path.join(tmpdir(), "moedove-docstore-ui-")), disk = path.join(dir, "disk"), input = path.join(dir, "source.json"), output = path.join(dir, "documents.tar.gz"), resources = path.join(dir, "resources.json");
const tables = {
  cust: [{ id: 1, name: "Member A", autsys: 64501, status: 1 }, { id: 2, name: "Member B", autsys: 64502, status: 1 }],
  docstore_directories: [{ id: 1, name: "Mixed permissions", description: "Original source tree" }],
  docstore_customer_directories: [{ id: 1, cust_id: 1, name: "Member folder" }],
  docstore_files: [{ id: 1, name: "public-guide.txt", disk: "docstore", path: "public.txt", min_privs: 0, docstore_directory_id: 1, created_by: "OLD-STAFF-ACTOR" }, { id: 2, name: "admin-guide.txt", disk: "docstore", path: "admins.txt", min_privs: 2, docstore_directory_id: 1 }],
  docstore_customer_files: [{ id: 1, cust_id: 1, name: "member-admin.txt", disk: "docstore_customers", path: "member.txt", min_privs: 2, docstore_customer_directory_id: 1 }],
  docstore_logs: Array.from({ length: 25 }, (_, i) => ({ id: i + 1, docstore_file_id: 1, downloaded_by: null, created_at: "2020-01-02 03:04:05" })),
};
let child, browser, stderr = "";
try {
  await mkdir(disk);
  for (const [name, content] of [["public.txt", "PUBLIC GUIDE"], ["admins.txt", "ALL MEMBER ADMINISTRATORS"], ["member.txt", "MEMBER A ADMINISTRATORS"]]) await writeFile(path.join(disk, name), content);
  await writeFile(input, JSON.stringify({ tables }));
  await exec(process.env.TEST_MIGRATE_BIN || "/tmp/moedove-docstore-migrate", ["--input", input, "--resources-only", "--output", resources]);
  await exec(process.env.TEST_MIGRATE_BIN || "/tmp/moedove-docstore-migrate", ["--input", input, "--docstore-bundle", "--docstore-disk-root", `docstore=${disk}`, "--docstore-disk-root", `docstore_customers=${disk}`, "--source-timezone", "UTC", "--output", output]);
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve)); const port = listener.address().port; await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`, password = "Docstore-browser-fixture-password-123";
  child = spawn(process.env.TEST_SERVER_BIN || "/tmp/moedove-docstore-server", ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web", "dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "docstore-ui-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", data => { stderr = (stderr + data).slice(-4000); });
  for (let i = 0; ; i++) { if (child.exitCode !== null) throw new Error(`fixture server stopped: ${stderr}`); if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break; if (i > 100) throw new Error("fixture server did not start"); await new Promise(resolve => setTimeout(resolve, 100)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox"] });
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }), page = await context.newPage(), errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const setup = await context.request.post(base + "/api/setup", { data: { setupToken: "docstore-ui-setup", name: "Docstore QA", email: "admin@example.test", password, ixpName: "Document QA", asn: 64500 } }); assert.equal(setup.status(), 200, await setup.text());
  const api = async (who, target, data) => { const session = await (await who.request.get(base + "/api/session")).json(); const response = await who.request.fetch(base + "/api" + target, { method: data ? "POST" : "GET", headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(data ? { data } : {}) }); assert.ok(response.ok(), `${target}: ${response.status()} ${await response.text()}`); return response.json(); };
  await api(context, "/import", JSON.parse(await readFile(resources, "utf8")));
  await page.goto(base + "/#settings"); await page.getByRole("heading", { name: "离线文档迁移包", exact: true }).waitFor();
  await page.getByLabel("选择离线文档迁移包", { exact: true }).setInputFiles(output);
  const imported = page.waitForResponse(response => response.url().endsWith("/api/import/docstore"));
  await page.getByRole("button", { name: "导入文档迁移包", exact: true }).click(); const response = await imported; assert.equal(response.status(), 200, await response.text());
  await page.getByText(/已导入 3 份文件、2 条原目录和 25 条访问记录/).waitFor();
  await page.getByRole("button", { name: "查看旧文档归档", exact: true }).click(); await page.getByRole("heading", { name: "旧文档归档", exact: true }).waitFor();
  await page.getByRole("article").filter({ hasText: "member-admin.txt" }).waitFor();
  await page.getByRole("button", { name: "旧访问记录", exact: true }).click(); assert.equal(await page.getByRole("article").count(), 20);
  await page.getByRole("button", { name: "下一页", exact: true }).click(); assert.equal(await page.getByRole("article").count(), 5);
  assert.ok((await page.locator("body").innerText()).includes("结果未知"));
  await page.setViewportSize({ width: 390, height: 844 }); assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "archive overflows mobile viewport");
  await page.getByRole("button", { name: "文件与权限", exact: true }).click();
  await page.getByRole("article").filter({ hasText: "member-admin.txt" }).getByRole("button", { name: "查看只读文档" }).click();
  await page.getByRole("button", { name: "查看文本 member-admin.txt", exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "上传文件", exact: true }).count(), 0);
  await page.getByRole("button", { name: "查看文本 member-admin.txt", exact: true }).click();
  const dialog = page.getByRole("dialog"); assert.equal(await dialog.getByLabel("文档文本内容").inputValue(), "MEMBER A ADMINISTRATORS"); assert.equal(await dialog.getByLabel("文档文本内容").getAttribute("readonly"), ""); await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  for (const [name, email, role, memberId, admins] of [["Ordinary", "member@example.test", "member", "legacy-members-1", []], ["Member administrator", "member-admin@example.test", "member", "legacy-members-1", ["legacy-members-1"]], ["Other administrator", "other@example.test", "member", "legacy-members-2", ["legacy-members-2"]], ["Viewer", "viewer@example.test", "viewer", "", []]]) await api(context, "/resources/users", { name, email, role, password, ...(memberId ? { memberId, memberAdminIds: admins } : {}) });
  for (const [email, count, privateStatus] of [["member@example.test", 1, 404], ["member-admin@example.test", 3, 200], ["other@example.test", 2, 404]]) {
    const member = await browser.newContext({ viewport: { width: 390, height: 844 } }), view = await member.newPage(); view.on("pageerror", error => errors.push(error.message));
    const login = await member.request.post(base + "/api/login", { data: { email, password } }); assert.equal(login.status(), 200);
    assert.equal((await api(member, "/documents")).items.length, count);
    assert.equal((await member.request.get(base + "/api/documents/legacy-docstore-customer-file-1/download")).status(), privateStatus);
    await view.goto(base + "/#documents?view=legacy"); await view.getByRole("heading", { name: "文档库", exact: true }).waitFor(); assert.equal(await view.getByRole("button", { name: "旧文档归档", exact: true }).count(), 0);
    assert.ok(!(await view.locator("body").innerText()).includes("OLD-STAFF-ACTOR")); assert.ok(await view.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await member.close();
  }
  const anon = await browser.newContext(); const publicDocs = await (await anon.request.get(base + "/api/public/documents")).json(); assert.equal(publicDocs.items.length, 1); await anon.close();
  assert.deepEqual(errors, []);
  console.log("Legacy Docstore import, source archive, mixed privileges, member isolation, immutable text and mobile layout passed");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { child.kill("SIGTERM"); await new Promise(resolve => child.once("exit", resolve)); }
  await rm(dir, { recursive: true, force: true });
}
