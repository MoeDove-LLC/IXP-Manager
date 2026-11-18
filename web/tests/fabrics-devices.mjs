// Fresh isolated manager, no real SNMP device or external network access.
import assert from "node:assert/strict";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  }),
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 } }),
  errors = [];
page.on("pageerror", (e) => errors.push(e.message));
const base = process.env.TEST_URL || "http://127.0.0.1:18080",
  password = "Fabric-device-test-password-123";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const call = async (
  target,
  path,
  body,
  method = body ? "POST" : "GET",
  check = true,
) =>
  target.evaluate(
    async ({ path, body, method, check }) => {
      const s = await (await fetch("/api/session")).json();
      // Test fixture setup deliberately reads a current baseline; production editors never do this before saving.
      const fixtureHeaders = {};
      if (
        ["PUT", "DELETE"].includes(method) &&
        /^\/resources\/(?!provisioning-profiles\/)[^/]+\/[^/]+$/.test(path)
      ) {
        const baselineResponse = await fetch(`/api${path}`);
        if (baselineResponse.ok) {
          const baseline = await baselineResponse.json();
          if (baseline._etag)
            fixtureHeaders["If-Match"] = JSON.stringify(baseline._etag);
        }
        if (body && typeof body === "object" && !Array.isArray(body)) {
          const { _etag, ...values } = body;
          body = values;
        }
      }
      const r = await fetch(`/api${path}`, {
        method,
        headers: {
          ...fixtureHeaders,
          "Content-Type": "application/json",
          "X-CSRF-Token": s.csrfToken,
        },
        body: body ? JSON.stringify(body) : undefined,
      });
      const d = await r.json();
      if (check && !r.ok) throw new Error(d.error);
      return check ? d : { status: r.status, data: d };
    },
    { path, body, method, check },
  );
