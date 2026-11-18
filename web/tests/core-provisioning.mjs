// Local, self-owned fixture only. No switch or external service is contacted.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { verifyProvisioningDownload } from "../src/lib/provisioning.ts";

const revision = { id: "revision", profileId: "profile", kind: "switch-core", switchId: "switch-a" };
const plan = { version: 1, kind: "switch-core", switchId: "switch-a", inventoryOnly: true, nativeValidated: false, requiresTargetAdapter: true };
const encode = value => new TextEncoder().encode(JSON.stringify(value)).buffer;
const hash = bytes => createHash("sha256").update(Buffer.from(bytes)).digest("hex");
const headers = bytes => new Headers({ "X-Config-SHA256": hash(bytes), "X-Provisioning-Revision": revision.id, "X-Provisioning-Profile-ID": revision.profileId, "X-Provisioning-Kind": revision.kind, "X-Provisioning-Switch-ID": revision.switchId });
const bytes = encode(plan);
assert.equal(await verifyProvisioningDownload(revision, headers(bytes), bytes), hash(bytes));
for (const key of ["X-Config-SHA256", "X-Provisioning-Revision", "X-Provisioning-Profile-ID", "X-Provisioning-Kind", "X-Provisioning-Switch-ID"]) {
  const changed = headers(bytes);
  changed.set(key, "wrong-target");
  await assert.rejects(verifyProvisioningDownload(revision, changed, bytes));
}
for (const change of [{ switchId: "switch-b" }, { kind: "nagios" }, { version: 2 }, { nativeValidated: true }, { requiresTargetAdapter: false }, { inventoryOnly: false }]) {
  const changed = encode({ ...plan, ...change });
  await assert.rejects(verifyProvisioningDownload(revision, headers(changed), changed));
}
await assert.rejects(verifyProvisioningDownload({ ...revision, switchId: undefined }, headers(bytes), bytes));
console.log("download SHA, version/profile/kind, switch header and JSON target/claims checks passed");

