// Receiver approval UI smoke: isolated inventory, no external UDP delivery.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-receiver-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "SFlow-browser-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-receiver-ui-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "sflow-ui-bootstrap" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser,
  stderr = "";
child.stderr.on("data", (chunk) => {
  stderr = (stderr + chunk.toString()).slice(-4000);
});
try {
  for (let attempt = 0; ; attempt++) {
    if (child.exitCode !== null)
      throw new Error(`sFlow fixture exited: ${stderr}`);
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (attempt > 100) throw new Error("sFlow fixture startup timeout");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
      timezoneId: "Asia/Shanghai",
    }),
    page = await context.newPage(),
    errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const setup = await context.request.post(base + "/api/setup", {
    data: {
      setupToken: "sflow-ui-bootstrap",
      name: "sFlow QA",
      email: "admin@example.test",
      password,
      ixpName: "Sampled Flow QA",
      asn: 64500,
    },
  });
  assert.equal(setup.status(), 200);
  const api = async (
    path,
    data,
    method = data ? "POST" : "GET",
    etag,
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: {
        "X-CSRF-Token": session.csrfToken || "",
        ...(etag ? { "If-Match": JSON.stringify(etag) } : {}),
      },
      ...(data ? { data } : {}),
    });
    const result = await response.json();
    assert.ok(
      response.ok(),
      `${path}: ${response.status()} ${JSON.stringify(result)}`,
    );
    return result;
  };
  const location = await api("/resources/locations", { name: "Sampling DC" }),
    sw = await api("/resources/switches", {
      name: "Sampling switch",
      locationId: location.id,
    });
  const vlan = await api("/resources/vlans", {
    name: "Sampling VLAN",
    tag: 511,
    ipv4Prefix: "192.0.2.0/24",
    ipv6Prefix: "2001:db8:511::/64",
  });
  const exporter = await api("/resources/sflow-exporters", {
    name: "Receiver exporter",
    switchId: sw.id,
    sourceAddress: "198.51.100.20",
    agentAddress: "198.51.100.20",
    subAgentId: 0,
    vlanIds: [vlan.id],
    enabled: true,
  });
  // Real, reviewed inventory attribution for two directed metadata samples.
  const members = [],
    ports = [],
    connections = [];
  for (const [index, name] of ["Member A", "Private member B"].entries()) {
    const member = await api("/resources/members", {
      name,
      asn: 64510 + index,
      status: "active",
      ...(index === 1 ? { peeringMatrixEnabled: false } : {}),
    });
    const physical = await api("/resources/ports", {
      name: `Sample member port${index + 1}`,
      switchId: sw.id,
      status: "active",
      speed: 10000,
    });
    const connection = await api("/resources/connections", {
      name: `Private connection${index + 1}`,
      memberId: member.id,
      portId: physical.id,
      vlanId: vlan.id,
      ipv4: `192.0.2.${10 + index}`,
      ipv6: `2001:db8:511::${10 + index}`,
      status: "active",
    });
    await api("/resources/mac-addresses", {
      name: `Sample MAC${index + 1}`,
      connectionId: connection.id,
      address: `02:00:00:00:51:0${index + 1}`,
    });
    members.push(member);
    ports.push(physical);
    connections.push(connection);
  }
  const discoveryToken = await api("/account/tokens", {
    name: "Local inventory-review fixture",
    scopes: ["devices:write"],
    switchIds: [sw.id],
    currentPassword: password,
  });
  const discoveryResponse = await fetch(base + "/api/devices/discoveries", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${discoveryToken.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      version: 1,
      switchId: sw.id,
      observedAt: new Date().toISOString(),
      security: "authPriv",
      identity: {
        name: "Sampling switch",
        description: "Local fixture",
        objectId: "1.3.6.1.4.1.9",
        location: "Local",
        contact: "fixture@example.test",
      },
      uptimeTicks: "10000",
      engineIdentity: "a".repeat(64),
      engineEpoch: "fixture",
      interfaces: ports.map((port, index) => ({
        ifIndex: index + 1,
        name: port.name,
        type: 6,
        mtu: 1500,
        speedMbps: 10000,
        adminStatus: "up",
        operStatus: "up",
        physicalAddress: `02:aa:00:00:00:0${index + 1}`,
        inOctets: "1000",
        outOctets: "2000",
        counterBits: 64,
        discontinuityTicks: "0",
        lastChangeTicks: "0",
      })),
      entities: [],
      sensors: [],
      fdb: [],
      warnings: [],
    }),
  });
  assert.equal(
    discoveryResponse.status,
    201,
    await discoveryResponse.clone().text(),
  );
  const discovery = await discoveryResponse.json();
  const preview = await api(`/devices/discoveries/${discovery.id}/preview`);
  await api(`/devices/discoveries/${discovery.id}/apply`, {
    baseChecksum: preview.baseChecksum,
    ports: ports.map((port, index) => ({
      ifIndex: index + 1,
      portId: port.id,
    })),
  });
  assert.equal((await api(`/resources/ports/${ports[0].id}`)).snmpIfIndex, 1);
  await page.goto(base + "/#sflow");
  await page.getByRole("button", { name: "成员接收器", exact: true }).click();
  await page.getByRole("button", { name: "新增接收器", exact: true }).click();
  await page.locator("#receiver-name").fill("Reviewed member receiver");
  await page.locator("#receiver-member").selectOption(members[0].id);
  await page.locator("#receiver-address").fill("203.0.113.50");
  await page.locator("#receiver-port").fill("6343");
  await page
    .locator(`input[name="receiver-connection"][value="${connections[0].id}"]`)
    .check();
  await page.locator(`input[name="receiver-vlan"][value="${vlan.id}"]`).check();
  await page.locator("#receiver-enabled").check();
  const created = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === "/api/sflow/receivers" &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "保存申请", exact: true }).click();
  const createdResponse = await created;
  assert.equal(createdResponse.status(), 201, await createdResponse.text());
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const card = page
    .getByTestId("sflow-receiver")
    .filter({ hasText: "Reviewed member receiver" });
  await card.getByText("待审批", { exact: true }).waitFor();
  const receiverId = await card.getAttribute("data-receiver-id");
  await card.getByRole("button", { name: "审阅范围", exact: true }).click();
  await page.getByRole("region", { name: "采样包头数据披露" }).waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "批准此修订", exact: true })
      .isDisabled(),
    true,
  );
  await page.locator("#receiver-disclosure-ack").check();
  await page
    .locator("#receiver-approval-password")
    .fill("wrong-current-password");
  const rejectedPassword = page.waitForResponse((r) =>
    r.url().endsWith(`/receivers/${receiverId}/approve`),
  );
  await page.getByRole("button", { name: "批准此修订", exact: true }).click();
  assert.equal((await rejectedPassword).status(), 403);
  await page.getByRole("dialog").getByRole("alert").waitFor();
  assert.equal(
    await page.locator("#receiver-approval-password").inputValue(),
    "",
  );
  assert.equal((await api(`/sflow/receivers/${receiverId}`)).status, "draft");
  await page.locator("#receiver-approval-password").fill(password);
  const approved = page.waitForResponse((r) =>
    r.url().endsWith(`/receivers/${receiverId}/approve`),
  );
  await page.getByRole("button", { name: "批准此修订", exact: true }).click();
  const approvedResponse = await approved;
  assert.equal(approvedResponse.status(), 200, await approvedResponse.text());
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await card.getByText("已审批", { exact: true }).waitFor();
  const downloaded = page.waitForEvent("download");
  await card.getByRole("button", { name: "导出静态计划", exact: true }).click();
  const download = await downloaded;
  const planPath = `${dir}/receiver-plan.json`;
  await download.saveAs(planPath);
  const { readFile } = await import("node:fs/promises"),
    plan = JSON.parse(await readFile(planPath, "utf8"));
  assert.equal(plan.receiver.id, receiverId);
  assert.equal(plan.forwardingEnabled, false);
  assert.equal(plan.requiresCurrentOwnershipCheck, true);
  assert.ok(plan.bindings.length > 0);
  await page
    .getByText(
      "已核对修订及校验和并导出静态计划。它不授予实时转发，也不证明目的端收到数据。",
      { exact: true },
    )
    .waitFor();
  console.log(
    "receiver real create -> review -> reauthenticated approval -> checksum-verified advisory plan passed",
  );
  await page.screenshot({
    path: "/tmp/moedove-receiver-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
    false,
  );
  await page.screenshot({
    path: "/tmp/moedove-receiver-mobile.png",
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  console.log("receiver responsive 390px and no page errors passed");

  await page.setViewportSize({ width: 1440, height: 1000 });
  await card.getByRole("button", { name: "编辑申请", exact: true }).click();
  await page.locator("#receiver-address").fill("203.0.113.51");
  const before = await api(`/sflow/receivers/${receiverId}`);
  await api(
    `/sflow/receivers/${receiverId}`,
    {
      name: before.name,
      spec: { ...before.spec, port: 6344 },
      expectedRevision: before.revision,
    },
    "PUT",
  );
  const conflicted = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/receivers/${receiverId}`) &&
      r.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "保存申请", exact: true }).click();
  assert.equal((await conflicted).status(), 409);
  await page.getByRole("region", { name: "接收器版本冲突" }).waitFor();
  assert.equal(
    await page.locator("#receiver-address").inputValue(),
    "203.0.113.51",
  );
  await page
    .getByRole("button", { name: "按当前版本重新核对草稿", exact: true })
    .click();
  const merged = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/receivers/${receiverId}`) &&
      r.request().method() === "PUT",
  );
  await page.getByRole("button", { name: "保存申请", exact: true }).click();
  assert.equal((await merged).status(), 200);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await card.getByText("待审批", { exact: true }).waitFor();
  assert.equal(
    await card
      .getByRole("button", { name: "导出静态计划", exact: true })
      .count(),
    0,
  );
  const savedDraft = await api(`/sflow/receivers/${receiverId}`);
  assert.equal(savedDraft.spec.address, "203.0.113.51");
  assert.equal(savedDraft.status, "draft");
  console.log(
    "receiver CAS rejects concurrent write, retains draft, explicit re-review and clears prior approval passed",
  );

  await card
    .getByRole("button", { name: "停发并撤销授权", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("checkbox").check();
  await page.getByRole("button", { name: "确认停发", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await card.getByText("已停用", { exact: true }).waitFor();
  const stopped = await api(`/sflow/receivers/${receiverId}`);
  assert.equal(stopped.spec.enabled, false);
  const stopDownload = page.waitForEvent("download");
  await card.getByRole("button", { name: "导出静态计划", exact: true }).click();
  await (await stopDownload).saveAs(`${dir}/stop-plan.json`);
  const stopPlan = JSON.parse(await readFile(`${dir}/stop-plan.json`, "utf8"));
  assert.equal(stopPlan.receiver.enabled, false);
  assert.deepEqual(stopPlan.bindings, []);
  await card.getByRole("button", { name: "删除", exact: true }).click();
  await page
    .locator("#receiver-delete-reason")
    .fill("Reviewed fixture decommission");
  await page.getByRole("dialog").getByRole("checkbox").check();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await card.waitFor({ state: "hidden" });
  console.log(
    "receiver explicit disable, revoked desired state, empty stop plan and reasoned deletion passed",
  );

  const foreign = await api("/sflow/receivers", {
    name: "Private other member receiver",
    spec: {
      memberId: members[1].id,
      connectionIds: [connections[1].id],
      vlanIds: [vlan.id],
      address: "203.0.113.80",
      port: 6343,
      enabled: false,
    },
  });
  await api("/resources/users", {
    name: "Receiver member admin",
    email: "receiver-member@example.test",
    password,
    role: "member",
    memberIds: [members[0].id],
    memberAdminIds: [members[0].id],
  });
  const memberContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
  });
  assert.equal(
    (
      await memberContext.request.post(base + "/api/login", {
        data: { email: "receiver-member@example.test", password },
      })
    ).status(),
    200,
  );
  const memberPage = await memberContext.newPage();
  memberPage.on("pageerror", (e) => errors.push(e.message));
  await memberPage.goto(base + "/#sflow");
  await memberPage
    .getByRole("button", { name: "成员接收器", exact: true })
    .click();
  await memberPage
    .getByRole("button", { name: "新增接收器", exact: true })
    .click();
  assert.equal(await memberPage.locator("#receiver-member").isDisabled(), true);
  assert.equal(
    await memberPage.locator("#receiver-member").inputValue(),
    members[0].id,
  );
  assert.equal(
    await memberPage
      .locator(
        `input[name="receiver-connection"][value="${connections[1].id}"]`,
      )
      .count(),
    0,
  );
  await memberPage.locator("#receiver-name").fill("Own member receiver");
  await memberPage.locator("#receiver-address").fill("203.0.113.90");
  await memberPage
    .locator(`input[name="receiver-connection"][value="${connections[0].id}"]`)
    .check();
  await memberPage
    .locator(`input[name="receiver-vlan"][value="${vlan.id}"]`)
    .check();
  await memberPage
    .getByRole("button", { name: "保存申请", exact: true })
    .click();
  await memberPage.getByRole("dialog").waitFor({ state: "hidden" });
  const memberCard = memberPage
    .getByTestId("sflow-receiver")
    .filter({ hasText: "Own member receiver" });
  await memberCard.getByText("已停用", { exact: true }).waitFor();
  assert.equal(
    await memberPage
      .getByText("Private other member receiver", { exact: true })
      .count(),
    0,
  );
  await memberCard
    .getByRole("button", { name: "审阅范围", exact: true })
    .click();
  await memberPage.getByRole("region", { name: "采样包头数据披露" }).waitFor();
  assert.equal(
    await memberPage
      .getByRole("button", { name: "批准此修订", exact: true })
      .count(),
    0,
  );
  await memberPage
    .getByRole("button", { name: "关闭审阅", exact: true })
    .click();
  const memberSession = await (
    await memberContext.request.get(base + "/api/session")
  ).json();
  const denied = await memberContext.request.post(
    base + "/api/sflow/receivers",
    {
      headers: { "X-CSRF-Token": memberSession.csrfToken },
      data: {
        name: "Forged",
        spec: { ...foreign.spec, id: undefined, revision: undefined },
      },
    },
  );
  assert.equal(denied.status(), 403);
  assert.equal(
    (
      await memberContext.request.get(
        base + `/api/sflow/receivers/${foreign.id}`,
      )
    ).status(),
    404,
  );
  assert.equal(
    await memberPage.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
    false,
  );
  await memberContext.close();
  await api("/resources/users", {
    name: "Receiver readonly member",
    email: "receiver-viewer@example.test",
    password,
    role: "member",
    memberIds: [members[0].id],
    memberAdminIds: [],
  });
  const readonlyContext = await browser.newContext();
  assert.equal(
    (
      await readonlyContext.request.post(base + "/api/login", {
        data: { email: "receiver-viewer@example.test", password },
      })
    ).status(),
    200,
  );
  const readonlyPage = await readonlyContext.newPage();
  readonlyPage.on("pageerror", (e) => errors.push(e.message));
  await readonlyPage.goto(base + "/#sflow");
  await readonlyPage
    .getByRole("button", { name: "成员接收器", exact: true })
    .click();
  await readonlyPage
    .getByTestId("sflow-receiver")
    .filter({ hasText: "Own member receiver" })
    .waitFor();
  for (const name of [
    "新增接收器",
    "编辑申请",
    "停发并撤销授权",
    "删除",
    "批准此修订",
  ])
    assert.equal(
      await readonlyPage.getByRole("button", { name, exact: true }).count(),
      0,
    );
  await readonlyContext.close();
  await page.goto(base + "/#security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await page
    .getByLabel("令牌名称", { exact: true })
    .fill("Reviewed forwarding token");
  const forwardScope = page.getByRole("checkbox", {
    name: "采集器为已审批接收器申请逐包转发授权",
    exact: true,
  });
  const writeScope = page.getByRole("checkbox", {
    name: "采集器上报指定交换机的 sFlow 元数据",
    exact: true,
  });
  await forwardScope.check();
  assert.equal(await writeScope.isChecked(), true);
  await writeScope.uncheck();
  assert.equal(await forwardScope.isChecked(), false);
  await forwardScope.check();
  await page
    .getByRole("dialog")
    .getByLabel("当前密码", { exact: true })
    .fill(password);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("alert")
    .filter({ hasText: "必须至少绑定一台交换机" })
    .waitFor();
  await page
    .getByRole("checkbox", { name: "Sampling switch", exact: true })
    .check();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  await page.getByLabel("API 令牌", { exact: true }).waitFor();
  await page.getByLabel("我已妥善保存").check();
  await page.getByRole("button", { name: "完成", exact: true }).click();
  const issued = (await api("/account/tokens")).items.find(
    (t) => t.name === "Reviewed forwarding token",
  );
  assert.ok(
    issued.scopes.includes("sflow:forward") &&
      issued.scopes.includes("sflow:write"),
  );
  assert.deepEqual(issued.switchIds, [sw.id]);
  console.log(
    "forward scope UI requires write and selected switch, with explicit token secret acknowledgement passed",
  );
  assert.deepEqual(errors, []);
  console.log(
    "member-admin own-only create, no approval control, forged destination owner rejection and tenant isolation passed",
  );
} finally {
  if (browser) await browser.close();
  if (child.exitCode === null) {
    child.kill("SIGTERM");
    await Promise.race([
      new Promise((resolve) => child.once("exit", resolve)),
      new Promise((resolve) => setTimeout(resolve, 5000)),
    ]);
    if (child.exitCode === null) {
      child.kill("SIGKILL");
      await new Promise((resolve) => child.once("exit", resolve));
    }
  }
  console.log("sFlow isolated browser and manager stopped");
}