const go = async (target, route) => {
  await target.goto(`${base}/#${route}`);
  await target.locator("main h1").waitFor();
};
const login = async (target, email) => {
  await target.goto(base);
  await target.getByLabel("邮箱地址").fill(email);
  await target.getByLabel("密码", { exact: true }).fill(password);
  await target.getByRole("button", { name: "登录控制台", exact: true }).click();
  await target.locator("main").waitFor();
};
try {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("Fabric Device Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill(process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token");
  await page.getByLabel("管理员姓名").fill("设备管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
  const a = await call(page, "/resources/members", {
      name: "Network A",
      asn: 64513,
      status: "active",
    }),
    b = await call(page, "/resources/members", {
      name: "Network B",
      asn: 64514,
      status: "active",
    });
  for (const [email, member] of [
    ["a@example.test", a],
    ["b@example.test", b],
  ])
    await call(page, "/resources/users", {
      name: member.name,
      email,
      password,
      role: "member",
      memberId: member.id,
    });
  const fA = await call(page, "/resources/infrastructures", {
      name: "Fabric A",
      shortName: "FA",
    }),
    fB = await call(page, "/resources/infrastructures", {
      name: "Fabric B",
      shortName: "FB",
    });
  assert.equal(fA.excludeIxf, true);
  await go(page, "infrastructures");
  await page.getByRole("button", { name: "新增交换网络", exact: true }).click();
  await page.locator("#field-name").fill("Fabric C");
  await page.locator("#field-shortName").fill("FC");
  assert.equal(await page.locator("#field-excludeIxf").isChecked(), true);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const fC = (await call(page, "/resources/infrastructures")).items.find(
    (f) => f.name === "Fabric C",
  );
  const loc = await call(page, "/resources/locations", { name: "Test DC" }),
    swA = await call(page, "/resources/switches", {
      name: "Switch A",
      locationId: loc.id,
      infrastructureId: fA.id,
      managementIp: "10.99.0.1",
    }),
    swA2 = await call(page, "/resources/switches", {
      name: "Switch A2",
      locationId: loc.id,
      infrastructureId: fA.id,
    }),
    swB = await call(page, "/resources/switches", {
      name: "Switch B",
      locationId: loc.id,
      infrastructureId: fB.id,
    });
  const portBody = {
      name: "Owned port",
      switchId: swA.id,
      memberId: a.id,
      speed: 10000,
      status: "active",
    },
    port = await call(page, "/resources/ports", portBody),
    mate = await call(page, "/resources/ports", {
      name: "LAG mate",
      switchId: swA2.id,
      memberId: a.id,
      speed: 10000,
      status: "active",
    }),
    bPort = await call(page, "/resources/ports", {
      name: "B port",
      switchId: swB.id,
      memberId: b.id,
      speed: 10000,
      status: "active",
    });
  await go(page, "vlans");
  await page
    .locator(".page-heading")
    .getByRole("button", { name: "新增VLAN", exact: true })
    .click();
  await page.locator("#field-name").fill("Multi-subnet LAN");
  await page.locator("#field-tag").fill("100");
  await page.locator("#field-infrastructureId").selectOption(fA.id);
  await page.locator("#field-ipv4Prefix").fill("192.0.2.0/24");
  await page.locator("#field-ipv6Prefix").fill("2001:db8:1::/64");
  await page
    .locator("#field-prefixes")
    .fill("198.51.100.0/24\n2001:db8:2::/64");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const vA = (await call(page, "/resources/vlans")).items.find(
    (v) => v.name === "Multi-subnet LAN",
  );
  const vB = await call(page, "/resources/vlans", {
    name: "Same tag different fabric",
    tag: 100,
    infrastructureId: fB.id,
    ipv4Prefix: "203.0.113.0/24",
  });
  const connection = await call(page, "/resources/connections", {
    name: "A LAG",
    memberId: a.id,
    portIds: [port.id, mate.id],
    linkMode: "lag",
    vlanId: vA.id,
    ipv4: "192.0.2.10",
    status: "active",
  });
  await call(page, "/resources/connections", {
    name: "B connection",
    memberId: b.id,
    portIds: [bPort.id],
    vlanId: vB.id,
    ipv4: "203.0.113.10",
    status: "active",
  });
  await go(page, "infrastructures");
  await page
    .getByRole("button", { name: "批量调整网络归属", exact: true })
    .click();
  await page.getByLabel("目标交换网络", { exact: true }).selectOption(fC.id);
  await page.getByRole("dialog").locator(`input[value="${swA.id}"]`).check();
  await page
    .getByRole("button", { name: "确认更新库存归属", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  assert.equal(
    (await call(page, `/resources/switches/${swA.id}`)).infrastructureId,
    fA.id,
  );
  await page.getByRole("button", { name: "补全相连资源", exact: true }).click();
  assert.equal(
    await page
      .getByRole("dialog")
      .locator(`input[value="${swA2.id}"]`)
      .isChecked(),
    true,
  );
  assert.equal(
    await page
      .getByRole("dialog")
      .locator(`input[value="${vA.id}"]`)
      .isChecked(),
    true,
  );
  await page
    .getByRole("button", { name: "确认更新库存归属", exact: true })
    .click();
  await page
    .getByRole("status")
    .filter({ hasText: "库存关联已原子更新" })
    .waitFor();
  await page
    .getByRole("dialog")
    .locator("form")
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  assert.equal(
    (await call(page, `/resources/vlans/${vA.id}`)).infrastructureId,
    fC.id,
  );
  assert.equal(
    (await call(page, `/resources/switches/${swA2.id}`)).infrastructureId,
    fC.id,
  );
  await go(page, "ipam");
  await page.getByLabel("交换 VLAN", { exact: true }).selectOption(vA.id);
  await page.getByRole("button", { name: "分配地址", exact: true }).click();
  await page.getByLabel("成员网络", { exact: true }).selectOption(a.id);
  await page.getByLabel("地址族", { exact: true }).selectOption("4");
  assert.equal(await page.locator("#allocate-family option").count(), 2);
  assert.equal(await page.locator("#allocate-prefix option").count(), 2);
  await page
    .getByLabel("目标子网", { exact: true })
    .selectOption("198.51.100.0/24");
  const reservation = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/ipam/allocate") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "确认预留地址", exact: true }).click();
  const reserved = await reservation;
  assert.equal(reserved.status(), 201);
  assert.equal(reserved.request().postDataJSON().prefix, "198.51.100.0/24");
  assert.ok((await reserved.json()).items[0].address.startsWith("198.51.100."));
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page.screenshot({
    path: "/tmp/moedove-multi-prefix.png",
    fullPage: true,
  });
  console.log(
    "Scoped duplicate VLAN tags, connected LAG atomic fabric migration, private export default and explicit multi-prefix allocation passed",
  );
  await go(page, "security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await page.getByLabel("令牌名称", { exact: true }).fill("SNMP collector");
  await page
    .getByRole("checkbox", { name: "读取网络资源", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "读取指定交换机诊断", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "采集器上报设备发现与观测", exact: true })
    .check();
  await page
    .getByRole("dialog")
    .getByRole("checkbox", { name: "Switch A", exact: true })
    .check();
  await page
    .getByRole("dialog")
    .getByLabel("当前密码", { exact: true })
    .fill(password);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  const collector = await page
    .getByLabel("API 令牌", { exact: true })
    .innerText();
  await page.getByLabel("我已妥善保存").check();
  await page.getByRole("button", { name: "完成", exact: true }).click();
  const agent = async (
    path,
    body,
    method = body ? "POST" : "GET",
    expected = 200,
  ) => {
    const r = await fetch(base + "/api" + path, {
      method,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${collector}`,
      },
      body: body ? JSON.stringify(body) : undefined,
    });
    const data = await r.json();
    assert.equal(r.status, expected, JSON.stringify(data));
    return data;
  };
  let currentMAC = "02:00:00:00:00:01";
  const iface = (
    ifIndex,
    name,
    inOctets = "9007199254740993",
    outOctets = "9007199254740993",
  ) => ({
    ifIndex,
    name,
    type: 6,
    mtu: 1500,
    speedMbps: 1000,
    adminStatus: "down",
    operStatus: "up",
    physicalAddress: currentMAC,
    inOctets,
    outOctets,
    counterBits: 64,
    discontinuityTicks: "0",
    lastChangeTicks: "0",
    inErrors: "1",
    outErrors: "2",
    inDiscards: "3",
    outDiscards: "4",
  });
  const snapshot = (interfaces, at = new Date().toISOString(), extra = {}) => ({
    version: 1,
    switchId: swA.id,
    observedAt: at,
    security: "authPriv",
    identity: {
      name: "snmp-switch",
      description: "SNMP fixture hardware",
      objectId: "1.3.6.1.4.1.9",
      location: "Private cabinet",
      contact: "secret-noc@example.test",
    },
    uptimeTicks: "10000",
    engineEpoch: "fixture-engine",
    engineIdentity: "a".repeat(64),
    interfaces,
    entities: [],
    sensors: [],
    fdb: [],
    warnings: [],
    ...extra,
  });
  const first = await agent(
    "/devices/discoveries",
    snapshot(
      [iface(1, "Observed Ethernet1"), iface(2, "Observed Ethernet2")],
      new Date().toISOString(),
      {
        entities: [
          {
            index: 1,
            parentIndex: 0,
            ifIndex: 1,
            class: 9,
            name: "PRIVATE ENTITY",
            manufacturer: "Fixture",
            model: "Switch",
            serial: "PRIVATE-SERIAL",
          },
        ],
        sensors: [
          {
            entityIndex: 1,
            ifIndex: 1,
            name: "PRIVATE TEMPERATURE",
            type: 8,
            scale: 9,
            precision: 1,
            rawValue: "220",
            value: 22,
            unit: "°C",
            status: "ok",
          },
        ],
        fdb: [
          {
            fdbId: 1,
            vlanIds: [100],
            mac: "02:00:00:00:00:02",
            bridgePort: 1,
            ifIndex: 1,
            status: "learned",
          },
        ],
      },
    ),
    "POST",
    201,
  );
  assert.ok(
    (await agent("/devices/status")).items.every(
      (item) => item.switchId === swA.id,
    ),
  );
  await agent(`/devices/ports?switchId=${swB.id}`, undefined, "GET", 403);
  const blocked = await fetch(base + "/api/devices/discoveries", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${collector}`,
    },
    body: JSON.stringify({
      ...snapshot([iface(1, "outside")]),
      switchId: swB.id,
    }),
  });
  assert.equal(blocked.status, 403);
  await go(page, "devices");
  await page.locator(`button[data-switch-id="${swA.id}"]`).click();
  await page
    .getByLabel("选择设备发现快照", { exact: true })
    .selectOption(first.id);
  await page
    .getByRole("checkbox", { name: "选择发现接口 1", exact: true })
    .check();
  await page
    .getByLabel("接口 1 的库存映射", { exact: true })
    .selectOption(port.id);
  await page
    .getByRole("checkbox", { name: "选择发现接口 2", exact: true })
    .check();
  await page
    .getByLabel("接口 2 的新端口名称", { exact: true })
    .fill("Discovered new port");
  await call(
    page,
    `/resources/ports/${port.id}`,
    { ...portBody, name: "Owned port renamed" },
    "PUT",
  );
  await page
    .getByRole("button", { name: "审查并应用 2 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.getByRole("button", { name: "继续检查", exact: true }).click();
  await page
    .getByRole("button", { name: "重新读取并审查", exact: true })
    .click();
  await page
    .getByRole("checkbox", { name: "选择发现接口 1", exact: true })
    .check();
  await page
    .getByLabel("接口 1 的库存映射", { exact: true })
    .selectOption(port.id);
  await page
    .getByRole("checkbox", { name: "选择发现接口 2", exact: true })
    .check();
  await page
    .getByLabel("接口 2 的新端口名称", { exact: true })
    .fill("Discovered new port");
  await page.screenshot({ path: "/tmp/moedove-discovery.png", fullPage: true });
  await page
    .getByRole("button", { name: "审查并应用 2 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const after = await call(page, `/resources/ports/${port.id}`);
  assert.equal(after.name, "Owned port renamed");
  assert.equal(after.memberId, a.id);
  assert.equal(after.speed, 10000);
  assert.equal(after.status, "active");
  assert.equal(after.snmpIfIndex, 1);
  const discovered = (await call(page, "/resources/ports")).items.find(
    (p) => p.name === "Discovered new port",
  );
  assert.equal(discovered.status, "inactive");
  assert.equal(discovered.memberId ?? "", "");
  const remap = await agent(
    "/devices/discoveries",
    snapshot([{ ...iface(7, "Observed renumbered"), portId: port.id }]),
    "POST",
    201,
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await page
    .getByLabel("选择设备发现快照", { exact: true })
    .selectOption(remap.id);
  await page
    .getByRole("checkbox", { name: "选择发现接口 7", exact: true })
    .check();
  await page
    .getByLabel("接口 7 的库存映射", { exact: true })
    .selectOption(port.id);
  await page
    .getByRole("button", { name: "审查并应用 1 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.getByRole("button", { name: "继续检查", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "确认接口 7 替换原映射", exact: true })
    .check();
  await page
    .getByRole("button", { name: "审查并应用 1 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/resources/ports/${port.id}`)).snmpIfIndex,
    7,
  );
  await go(page, "ports");
  await page
    .getByRole("button", { name: "编辑 Owned port renamed", exact: true })
    .click();
  assert.equal(
    await page.locator("#field-snmpIfIndex").getAttribute("readonly"),
    "",
  );
  await page.locator("#field-name").fill("Owned port final");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/resources/ports/${port.id}`)).snmpIfIndex,
    7,
  );
  console.log(
    "Bound collector discovery, stale review rejection, explicit ifIndex remap and metadata preservation passed",
  );
  currentMAC = "02:00:00:00:00:09";
  const changedIdentity = await agent(
    "/devices/discoveries",
    snapshot([{ ...iface(7, "Observed renumbered"), portId: port.id }]),
    "POST",
    201,
  );
  await go(page, "devices");
  await page.locator(`button[data-switch-id="${swA.id}"]`).click();
  await page
    .getByLabel("选择设备发现快照", { exact: true })
    .selectOption(changedIdentity.id);
  await page
    .getByRole("checkbox", { name: "选择发现接口 7", exact: true })
    .check();
  await page
    .getByRole("checkbox", { name: "确认接口 7 替换原映射", exact: true })
    .waitFor();
  await page.getByText("对比已确认的接口身份", { exact: true }).click();
  await page.getByText(/02:00:00:00:00:01 → 02:00:00:00:00:09/).waitFor();
  await page
    .getByRole("button", { name: "审查并应用 1 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.getByRole("button", { name: "继续检查", exact: true }).click();
  await page
    .getByRole("checkbox", { name: "确认接口 7 替换原映射", exact: true })
    .check();
  await page
    .getByRole("button", { name: "审查并应用 1 个接口", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认应用库存映射", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/resources/ports/${port.id}`)).snmpPhysicalAddress,
    currentMAC,
  );
  const bindings = await agent(`/devices/bindings?switchId=${swA.id}`),
    owner = bindings.mappings.find((m) => m.portId === port.id),
    started = Date.now();
  const observed = (
    inCount,
    outCount,
    at,
    revision = owner.ownershipRevision,
  ) =>
    snapshot(
      [
        {
          ...iface(7, "Observed renumbered", inCount, outCount),
          portId: port.id,
          ownershipRevision: revision,
        },
      ],
      at,
      {
        sensors: [
          {
            entityIndex: 1,
            ifIndex: 7,
            name: "PRIVATE TEMPERATURE",
            type: 8,
            scale: 9,
            precision: 1,
            rawValue: "220",
            value: 22,
            unit: "°C",
            status: "ok",
          },
        ],
      },
    );
  await agent(
    "/devices/observations",
    observed(
      "9007199254740993",
      "9007199254740993",
      new Date(started).toISOString(),
    ),
  );
  await sleep(1100);
  await agent(
    "/devices/observations",
    observed(
      "9007199254741118",
      "9007199254741243",
      new Date(started + 1000).toISOString(),
    ),
  );
  await go(page, "devices");
  await page.locator(`button[data-switch-id="${swA.id}"]`).click();
  await page.getByText("观测正常", { exact: true }).first().waitFor();
  const contextA = await browser.newContext(),
    memberA = await contextA.newPage();
  await login(memberA, "a@example.test");
  await go(memberA, "devices");
  await memberA.locator(`button[data-switch-id="${swA.id}"]`).click();
  await memberA.getByRole("cell", { name: /Owned port final/ }).waitFor();
  assert.equal(
    await memberA.getByText("设备发现快照审查", { exact: true }).count(),
    0,
  );
  assert.equal(
    await memberA
      .getByRole("columnheader", { name: "错误 / 丢弃计数", exact: true })
      .count(),
    0,
  );
  const ownStatus = await call(memberA, "/devices/status");
  assert.ok(
    ownStatus.items.every((d) => !("identity" in d) && !("managementIp" in d)),
  );
  const ownPorts = (await call(memberA, `/devices/ports?switchId=${swA.id}`))
    .items;
  assert.equal(ownPorts.length, 1);
  assert.equal(ownPorts[0].inErrors, undefined);
  assert.equal(ownPorts[0].inOctets, undefined);
  assert.equal(ownPorts[0].inBps, 1000);
  assert.equal(ownPorts[0].sensors[0].name, undefined);
  assert.equal(
    (await memberA.request.get(base + "/api/devices/discoveries")).status(),
    403,
  );
  await memberA.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await memberA.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await memberA.screenshot({
    path: "/tmp/moedove-device-member-mobile.png",
    fullPage: true,
  });
  await agent(
    "/devices/observations",
    snapshot([], new Date().toISOString(), {
      errorCode: "timeout",
      uptimeTicks: "",
      identity: {
        name: "",
        description: "",
        objectId: "",
        location: "",
        contact: "",
      },
    }),
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await page.getByText("采集失败", { exact: true }).first().waitFor();
  assert.ok(
    (await call(page, "/devices/status")).items.find(
      (d) => d.switchId === swA.id,
    ).lastSuccessAt,
  );
  await call(
    page,
    `/resources/connections/${connection.id}`,
    undefined,
    "DELETE",
  );
  await call(
    page,
    `/resources/ports/${port.id}`,
    { ...portBody, name: "Owned port final", memberId: b.id },
    "PUT",
  );
  await call(page, "/resources/connections", {
    name: "New B ownership",
    memberId: b.id,
    portIds: [port.id],
    vlanId: vA.id,
    ipv4: "192.0.2.20",
    status: "active",
  });
  const delayed = await fetch(base + "/api/devices/observations", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${collector}`,
    },
    body: JSON.stringify(
      observed(
        "9007199254741200",
        "9007199254741300",
        new Date(Date.now() + 1000).toISOString(),
        owner.ownershipRevision,
      ),
    ),
  });
  assert.equal(delayed.status, 422);
  const contextB = await browser.newContext(),
    memberB = await contextB.newPage();
  await login(memberB, "b@example.test");
  const beforeFresh = (
    await call(memberB, `/devices/ports?switchId=${swA.id}`)
  ).items.find((p) => p.portId === port.id);
  assert.equal(beforeFresh.status, "unknown");
  const freshOwner = (
    await agent(`/devices/bindings?switchId=${swA.id}`)
  ).mappings.find((m) => m.portId === port.id);
  assert.notEqual(freshOwner.ownershipRevision, owner.ownershipRevision);
  await agent(
    "/devices/observations",
    observed(
      "9007199254741400",
      "9007199254741500",
      new Date().toISOString(),
      freshOwner.ownershipRevision,
    ),
  );
  const afterFresh = (
    await call(memberB, `/devices/ports?switchId=${swA.id}`)
  ).items.find((p) => p.portId === port.id);
  assert.equal(afterFresh.status, "fresh");
  assert.equal(afterFresh.inBps, null);
  await go(page, "devices");
  await page.locator(`button[data-switch-id="${swA.id}"]`).click();
  await page.getByText("存储用量与显式保留控制", { exact: true }).click();
  const usageBefore = await call(page, "/devices/usage");
  assert.ok(usageBefore.discovery.usedBytes > 0);
  assert.ok(usageBefore.observations.usedBytes > 0);
  await page
    .getByRole("button", { name: `清理设备缓存 ${swA.id}`, exact: true })
    .click();
  await page
    .getByLabel("我理解当前诊断会暂时显示未知，历史遥测不会被删除。", {
      exact: true,
    })
    .check();
  await page
    .getByLabel("清理原因", { exact: true })
    .fill("Stale-cache review fixture");
  await sleep(1100);
  await agent(
    "/devices/observations",
    observed(
      "9007199254741525",
      "9007199254741750",
      new Date().toISOString(),
      freshOwner.ownershipRevision,
    ),
  );
  await page
    .getByRole("button", { name: "确认清理最新缓存", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  const refreshedUsage = page.waitForResponse((r) =>
    r.url().endsWith("/api/devices/usage"),
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await refreshedUsage;
  await page
    .getByRole("button", { name: `清理设备缓存 ${swA.id}`, exact: true })
    .click();
  await page
    .getByLabel("我理解当前诊断会暂时显示未知，历史遥测不会被删除。", {
      exact: true,
    })
    .check();
  await page
    .getByLabel("清理原因", { exact: true })
    .fill("Explicit latest-cache eviction");
  await page
    .getByRole("button", { name: "确认清理最新缓存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, "/devices/usage")).observations.items.some(
      (item) => item.switchId === swA.id,
    ),
    false,
  );
  assert.ok(
    (await call(memberB, `/monitoring/series?portId=${port.id}`)).points
      .length >= 2,
  );
  const pendingRetention = await agent(
    "/devices/discoveries",
    snapshot([{ ...iface(7, "Observed renumbered"), portId: port.id }]),
    "POST",
    201,
  );
  assert.equal(
    (
      await call(
        page,
        `/devices/discoveries/${pendingRetention.id}/prune`,
        { reason: "Must not prune pending" },
        "POST",
        false,
      )
    ).status,
    409,
  );
  const refreshedList = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/devices/discoveries") &&
      r.request().method() === "GET",
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await refreshedList;
  await page.getByText("发现提案与保留状态", { exact: true }).click();
  const retentionRow = page
    .getByRole("row")
    .filter({ hasText: pendingRetention.id.slice(0, 12) });
  await retentionRow
    .getByRole("button", { name: "保留库存并关闭", exact: true })
    .click();
  await page
    .getByLabel("处理原因", { exact: true })
    .fill("Not applying this inventory observation");
  await page.getByRole("button", { name: "确认关闭提案", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await retentionRow
    .getByRole("button", { name: "清理原始快照", exact: true })
    .click();
  await page
    .getByLabel("处理原因", { exact: true })
    .fill("Reviewed snapshot retention cleanup");
  await page
    .getByRole("button", { name: "确认清理原始快照", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    await retentionRow
      .getByRole("button", { name: "预览", exact: true })
      .isDisabled(),
    true,
  );
  assert.ok(
    (
      await page.request.get(
        `${base}/api/devices/discoveries/${pendingRetention.id}/preview`,
      )
    ).status() >= 400,
  );
  const exported = page.waitForEvent("download");
  await retentionRow
    .getByRole("button", {
      name: `导出审查记录 ${pendingRetention.id}`,
      exact: true,
    })
    .click();
  assert.match(
    (await exported).suggestedFilename(),
    /discovery-.*-review\.json/,
  );
  await retentionRow
    .getByRole("button", {
      name: `删除提案记录 ${pendingRetention.id}`,
      exact: true,
    })
    .click();
  await page
    .getByLabel("我已保存所需明细，理解删除列表记录后只保留审计摘要。", {
      exact: true,
    })
    .check();
  await page
    .getByLabel("处理原因", { exact: true })
    .fill("Audit export complete");
  await page
    .getByRole("button", { name: "确认删除列表记录", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, "/devices/discoveries")).items.some(
      (item) => item.id === pendingRetention.id,
    ),
    false,
  );
  console.log(
    "Explicit discovery close/prune/export/delete, pending retention guard and checksum-racing cache eviction passed",
  );
  await contextA.close();
  await contextB.close();
  assert.deepEqual(errors, []);
  console.log(
    "Actual diagnostic rate/sensors, failure preservation, member redaction and future-clock stale-ownership rejection passed",
  );
} finally {
  await browser.close();
}
