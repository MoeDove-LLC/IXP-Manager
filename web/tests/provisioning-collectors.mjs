// Focused local fixture; generates artifacts without applying any device/service configuration.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const workspace = await mkdtemp("/tmp/moedove-provision-browser-");
const socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "Provisioning-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    workspace,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
    ...(process.env.TEST_BIRD_BIN ? ["-bird", process.env.TEST_BIRD_BIN] : []),
    ...(process.env.TEST_NAMED_CHECKZONE_BIN
      ? ["-named-checkzone", process.env.TEST_NAMED_CHECKZONE_BIN]
      : []),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "provisioning-fixture-bootstrap" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser;
const errors = [];
try {
  for (let attempt = 0; ; attempt++) {
    if (child.exitCode !== null) throw new Error("Isolated manager exited");
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (attempt > 100) throw new Error("Isolated manager startup timed out");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    }),
    page = await context.newPage();
  page.on("pageerror", (e) => errors.push(e.message));
  const request = async (
    path,
    body,
    method = body ? "POST" : "GET",
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    // Fixture-only rebasing: every intentional inventory setup update obtains its own current baseline.
    const fixtureHeaders = {};
    if (
      ["PUT", "DELETE"].includes(method) &&
      /^\/resources\/(?!provisioning-profiles\/)[^/]+\/[^/]+$/.test(path)
    ) {
      const baselineResponse = await ctx.request.get(base + "/api" + path);
      if (baselineResponse.ok()) {
        const baseline = await baselineResponse.json();
        if (baseline._etag)
          fixtureHeaders["If-Match"] = JSON.stringify(baseline._etag);
      }
      if (body && typeof body === "object" && !Array.isArray(body)) {
        const { _etag, ...values } = body;
        body = values;
      }
    }
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: { ...fixtureHeaders, "X-CSRF-Token": session.csrfToken || "" },
      ...(body ? { data: body } : {}),
    });
    return { status: response.status(), data: await response.json() };
  };
  const api = async (path, body, method) => {
    const r = await request(path, body, method);
    assert.ok(r.status < 300, `${path}: ${r.status} ${r.data.error || ""}`);
    return r.data;
  };
  const setup = await context.request.post(base + "/api/setup", {
    data: {
      setupToken: "provisioning-fixture-bootstrap",
      name: "Provision QA",
      email: "admin@example.test",
      password,
      ixpName: "Provision Test IXP",
      asn: 64500,
    },
  });
  assert.equal(setup.status(), 200);
  const create = (kind, data) => api(`/resources/${kind}`, data);
  const member = await create("members", {
    name: "Test Network",
    asn: 64501,
    status: "active",
  });
  const dc = await create("locations", {
    name: "Test DC",
    city: "Shanghai",
    country: "CN",
  });
  const sw = await create("switches", {
    name: "sw-test",
    locationId: dc.id,
    managementIp: "192.0.2.250",
  });
  const portRow = await create("ports", {
    name: "Ethernet1",
    switchId: sw.id,
    speed: 10000,
    status: "active",
  });
  const vlan = await create("vlans", {
    name: "Test LAN",
    tag: 100,
    ipv4Prefix: "192.0.2.0/24",
    ipv6Prefix: "2001:db8::/64",
  });
  await create("connections", {
    name: "Test Link",
    memberId: member.id,
    portId: portRow.id,
    vlanId: vlan.id,
    ipv4: "192.0.2.10",
    ipv6: "2001:db8::10",
    status: "active",
    canPing4: "",
    canPing6: "",
  });
  const go = async (name) => {
    await page.goto(base + "/#" + name);
    await page.locator("main").waitFor();
  };
  await go("connections");
  await page
    .getByRole("button", { name: "编辑 Test Link", exact: true })
    .click();
  assert.equal(await page.locator("#field-canPing4").isChecked(), true);
  assert.equal(await page.locator("#field-canPing6").isChecked(), true);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await go("routers");
  await page.locator(".page-heading button").click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#field-name").fill("Test collector");
  await dialog.locator("#field-role").selectOption("route-collector");
  await dialog.locator("#field-collectorPolicy").selectOption("observe");
  await dialog.locator("#field-platform").selectOption("junos");
  await dialog.getByRole("alert").waitFor();
  await dialog.locator("#field-platform").selectOption("bird2");
  await dialog.locator("#field-asn").fill("64500");
  await dialog.locator("#field-routerId").fill("192.0.2.1");
  await dialog.locator("#field-vlanId").selectOption(vlan.id);
  await dialog.locator("#field-localIpv4").fill("192.0.2.1");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  const collector = (await api("/resources/routers")).items.find(
    (r) => r.name === "Test collector",
  );
  assert.equal(collector.role, "route-collector");
  assert.equal(collector.collectorPolicy, "observe");
  await go("peers");
  await page.locator(".page-heading button").click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#field-name").fill("Observe empty allowlist");
  await dialog.locator("#field-routerId").selectOption(collector.id);
  await dialog.locator("#field-memberId").selectOption(member.id);
  await dialog.locator("#field-asn").fill("64501");
  await dialog.locator("#field-address").fill("192.0.2.10");
  await dialog.locator("#field-enabled").check();
  await dialog.locator("#field-maxPrefixes").fill("100");
  await dialog.locator("#field-allowedPrefixes").fill("");
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page.locator("#deployment-router").selectOption(collector.id);
  await page
    .getByText("始终不导出路由，不写入内核转发表。", { exact: false })
    .waitFor();
  await page.getByRole("button", { name: "生成预览", exact: true }).click();
  await page.locator("pre code").waitFor();
  const revision = (await api("/deployments")).items[0];
  assert.equal(revision.routerRole, "route-collector");
  assert.equal(revision.collectorPolicy, "observe");
  const config = (await api(`/deployments/${revision.id}`)).config;
  assert.ok(config.includes("export none"));
  assert.ok(!config.includes("protocol kernel"));
  console.log(
    "collector role, unsupported platform explanation, observe empty allowlist and no-export artifact passed",
  );
  const makeProfile = async (name, kind, fill) => {
    await go("provisioning");
    await page.getByRole("button", { name: "生成规则", exact: true }).click();
    await page
      .getByRole("button", { name: "新增生成规则", exact: true })
      .first()
      .click();
    const form = page.getByRole("dialog");
    await form.locator("#profile-name").fill(name);
    await form.locator("#profile-kind").selectOption(kind);
    await form.locator("#profile-vlan").selectOption(vlan.id);
    if (fill) await fill(form);
    await form
      .getByRole("button", { name: "保存生成规则", exact: true })
      .click();
    await form.waitFor({ state: "hidden" });
    await page.getByRole("cell", { name, exact: true }).waitFor();
    return (await api("/resources/provisioning-profiles")).items.find(
      (row) => row.name === name,
    );
  };
  let nagios = await makeProfile("Test Nagios", "nagios");
  assert.equal(nagios.options.kind, "nagios");
  await page
    .getByRole("button", { name: "编辑生成规则 Test Nagios", exact: true })
    .click();
  const changed = await api(
    `/resources/provisioning-profiles/${nagios.id}`,
    {
      name: nagios.name,
      options: {
        ...nagios.options,
        nagios: {
          ...nagios.options.nagios,
          memberHostTemplate: "qa-member-host",
        },
      },
      expectedRevision: nagios.revision,
    },
    "PUT",
  );
  dialog = page.getByRole("dialog");
  await dialog
    .getByRole("button", { name: "保存生成规则", exact: true })
    .click();
  await dialog.getByRole("alert").waitFor();
  assert.equal(
    await dialog
      .getByRole("button", { name: "保存生成规则", exact: true })
      .isDisabled(),
    true,
  );
  await dialog.getByRole("button", { name: "取消", exact: true }).click();
  nagios = changed;
  assert.equal(
    (await api("/resources/provisioning-profiles")).items.find(
      (row) => row.id === nagios.id,
    ).revision,
    changed.revision,
  );
  console.log("typed profile CRUD and stale-revision rejection passed");
  const generate = async (profile) => {
    await go("provisioning");
    await page.getByRole("button", { name: "生成版本", exact: true }).click();
    await page
      .getByRole("button", { name: "生成工具配置", exact: true })
      .click();
    await page.locator("#provisioning-profile").selectOption(profile.id);
    const createdResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith("/api/provisioning") &&
        response.request().method() === "POST",
    );
    await page
      .getByRole("button", { name: "生成待审版本", exact: true })
      .click();
    const created = await (await createdResponse).json();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.locator(`[data-provisioning-id="${created.id}"]`).waitFor();
    await page.locator("pre code").waitFor();
    const list = (await api("/provisioning")).items;
    const record = list.find((row) => row.id === created.id);
    assert.ok(!Object.hasOwn(record, "config"));
    const detail = await api(`/provisioning/${record.id}`);
    assert.equal(detail.redacted, true);
    return record;
  };
  const approve = async (record) => {
    await page
      .getByRole("button", {
        name: record.kind === "dns-arpa" ? "运行 DNS 原生校验" : "检查片段结构",
        exact: true,
      })
      .click();
    await page.getByRole("button", { name: "审核批准", exact: true }).click();
    const form = page.getByRole("dialog");
    await form.getByRole("checkbox").check();
    await form
      .getByRole("button", { name: "确认审核批准", exact: true })
      .click();
    await form.waitFor({ state: "hidden" });
    const downloading = page.waitForEvent("download");
    await page
      .getByRole("button", { name: "下载已批准文件", exact: true })
      .click();
    const received = await downloading,
      bytes = await readFile(await received.path());
    const actual = createHash("sha256").update(bytes).digest("hex");
    await page.getByLabel("工具配置真实 SHA-256").waitFor();
    assert.equal(
      await page.getByLabel("工具配置真实 SHA-256").innerText(),
      actual,
    );
    assert.notEqual(actual, record.checksum);
    return bytes.toString("utf8");
  };
  const nagiosRevision = await generate(nagios);
  assert.equal(nagiosRevision.complete, false);
  assert.equal(nagiosRevision.validationLevel, "structural");
  await page.getByRole("button", { name: "版本差异", exact: true }).click();
  await page.getByText("尚无已批准基线", { exact: false }).waitFor();
  await approve(nagiosRevision);
  assert.equal(
    await page.getByText("片段结构校验", { exact: true }).count(),
    1,
  );
  console.log(
    "Nagios lazy preview, structural review, approval and actual-SHA download passed",
  );
  // Retention is explicit: a newer approved version permits pruning the old artifact.
  const newerNagios = await generate(nagios);
  await approve(newerNagios);
  await page.locator(`[data-provisioning-id="${nagiosRevision.id}"]`).click();
  await page
    .getByRole("button", { name: "清理旧版本文件", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox").check();
  await dialog
    .getByRole("button", { name: "确认清理文件", exact: true })
    .click();
  await dialog.waitFor({ state: "hidden" });
  await page.getByText("此版本文件已清理", { exact: false }).waitFor();
  await page.locator(`[data-provisioning-id="${newerNagios.id}"]`).click();
  await page.getByRole("button", { name: "版本差异", exact: true }).click();
  await page.getByText("旧基线文件已清理", { exact: false }).waitFor();
  console.log("explicit artifact cleanup and pruned-baseline warning passed");
  const smoke = await makeProfile(
    "Test Smokeping",
    "smokeping",
    async (form) => {
      await form.locator("#profile-smoke-level").selectOption("2");
    },
  );
  const smokeRevision = await generate(smoke);
  assert.equal(smokeRevision.complete, false);
  await approve(smokeRevision);
  console.log(
    "Smokeping typed probes, structural approval and download passed",
  );
  const dns = await makeProfile(
    "Test reverse DNS",
    "dns-arpa",
    async (form) => {
      await form.locator("#profile-dns-mode").selectOption("new");
      await form.locator("#profile-dns-prefix").selectOption("192.0.2.0/24");
      await form.locator("#profile-dns-primary").fill("ns1.example.test.");
      await form
        .locator("#profile-dns-mailbox")
        .fill("hostmaster.example.test.");
      await form.locator("#profile-dns-suffix").fill("members.example.test.");
      await form
        .locator("#profile-dns-servers")
        .fill("ns1.example.test.\nns2.example.test.");
    },
  );
  assert.ok(!dns.options.dns.serial);
  const dnsRevision = await generate(dns),
    dnsDetail = await api(`/provisioning/${dnsRevision.id}`);
  assert.equal(dnsRevision.complete, true);
  assert.ok(dnsDetail.config.includes("SOA"));
  assert.equal(typeof dnsRevision.dnsSerial, "number");
  assert.ok(dnsRevision.dnsSerial > 0);
  assert.ok(dnsDetail.config.includes(String(dnsRevision.dnsSerial)));
  await page.getByLabel("DNS SOA 序列号", { exact: true }).waitFor();
  assert.equal(
    await page.getByLabel("DNS SOA 序列号", { exact: true }).innerText(),
    `SOA serial ${dnsRevision.dnsSerial}`,
  );
  const caps = (await api("/provisioning/capabilities")).items;
  if (caps.find((cap) => cap.kind === "dns-arpa").nativeCheckerAvailable) {
    await approve(dnsRevision);
    console.log(
      "DNS full zone native validation, approval and download passed",
    );
  } else {
    await page
      .getByText("服务端尚未配置可用的 DNS 原生校验器", { exact: false })
      .waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "运行 DNS 原生校验", exact: true })
        .isDisabled(),
      true,
    );
    assert.ok(
      (await request(`/provisioning/${dnsRevision.id}/validate`, {})).status >=
        400,
    );
    assert.equal(
      (await api("/provisioning")).items.find(
        (row) => row.id === dnsRevision.id,
      ).status,
      "draft",
    );
    console.log(
      "DNS full zone preview and honest missing-native-checker refusal passed",
    );
  }
  await create("users", {
    name: "Viewer",
    email: "viewer@example.test",
    role: "viewer",
    password,
  });
  const viewerContext = await browser.newContext();
  await viewerContext.request.post(base + "/api/login", {
    data: { email: "viewer@example.test", password },
  });
  const viewer = await viewerContext.newPage();
  await viewer.goto(base + "/#provisioning");
  await viewer.locator("pre code").waitFor();
  assert.equal(
    await viewer
      .getByRole("button", { name: "生成工具配置", exact: true })
      .count(),
    0,
  );
  assert.equal(
    await viewer
      .getByRole("button", { name: "下载已批准文件", exact: true })
      .count(),
    0,
  );
  assert.equal(
    (
      await viewerContext.request.get(
        base + `/api/provisioning/${newerNagios.id}/config`,
      )
    ).status(),
    403,
  );
  await viewer.getByRole("button", { name: "生成规则", exact: true }).click();
  await viewer
    .getByRole("cell", { name: "Test Nagios", exact: true })
    .waitFor();
  assert.equal(
    await viewer
      .getByRole("button", { name: "新增生成规则", exact: true })
      .count(),
    0,
  );
  assert.equal(await viewer.getByRole("button", { name: "监控配置检查", exact: true }).count(), 0);
  assert.equal((await viewerContext.request.get(base + "/api/provisioning/monitoring-health")).status(), 403);
  await go("security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("令牌名称").fill("profile-bound pull");
  await dialog
    .getByRole("checkbox", {
      name: "下载指定规则的已批准工具配置",
      exact: true,
    })
    .check();
  await dialog
    .getByRole("checkbox", { name: "Test Nagios", exact: true })
    .check();
  await dialog.getByLabel("当前密码", { exact: true }).fill(password);
  const tokenResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/account/tokens") &&
      response.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "继续", exact: true }).click();
  const issued = await (await tokenResponse).json();
  assert.deepEqual(issued.item.profileIds, [nagios.id]);
  const tokenGet = (path) =>
    fetch(base + "/api" + path, {
      headers: { Authorization: `Bearer ${issued.token}` },
    });
  assert.equal(
    (await tokenGet(`/provisioning/${newerNagios.id}/config`)).status,
    200,
  );
  assert.equal(
    (await tokenGet(`/provisioning/${smokeRevision.id}/config`)).status,
    403,
  );
  console.log(
    "viewer read-only and profile-bound provisioning pull token passed",
  );
  const monitoringChecks = (await api("/provisioning/monitoring-health")).items;
  const nagiosObservation = monitoringChecks.find((row) => row.profileId === nagios.id);
  const smokeObservation = monitoringChecks.find((row) => row.profileId === smoke.id);
  assert.equal(nagiosObservation.revisionId, newerNagios.id);
  assert.equal(nagiosObservation.approvalStatus, "current");
  assert.equal(nagiosObservation.pullStatus, "recent");
  assert.ok(nagiosObservation.lastPulledAt && nagiosObservation.lastDownloadedAt);
  assert.equal(smokeObservation.pullStatus, "not-observed");
  assert.ok(smokeObservation.lastDownloadedAt);
  assert.ok(!monitoringChecks.some((row) => row.profileId === dns.id));
  await api(`/resources/provisioning-profiles/${smoke.id}`, {
    name: "Test Smokeping revised", options: smoke.options, expectedRevision: smoke.revision,
  }, "PUT");
  await go("provisioning");
  await page.getByRole("button", { name: "监控配置检查", exact: true }).click();
  const monitoringPanel = page.getByRole("region", { name: "监控配置检查", exact: true });
  const nagiosHealth = monitoringPanel.locator(`[data-profile-id="${nagios.id}"]`);
  const smokeHealth = monitoringPanel.locator(`[data-profile-id="${smoke.id}"]`);
  await nagiosHealth.getByText("已批准且匹配当前配置", { exact: true }).waitFor();
  await smokeHealth.getByText("批准版本待更新", { exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "monitoring report must fit 390px viewport");
  if (process.env.MONITORING_HEALTH_SCREENSHOT) {
    await monitoringPanel.screenshot({ path: process.env.MONITORING_HEALTH_SCREENSHOT });
  }
  // Simulate a rule added by another session while this page still has its old rule list.
  const added = await create("provisioning-profiles", {
    name: "New monitoring rule on open page",
    options: { kind: "nagios", families: [4], vlanId: vlan.id },
  });
  await monitoringPanel.getByRole("button", { name: "刷新监控检查", exact: true }).click();
  await monitoringPanel.locator(`[data-profile-id="${added.id}"]`).getByText("无批准版本", { exact: true }).waitFor();
  await monitoringPanel.getByRole("link", { name: `查看规则 ${added.name}`, exact: true }).click();
  await page.getByRole("cell", { name: added.name, exact: true }).waitFor();
  assert.match(page.url(), /#provisioning\?profileId=/);
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log("PASS monitoring report, admin gate, approved drift, separate pull and browser download, and 390px rule link");
  await go("provisioning");
  await page.getByRole("button", { name: "生成版本", exact: true }).click();
  await page.locator(`[data-provisioning-id="${newerNagios.id}"]`).click();
  await page.locator("pre code").waitFor();
  await page.screenshot({
    path: "/tmp/moedove-provisioning-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-provisioning-mobile.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "生成规则", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑生成规则 Test reverse DNS", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  assert.ok(
    await dialog.evaluate(
      (element) => element.scrollWidth <= element.clientWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-provisioning-profile-mobile.png",
    fullPage: true,
  });
  console.log("provisioning 390px layouts passed");
  assert.deepEqual(errors, []);
  console.log(`Fixture: ${workspace}`);
} finally {
  await browser?.close();
  if (child.exitCode === null) {
    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(timer);
  }
}
