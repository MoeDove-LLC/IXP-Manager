// Minimal legacy text-action contract check: only synthetic txt/md/csv/log data.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { createHash } from "node:crypto";
import { gzipSync } from "node:zlib";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright");
const dir = await mkdtemp("/tmp/moedove-document-text-"), contents = [
  ["original.txt", "Synthetic plain text\n"], ["original.md", "# Synthetic markdown\n"],
  ["original.csv", "column,value\nrow,1\n"], ["original.log", "Synthetic log line\n"],
];
const digest = (value) => createHash("sha256").update(value).digest("hex");
const tarEntry = (name, content) => {
  const data = Buffer.from(content), header = Buffer.alloc(512);
  const octal = (offset, length, value) => header.write(value.toString(8).padStart(length - 1, "0") + "\0", offset, length, "ascii");
  header.write(name, 0, 100, "ascii"); octal(100, 8, 0o600); octal(108, 8, 0); octal(116, 8, 0); octal(124, 12, data.length); octal(136, 12, 0);
  header.fill(32, 148, 156); header[156] = 48; header.write("ustar\0", 257, 6, "ascii"); header.write("00", 263, 2, "ascii");
  header.write([...header].reduce((sum, value) => sum + value, 0).toString(8).padStart(6, "0") + "\0 ", 148, 8, "ascii");
  return Buffer.concat([header, data, Buffer.alloc((512 - data.length % 512) % 512)]);
};
const files = contents.map(([name, content], index) => {
  const id = index + 1, storageKey = digest(`original-storage-key-${name}`);
  return { id: `legacy-patch-current-file-${id}`, legacyId: id, kind: "current", name, private: true, storageKey, size: Buffer.byteLength(content), checksum: digest(content), entry: `files/current-${id}`,
    facts: { id: String(id), name, storage_location: storageKey, size: String(Buffer.byteLength(content)), is_private: "1", type: "text/plain" } };
});
const manifest = { version: 1, kind: "ixp-manager-patching", source: "ixp-manager-legacy", resources: {}, warnings: [], patching: { version: 1, sourceTimezone: "unknown", panels: [], ports: [], histories: [], files } };
const bundle = gzipSync(Buffer.concat([tarEntry("manifest.json", JSON.stringify(manifest)), ...files.map((file, index) => tarEntry(file.entry, contents[index][1])), Buffer.alloc(1024)]));
let child, browser, errorLog = "";
try {
  const reserved = net.createServer(); await new Promise((resolve) => reserved.listen(0, "127.0.0.1", resolve)); const port = reserved.address().port; await new Promise((resolve) => reserved.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN || "/tmp/moedove-patch-migration-server", ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", fileURLToPath(new URL("../dist", import.meta.url))], { env: { ...process.env, IXP_SETUP_TOKEN: "text-actions-fixture" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", (chunk) => { errorLog = (errorLog + chunk.toString()).slice(-3000); });
  for (let attempt = 0; ; attempt++) { if (child.exitCode !== null) throw new Error(`fixture service stopped: ${errorLog}`); if (await fetch(base + "/api/setup").then((response) => response.ok).catch(() => false)) break; if (attempt > 100) throw new Error("fixture startup timeout"); await new Promise((resolve) => setTimeout(resolve, 100)); }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox"] });
  const context = await browser.newContext(), page = await context.newPage(), errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const setup = await context.request.post(base + "/api/setup", { data: { setupToken: "text-actions-fixture", name: "Text QA", email: "text-qa@example.test", password: "Synthetic-text-actions-password-123", ixpName: "Text QA", asn: 64500 } }); assert.equal(setup.status(), 200, await setup.text());
  const session = await (await context.request.get(base + "/api/session")).json();
  const imported = await context.request.post(base + "/api/import/patching", { headers: { "X-CSRF-Token": session.csrfToken }, multipart: { bundle: { name: "synthetic.tar.gz", mimeType: "application/gzip", buffer: bundle } } }); assert.equal(imported.status(), 200, await imported.text());
  const listing = await (await context.request.get(base + "/api/documents")).json(); assert.equal(listing.items.length, 4);
  for (const [name, content] of contents) {
    const doc = listing.items.find((item) => item.originalName === name); assert.ok(doc?.legacyPatch && doc.readOnly);
    await page.goto(base + `/#documents?id=${doc.id}`);
    await page.getByRole("region", { name: "遗留文档说明" }).getByText(doc.name, { exact: true }).waitFor();
    const preview = page.getByRole("button", { name: `查看文本 ${doc.name}`, exact: true });
    assert.equal(await page.getByRole("button", { name: `替换内容 ${doc.name}`, exact: true }).count(), 0);
    assert.equal(await page.getByRole("button", { name: `编辑文本 ${doc.name}`, exact: true }).count(), 0);
    if (/\.(txt|md)$/.test(name)) {
      await preview.click(); const dialog = page.getByRole("dialog"); assert.equal(await dialog.getByLabel("文档文本内容").inputValue(), content); assert.equal(await dialog.getByLabel("文档文本内容").getAttribute("readonly"), ""); assert.equal(await dialog.getByRole("button", { name: "保存文本", exact: true }).count(), 0); await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    } else {
      assert.equal(await preview.count(), 0, `${name} should offer download but not unsupported text preview`);
      assert.equal((await context.request.get(base + `/api/documents/${doc.id}/text`)).status(), 415);
      const downloaded = page.waitForEvent("download"); await page.getByRole("button", { name: `下载 ${doc.name}`, exact: true }).click(); const download = await downloaded; assert.equal(await readFile(await download.path(), "utf8"), content);
    }
  }
  assert.deepEqual(errors, []); console.log("minimal text actions passed: legacy txt/md readonly preview, csv/log download-only, no editing controls");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { child.kill("SIGTERM"); await Promise.race([new Promise((resolve) => child.once("exit", resolve)), new Promise((resolve) => setTimeout(resolve, 5000))]); if (child.exitCode === null) { child.kill("SIGKILL"); await new Promise((resolve) => child.once("exit", resolve)); } }
  await rm(dir, { recursive: true, force: true }); console.log("minimal text fixture service/browser/data cleaned up");
}
