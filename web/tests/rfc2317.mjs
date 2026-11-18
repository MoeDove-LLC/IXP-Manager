import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { isRFC2317Metadata, RFC2317_MERGE_KIND, sameRFC2317Metadata, verifyRFC2317Merge } from "../src/lib/rfc2317.ts";

const sha256 = data => createHash("sha256").update(data).digest("hex");
const encode = value => new TextEncoder().encode(JSON.stringify(value)).buffer;
const metadataOf = bundle => ({
  parentZone: bundle.review.input.parent.zone,
  childZone: bundle.review.input.childZone,
  previousParentSerial: bundle.previousParentSerial,
  parentSerial: bundle.parentSerial,
  childSerial: bundle.childSerial,
  newChildZone: bundle.review.input.newChildZone,
  previousChildSerial: bundle.review.input.previousChildSerial ?? undefined,
  addedRecords: bundle.addedRecords,
});

const kind = RFC2317_MERGE_KIND;
const parentZone = "2.0.192.in-addr.arpa.";
const childZone = `128-131.${parentZone}`;
const parentSnapshot = "; Source snapshot\n";
const fakeDigest = "a".repeat(64);
const makeFile = (path, zone, serial) => {
  const content = `$ORIGIN ${zone}\n@ IN SOA ns.example.test. hostmaster.example.test. (${serial} 3600 600 604800 300)\n`;
  return { path, zone, serial, complete: true, sha256: sha256(content), content };
};
const synthetic = {
  version: 1, kind, reviewSHA256: fakeDigest, parentSnapshot, parentSnapshotSHA256: sha256(parentSnapshot),
  previousParentSerial: 0xffffffff, parentSerial: 1, childSerial: 12, addedRecords: 6,
  review: {
    version: 1, kind: "dns-rfc2317-review", inputSHA256: fakeDigest, files: [],
    input: { childZone, newChildZone: false, previousChildSerial: 11, dns: { serial: 12 }, parent: { zone: parentZone, observedSerial: 0xffffffff } },
    approved: false, activated: false, parentIntegrated: false, parentSnapshotVerified: false,
  },
  files: [makeFile("child.zone", childZone, 12), makeFile("parent.zone", parentZone, 1)],
  approved: false, activated: false, liveParentVerified: false, parentSnapshotVerified: true,
};
const expected = metadataOf(synthetic);
assert.equal(isRFC2317Metadata(expected), true, "parent serial wrap is valid");
assert.equal(isRFC2317Metadata({ ...expected, previousChildSerial: 12 }), false);
assert.equal(isRFC2317Metadata({ ...expected, newChildZone: true, previousChildSerial: undefined }), true);
assert.equal(sameRFC2317Metadata(expected, { ...expected, childSerial: 13 }), false);
await verifyRFC2317Merge(expected, encode(synthetic));
const newChild = structuredClone(synthetic);
newChild.review.input.newChildZone = true;
newChild.review.input.previousChildSerial = null;
await verifyRFC2317Merge(metadataOf(newChild), encode(newChild));
for (const alter of [
  b => { b.kind = "dns-rfc2317-review"; },
  b => { b.version = 2; },
  b => { b.approved = true; },
  b => { b.activated = true; },
  b => { b.liveParentVerified = true; },
  b => { b.LiveParentVerified = true; },
  b => { b.parentSnapshotVerified = false; },
  b => { b.review.parentIntegrated = true; },
  b => { b.review.input.parent.observedSerial = 123; },
  b => { b.review.input.dns.serial++; },
  b => { b.review.input.previousChildSerial--; },
  b => { b.addedRecords++; },
  b => { b.files.pop(); },
  b => { b.files[1] = b.files[0]; },
  b => { b.files[0].path = "../child.zone"; },
  b => { b.files[0].zone = parentZone; },
  b => { b.files[1].serial++; },
  b => { b.files[0].complete = false; },
  b => { b.files[0].content += "; tampered child\n"; },
  b => { b.files[1].content += "; tampered parent\n"; },
]) {
  const candidate = structuredClone(synthetic);
  alter(candidate);
  await assert.rejects(verifyRFC2317Merge(expected, encode(candidate)));
}
await assert.rejects(verifyRFC2317Merge(expected, new Uint8Array([255]).buffer));
console.log("PASS RFC2317 paired manifest, independent SHA-256 checks, serial wrap and typed metadata binding");

