// Real browser and local synthetic database, with an optional isolated Quagga parser.
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp(path.join(tmpdir(), "deployment-readiness-"));
const password = "Readiness-browser-fixture-123";
const quaggaChecker = process.env.QUAGGA_BGPD_BIN;
let child, browser, stderr = "";
const eventually = async (check, message) => {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (await check()) return;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.fail(message);
};
try {
  const listener = net.createServer();
  await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN || path.join(repo, ".cache/readiness-service/ixp-manager"), [
    "-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web/dist"),
    "-bird", "/missing/bird2", "-bird3", "/missing/bird3", "-frr", "/missing/bgpd", "-openbgpd", "/missing/openbgpd", "-quagga-bgpd", quaggaChecker || "/missing/quagga-bgpd",
  ], { env: { ...process.env, IXP_SETUP_TOKEN: "readiness-setup-fixture" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
  for (let i = 0; ; i++) {
    if (child.exitCode !== null) throw new Error(`fixture service stopped: ${stderr}`);
    if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break;
    if (i > 100) throw new Error("fixture startup timeout");
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  const page = await admin.newPage(), errors = [];
  page.setDefaultTimeout(10000);
  page.on("pageerror", error => errors.push(error.message));
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "readiness-setup-fixture", name: "Readiness QA", email: "admin@example.test", password, ixpName: "Readiness QA", asn: 64500 } });
  assert.equal(setup.status(), 200, await setup.text());
  const call = async (ctx, route, data, method = data ? "POST" : "GET", etag) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + route, { method, headers: { "X-CSRF-Token": session.csrfToken || "", ...(etag ? { "If-Match": JSON.stringify(etag) } : {}) }, ...(data ? { data } : {}) });
    assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`);
    return response.json();
  };
  const vlan = await call(admin, "/resources/vlans", { name: "Readiness LAN", tag: 100, ipv4Prefix: "192.0.2.0/24" });
  const fixtures = {};
  for (const [i, platform] of ["bird2", "bird3", "frr", "openbgpd", "routeros7", "junos", "quagga"].entries()) {
    const router = await call(admin, "/resources/routers", { name: `${platform} readiness`, platform, asn: 64500, routerId: `192.0.2.${i + 1}`, localIpv4: `192.0.2.${i + 1}`, vlanId: vlan.id });
    fixtures[platform] = { router, deployment: await call(admin, "/deployments", { routerId: router.id }) };
  }
  const region = page.getByRole("region", { name: "版本发布就绪检查", exact: true });
  const download = page.getByRole("button", { name: "下载配置", exact: true });
  const select = async id => {
    await page.locator(`[data-deployment-id="${id}"]`).click();
    await region.getByText("当前输入一致", { exact: true }).waitFor();
    await page.locator("main pre code").waitFor();
  };
  const refresh = async () => {
    await region.getByRole("button", { name: "重新检查", exact: true }).click();
    await region.getByText("当前输入一致", { exact: true }).waitFor();
  };
  await page.goto(base + "/#deployments");
  await page.getByRole("heading", { name: "配置中心", exact: true }).waitFor();
  for (const platform of ["bird2", "bird3", "frr", "openbgpd"]) {
    await select(fixtures[platform].deployment.id);
    await region.getByText("原生语法校验流程", { exact: true }).waitFor();
    await region.getByText("尚未找到此平台检查器，不能开始原生校验。", { exact: true }).waitFor();
    assert.equal(await page.getByRole("button", { name: "校验配置", exact: true }).isDisabled(), true);
    assert.equal(await download.isDisabled(), true);
    assert.ok((await region.innerText()).includes(platform.startsWith("bird") ? "限时配置确认" : "本机看护服务"));
  }
  await select(fixtures.routeros7.deployment.id);
  await region.getByText("仅预览", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "校验配置", exact: true }).isDisabled(), true);
  assert.equal(await download.isDisabled(), true);
  console.log("PASS native checker absence and RouterOS preview boundaries");

  const quagga = fixtures.quagga.deployment;
  await select(quagga.id);
  await region.getByText(/同一 UID 和私密 Unix socket.*ixp-agent stage.*中断 BGP 会话/).waitFor();
  await region.getByText(/GR、TCP-MD5 握手和生产目标仍待验收/).waitFor();
  assert.equal(await download.isDisabled(), true);
  assert.equal(await page.getByRole("button", { name: "应用配置", exact: true }).count(), 0);
  assert.equal(await page.getByRole("button", { name: "回退配置", exact: true }).count(), 0);
  const quaggaCapability = (await call(admin, "/router-platforms")).items.find(item => item.platform === "quagga");
  assert.equal(quaggaCapability.apply, false);
  assert.equal(quaggaCapability.rollback, false);
  const quaggaValidate = page.getByRole("button", { name: "校验配置", exact: true });
  if (quaggaChecker) {
    await eventually(() => quaggaValidate.isEnabled(), "Quagga validation action stayed disabled");
    const validatingQuagga = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${quagga.id}/validate`));
    await quaggaValidate.click();
    assert.equal((await validatingQuagga).status(), 200);
    await page.getByRole("button", { name: "批准版本", exact: true }).click();
    const approvalDialog = page.getByRole("dialog");
    await approvalDialog.getByText(/Quagga 由目标机本机守护程序处理。暂存和回退都会中断 BGP 会话/).waitFor();
    const approvingQuagga = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${quagga.id}/approve`));
    await approvalDialog.getByRole("button", { name: "确认批准", exact: true }).click();
    assert.equal((await approvingQuagga).status(), 200);
    await eventually(() => download.isEnabled(), "approved Quagga file stayed unavailable");
    const servingQuagga = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${quagga.id}/config`));
    const savingQuagga = page.waitForEvent("download");
    await download.click();
    const quaggaBytes = await readFile(await (await savingQuagga).path());
    assert.ok(quaggaBytes.toString().includes("view MOEDOVE_QUAGGA"));
    assert.equal(createHash("sha256").update(quaggaBytes).digest("hex"), (await servingQuagga).headers()["x-config-sha256"]);
    await region.getByText(/可以交付配置文件，尚未验证或应用到目标设备/).waitFor();
    const currentQuaggaReadiness = await call(admin, `/deployments/${quagga.id}/readiness`);
    assert.equal(currentQuaggaReadiness.activationMode, "quagga-local-watchdog");
    assert.equal(currentQuaggaReadiness.targetChecked, false);
    assert.deepEqual(Object.keys(currentQuaggaReadiness.actions).sort(), ["approve", "download", "generate", "validate"]);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "Quagga readiness overflowed 390px viewport");
    if (process.env.QUAGGA_READINESS_SCREENSHOT) {
      await region.scrollIntoViewIfNeeded();
      await page.evaluate(() => document.fonts.ready);
      await region.screenshot({ path: process.env.QUAGGA_READINESS_SCREENSHOT, animations: "disabled" });
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    console.log("PASS actual Quagga validation, approved exact download and local-only watchdog guidance");
  } else {
    await region.getByText(/请在管理服务用.*--quagga-bgpd/).waitFor();
    assert.equal(await quaggaValidate.isDisabled(), true);
    console.log("PASS Quagga missing checker and local-only watchdog guidance");
  }

  const original = fixtures.junos.deployment;
  await select(original.id);
  await region.getByText("仅结构检查", { exact: true }).waitFor();
  assert.ok((await region.innerText()).includes("candidate validate 和 confirmed-commit"));
  assert.equal(await region.getByText(/原生语法|已找到检查器/).count(), 0);
  await eventually(() => page.getByRole("button", { name: "检查配置结构", exact: true }).isEnabled(), "Junos structure action stayed disabled");
  const validation = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${original.id}/validate`));
  await page.getByRole("button", { name: "检查配置结构", exact: true }).click();
  assert.equal((await validation).status(), 200);
  await eventually(() => page.getByRole("button", { name: "批准版本", exact: true }).isEnabled(), "Junos approval stayed disabled");
  await page.getByRole("button", { name: "批准版本", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByText(/当前校验仅检查 XML 结构/).waitFor();
  const approval = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${original.id}/approve`));
  await dialog.getByRole("button", { name: "确认批准", exact: true }).click();
  assert.equal((await approval).status(), 200);
  await eventually(() => download.isEnabled(), "approved Junos download stayed disabled");
  const saving = page.waitForEvent("download");
  await download.click();
  const saved = await readFile(await (await saving).path(), "utf8");
  assert.ok(saved.includes("<configuration") && saved.includes("192.0.2.6"));
  await page.getByLabel("下载文件真实 SHA-256", { exact: true }).waitFor();
  console.log("PASS actual Junos structure validation, explicit approval and verified download");

  // Every failed refresh invalidates old permission projections until a new check.
  const readinessURL = base + `/api/deployments/${original.id}/readiness`;
  await page.route(readinessURL, route => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "readiness fixture unavailable" }) }), { times: 1 });
  await region.getByRole("button", { name: "重新检查", exact: true }).click();
  await region.getByText(/当前版本的校验、批准和下载已停用/).waitFor();
  assert.equal(await download.isDisabled(), true);
  await refresh(); await eventually(() => download.isEnabled(), "readiness retry did not restore current download");
  await page.route(base + "/api/deployments", route => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "version list fixture unavailable" }) }), { times: 1 });
  await page.getByRole("button", { name: "刷新配置版本", exact: true }).click();
  await page.getByText("version list fixture unavailable", { exact: true }).waitFor();
  assert.equal(await download.isDisabled(), true, "failed list refresh reused old Ready");
  await refresh(); await eventually(() => download.isEnabled(), "explicit check did not recover list refresh failure");

  let releaseReload, observedReload, finishedReload;
  const reloadGate = new Promise(resolve => { releaseReload = resolve; });
  const reloadStarted = new Promise(resolve => { observedReload = resolve; });
  const reloadFinished = new Promise(resolve => { finishedReload = resolve; });
  await page.route(readinessURL, async route => {
    const response = await route.fetch(); observedReload(); await reloadGate;
    await route.fulfill({ response }).catch(() => {}); finishedReload();
  }, { times: 1 });
  await region.getByRole("button", { name: "重新检查", exact: true }).click();
  await reloadStarted;
  await page.route(base + "/api/deployments", route => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "crossed reload fixture unavailable" }) }), { times: 1 });
  await page.getByRole("button", { name: "刷新配置版本", exact: true }).click();
  await page.getByText("crossed reload fixture unavailable", { exact: true }).waitFor();
  releaseReload(); await reloadFinished;
  // Wait for the fulfilled old fetch and React's effect turn, not a long timeout.
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await download.isDisabled(), true, "late readiness restored authority after list refresh failed");
  await region.getByText("尚未取得当前检查结果。", { exact: true }).waitFor();
  await refresh(); await eventually(() => download.isEnabled(), "fresh check did not recover crossed reload failure");

  // A valid readiness document cannot authorize a mismatched preview or vice versa.
  await page.route(base + `/api/deployments/${original.id}`, async route => {
    const response = await route.fetch(), body = await response.json();
    await route.fulfill({ response, json: { ...body, checksum: "wrong-review-identifier" } });
  }, { times: 1 });
  await refresh();
  await page.getByText("配置预览未与选中版本的目标和审阅标识绑定，已禁用后续操作。", { exact: true }).waitFor();
  assert.equal(await download.isDisabled(), true);
  await refresh(); await eventually(() => download.isEnabled(), "matching detail did not recover download");
  await page.route(readinessURL, async route => {
    const response = await route.fetch(), body = await response.json();
    await route.fulfill({ response, json: { ...body, reviewIdentifier: "wrong-review-identifier" } });
  }, { times: 1 });
  await region.getByRole("button", { name: "重新检查", exact: true }).click();
  await region.getByText(/就绪检查未与当前版本正确绑定/).waitFor();
  assert.equal(await download.isDisabled(), true);
  await refresh(); await eventually(() => download.isEnabled(), "matching readiness did not recover download");

  let release, observed;
  const gate = new Promise(resolve => { release = resolve; }), started = new Promise(resolve => { observed = resolve; });
  await page.route(readinessURL, async route => {
    const response = await route.fetch(); observed(); await gate;
    await route.fulfill({ response }).catch(() => {}); // Switching aborts the old browser fetch.
  }, { times: 1 });
  await region.getByRole("button", { name: "重新检查", exact: true }).click();
  await started; assert.equal(await download.isDisabled(), true, "pending refresh reused Ready");
  await select(fixtures.bird2.deployment.id);
  release();
  await region.getByText("尚未找到此平台检查器，不能开始原生校验。", { exact: true }).waitFor();
  assert.equal(await download.isDisabled(), true);
  assert.equal(await region.getByText(/此版本目前可以交付配置文件/).count(), 0);
  await select(original.id); await eventually(() => download.isEnabled(), "reselection did not load current readiness");
  console.log("PASS failed requests, current preview/readiness binding and late previous-version responses");

  // The execution endpoint independently detects changes after a successful check.
  const router = await call(admin, `/resources/routers/${fixtures.junos.router.id}`);
  await call(admin, `/resources/routers/${router.id}`, { name: router.name, platform: router.platform, asn: router.asn, localIpv4: router.localIpv4, vlanId: router.vlanId, routerId: "192.0.2.99" }, "PUT", router._etag);
  const staleDownload = page.waitForResponse(r => r.url().endsWith(`/api/deployments/${original.id}/config`));
  await download.click(); assert.equal((await staleDownload).status(), 409);
  await eventually(() => download.isDisabled(), "execution failure left download enabled");
  await region.getByRole("button", { name: "重新检查", exact: true }).click();
  await region.getByText("输入已变化", { exact: true }).waitFor();
  assert.equal(await download.isDisabled(), true);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "readiness page overflowed 390px viewport");
  assert.ok((await region.boundingBox()).width >= 250, "mobile readiness panel collapsed");
  if (process.env.READINESS_SCREENSHOT) await page.screenshot({ path: process.env.READINESS_SCREENSHOT, fullPage: true, animations: "disabled" });
  const creating = page.waitForResponse(r => r.url().endsWith("/api/deployments") && r.request().method() === "POST");
  await region.getByRole("button", { name: "按当前输入生成新版本", exact: true }).click();
  assert.equal((await creating).status(), 201);
  await region.getByText("当前输入一致", { exact: true }).waitFor();
  await eventually(() => page.getByRole("button", { name: "检查配置结构", exact: true }).isEnabled(), "new Junos draft not ready for structural review");
  const versions = (await call(admin, "/deployments")).items;
  assert.equal(versions.find(v => v.id === original.id).status, "approved");
  assert.equal(versions.filter(v => v.routerId === original.routerId).length, 2);
  assert.equal(await download.isDisabled(), true);
  console.log("PASS execution recheck, stale input recovery, preserved history and 390px layout");

  await call(admin, "/resources/users", { name: "Read-only QA", email: "viewer@example.test", role: "viewer", password });
  const member = await call(admin, "/resources/members", { name: "Readiness member", asn: 64501, status: "active" });
  await call(admin, "/resources/users", { name: "Member QA", email: "member@example.test", role: "member", memberId: member.id, password });
  const login = async email => {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const response = await ctx.request.post(base + "/api/login", { data: { email, password } });
    assert.equal(response.status(), 200, await response.text()); return ctx;
  };
  const viewer = await login("viewer@example.test"), viewerPage = await viewer.newPage();
  viewerPage.on("pageerror", error => errors.push(error.message));
  await viewerPage.goto(base + "/#deployments");
  await viewerPage.getByRole("region", { name: "版本发布就绪检查", exact: true }).getByText("当前输入一致", { exact: true }).waitFor();
  for (const name of ["下载配置", "检查配置结构", "批准版本", "生成配置", "按当前输入生成新版本"]) assert.equal(await viewerPage.getByRole("button", { name, exact: true }).count(), 0);
  const ordinary = await login("member@example.test"), ordinaryPage = await ordinary.newPage(), requests = [];
  ordinaryPage.on("request", r => requests.push(new URL(r.url()).pathname));
  await ordinaryPage.goto(base + "/#deployments"); await ordinaryPage.locator("main").waitFor();
  assert.equal(await ordinaryPage.getByRole("region", { name: "版本发布就绪检查", exact: true }).count(), 0);
  assert.ok(!requests.some(value => value.startsWith("/api/deployments")));
  assert.equal((await ordinary.request.get(readinessURL)).status(), 403);
  assert.deepEqual(errors, []);
  console.log("PASS read-only staff and member boundaries; deployment readiness browser suite passed");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { child.kill("SIGTERM"); await new Promise(resolve => child.once("exit", resolve)); }
  await rm(dir, { recursive: true, force: true });
}
