// Actual server-backed import review in a synthetic loopback fixture.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "ixp-import-preview-"));
const host = "127.0.0.1";
const setupToken = "import-review-fixture-setup";
let server, browser;
try {
  const reservation = net.createServer();
  await new Promise(resolve => reservation.listen(0, host, resolve));
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const base = `http://${host}:${port}`;
  server = spawn(process.env.TEST_SERVER_BIN, ["-listen", `${host}:${port}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist")], {
    env: { ...process.env, IXP_SETUP_TOKEN: setupToken }, stdio: "ignore",
  });
  let ready = false;
  for (let attempt = 0; attempt < 150; attempt++) {
    if (server.exitCode !== null) throw new Error("fixture service exited before startup");
    if (await fetch(`${base}/api/setup`).then(r => r.ok).catch(() => false)) {
      ready = true;
      break;
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.ok(ready);
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const setup = await context.request.post(`${base}/api/setup`, { data: {
    setupToken, name: "Fixture admin", email: "import@example.test", password: "Import-browser-fixture-password", ixpName: "Import fixture", asn: 64500,
  } });
  assert.equal(setup.status(), 200);
  const { csrfToken } = await setup.json();
  const member = (id, asn) => ({ id, name: id, asn, status: "active" });
  const payload = rows => ({ version: 1, resources: { members: rows }, warnings: ["请核对旧来源备注"] });
  const importDirect = async rows => {
    const response = await context.request.post(`${base}/api/import`, { headers: { "X-CSRF-Token": csrfToken }, data: payload(rows) });
    assert.equal(response.status(), 200, await response.text());
  };
  const memberIds = async () => {
    const response = await context.request.get(`${base}/api/resources/members`);
    assert.equal(response.status(), 200);
    return (await response.json()).items.map(item => item.id).sort();
  };
  await importDirect([member("already-present", 64501)]);
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`${base}/#settings`);
  const choose = page.getByLabel("选择导入文件", { exact: true });
  const dialog = page.getByRole("dialog");
  const commit = dialog.getByRole("button", { name: "确认导入", exact: true });
  const upload = async rows => choose.setInputFiles({ name: "fixture-inventory.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(payload(rows))) });
  await upload([member("already-present", 64501), member("must-not-be-created", 64502)]);
  await dialog.getByRole("alert").filter({ hasText: "duplicate id" }).waitFor();
  assert.ok(await commit.isDisabled());
  assert.deepEqual(await memberIds(), ["already-present"]);
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });

  await upload([member("racing-member", 64503), member("still-must-not-be-created", 64504)]);
  await dialog.getByText("2 条资源通过预检。", { exact: true }).waitFor();
  assert.deepEqual(await memberIds(), ["already-present"]);
  await importDirect([member("racing-member", 64503)]);
  await commit.click();
  await dialog.getByRole("alert").filter({ hasText: "duplicate id" }).waitFor();
  assert.ok(await commit.isDisabled());
  assert.deepEqual(await memberIds(), ["already-present", "racing-member"]);
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });

  await upload([member("reviewed-member", 64505)]);
  await dialog.getByText("1 条资源通过预检。", { exact: true }).waitFor();
  await dialog.getByText("请核对旧来源备注", { exact: true }).waitFor();
  if (process.env.IMPORT_PREVIEW_SCREENSHOT) await page.screenshot({ path: process.env.IMPORT_PREVIEW_SCREENSHOT });
  assert.ok(await dialog.evaluate(element => element.getBoundingClientRect().width <= innerWidth));
  await commit.click();
  await page.getByText("成功导入 1 条记录。", { exact: true }).waitFor();
  assert.deepEqual(await memberIds(), ["already-present", "racing-member", "reviewed-member"]);
  assert.deepEqual(errors, []);
  console.log("PASS server-backed preview detects conflicts, commit rechecks concurrent changes, valid mobile flow imports after confirmation");
} finally {
  if (browser) await browser.close();
  if (server && server.exitCode === null && server.signalCode === null) {
    const exited = new Promise(resolve => server.once("exit", resolve));
    server.kill("SIGTERM");
    const deadline = setTimeout(() => server.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(deadline);
  }
  await rm(dir, { recursive: true, force: true });
}
