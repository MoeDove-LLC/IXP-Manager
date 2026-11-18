import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "ixp-routeros-handoff-"));
const host = "127.0.0.1";
const setupToken = "routeros-handoff-fixture-setup";
const password = "RouterOS-handoff-fixture-password";
const secret = "Fixture_RouterOS_Key_234";
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
  for (let attempt = 0; ; attempt++) {
    if (server.exitCode !== null || attempt === 150) throw new Error("handoff fixture service failed to start");
    if (await fetch(`${base}/api/setup`).then(r => r.ok).catch(() => false)) break;
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, acceptDownloads: true });
  const setup = await context.request.post(`${base}/api/setup`, { data: {
    setupToken, name: "Handoff QA", email: "handoff@example.test", password, ixpName: "Handoff QA", asn: 64500,
  } });
  assert.equal(setup.status(), 200);
  const { csrfToken } = await setup.json();
  const api = async (route, data, method = data ? "POST" : "GET", etag) => {
    const response = await context.request.fetch(`${base}/api${route}`, {
      method, data, headers: { "X-CSRF-Token": csrfToken, ...(etag ? { "If-Match": JSON.stringify(etag) } : {}) },
    });
    assert.ok(response.ok(), `${route}: ${response.status()}`);
    return response.json();
  };
  const location = await api("/resources/locations", { name: "Handoff DC" });
  const sw = await api("/resources/switches", { name: "Handoff switch", locationId: location.id });
  const portRow = await api("/resources/ports", { name: "eth1", switchId: sw.id, status: "active" });
  const member = await api("/resources/members", { name: "Handoff member", asn: 64501, status: "active" });
  const vlan = await api("/resources/vlans", { name: "Handoff VLAN", tag: 100, ipv4Prefix: "192.0.2.0/24", ipv6Prefix: "2001:db8:10::/64" });
  await api("/resources/connections", { name: "Handoff link", memberId: member.id, portId: portRow.id, vlanId: vlan.id, ipv4: "192.0.2.10", ipv6: "2001:db8:10::10", status: "active" });
  const router = await api("/resources/routers", { name: "Handoff RouterOS", platform: "routeros7", asn: 64500, routerId: "192.0.2.1", localIpv4: "192.0.2.1", localIpv6: "2001:db8:10::1", vlanId: vlan.id });
  const bird = await api("/resources/routers", { name: "Handoff BIRD", platform: "bird2", asn: 64500, routerId: "192.0.2.2", localIpv4: "192.0.2.2", vlanId: vlan.id });
  const peers = [];
  for (const [address, prefix] of [["192.0.2.10", "198.51.100.0/24"], ["2001:db8:10::10", "2001:db8:20::/48"]]) {
    peers.push(await api("/resources/peers", { name: `Session ${address}`, routerId: router.id, memberId: member.id, address, asn: 64501, enabled: true, maxPrefixes: 100, allowedPrefixes: [prefix], allowedRoutes: [{ prefix, originAsn: 64501 }] }));
  }
  const credentialPath = `/peer-credentials/${peers[0].id}`;
  const old = await api(credentialPath);
  const credential = await api(credentialPath, { expectedRevision: old.revision, expectedPeerChecksum: old.peerChecksum, currentPassword: password, secret }, "PUT");
  const other = await api("/deployments", { routerId: bird.id });
  const draft = await api("/deployments", { routerId: router.id });
  const page = await context.newPage();
  const errors = [];
  const downloads = [];
  page.on("pageerror", error => errors.push(error.message));
  page.on("download", download => downloads.push(download.suggestedFilename()));
  await page.goto(`${base}/#deployments`);
  const select = id => page.locator(`[data-deployment-id="${id}"]`).click();
  await select(draft.id);
  const button = page.getByRole("button", { name: "下载 RouterOS 规划文件", exact: true });
  const enabled = button.and(page.locator(":enabled"));
  assert.equal(await page.getByRole("button", { name: "下载配置", exact: true }).isDisabled(), true);
  const endpoint = `${base}/api/deployments/${draft.id}/routeros-handoff`;
  const successful = page.waitForEvent("download");
  const request = page.waitForRequest(r => r.url() === endpoint);
  await button.click();
  assert.equal((await request).headers()["x-ixp-review-identifier"], draft.checksum);
  const downloaded = await successful;
  assert.equal(downloaded.suggestedFilename(), `routeros-handoff-${draft.id}.json`);
  const handoffFile = path.join(dir, "download.json");
  await downloaded.saveAs(handoffFile);
  await chmod(handoffFile, 0o644);
  const content = await readFile(handoffFile, "utf8");
  assert.ok(!content.includes(secret) && !content.includes("password"));
  const handoff = JSON.parse(content);
  assert.equal(handoff.reviewIdentifier, draft.checksum);
  assert.equal(handoff.desired.routerId, router.routerId);
  assert.deepEqual(handoff.desired.peers.map(peer => peer.address).sort(), ["192.0.2.10", "2001:db8:10::10"]);
  assert.ok(handoff.desired.peers.every(peer => peer.allowedRoutes[0].originAsn === 64501 && peer.maxPrefixes === 100));

  // Synthetic strict-review input for the offline planner. No device is contacted.
  const target = { routerId: router.id, version: "7.24.6", deviceId: "offline-fixture", transportIdentitySha256: "a".repeat(64) };
  const owner = `md_${createHash("sha256").update(router.id).digest("hex").slice(0, 16)}`;
  const review = { ok: true, review: { knownState: { target }, snapshot: { target, owner, namespaceEmpty: true, crossChannelIdentityVerified: true }, observedAt: new Date().toISOString() } };
  const reviewFile = path.join(dir, "review.json");
  const stage = path.join(dir, "stage.json");
  await writeFile(reviewFile, JSON.stringify(review), { mode: 0o600 });
  const credentialsFile = path.join(dir, "credentials.json");
  await writeFile(credentialsFile, JSON.stringify({ credentials: { [peers[0].id]: { revision: credential.revision, password: secret } } }), { mode: 0o600 });
  const plan = (...extra) => spawnSync(process.env.TEST_ROUTEROS_BIN, ["plan", "--review", reviewFile, "--handoff", handoffFile, "--output", stage, ...extra], { encoding: "utf8" });
  const missing = plan();
  assert.equal(missing.status, 1);
  assert.ok(!missing.stderr.includes(secret));
  await assert.rejects(stat(stage), { code: "ENOENT" });
  const planned = plan("--credentials", credentialsFile);
  assert.equal(planned.status, 0, planned.stderr);
  assert.ok(!planned.stdout.includes(secret));
  assert.equal(JSON.parse(planned.stdout).nativeValidated, false);
  assert.equal((await stat(stage)).mode & 0o777, 0o600);
  const stageBody = JSON.parse(await readFile(stage, "utf8"));
  assert.deepEqual(stageBody.plan.desired, handoff.desired);
  assert.equal(stageBody.exclusiveAccessAcknowledged, false);
  assert.equal(stageBody.plan.target.routerId, router.id);

  let replyReady, releaseReply, replyDone;
  const received = new Promise(resolve => { replyReady = resolve; });
  const release = new Promise(resolve => { releaseReply = resolve; });
  const done = new Promise(resolve => { replyDone = resolve; });
  await page.route(endpoint, async route => {
    try {
      const actual = await route.fetch();
      replyReady(actual.status());
      await release;
      await route.fulfill({ response: actual });
    } catch {
      replyReady(0);
    } finally { replyDone(); }
  }, { times: 1 });
  await button.click();
  assert.equal(await received, 200);
  await select(other.id);
  releaseReply();
  await done;
  await select(draft.id);
  await enabled.waitFor();
  assert.equal(downloads.length, 1, "late response downloaded a previous draft");

  if (process.env.ROUTEROS_HANDOFF_SCREENSHOT) await page.screenshot({ path: process.env.ROUTEROS_HANDOFF_SCREENSHOT, fullPage: true, animations: "disabled" });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await api(`/resources/routers/${router.id}`, { name: router.name, platform: router.platform, asn: router.asn, routerId: "192.0.2.99", localIpv4: router.localIpv4, localIpv6: router.localIpv6, vlanId: router.vlanId }, "PUT", router._etag);
  const refused = page.waitForResponse(r => r.url() === endpoint);
  await button.click();
  assert.equal((await refused).status(), 409);
  await enabled.waitFor({ state: "hidden" });
  assert.equal((await api("/deployments")).items.find(item => item.id === draft.id).status, "draft");
  assert.equal(downloads.length, 1);
  assert.deepEqual(errors, []);
  console.log("PASS real RouterOS draft download, offline planner, private credentials, obsolete response suppression, drift and 390px layout");
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