if (!process.env.CORE_DOWNLOAD_UNIT_ONLY) {
  const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || "playwright");
  const workspace = await mkdtemp("/tmp/moedove-core-provision-browser-");
  const socket = net.createServer();
  await new Promise(resolve => socket.listen(0, "127.0.0.1", resolve));
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const base = `http://127.0.0.1:${port}`;
  const password = "Core-provision-fixture-password-123";
  const child = spawn(process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server", ["-listen", `127.0.0.1:${port}`, "-data", workspace, "-web", fileURLToPath(new URL("../dist", import.meta.url))], { env: { ...process.env, IXP_SETUP_TOKEN: "core-provision-fixture" }, stdio: ["ignore", "ignore", "pipe"] });
  let browser;
  const errors = [];
  try {
    for (let attempt = 0; ; attempt++) {
      if (child.exitCode !== null) throw new Error("Fixture manager exited");
      if (await fetch(base + "/api/setup").then(r => r.ok).catch(() => false)) break;
      if (attempt > 100) throw new Error("Fixture startup timed out");
      await new Promise(resolve => setTimeout(resolve, 100));
    }
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox"] });
    const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
    const page = await context.newPage();
    page.on("pageerror", e => errors.push(e.message));
    let downloads = 0;
    page.on("download", () => downloads++);
    const setup = await context.request.post(base + "/api/setup", { data: { setupToken: "core-provision-fixture", name: "Core QA", email: "core-admin@example.test", password, ixpName: "Core Test IXP", asn: 64500 } });
    assert.equal(setup.status(), 200);
    const request = async (path, body, method = body ? "POST" : "GET") => {
      const session = await (await context.request.get(base + "/api/session")).json();
      const response = await context.request.fetch(base + "/api" + path, { method, headers: { "X-CSRF-Token": session.csrfToken }, ...(body ? { data: body } : {}) });
      return { status: response.status(), body: await response.json() };
    };
    const api = async (path, body, method) => {
      const response = await request(path, body, method);
      assert.ok(response.status < 300, `${path}: ${response.status} ${JSON.stringify(response.body)}`);
      return response.body;
    };
    const create = (kind, body) => api(`/resources/${kind}`, body);
    const location = await create("locations", { name: "Fixture DC" });
    const fabric = await create("infrastructures", { name: "Fixture fabric" });
    const vlan = await create("vlans", { name: "Prior form VLAN", tag: 100, ipv4Prefix: "198.51.100.0/24", infrastructureId: fabric.id });
    const switchA = await create("switches", { name: "核心交换机 A", locationId: location.id, infrastructureId: fabric.id, asn: 64500, loopbackIpv4: "10.0.0.1", loopbackName: "lo0" });
    const switchB = await create("switches", { name: "核心交换机 B", locationId: location.id, infrastructureId: fabric.id, asn: 64501, loopbackIpv4: "10.0.0.2", loopbackName: "lo0" });
    const portA = await create("ports", { name: "Ethernet1", switchId: switchA.id, speed: 10000 });
    const portB = await create("ports", { name: "Ethernet2", switchId: switchB.id, speed: 10000 });
    const preview = await api("/core/preview", { bundle: { name: "双栈核心链路", type: "ecmp", enabled: true, sideA: { switchId: switchA.id, mtu: 9000 }, sideB: { switchId: switchB.id, mtu: 9000 }, links: [{ enabled: true, bfd: true, ipv4Subnet: "192.0.2.0/31", ipv6Subnet: "2001:db8::/127", sideA: { portId: portA.id, duplex: "full", autoneg: true }, sideB: { portId: portB.id, duplex: "full", autoneg: true } }] } });
    const tags = async () => Object.fromEntries((await api("/core/inventory")).ports.map(p => [p.id, p._etag]));
    let bundle = await api("/core/bundles", { bundle: preview.bundle, portEtags: await tags() });
    const editBundle = async name => {
      const current = await api(`/core/bundles/${bundle.id}`);
      const { revision, derived, warnings, ...values } = current;
      bundle = await api(`/core/bundles/${bundle.id}`, { bundle: { ...values, name }, expectedRevision: revision, portEtags: await tags() }, "PUT");
    };
    await page.goto(base + "/#provisioning");
    await page.getByRole("heading", { name: "配置工具", exact: true }).waitFor();
    await page.getByRole("button", { name: "生成规则", exact: true }).click();
    await page.getByRole("button", { name: "新增生成规则", exact: true }).first().click();
    let dialog = page.getByRole("dialog");
    await dialog.locator("#profile-name").fill("核心 A 交换机计划");
    await dialog.locator("#profile-fabric").selectOption(fabric.id);
    await dialog.locator("#profile-vlan").selectOption(vlan.id);
    await dialog.locator("#profile-kind").selectOption("switch-core");
    assert.equal(await dialog.locator("#profile-vlan").count(), 0);
    assert.equal(await dialog.locator("#profile-fabric").count(), 0);
    assert.equal(await dialog.locator('input[type="checkbox"]').count(), 0);
    await dialog.locator("#profile-switch").selectOption(switchA.id);
    const saving = page.waitForRequest(r => r.url().endsWith("/api/resources/provisioning-profiles") && r.method() === "POST");
    await dialog.getByRole("button", { name: "保存生成规则", exact: true }).click();
    assert.deepEqual((await saving).postDataJSON().options, { kind: "switch-core", switchId: switchA.id, families: [4, 6] });
    await dialog.waitFor({ state: "hidden" });
    await page.getByRole("cell", { name: "核心交换机 A", exact: true }).waitFor();
    const profile = (await api("/resources/provisioning-profiles")).items.find(p => p.name === "核心 A 交换机计划");
    assert.equal(profile.options.switchId, switchA.id);
    console.log("switch profile UI selected explicit target and omitted previous scope/template options");
    const generate = async () => {
      await page.getByRole("button", { name: "生成版本", exact: true }).click();
      await page.getByRole("button", { name: "生成工具配置", exact: true }).click();
      await page.locator("#provisioning-profile").selectOption(profile.id);
      const response = page.waitForResponse(r => r.url().endsWith("/api/provisioning") && r.request().method() === "POST");
      await page.getByRole("button", { name: "生成待审版本", exact: true }).click();
      const data = await (await response).json();
      assert.ok(data.id, JSON.stringify(data));
      await page.getByRole("dialog").waitFor({ state: "hidden" });
      await page.getByLabel("计划目标交换机").waitFor();
      assert.equal(await page.getByLabel("计划目标交换机").innerText(), switchA.id);
      await page.locator("pre code").waitFor();
      assert.equal(data.kind, "switch-core");
      assert.equal(data.validationLevel, "structural");
      assert.equal(data.complete, false);
      return data;
    };
    const validate = async () => {
      await page.getByRole("button", { name: "检查计划结构", exact: true }).click();
      await page.getByRole("button", { name: "审核批准", exact: true }).waitFor();
    };
    const approve = async () => {
      await page.getByRole("button", { name: "审核批准", exact: true }).click();
      const form = page.getByRole("dialog");
      await form.getByRole("checkbox").check();
      await form.getByRole("button", { name: "确认审核批准", exact: true }).click();
      await form.waitFor({ state: "hidden" });
    };
    const first = await generate();
    await validate();
    await approve();
    const downloading = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载已批准文件", exact: true }).click();
    const file = await downloading;
    const body = await readFile(await file.path());
    const downloaded = JSON.parse(body);
    assert.equal(downloaded.switchId, switchA.id);
    assert.equal(downloaded.bgp.neighbors.length, 2);
    assert.equal(downloaded.nativeValidated, false);
    await page.getByLabel("工具配置真实 SHA-256").waitFor();
    assert.equal(await page.getByLabel("工具配置真实 SHA-256").innerText(), hash(body));
    const downloadURL = base + `/api/provisioning/${first.id}/config`;
    await page.route(downloadURL, async route => {
      const response = await route.fetch();
      await route.fulfill({ response, headers: { ...response.headers(), "x-provisioning-switch-id": switchB.id } });
    });
    await page.getByRole("button", { name: "下载已批准文件", exact: true }).click();
    await page.getByText("下载交换机标识不匹配，未保存文件。", { exact: true }).waitFor();
    assert.equal(downloads, 1);
    await page.unroute(downloadURL);
    await page.route(downloadURL, async route => {
      const response = await route.fetch();
      const modified = { ...await response.json(), switchId: switchB.id };
      const body = Buffer.from(JSON.stringify(modified));
      await route.fulfill({ response, body, headers: { ...response.headers(), "x-config-sha256": hash(body), "content-length": String(body.length) } });
    });
    await page.getByRole("button", { name: "下载已批准文件", exact: true }).click();
    await page.getByText("JSON 内的交换机目标或计划校验范围不匹配，未保存文件。", { exact: true }).waitFor();
    assert.equal(downloads, 1);
    await page.unroute(downloadURL);
    console.log("actual JSON download verified; mismatched target header and rehashed wrong-target JSON blocked");
    await editBundle("经复核的新核心链路名称");
    await page.getByRole("button", { name: "下载已批准文件", exact: true }).click();
    await page.getByRole("alert").waitFor();
    assert.equal(downloads, 1);
    const second = await generate();
    await page.getByRole("button", { name: "版本差异", exact: true }).click();
    await page.getByText("经复核的新核心链路名称", { exact: false }).first().waitFor();
    await validate();
    await editBundle("再次变更，旧审查已失效");
    await approve();
    await page.getByRole("alert").waitFor();
    assert.equal((await api("/provisioning")).items.find(r => r.id === second.id).status, "validated");
    console.log("typed plan diff, stale approved download and stale approval rejection passed");
    await page.setViewportSize({ width: 390, height: 844 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
    await page.screenshot({ path: "/tmp/moedove-core-provisioning-mobile.png", fullPage: true });
    assert.deepEqual(errors, []);
    console.log("core provisioning Chromium workflow and mobile viewport passed");
  } finally {
    await browser?.close();
    child.kill("SIGTERM");
  }
}