if (process.env.RFC2317_UNIT_ONLY !== "1") {
  const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright");
  const fixture = await readFile(process.env.TEST_RFC2317_MERGE_FILE);
  const canonical = JSON.parse(fixture);
  const pair = metadataOf(canonical);
  await verifyRFC2317Merge(pair, new Uint8Array(fixture).buffer);
  const dir = await mkdtemp(path.join(tmpdir(), "ixp-rfc2317-ui-"));
  const host = "127.0.0.1";
  const setupToken = "rfc2317-disposable-setup";
  const password = "RFC2317-offline-fixture-password";
  const errors = [];
  let manager, browser;
  try {
    const reserve = net.createServer();
    await new Promise(resolve => reserve.listen(0, host, resolve));
    const port = reserve.address().port;
    await new Promise(resolve => reserve.close(resolve));
    const base = `http://${host}:${port}`;
    manager = spawn(process.env.TEST_SERVER_BIN, ["-listen", `${host}:${port}`, "-data", dir,
      "-web", fileURLToPath(new URL("../dist", import.meta.url)), "-named-checkzone", process.env.NAMED_CHECKZONE_BIN],
      { env: { ...process.env, IXP_SETUP_TOKEN: setupToken }, stdio: "ignore" });
    for (let attempt = 0; ; attempt++) {
      if (manager.exitCode !== null || attempt === 150) throw new Error("RFC 2317 fixture manager failed to start");
      if (await fetch(`${base}/api/setup`).then(response => response.ok).catch(() => false)) break;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
    const context = () => browser.newContext({ viewport: { width: 390, height: 844 }, acceptDownloads: true });
    const admin = await context();
    const setup = await admin.request.post(`${base}/api/setup`, { data: { setupToken, name: "RFC 2317 QA", email: "admin@example.test", password, ixpName: "RFC2317 QA", asn: 64500 } });
    assert.equal(setup.status(), 200);
    const call = async (ctx, route, data) => {
      const { csrfToken } = await (await ctx.request.get(`${base}/api/session`)).json();
      return ctx.request.fetch(`${base}/api${route}`, {
        method: data === undefined ? "GET" : "POST", data, headers: { "X-CSRF-Token": csrfToken || "" },
      });
    };
    const create = async (route, data) => {
      const response = await call(admin, route, data);
      assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`);
      return response.json();
    };
    const login = async role => {
      const email = `${role}@example.test`;
      await create("/resources/users", { name: role, email, password, role });
      const ctx = await context();
      assert.equal((await ctx.request.post(`${base}/api/login`, { data: { email, password } })).status(), 200);
      return ctx;
    };
    const operator = await login("operator");
    const viewer = await login("viewer");
    const profile = await create("/resources/provisioning-profiles", { name: "其他版本", options: { kind: "nagios", families: [4, 6] } });
    const other = await create("/provisioning", { profileId: profile.id });
    const open = async ctx => {
      const page = await ctx.newPage();
      page.on("pageerror", error => errors.push(error.message));
      await page.goto(`${base}/#provisioning`);
      await page.getByRole("heading", { name: "配置工具", exact: true }).waitFor();
      return page;
    };
    const operatorPage = await open(operator);
    await operatorPage.getByRole("button", { name: "生成规则", exact: true }).click();
    await operatorPage.getByRole("button", { name: "新增生成规则", exact: true }).first().click();
    const profileDialog = operatorPage.getByRole("dialog");
    assert.equal(await profileDialog.locator(`#profile-kind option[value="${kind}"]`).count(), 0, "import-only kind was offered as a generation rule");
    await profileDialog.getByRole("button", { name: "取消", exact: true }).click();
    await operatorPage.getByRole("button", { name: "生成版本", exact: true }).click();
    await operatorPage.getByRole("button", { name: "版本差异", exact: true }).click();
    await operatorPage.getByRole("button", { name: "导入 RFC 2317 合并包", exact: true }).click();
    const importDialog = operatorPage.getByRole("dialog");
    const file = importDialog.getByLabel("RFC 2317 合并包", { exact: true });
    await file.setInputFiles({ name: "merge.json", mimeType: "application/json", buffer: fixture });
    const importing = operatorPage.waitForResponse(response => response.url().endsWith("/api/provisioning/rfc2317-import"));
    await importDialog.getByRole("button", { name: "创建待审版本", exact: true }).click();
    const imported = await importing;
    assert.equal(imported.status(), 201);
    assert.equal(imported.request().headers()["content-type"], "application/json");
    assert.ok(imported.request().headers()["x-csrf-token"]);
    // Chromium may omit a Blob request's body from Playwright metadata. The
    // successful canonical import and final exact download prove the bytes.
    const visibleBody = imported.request().postDataBuffer();
    if (visibleBody) assert.ok(visibleBody.equals(fixture));
    const draft = await imported.json();
    assert.equal(draft.status, "draft");
    assert.equal(draft.profileId, "rfc2317");
    assert.equal(sameRFC2317Metadata(draft.rfc2317, pair), true);
    await importDialog.waitFor({ state: "hidden" });
    await operatorPage.getByLabel("RFC 2317 父区与子区", { exact: true }).waitFor();
    assert.equal(await operatorPage.getByRole("button", { name: "版本差异", exact: true }).count(), 0);
    const preview = await operatorPage.locator("pre code").textContent();
    const childRecords = canonical.files.find(file => file.path === "child.zone").content.split("\n").filter(line => line && !line.startsWith(";"));
    assert.ok(childRecords.every(line => preview.includes(`${line}\n`)));
    assert.ok(preview.includes(`; Parent SOA ${pair.previousParentSerial} -> ${pair.parentSerial}`) && preview.includes("CNAME"));
    assert.ok(!preview.includes(canonical.parentSnapshot) && !preview.includes(canonical.parentSnapshotSHA256));
    for (const line of canonical.parentSnapshot.split("\n").filter(line => line.includes("unrelated"))) {
      assert.ok(!preview.includes(line), "a source-only parent record appeared in the preview");
    }
    assert.equal(await operatorPage.getByRole("button", { name: "下载已批准区包", exact: true }).isDisabled(), true);
    const revision = `/provisioning/${draft.id}`;
    const validating = operatorPage.waitForResponse(response => response.url().endsWith(`${revision}/validate`));
    await operatorPage.getByRole("button", { name: "原生校验父区和子区", exact: true }).click();
    assert.equal((await validating).status(), 200);
    assert.equal((await call(operator, `${revision}/approve`, { checksum: draft.checksum })).status(), 403);
    assert.equal(await operatorPage.getByRole("button", { name: "审核批准", exact: true }).count(), 0);
    console.log("PASS operator byte-exact CSRF import, safe paired preview and native validation; approval restricted to admin");

    const page = await open(admin);
    const select = id => page.locator(`[data-provisioning-id="${id}"]`).click();
    await select(draft.id);
    await page.getByRole("button", { name: "审核批准", exact: true }).click();
    const approval = page.getByRole("dialog", { name: "联合批准父区和子区？", exact: true });
    assert.ok((await approval.getByLabel("RFC 2317 父区与子区").innerText()).includes(pair.childZone));
    await approval.getByRole("checkbox").check();
    const approving = page.waitForResponse(response => response.url().endsWith(`${revision}/approve`));
    await approval.getByRole("button", { name: "确认审核批准", exact: true }).click();
    const approved = await approving;
    assert.equal(approved.status(), 200);
    assert.deepEqual(approved.request().postDataJSON(), { checksum: draft.checksum });
    await approval.waitFor({ state: "hidden" });

    const download = page.getByRole("button", { name: "下载已批准区包", exact: true });
    const enabledDownload = download.and(page.locator(":enabled"));
    let downloads = 0;
    page.on("download", () => ++downloads);
    const saved = page.waitForEvent("download");
    await enabledDownload.click();
    const downloaded = await saved;
    assert.match(downloaded.suggestedFilename(), /\.json$/);
    assert.ok((await readFile(await downloaded.path())).equals(fixture), "download did not match the canonical import bytes");
    await page.getByText("已核对合并包和两份区文件的 SHA-256", { exact: true }).waitFor();
    assert.equal(await page.getByLabel("工具配置真实 SHA-256").innerText(), sha256(fixture));
    if (process.env.RFC2317_SCREENSHOT) await page.screenshot({ path: process.env.RFC2317_SCREENSHOT, fullPage: true, animations: "disabled" });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));

    const endpoint = `${base}/api${revision}/config`;
    await page.route(endpoint, async route => {
      const actual = await route.fetch();
      const body = await actual.json();
      body.files.find(file => file.path === "parent.zone").content += "; changed without updating file SHA\n";
      const raw = JSON.stringify(body);
      const headers = { ...actual.headers(), "x-config-sha256": sha256(raw) };
      delete headers["content-length"];
      await route.fulfill({ response: actual, headers, body: raw });
    }, { times: 1 });
    await enabledDownload.click();
    await page.getByText("RFC 2317 下载包的区名、序列号或完整文件校验不匹配，未保存文件。", { exact: true }).waitFor();
    assert.equal(downloads, 1);
    await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click();
    await enabledDownload.waitFor();

    let arrival, release, complete;
    const arrived = new Promise(resolve => { arrival = resolve; });
    const allowed = new Promise(resolve => { release = resolve; });
    const completed = new Promise(resolve => { complete = resolve; });
    await page.route(endpoint, async route => {
      try {
        const actual = await route.fetch();
        arrival(actual.status());
        await allowed;
        await route.fulfill({ response: actual });
      } catch { arrival(0); }
      finally { complete(); }
    }, { times: 1 });
    await enabledDownload.click();
    assert.equal(await arrived, 200);
    try {
      await select(other.id);
      await page.getByRole("heading", { name: "其他版本", exact: true }).waitFor();
    } finally { release(); }
    await completed;
    await select(draft.id);
    await enabledDownload.waitFor();
    assert.equal(downloads, 1, "late response downloaded a previous selection");

    const viewerPage = await open(viewer);
    await viewerPage.getByLabel("RFC 2317 父区与子区", { exact: true }).waitFor();
    assert.equal(await viewerPage.getByRole("button", { name: "导入 RFC 2317 合并包", exact: true }).count(), 0);
    assert.equal(await viewerPage.getByRole("button", { name: "下载已批准区包", exact: true }).count(), 0);
    assert.equal((await call(viewer, `${revision}/config`)).status(), 403);
    assert.deepEqual(errors, []);
    console.log("PASS admin joint approval, byte-exact paired download, tampered file rejection, late response suppression, viewer boundary and 390 px layout");
  } finally {
    if (browser) await browser.close();
    if (manager && manager.exitCode === null && manager.signalCode === null) {
      const exited = new Promise(resolve => manager.once("exit", resolve));
      manager.kill("SIGTERM");
      const deadline = setTimeout(() => manager.kill("SIGKILL"), 5000);
      await exited;
      clearTimeout(deadline);
    }
    await rm(dir, { recursive: true, force: true });
  }
}
