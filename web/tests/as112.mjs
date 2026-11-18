// Synthetic local console and native offline checkers only. This browser suite
// never starts BGP or DNS; actual DNS transport is covered by native_test.go.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, readFile, writeFile, stat, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { chromium } from "playwright";

const exec = promisify(execFile), repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp(path.join(tmpdir(), "as112-browser-")), password = "AS112-browser-fixture-123";
const until = async (check, label) => {
  for (let i = 0; i < 200; i++) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 50)); }
  assert.fail(label);
};
let child, browser, stderr = "";
try {
  const listener = net.createServer(); await new Promise(resolve => listener.listen(0, "127.0.0.1", resolve));
  const port = listener.address().port; await new Promise(resolve => listener.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  const flags = [["-bird", "BIRD_BIN"], ["-bird3", "BIRD3_BIN"], ["-frr", "FRR_BIN"], ["-openbgpd", "OPENBGPD_BIN"], ["-named-checkzone", "NAMED_CHECKZONE_BIN"], ["-pdns-server", "PDNS_SERVER_BIN"]];
  for (const [, name] of flags) assert.ok(process.env[name], `${name} must point to the actual native checker`);
  child = spawn(process.env.TEST_SERVER_BIN || path.join(repo, ".cache/as112-service/ixp-manager"), ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web/dist"), ...flags.flatMap(([flag, name]) => [flag, process.env[name]])], { env: { ...process.env, IXP_SETUP_TOKEN: "as112-browser-setup" }, stdio: ["ignore", "ignore", "pipe"] });
  child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
  await until(async () => { if (child.exitCode !== null) throw new Error(`fixture service exited: ${stderr}`); return fetch(base + "/api/setup").then(response => response.ok).catch(() => false); }, "AS112 local server startup timed out");
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await admin.newPage(), errors = [];
  page.setDefaultTimeout(10000); page.on("pageerror", error => errors.push(error.message));
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "as112-browser-setup", name: "AS112 管理员", email: "admin@example.test", password, ixpName: "MoeDove AS112 测试", asn: 64500 } }); assert.equal(setup.status(), 200, await setup.text());
  const api = async (route, body, method = body ? "POST" : "GET", ctx = admin) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + route, { method, headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(body ? { data: body } : {}) });
    assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`); return response.json();
  };
  const member = await api("/resources/members", { name: "AS112 明确选择的邻居", asn: 64501, status: "active" });
  const location = await api("/resources/locations", { name: "AS112 合成机房" });
  const sw = await api("/resources/switches", { name: "AS112 合成交换机", locationId: location.id });
  const physical = await api("/resources/ports", { name: "Ethernet fixture", switchId: sw.id, speed: 10000, status: "active" });
  const vlan = await api("/resources/vlans", { name: "AS112 双栈 LAN", tag: 112, ipv4Prefix: "192.0.2.0/24", ipv6Prefix: "2001:db8:112::/64" });
  await api("/resources/connections", { name: "AS112 双栈接入", memberId: member.id, portId: physical.id, vlanId: vlan.id, ipv4: "192.0.2.10", ipv6: "2001:db8:112::10", status: "active" });
  const go = async route => { await page.goto(base + "/#" + route); await page.reload(); await page.locator("main h1").waitFor(); };
  const routers = {};
  const createRouterUI = async platform => {
    await go("routers"); await page.locator(".page-heading button").click();
    const dialog = page.getByRole("dialog");
    await dialog.locator("#field-name").fill(`AS112 ${platform}`);
    await dialog.locator("#field-platform").selectOption(platform);
    await dialog.locator("#field-role").selectOption("as112");
    assert.equal(await dialog.locator("#field-asn").inputValue(), "112");
    assert.equal(await dialog.locator("#field-asn").isEditable(), false);
    assert.equal(await dialog.locator("#field-as112Service").inputValue(), "both");
    assert.equal(await dialog.locator("#field-as112IPv4").isChecked(), true); assert.equal(await dialog.locator("#field-as112IPv6").isChecked(), true);
    for (const field of ["collectorPolicy", "rpkiPolicy", "rfc1997Passthrough"]) assert.equal(await dialog.locator(`#field-${field}`).count(), 0);
    const suffix = platform === "junos" ? 116 : 112;
    await dialog.locator("#field-routerId").fill(`192.0.2.${suffix}`); await dialog.locator("#field-localIpv4").fill(`192.0.2.${suffix}`); await dialog.locator("#field-localIpv6").fill(`2001:db8:112::${suffix}`); await dialog.locator("#field-vlanId").selectOption(vlan.id);
    if (platform === "junos") {
      await dialog.locator("#field-as112NextHop4").fill("192.0.2.200"); await dialog.locator("#field-as112NextHop6").fill("2001:db8:112::200");
      await dialog.getByRole("region", { name: "AS112 服务边界", exact: true }).getByText(/不在路由器上运行 PowerDNS/).waitFor();
    } else {
      assert.equal(await dialog.locator("#field-as112NextHop4").count(), 0);
      await dialog.locator("#field-platform").selectOption("routeros7");
      await dialog.getByText("当前平台未实现所选路由角色，不能保存此组合。请根据平台能力更换平台或角色。", { exact: true }).waitFor();
      assert.equal(await dialog.getByRole("button", { name: "保存", exact: true }).isDisabled(), true);
      await dialog.locator("#field-platform").selectOption(platform);
    }
    const saving = page.waitForResponse(response => response.url().endsWith("/api/resources/routers") && response.request().method() === "POST");
    await dialog.getByRole("button", { name: "保存", exact: true }).click();
    const saved = await saving; assert.equal(saved.status(), 201, await saved.text());
    await dialog.waitFor({ state: "hidden" });
    return (await api("/resources/routers")).items.find(router => router.name === `AS112 ${platform}`);
  };
  routers.bird2 = await createRouterUI("bird2"); routers.junos = await createRouterUI("junos");
  for (const [index, platform] of ["bird3", "frr", "openbgpd"].entries()) { const suffix = index + 113; routers[platform] = await api("/resources/routers", { name: `AS112 ${platform}`, platform, role: "as112", asn: 112, as112Service: "both", as112IPv4: true, as112IPv6: true, routerId: `192.0.2.${suffix}`, localIpv4: `192.0.2.${suffix}`, localIpv6: `2001:db8:112::${suffix}`, vlanId: vlan.id }); }
  assert.equal((await api("/resources/peers")).items.length, 0, "AS112 role silently created peers");
  for (const [platform, router] of Object.entries(routers)) {
    for (const [family, address] of [[4, "192.0.2.10"], [6, "2001:db8:112::10"]]) {
      if (platform === "bird2") {
        await go("peers"); await page.locator(".page-heading button").click(); const dialog = page.getByRole("dialog");
        await dialog.locator("#field-name").fill(`AS112 explicit v${family}`); await dialog.locator("#field-routerId").selectOption(router.id); await dialog.locator("#field-memberId").selectOption(member.id);
        await dialog.locator("#field-asn").fill("64501"); await dialog.locator("#field-address").fill(address); await dialog.locator("#field-enabled").check();
        assert.equal(await dialog.locator("#field-allowedPrefixes").count(), 0); await dialog.getByText(/固定拒收所有 BGP 路由/).waitFor();
        await dialog.getByRole("button", { name: "保存", exact: true }).click(); await dialog.waitFor({ state: "hidden" });
      } else await api("/resources/peers", { name: `${platform} explicit v${family}`, routerId: router.id, memberId: member.id, asn: 64501, address, enabled: true, maxPrefixes: 10, allowedPrefixes: [], allowedRoutes: [] });
    }
  }
  console.log("PASS AS112 role/ASN/family defaults, explicit empty-ingress peers, unsupported platform and Junos DNS next-hop inputs");

  const deployments = {};
  for (const [platform, router] of Object.entries(routers)) deployments[platform] = await api("/deployments", { routerId: router.id });
  await go("deployments");
  for (const [platform, deployment] of Object.entries(deployments)) {
    await page.locator(`[data-deployment-id="${deployment.id}"]`).click();
    await page.getByRole("region", { name: "版本发布就绪检查", exact: true }).getByText("当前输入一致", { exact: true }).waitFor();
    await page.getByRole("region", { name: "AS112 服务边界", exact: true }).waitFor();
    const label = platform === "junos" ? "检查配置结构" : "校验配置";
    await until(() => page.getByRole("button", { name: label, exact: true }).isEnabled(), `${platform} validation stayed disabled`);
    const response = page.waitForResponse(value => value.url().endsWith(`/api/deployments/${deployment.id}/validate`));
    await page.getByRole("button", { name: label, exact: true }).click(); assert.equal((await response).status(), 200);
    if (platform === "junos") await page.getByText("仅结构检查", { exact: true }).waitFor();
  }
  await page.locator(`[data-deployment-id="${deployments.bird2.id}"]`).click();
  await until(() => page.getByRole("button", { name: "批准版本", exact: true }).isEnabled(), "BGP approval stayed disabled");
  await page.getByRole("button", { name: "批准版本", exact: true }).click(); await page.getByRole("dialog").getByRole("button", { name: "确认批准", exact: true }).click();
  await until(() => page.getByRole("button", { name: "下载配置", exact: true }).isEnabled(), "BGP download stayed disabled");
  const bgpDownload = page.waitForEvent("download"); await page.getByRole("button", { name: "下载配置", exact: true }).click();
  assert.ok((await readFile(await (await bgpDownload).path(), "utf8")).includes("AS112"));
  console.log("PASS four actual native BGP parsers, Junos structure-only boundary, explicit approval and verified BGP download");

  await go("provisioning"); await page.getByRole("button", { name: "生成规则", exact: true }).click(); await page.getByRole("button", { name: "新增生成规则", exact: true }).first().click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#profile-name").fill("AS112 权威 DNS 合成规则"); await dialog.locator("#profile-kind").selectOption("as112-dns"); await dialog.locator("#profile-as112-router").selectOption(routers.bird2.id);
  assert.equal(await dialog.locator("#profile-vlan").count(), 0);
  for (const [key, value] of Object.entries({ serverName: "as112.example.test", responsibleMailbox: "hostmaster.example.test", facility: "合成交换中心", location: "Shanghai, CN", uniqueIP: "192.0.2.112", loc: "31 13 0 N 121 28 0 E 10m" })) await dialog.locator(`#profile-as112-${key}`).fill(value);
  await dialog.getByRole("button", { name: "保存生成规则", exact: true }).click(); await dialog.waitFor({ state: "hidden" });
  let profile = (await api("/resources/provisioning-profiles")).items.find(item => item.name === "AS112 权威 DNS 合成规则"); assert.ok(profile);
  await page.getByRole("button", { name: "生成版本", exact: true }).click(); await page.getByRole("button", { name: "生成工具配置", exact: true }).click();
  await dialog.locator("#provisioning-profile").selectOption(profile.id); await dialog.getByRole("button", { name: "生成待审版本", exact: true }).click(); await dialog.waitFor({ state: "hidden" });
  const summary = page.getByRole("region", { name: "AS112 DNS 服务包摘要", exact: true }); await summary.waitFor();
  await summary.getByText("目标健康检查尚未执行", { exact: true }).waitFor();
  const revision = (await api("/provisioning")).items.find(item => item.profileId === profile.id);
  const validating = page.waitForResponse(response => response.url().endsWith(`/api/provisioning/${revision.id}/validate`));
  await page.getByRole("button", { name: "运行 AS112 原生校验", exact: true }).click(); assert.equal((await validating).status(), 200);
  await until(() => page.getByRole("button", { name: "审核批准", exact: true }).isEnabled(), "DNS approval stayed disabled");
  await summary.getByText("目标健康检查尚未执行", { exact: true }).waitFor();
  await page.getByRole("button", { name: "审核批准", exact: true }).click(); await dialog.getByRole("checkbox").check(); await dialog.getByRole("button", { name: "确认审核批准", exact: true }).click(); await dialog.waitFor({ state: "hidden" });
  const download = page.getByRole("button", { name: "下载已批准文件", exact: true }); await until(() => download.isEnabled(), "DNS bundle download stayed disabled");
  const saving = page.waitForEvent("download"); await download.click(); const downloaded = await readFile(await (await saving).path());
  const bundle = JSON.parse(downloaded), sha = createHash("sha256").update(downloaded).digest("hex");
  assert.equal(bundle.routerId, routers.bird2.id); assert.equal(bundle.kind, "as112-dns"); assert.equal(bundle.asn, 112); assert.equal(bundle.healthPlan.executed, false);
  assert.deepEqual(bundle.families, [4, 6]); assert.equal(bundle.listenAddresses.length, 8); assert.equal(bundle.zones.length, 24);
  for (const zone of ["home.arpa.", "service.arpa.", "empty.as112.arpa.", "hostname.as112.net.", "hostname.as112.arpa."]) assert.ok(bundle.zones.includes(zone));
  assert.ok(bundle.files.find(file => file.path === "pdns.conf").content.includes("resolver=no"));
  await summary.getByText("查看 DNS 健康检查计划", { exact: true }).click();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "AS112 service preview overflowed 390px");
  assert.ok(await summary.evaluate(element => element.scrollWidth <= element.clientWidth), "AS112 summary overflowed its own panel");
  if (process.env.AS112_SCREENSHOT) await page.screenshot({ path: process.env.AS112_SCREENSHOT, fullPage: true, animations: "disabled" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log("PASS PowerDNS profile generation, actual native validation, explicit approval, exact bundle download and 390px health-plan preview");

  const cli = process.env.TEST_PROVISION_BIN; assert.ok(cli, "TEST_PROVISION_BIN is required for the real CLI boundary");
  const bundleFile = path.join(dir, "as112.json"), extracted = path.join(dir, "extracted"); await writeFile(bundleFile, downloaded, { mode: 0o600 });
  const checked = JSON.parse((await exec(cli, ["check", "--kind", "as112-dns", "--file", bundleFile, "--checksum", sha, "--named-checkzone", process.env.NAMED_CHECKZONE_BIN, "--pdns-server", process.env.PDNS_SERVER_BIN])).stdout);
  assert.equal(checked.validation.level, "native"); assert.equal(checked.activated, false);
  const extractedResult = JSON.parse((await exec(cli, ["as112-extract", "--file", bundleFile, "--checksum", sha, "--router", bundle.routerId, "--output", extracted])).stdout);
  assert.equal(extractedResult.installed, false); assert.equal(extractedResult.healthExecuted, false); assert.equal((await stat(extracted)).mode & 0o777, 0o700);
  for (const file of bundle.files) assert.equal(await readFile(path.join(extracted, file.path), "utf8"), file.content);
  await assert.rejects(exec(cli, ["as112-extract", "--file", bundleFile, "--checksum", sha, "--router", bundle.routerId, "--output", extracted]));
  await assert.rejects(exec(cli, ["as112-extract", "--file", bundleFile, "--checksum", sha, "--router", "wrong_router", "--output", path.join(dir, "wrong-target")]));
  let probeFailed = false;
  try { await exec(cli, ["as112-probe", "--file", bundleFile, "--checksum", sha, "--router", bundle.routerId]); }
  catch (error) { const result = JSON.parse(error.stdout); assert.equal(error.code, 3); assert.equal(result.queries, 0); assert.equal(result.healthy, false); assert.equal(result.localAddressesPresent, false); probeFailed = true; }
  assert.ok(probeFailed, "unconfigured namespace was reported healthy");
  console.log("PASS actual CLI native check, private exact extraction, no overwrite/wrong target and zero-query missing-address refusal");

  const detailURL = base + `/api/provisioning/${revision.id}`;
  await page.route(detailURL, async route => { const response = await route.fetch(), body = await response.json(), value = JSON.parse(body.config); value.healthPlan.executed = true; await route.fulfill({ response, json: { ...body, config: JSON.stringify(value) } }); }, { times: 1 });
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click();
  await page.getByText("AS112 包的目标、服务边界或未执行的健康计划不匹配，已禁用后续操作。", { exact: true }).waitFor(); assert.equal(await download.isDisabled(), true);
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await until(() => download.isEnabled(), "fresh exact detail did not recover download");
  let savedCount = 0; page.on("download", () => savedCount++);
  await page.route(detailURL + "/config", async route => { const response = await route.fetch(); await route.fulfill({ response, headers: { ...response.headers(), "x-provisioning-router-id": routers.junos.id } }); }, { times: 1 });
  await download.click(); await page.getByText("下载 AS112 路由节点标识不匹配，未保存文件。", { exact: true }).waitFor(); assert.equal(savedCount, 0); assert.equal(await download.isDisabled(), true);
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await until(() => download.isEnabled(), "fresh detail did not restore download after wrong header");
  let release, observed, completed; const gate = new Promise(resolve => { release = resolve; }), started = new Promise(resolve => { observed = resolve; }), finished = new Promise(resolve => { completed = resolve; });
  await page.route(detailURL, async route => { const response = await route.fetch(); observed(); await gate; await route.fulfill({ response }).catch(() => {}); completed(); }, { times: 1 });
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await started; assert.equal(await download.isDisabled(), true);
  await page.route(base + "/api/provisioning", route => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ error: "fixture version reload unavailable" }) }), { times: 1 });
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await page.getByText("fixture version reload unavailable", { exact: true }).waitFor(); release(); await finished;
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))); assert.equal(await download.isDisabled(), true, "late old detail restored authority after list failure");
  await page.getByRole("button", { name: "刷新工具配置版本", exact: true }).click(); await until(() => download.isEnabled(), "explicit reload did not recover frozen controls");
  console.log("PASS target/header binding, false-health-claim rejection and delayed detail after failed list refresh");

  await page.getByRole("button", { name: "生成规则", exact: true }).click(); await page.getByRole("button", { name: `编辑生成规则 ${profile.name}`, exact: true }).click();
  await dialog.locator("#profile-as112-facility").fill("本地尚未提交的设施编辑");
  await api(`/resources/provisioning-profiles/${profile.id}`, { name: "并发修改后的 AS112 规则", expectedRevision: profile.revision, options: { ...profile.options, as112: { ...profile.options.as112, facility: "远端已提交设施" } } }, "PUT");
  const conflict = page.waitForResponse(response => response.url().endsWith(`/resources/provisioning-profiles/${profile.id}`) && response.request().method() === "PUT");
  await dialog.getByRole("button", { name: "保存生成规则", exact: true }).click(); assert.equal((await conflict).status(), 409);
  assert.equal(await dialog.locator("#profile-as112-facility").inputValue(), "本地尚未提交的设施编辑"); assert.equal(await dialog.getByRole("button", { name: "保存生成规则", exact: true }).isDisabled(), true);
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: "生成版本", exact: true }).click(); await page.locator(`[data-provisioning-id="${revision.id}"]`).click(); await until(() => download.isEnabled(), "historical approved preview not readable");
  const stale = page.waitForResponse(response => response.url() === detailURL + "/config"); await download.click(); assert.equal((await stale).status(), 409); assert.equal(await download.isDisabled(), true);
  console.log("PASS profile CAS preserves edits and changed inputs independently block historical approved download");

  for (const role of ["viewer", "member"]) {
    await api("/resources/users", { name: `${role} AS112 QA`, email: `${role}@example.test`, password, role, ...(role === "member" ? { memberId: member.id } : {}) });
    const ctx = await browser.newContext(), other = await ctx.newPage(); const login = await ctx.request.post(base + "/api/login", { data: { email: `${role}@example.test`, password } }); assert.equal(login.status(), 200);
    const requests = []; other.on("request", request => requests.push(new URL(request.url()).pathname)); await other.goto(base + "/#provisioning"); await other.locator("main").waitFor();
    if (role === "viewer") { await other.getByRole("region", { name: "AS112 DNS 服务包摘要", exact: true }).waitFor(); assert.equal(await other.getByRole("button", { name: "下载已批准文件", exact: true }).count(), 0); assert.equal(await other.getByRole("button", { name: "生成工具配置", exact: true }).count(), 0); }
    else { assert.ok(!requests.some(route => route.startsWith("/api/provisioning"))); assert.equal((await ctx.request.get(base + "/api/provisioning")).status(), 403); }
    await ctx.close();
  }
  assert.deepEqual(errors, []); console.log("PASS read-only staff/member boundaries; AS112 browser and CLI flow passed");
} finally {
  if (browser) await browser.close();
  if (child && child.exitCode === null) { const exited = new Promise(resolve => child.once("exit", resolve)); child.kill("SIGTERM"); const timer = setTimeout(() => child.kill("SIGKILL"), 5000); await exited; clearTimeout(timer); }
  await rm(dir, { recursive: true, force: true });
}
