// Real browser downloads against an isolated Go service; no external systems.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "ixf-versions-"));
let child, browser, stderr = "";
try {
  const listener = net.createServer();
  await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist")], { env: { ...process.env, IXP_SETUP_TOKEN: "ixf-browser-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
  let ready = false;
  for (let i = 0; i < 200; i++) {
    if (child.exitCode !== null) throw new Error(stderr);
    if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) { ready = true; break; }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.ok(ready, "server startup timeout");
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 1200, height: 900 } }), page = await context.newPage(), errors = [];
  page.setDefaultTimeout(10000); page.on("pageerror", e => errors.push(e.message));
  const setup = await context.request.post(base + "/api/setup", { data: { setupToken: "ixf-browser-setup", name: "IX-F test", email: "admin@example.test", password: "IX-F-browser-test-password", ixpName: "IX-F test", asn: 64500 } });
  assert.equal(setup.status(), 200);
  const { csrfToken } = await setup.json();
  assert.equal((await context.request.put(base + "/api/settings", { headers: { "X-CSRF-Token": csrfToken }, data: { name: "Saved IXP", asn: 64500, shortName: "SAVED", ixfId: 42, publishIxf: false } })).status(), 200);
  await page.goto(base + "/#settings");
  await page.getByLabel("IX-F 导出格式", { exact: true }).waitFor();
  await page.getByLabel("交换中心名称", { exact: true }).fill("Unsaved IXP");
  for (const version of ["1.0", "0.7", "0.6"]) {
    await page.getByLabel("IX-F 导出格式", { exact: true }).selectOption(version);
    const downloading = page.waitForEvent("download");
    await page.getByRole("button", { name: "导出 IX-F", exact: true }).click();
    const download = await downloading, data = JSON.parse(await readFile(await download.path(), "utf8"));
    assert.equal(download.suggestedFilename(), `ixf-members-${version}.json`);
    assert.equal(data.version, version); assert.equal(data.ixp_list[0].name, "Saved IXP");
    assert.deepEqual(data.member_list, []);
    assert.equal((await fetch(base + `/api/public/ixf/${version}`)).status, 404, "format selection enabled public access");
  }
  console.log("PASS exact-version downloads use saved data and preserve public opt-out");
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "mobile overflow");
  if (process.env.IXF_SCREENSHOT) await page.screenshot({ path: process.env.IXF_SCREENSHOT, fullPage: true, animations: "disabled" });
  assert.deepEqual(errors, []);
  console.log("PASS 390px settings and no browser exceptions");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) {
    const exited = new Promise(resolve => child.once("exit", resolve));
    child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited; clearTimeout(timer);
  }
  await rm(dir, { recursive: true, force: true });
}
