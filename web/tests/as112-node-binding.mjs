// Incremental AS112 node-binding/retarget checks. Synthetic local service only;
// existing parser/wire suites are intentionally not repeated here.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const repo = fileURLToPath(new URL("../..", import.meta.url)), dir = await mkdtemp(path.join(tmpdir(), "as112-binding-"));
const password = "AS112-node-binding-fixture-123";
const until = async (check, label) => { for (let i = 0; i < 200; i++) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 50)); } assert.fail(label); };
let child, browser, stderr = "";
try {
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve)); const port = listener.address().port; await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web/dist"), "-named-checkzone", process.env.NAMED_CHECKZONE_BIN, "-pdns-server", process.env.PDNS_SERVER_BIN], { env: { ...process.env, IXP_SETUP_TOKEN: "as112-node-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
  await until(async () => { if (child.exitCode !== null) throw new Error(stderr); return fetch(base + "/api/setup").then(response => response.ok).catch(() => false); }, "service startup timeout");
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await ctx.newPage(), errors = [];
  page.setDefaultTimeout(10000); page.on("pageerror", error => errors.push(error.message));
  const setup = await ctx.request.post(base + "/api/setup", { data: { setupToken: "as112-node-setup", name: "Node binding QA", email: "admin@example.test", password, ixpName: "AS112 node binding", asn: 64500 } }); assert.equal(setup.status(), 200);
  const request = async (route, data, method = data ? "POST" : "GET") => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    return ctx.request.fetch(base + "/api" + route, { method, headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(data ? { data } : {}) });
  };
  const api = async (route, data, method) => { const response = await request(route, data, method); assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`); return response.json(); };
  const vlan = await api("/resources/vlans", { name: "DNS backend LAN", tag: 112, ipv4Prefix: "192.0.2.0/24", ipv6Prefix: "2001:db8:112::/64" });
  const router = await api("/resources/routers", { name: "Junos 双栈 DNS 绑定", platform: "junos", role: "as112", asn: 112, as112Service: "both", as112IPv4: true, as112IPv6: true, routerId: "192.0.2.112", localIpv4: "192.0.2.112", localIpv6: "2001:db8:112::112", as112NextHop4: "192.0.2.200", as112NextHop6: "2001:db8:112::200", vlanId: vlan.id });
  const profile = await api("/resources/provisioning-profiles", { name: "AS112 节点迁移规则", options: { kind: "as112-dns", routerId: router.id, as112: { serverName: "node.example.test", responsibleMailbox: "hostmaster.example.test", facility: "节点绑定测试", location: "Shanghai, CN", uniqueIP: "192.0.2.200", loc: "" } } });
  const generate = () => api("/provisioning", { profileId: profile.id });
  let approved = await generate(); approved = await api(`/provisioning/${approved.id}/validate`, {}); approved = await api(`/provisioning/${approved.id}/approve`, { checksum: approved.checksum });
  const staleDraft = await generate(); let staleValidated = await generate(); staleValidated = await api(`/provisioning/${staleValidated.id}/validate`, {});
  const go = async name => { await page.goto(base + "/#" + name); await page.reload(); await page.locator("main h1").waitFor(); };
  await go("provisioning"); await page.locator(`[data-provisioning-id="${approved.id}"]`).click();
  const summary = page.getByRole("region", { name: "AS112 DNS 服务包摘要", exact: true }), download = page.getByRole("button", { name: "下载已批准文件", exact: true });
  await summary.waitFor(); await until(() => download.isEnabled(), "bound approved bundle did not become readable");
  const initial = JSON.parse((await api(`/provisioning/${approved.id}`)).config);
  assert.deepEqual(initial.nodeAddresses, ["192.0.2.200", "2001:db8:112::200"]);
  assert.equal(initial.requiredLocalAddresses.length, 10); assert.equal(new Set(initial.requiredLocalAddresses).size, 10); assert.ok(initial.routerCreatedAt);
  assert.equal(new Set([...initial.listenAddresses, ...initial.nodeAddresses]).size, initial.requiredLocalAddresses.length);
  const localList = summary.getByLabel("AS112 必需本机地址", { exact: true });
  assert.ok((await localList.innerText()).includes("192.0.2.200")); assert.ok((await localList.innerText()).includes("2001:db8:112::200"));
  await summary.getByText(/所有服务地址与所选地址族的节点地址必须属于同一台/).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)); assert.ok(await summary.evaluate(element => element.scrollWidth <= element.clientWidth));
  if (process.env.AS112_SCREENSHOT) await page.screenshot({ path: process.env.AS112_SCREENSHOT, fullPage: true, animations: "disabled" });
  await page.setViewportSize({ width: 1440, height: 1000 });

  const detailURL = base + `/api/provisioning/${approved.id}`;
  for (const fault of ["missing-fields", "missing-required-hop", "missing-hop-in-both", "missing-entity"]) {
    await page.route(detailURL, async route => {
      const response = await route.fetch(), body = await response.json(), value = JSON.parse(body.config);
      if (fault === "missing-fields") { delete value.nodeAddresses; delete value.requiredLocalAddresses; }
      if (fault === "missing-required-hop" || fault === "missing-hop-in-both") value.requiredLocalAddresses = value.requiredLocalAddresses.filter(address => address !== "2001:db8:112::200");
      if (fault === "missing-hop-in-both") value.nodeAddresses = value.nodeAddresses.filter(address => address !== "2001:db8:112::200");
      if (fault === "missing-entity") delete value.routerCreatedAt;
      await route.fulfill({ response, json: { ...body, config: JSON.stringify(value) } });
    }, { times: 1 });
    await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click();
    await page.getByText("AS112 包的节点地址或必需本机地址不完整，已禁用后续操作。", { exact: true }).waitFor();
    assert.equal(await download.isDisabled(), true); assert.equal(await summary.count(), 0);
    await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await until(() => download.isEnabled(), "fresh complete node binding did not recover");
  }
  console.log("PASS complete dual-next-hop display, 390px, missing contract/one-hop/self-consistent omission/entity rejection");

  await page.getByRole("button", { name: "生成规则", exact: true }).click(); await page.getByRole("button", { name: `编辑生成规则 ${profile.name}`, exact: true }).click();
  const dialog = page.getByRole("dialog"); await dialog.locator("#profile-as112-uniqueIP").fill("192.0.2.201");
  await dialog.getByText(/可先保存新的唯一 IP 意图/).waitFor();
  const changed = page.waitForResponse(response => response.url().endsWith(`/resources/provisioning-profiles/${profile.id}`) && response.request().method() === "PUT");
  await dialog.getByRole("button", { name: "保存生成规则", exact: true }).click(); assert.equal((await changed).status(), 200); await dialog.waitFor({ state: "hidden" });
  assert.equal((await api(`/resources/provisioning-profiles/${profile.id}`)).options.as112.uniqueIP, "192.0.2.201");
  const blockedGenerate = await request("/provisioning", { profileId: profile.id }); assert.ok([400, 409, 422].includes(blockedGenerate.status()));
  assert.equal((await request(`/provisioning/${staleDraft.id}/validate`, {})).status(), 409);
  assert.equal((await request(`/provisioning/${staleValidated.id}/approve`, { checksum: staleValidated.checksum })).status(), 409);
  await page.getByRole("button", { name: "生成版本", exact: true }).click(); await page.locator(`[data-provisioning-id="${approved.id}"]`).click(); await until(() => download.isEnabled(), "historical metadata not readable during retarget");
  let saves = 0; page.on("download", () => saves++);
  const blockedDownload = page.waitForResponse(response => response.url() === detailURL + "/config"); await download.click(); assert.equal((await blockedDownload).status(), 409); assert.equal(saves, 0); assert.equal(await download.isDisabled(), true);
  console.log("PASS explicit profile retarget intent, incompatible generation/validation/approval and historical delivery rejection");

  await go("routers"); await page.getByRole("button", { name: `编辑 ${router.name}`, exact: true }).click();
  await dialog.locator("#field-as112NextHop4").fill("192.0.2.201");
  const repaired = page.waitForResponse(response => response.url().endsWith(`/resources/routers/${router.id}`) && response.request().method() === "PUT");
  await dialog.getByRole("button", { name: "保存", exact: true }).click(); assert.equal((await repaired).status(), 200); await dialog.waitFor({ state: "hidden" });
  await go("provisioning"); await page.getByRole("button", { name: "生成工具配置", exact: true }).click(); await dialog.locator("#provisioning-profile").selectOption(profile.id);
  const generated = page.waitForResponse(response => response.url().endsWith("/api/provisioning") && response.request().method() === "POST"); await dialog.getByRole("button", { name: "生成待审版本", exact: true }).click(); assert.equal((await generated).status(), 201); await dialog.waitFor({ state: "hidden" });
  await summary.waitFor(); assert.ok((await localList.innerText()).includes("192.0.2.201")); assert.ok((await localList.innerText()).includes("2001:db8:112::200"));
  const next = (await api("/provisioning")).items.find(item => item.profileId === profile.id && ![approved.id, staleDraft.id, staleValidated.id].includes(item.id)); assert.ok(next);
  const parsed = JSON.parse((await api(`/provisioning/${next.id}`)).config); assert.deepEqual(parsed.nodeAddresses, ["192.0.2.201", "2001:db8:112::200"]); assert.ok(!parsed.requiredLocalAddresses.includes("192.0.2.200"));
  await page.getByRole("button", { name: "运行 AS112 原生校验", exact: true }).click(); await until(() => page.getByRole("button", { name: "审核批准", exact: true }).isEnabled(), "repaired bundle failed actual native validation");
  await page.getByRole("button", { name: "审核批准", exact: true }).click(); await dialog.getByRole("checkbox").check(); await dialog.getByRole("button", { name: "确认审核批准", exact: true }).click(); await dialog.waitFor({ state: "hidden" });
  await until(() => download.isEnabled(), "repaired current version not deliverable"); const saving = page.waitForEvent("download"); await download.click();
  const delivered = JSON.parse(await readFile(await (await saving).path(), "utf8")); assert.deepEqual(delivered.nodeAddresses, parsed.nodeAddresses); assert.deepEqual(delivered.requiredLocalAddresses, parsed.requiredLocalAddresses); assert.equal(delivered.healthPlan.executed, false);
  assert.equal((await request(`/provisioning/${approved.id}/config`)).status(), 409);
  assert.deepEqual(errors, []);
  console.log("PASS repaired Junos tuple, fresh generation/native review/approval/exact delivery and old-version refusal");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { const exited = new Promise(resolve => child.once("exit", resolve)); child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000); await exited; clearTimeout(timer); }
  await rm(dir, { recursive: true, force: true });
}
