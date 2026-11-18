// Verify configuration generation and native syntax checking in a private fixture.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const directory = await mkdtemp(path.join(tmpdir(), "moedove-bird1-browser-"));
const host = "127.0.0.1";
const setupToken = "bird1-browser-fixture-setup";
let child, browser, stderr = "";

try {
  assert.ok(process.env.TEST_SERVER_BIN && process.env.BIRD1_IPV6_BIN, "provide the fixture manager and actual BIRD 1 bird6 executable");
  const reservation = net.createServer();
  await new Promise(resolve => reservation.listen(0, host, resolve));
  const port = reservation.address().port;
  await new Promise(resolve => reservation.close(resolve));
  const base = `http://${host}:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, [
    "-listen", `${host}:${port}`, "-data", path.join(directory, "data"), "-web", path.resolve("dist"),
    "-bird1", "/missing/bird1", "-bird1-ipv6", process.env.BIRD1_IPV6_BIN,
  ], { env: { ...process.env, IXP_SETUP_TOKEN: setupToken }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", data => { stderr = (stderr + data).slice(-4000); });
  let ready = false;
  for (let attempt = 0; attempt < 150; attempt++) {
    if (child.exitCode !== null) throw new Error(`manager exited: ${stderr}`);
    if (await fetch(`${base}/api/setup`).then(response => response.ok).catch(() => false)) {
      ready = true;
      break;
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.ok(ready, "manager startup timeout");

  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const setup = await context.request.post(`${base}/api/setup`, { data: {
    setupToken, name: "BIRD 1 fixture", email: "bird1-fixture@example.test", password: "BIRD1-browser-fixture-password", ixpName: "BIRD 1 fixture IXP", asn: 64500,
  } });
  assert.equal(setup.status(), 200, await setup.text());
  const { csrfToken } = await setup.json();
  const api = async (url, data) => {
    const response = await context.request.fetch(`${base}/api${url}`, { method: data ? "POST" : "GET", headers: { "X-CSRF-Token": csrfToken }, data });
    assert.ok(response.ok(), `${url}: ${response.status()} ${await response.text()}`);
    return response.json();
  };
  const capability = (await api("/router-platforms")).items.find(row => row.platform === "bird1");
  assert.ok(capability.render && capability.validate && capability.fetch && !capability.apply && !capability.rollback);
  assert.deepEqual(capability.nativeCheckerFamilies, { ipv4: false, ipv6: true });
  const vlan = await api("/resources/vlans", { name: "Browser fixture LAN", tag: 100, ipv4Prefix: "192.0.2.0/24", ipv6Prefix: "2001:db8::/64" });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  page.setDefaultTimeout(10000);
  await page.goto(`${base}/#routers`);
  await page.locator("main .page-heading").getByRole("button", { name: "新增路由服务器", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.locator("#field-name").fill("BIRD 1 IPv6 fixture");
  await dialog.locator("#field-platform").selectOption("bird1");
  await dialog.locator("#field-asn").fill("64500");
  await dialog.locator("#field-routerId").fill("192.0.2.1");
  await dialog.locator("#field-vlanId").selectOption(vlan.id);
  await dialog.locator("#field-localIpv4").fill("192.0.2.1");
  await dialog.locator("#field-localIpv6").fill("2001:db8::1");
  const save = dialog.getByRole("button", { name: "保存", exact: true });
  await dialog.getByRole("alert").filter({ hasText: "每个实例只使用一个地址族" }).waitFor();
  assert.equal(await save.isDisabled(), true, "keep both entered addresses visible and block the incompatible combination");
  await dialog.locator("#field-localIpv4").clear();
  await dialog.getByText(/校验需使用实际的 BIRD 1 bird6/).waitFor();
  const saving = page.waitForResponse(response => response.url().endsWith("/api/resources/routers") && response.request().method() === "POST");
  await save.click();
  const created = await saving;
  assert.equal(created.status(), 201, await created.text());
  const ipv6 = await created.json();
  assert.equal(ipv6.localIpv6, "2001:db8::1");
  assert.equal(ipv6.localIpv4 ?? "", "");
  await dialog.waitFor({ state: "hidden" });
  const ipv4 = await api("/resources/routers", { platform: "bird1", name: "BIRD 1 IPv4 fixture", asn: 64500, routerId: "192.0.2.2", localIpv4: "192.0.2.2", vlanId: vlan.id });

  const v4 = await api("/deployments", { routerId: ipv4.id });
  const v6 = await api("/deployments", { routerId: ipv6.id });
  await page.goto(`${base}/#deployments`);
  const region = page.getByRole("region", { name: "版本发布就绪检查", exact: true });
  const validate = page.getByRole("button", { name: "校验配置", exact: true });
  await page.locator(`[data-deployment-id="${v4.id}"]`).click();
  await region.getByText(/此版本使用 IPv4/).waitFor();
  await region.getByText("尚未找到此平台检查器，不能开始原生校验。", { exact: true }).waitFor();
  assert.equal(await validate.isDisabled(), true, "IPv4 checker must not fall back to bird6");

  await page.locator(`[data-deployment-id="${v6.id}"]`).click();
  await region.getByText(/此版本使用 IPv6/).waitFor();
  await region.getByText("已找到检查器，仍须点击校验才能取得该版本的实际解析结果。", { exact: true }).waitFor();
  const validation = page.waitForResponse(response => response.url().endsWith(`/api/deployments/${v6.id}/validate`));
  await validate.click();
  const validated = await validation;
  assert.equal(validated.status(), 200, await validated.text());
  await page.getByRole("button", { name: "批准版本", exact: true }).click();
  await dialog.getByText(/BIRD 1 的在线应用与回退仍由目标机运维流程负责/).waitFor();
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  const downloading = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载配置", exact: true }).click();
  const bytes = await readFile(await (await downloading).path(), "utf8");
  assert.ok(bytes.includes("router id 192.0.2.1;"));
  assert.ok(!bytes.includes("protocol kernel"));
  assert.deepEqual(errors, []);
  if (process.env.BIRD1_SCREENSHOT) await page.screenshot({ path: process.env.BIRD1_SCREENSHOT, fullPage: true });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "configuration view fits 390px viewport");
  console.log("PASS BIRD 1 single-family form, no checker fallback, real bird6 syntax validation, approval and download");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = new Promise(resolve => child.once("exit", resolve));
    child.kill("SIGTERM");
    const deadline = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(deadline);
  }
  await rm(directory, { recursive: true, force: true });
}
