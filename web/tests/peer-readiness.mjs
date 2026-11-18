// Actual server-backed BGP draft readiness in a synthetic loopback fixture.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "ixp-peer-readiness-"));
const host = "127.0.0.1";
const setupToken = "peer-readiness-fixture-setup";
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
    setupToken, name: "Fixture admin", email: "peer-readiness@example.test", password: "Peer-readiness-browser-fixture-password", ixpName: "Peer readiness fixture", asn: 64500,
  } });
  assert.equal(setup.status(), 200);
  const { csrfToken } = await setup.json();
  const post = async (url, data) => {
    const response = await context.request.post(`${base}/api${url}`, { headers: { "X-CSRF-Token": csrfToken }, data });
    assert.ok(response.ok(), `${url}: ${response.status()} ${await response.text()}`);
    return response.json();
  };
  const member = await post("/resources/members", { name: "Fixture member", asn: 64501, status: "active" });
  const vlan = await post("/resources/vlans", { name: "Fixture VLAN", tag: 100, ipv4Prefix: "192.0.2.0/24" });
  const router = await post("/resources/routers", { name: "Quagga fixture", platform: "quagga", routerId: "192.0.2.1", localIpv4: "192.0.2.1", asn: 64500, vlanId: vlan.id });
  const draft = { id: "legacy-peer-fixture", name: "Imported BGP draft", routerId: router.id, memberId: member.id, address: "192.0.2.10", asn: 64501, enabled: false, maxPrefixes: 10, allowedPrefixes: [] };
  await post("/import", { version: 1, resources: { peers: [draft] } });
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`${base}/#peers`);
  const check = page.getByRole("button", { name: "检查 BGP 配置 Imported BGP draft", exact: true });
  await check.click();
  const dialog = page.getByRole("dialog");
  await dialog.getByText("待补齐配置条件", { exact: true }).waitFor();
  await dialog.getByText(/为此成员登记地址与邻居相同的已激活接入/).waitFor();
  await dialog.getByText(/保存明确的前缀白名单/).waitFor();

  const location = await post("/resources/locations", { name: "Fixture DC" });
  const device = await post("/resources/switches", { name: "Fixture switch", locationId: location.id });
  const physicalPort = await post("/resources/ports", { name: "eth1", switchId: device.id, status: "active" });
  await post("/resources/connections", { name: "Fixture uplink", memberId: member.id, portId: physicalPort.id, vlanId: vlan.id, ipv4: "192.0.2.10", status: "active" });
  const refreshed = page.waitForResponse(r => r.url().endsWith(`/api/peer-readiness/${draft.id}`));
  await dialog.getByRole("button", { name: "重新检查", exact: true }).click();
  assert.deepEqual((await (await refreshed).json()).issues, ["allowlist-required"]);
  await dialog.getByText(/为此成员登记地址与邻居相同的已激活接入/).waitFor({ state: "hidden" });
  await dialog.getByText(/保存明确的前缀白名单/).waitFor();

  await dialog.getByRole("button", { name: "编辑会话", exact: true }).click();
  await dialog.locator("#field-allowedPrefixes").fill("198.51.100.0/24");
  const saving = page.waitForResponse(r => r.url().endsWith(`/api/resources/peers/${draft.id}`) && r.request().method() === "PUT");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  const save = await saving;
  assert.equal(save.status(), 200, await save.text());
  await dialog.waitFor({ state: "hidden" });

  await check.click();
  await dialog.getByText("配置生成检查通过", { exact: true }).waitFor();
  await dialog.getByText("已停用", { exact: true }).waitFor();
  await dialog.getByText(/核对后可编辑并启用此会话/).waitFor();
  const stillDraft = await context.request.get(`${base}/api/resources/peers/${draft.id}`);
  assert.equal((await stillDraft.json()).enabled, false);
  const deployments = await context.request.get(`${base}/api/deployments`);
  assert.equal((await deployments.json()).items.length, 0);
  assert.ok(await dialog.evaluate(element => element.getBoundingClientRect().width <= innerWidth));
  if (process.env.PEER_READINESS_SCREENSHOT) await page.screenshot({ path: process.env.PEER_READINESS_SCREENSHOT });
  assert.deepEqual(errors, []);
  console.log("PASS actual Quagga draft diagnostics, inventory refresh, edit, and readiness without enabling or creating a deployment");
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
