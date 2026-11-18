// Isolated sFlow UI smoke. No real UDP exporter, packet capture or receiver delivery.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-sflow-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "SFlow-browser-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-sflow-server",
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
  const initialCatalog = await api("/sflow");
  assert.equal(initialCatalog.sourceKind, "sampled");
  // Feature availability is not evidence that any collector or receiver is active.
  assert.equal(initialCatalog.capabilities.liveForwarding, true);
  const initialQuery = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await page.goto(base + "/#sflow");
  await page
    .getByRole("heading", { name: "sFlow 采样", exact: true })
    .waitFor();
  assert.equal((await initialQuery).status(), 200);
  await page
    .getByRole("heading", { name: "所选范围没有采样记录", exact: true })
    .waitFor();
  assert.equal(await page.getByTestId("sflow-estimated-bytes").count(), 0);
  console.log(
    "sFlow real catalog/query, sample-only notices and honest empty report passed",
  );

  await page.getByRole("link", { name: "管理导出器", exact: true }).click();
  await page
    .getByRole("heading", { name: "sFlow 导出器", exact: true })
    .waitFor();
  await page
    .locator(".page-heading")
    .getByRole("button", { name: "新增sFlow 导出器", exact: true })
    .click();
  await page.locator("#field-name").fill("Reviewed sampling exporter");
  await page.locator("#field-switchId").selectOption(sw.id);
  await page.locator("#field-sourceAddress").fill("198.51.100.20");
  await page.locator("#field-agentAddress").fill("198.51.100.20");
  await page.locator(`#field-vlanIds input[value="${vlan.id}"]`).check();
  await page.locator("#field-enabled").check();
  const created = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/resources/sflow-exporters" &&
      response.request().method() === "POST",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  const createResponse = await created,
    exporter = await createResponse.json();
  assert.equal(createResponse.status(), 201, JSON.stringify(exporter));
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const returned = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await page.goto(base + "/#sflow");
  assert.equal((await returned).status(), 200);
  await page.locator("#sflow-exporter").selectOption(exporter.id);
  await page.locator("#sflow-vlan").selectOption(vlan.id);
  await page.locator("#sflow-family").selectOption("4");
  const filtered = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await page.getByRole("button", { name: "查询采样", exact: true }).click();
  const response = await filtered,
    report = await response.json();
  assert.equal(response.status(), 200, JSON.stringify(report));
  const url = new URL(response.url());
  assert.equal(url.searchParams.get("family"), "4");
  assert.equal(url.searchParams.get("exporterId"), exporter.id);
  assert.equal(url.searchParams.get("vlanId"), vlan.id);
  assert.ok(url.searchParams.get("from").endsWith("Z"));
  assert.equal(report.items?.length ?? 0, 0);
  await page
    .getByRole("heading", { name: "所选范围没有采样记录", exact: true })
    .waitFor();
  console.log(
    "sFlow real exporter creation through shared CAS editor and authorized filters passed",
  );

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
  await page.goto(base + "/#security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await page
    .getByLabel("令牌名称", { exact: true })
    .fill("Bound sampling collector");
  await page
    .getByRole("checkbox", { name: "读取网络资源", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", {
      name: "读取授权范围的 sFlow 采样估算",
      exact: true,
    })
    .check();
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
    .getByRole("checkbox", {
      name: "采集器上报指定交换机的 sFlow 元数据",
      exact: true,
    })
    .check();
  await page
    .getByRole("checkbox", { name: "Sampling switch", exact: true })
    .check();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  const collector = await page
    .getByLabel("API 令牌", { exact: true })
    .innerText();
  await page.getByLabel("我已妥善保存").check();
  await page.getByRole("button", { name: "完成", exact: true }).click();
  const auth = { Authorization: `Bearer ${collector}` };
  const bindingResponse = await fetch(
    `${base}/api/sflow/bindings?exporterId=${exporter.id}`,
    { headers: auth },
  );
  assert.equal(bindingResponse.status, 200);
  const binding = await bindingResponse.json();
  const sampleResponse = await fetch(base + "/api/sflow/ingest", {
    method: "POST",
    headers: { ...auth, "Content-Type": "application/json" },
    body: JSON.stringify({
      version: 1,
      exporterId: exporter.id,
      switchId: sw.id,
      mappingRevision: binding.mappingRevision,
      receivedAt: new Date().toISOString(),
      transportSource: "198.51.100.20",
      quality: {
        sequenceState: "first",
        counterSamples: 0,
        udpAuthenticated: false,
      },
      datagram: {
        version: 5,
        agentAddress: "198.51.100.20",
        subAgentId: 0,
        sequence: 1,
        uptimeMs: 10000,
        counters: [],
        flows: [
          {
            sequence: 1,
            sourceType: 0,
            sourceIndex: 1,
            samplingRate: 100,
            samplePool: 100,
            drops: 0,
            input: { format: 0, value: 1 },
            output: { format: 0, value: 2 },
            packetSize: 100,
            sourceMac: "02:00:00:00:51:01",
            destinationMac: "02:00:00:00:51:02",
            family: 4,
            headerVlans: [511],
            inputVlan: 511,
            outputVlan: 511,
            estimatedBytes: "10000",
            estimatedPackets: "100",
            sampleCount: 1,
            sampledTcp179Activity: true,
          },
          {
            sequence: 1,
            sourceType: 0,
            sourceIndex: 2,
            samplingRate: 50,
            samplePool: 50,
            drops: 0,
            input: { format: 0, value: 2 },
            output: { format: 0, value: 1 },
            packetSize: 80,
            sourceMac: "02:00:00:00:51:02",
            destinationMac: "02:00:00:00:51:01",
            family: 6,
            headerVlans: [511],
            inputVlan: 511,
            outputVlan: 511,
            estimatedBytes: "4000",
            estimatedPackets: "50",
            sampleCount: 1,
            sampledTcp179Activity: false,
          },
        ],
      },
    }),
  });
  assert.equal(sampleResponse.status, 200, await sampleResponse.clone().text());
  const sampleResult = await sampleResponse.json();
  assert.equal(sampleResult.acceptedFlows, 2);
  assert.equal(sampleResult.unattributedFlows, 0);
  assert.equal(sampleResult.memberForwarding, false);
  const sampleRead = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await page.goto(base + "/#sflow");
  assert.equal((await sampleRead).status(), 200);
  const querySamples = async () => {
    await page.locator("#sflow-to").fill(new Date().toISOString().slice(0, 19));
    const pending = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/sflow/query",
    );
    await page.getByRole("button", { name: "查询采样", exact: true }).click();
    const response = await pending,
      result = await response.json();
    assert.equal(response.status(), 200, JSON.stringify(result));
    return result;
  };
  await page.locator("#sflow-family").selectOption("4");
  const v4 = await querySamples();
  assert.equal(v4.items.length, 1);
  assert.equal(v4.items[0].estimatedBytes, "10000");
  assert.equal(v4.items[0].sourceMemberId, members[0].id);
  assert.equal(v4.items[0].destinationMemberId, members[1].id);
  assert.equal(v4.items[0].tcp179Samples, "1");
  assert.equal(v4.quality?.length ?? 0, 0);
  await page
    .getByTestId("sflow-estimated-bytes")
    .filter({ hasText: "10,000 B（估算）" })
    .waitFor();
  await page.locator("#sflow-family").selectOption("6");
  const v6 = await querySamples();
  assert.equal(v6.items.length, 1);
  assert.equal(v6.items[0].estimatedBytes, "4000");
  assert.equal(v6.items[0].sourceMemberId, members[1].id);
  assert.equal(v6.items[0].tcp179Samples, "0");
  const counter = await api(
    `/traffic/query?scope=port&id=${ports[0].id}&metric=octets&resolution=5m`,
  );
  assert.equal(counter.summary.in.total, null);
  await page.locator("#sflow-family").selectOption("0");
  const allSamples = await querySamples();
  assert.equal(allSamples.items.length, 2);
  assert.ok(allSamples.quality.length > 0);
  await page
    .getByRole("heading", { name: "整 UTC 日的导出器质量", exact: true })
    .waitFor();
  console.log(
    "sFlow reviewed SNMP mappings + bound scope UI + real typed IPv4/IPv6 directed estimates/TCP179 and separate counter archive passed",
  );

  const foreignFabric = await api("/resources/infrastructures", {
    name: "Outside token scope",
  });
  const foreignSwitch = await api("/resources/switches", {
    name: "Private other switch",
    locationId: location.id,
    infrastructureId: foreignFabric.id,
  });
  const foreignVlan = await api("/resources/vlans", {
    name: "Private other VLAN",
    infrastructureId: foreignFabric.id,
    tag: 512,
    ipv4Prefix: "198.18.0.0/24",
  });
  const foreignMember = await api("/resources/members", {
    name: "Unrelated member C",
    asn: 64599,
    status: "active",
  });
  const foreignPort = await api("/resources/ports", {
    name: "Private other port",
    switchId: foreignSwitch.id,
    status: "active",
  });
  const foreignConnection = await api("/resources/connections", {
    name: "Private unrelated connection",
    memberId: foreignMember.id,
    portId: foreignPort.id,
    vlanId: foreignVlan.id,
    ipv4: "198.18.0.10",
    status: "active",
  });
  const foreignExporter = await api("/resources/sflow-exporters", {
    name: "Private other exporter",
    switchId: foreignSwitch.id,
    sourceAddress: "198.51.100.21",
    agentAddress: "198.51.100.21",
    subAgentId: 0,
    vlanIds: [foreignVlan.id],
    enabled: true,
  });
  const boundCatalogResponse = await fetch(base + "/api/sflow", {
    headers: auth,
  });
  assert.equal(boundCatalogResponse.status, 200);
  const boundCatalog = await boundCatalogResponse.json();
  assert.ok(
    !boundCatalog.exporters.some((item) => item.id === foreignExporter.id),
  );
  assert.ok(
    !boundCatalog.selectors.switches.some(
      (item) => item.id === foreignSwitch.id,
    ),
  );
  assert.ok(
    !boundCatalog.selectors.vlans.some((item) => item.id === foreignVlan.id),
  );
  assert.ok(
    !boundCatalog.selectors.connections.some(
      (item) => item.id === foreignConnection.id,
    ),
  );
  assert.ok(
    !boundCatalog.selectors.members.some(
      (item) => item.id === foreignMember.id,
    ),
    "Bound token must not expose unrelated member directory",
  );
  assert.equal(boundCatalog.usage, null);
  assert.equal(
    (
      await fetch(`${base}/api/sflow/query?memberId=${foreignMember.id}`, {
        headers: auth,
      })
    ).status,
    404,
  );
  assert.equal(
    (
      await fetch(`${base}/api/sflow/query?memberId=nonexistent`, {
        headers: auth,
      })
    ).status,
    404,
  );
  assert.equal(
    (
      await fetch(`${base}/api/sflow/query?exporterId=${foreignExporter.id}`, {
        headers: auth,
      })
    ).status,
    404,
  );
  assert.equal(
    (await fetch(base + "/api/resources/switches", { headers: auth })).status,
    403,
  );
  for (const [email, memberId] of [
    ["sample-a@example.test", members[0].id],
    ["sample-c@example.test", foreignMember.id],
  ])
    await api("/resources/users", {
      name: email,
      email,
      role: "member",
      memberId,
      password,
    });
  const memberContext = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  assert.equal(
    (
      await memberContext.request.post(base + "/api/login", {
        data: { email: "sample-a@example.test", password },
      })
    ).status(),
    200,
  );
  const memberPage = await memberContext.newPage(),
    resourceRequests = [];
  memberPage.on("request", (request) => {
    if (new URL(request.url()).pathname.startsWith("/api/resources/"))
      resourceRequests.push(request.url());
  });
  const ownRead = memberPage.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await memberPage.goto(base + "/#sflow");
  const ownResponse = await ownRead,
    ownResult = await ownResponse.json();
  assert.equal(ownResponse.status(), 200);
  assert.equal(ownResult.items.length, 2);
  for (const row of ownResult.items) {
    assert.equal(row.exporterId, undefined);
    assert.equal(row.switchId, undefined);
    const otherSide =
      row.sourceMemberId === members[0].id ? "destination" : "source";
    assert.match(row[`${otherSide}MemberId`], /^peer-/);
    assert.equal(row[`${otherSide}ConnectionId`], undefined);
  }
  assert.equal(ownResult.quality?.length ?? 0, 0);
  await memberPage.getByTestId("sflow-estimated-bytes").first().waitFor();
  assert.equal(await memberPage.locator("#sflow-exporter").count(), 0);
  const memberText = await memberPage.locator("main").innerText();
  for (const secret of [
    members[1].name,
    members[1].id,
    connections[1].name,
    connections[1].id,
    exporter.id,
    exporter.sourceAddress,
  ])
    assert.ok(!memberText.includes(secret), `Member must not see ${secret}`);
  assert.deepEqual(resourceRequests, []);
  const ownCatalog = await api(
    "/sflow",
    undefined,
    "GET",
    undefined,
    memberContext,
  );
  assert.equal(ownCatalog.exporters.length, 0);
  assert.equal(ownCatalog.selectors.switches.length, 0);
  assert.equal(
    (
      await memberContext.request.get(
        `${base}/api/sflow/query?memberId=${foreignMember.id}`,
      )
    ).status(),
    403,
  );
  assert.equal(
    (
      await memberContext.request.get(
        `${base}/api/sflow/query?exporterId=${foreignExporter.id}`,
      )
    ).status(),
    403,
  );
  assert.equal(
    (
      await memberContext.request.get(
        `${base}/api/sflow/query?exporterId=nonexistent`,
      )
    ).status(),
    403,
  );
  await memberPage.goto(base + "/#security");
  await memberPage
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await memberPage
    .getByLabel("令牌名称", { exact: true })
    .fill("Member sampling read");
  await memberPage
    .getByRole("checkbox", { name: "读取网络资源", exact: true })
    .uncheck();
  await memberPage
    .getByRole("checkbox", {
      name: "读取授权范围的 sFlow 采样估算",
      exact: true,
    })
    .check();
  assert.equal(
    await memberPage.getByText("绑定的交换机（必选）", { exact: true }).count(),
    0,
  );
  await memberPage
    .getByRole("dialog")
    .getByLabel("当前密码", { exact: true })
    .fill(password);
  await memberPage
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  const memberToken = await memberPage
    .getByLabel("API 令牌", { exact: true })
    .innerText();
  await memberPage.getByLabel("我已妥善保存").check();
  await memberPage.getByRole("button", { name: "完成", exact: true }).click();
  assert.ok(
    !resourceRequests.some((url) => url.includes("/resources/switches")),
  );
  const tokenOwn = await fetch(base + "/api/sflow/query", {
    headers: { Authorization: `Bearer ${memberToken}` },
  });
  assert.equal(tokenOwn.status, 200);
  assert.equal((await tokenOwn.json()).items.length, 2);
  const memberRequery = memberPage.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/sflow/query",
  );
  await memberPage.goto(base + "/#sflow");
  await memberRequery;
  await memberPage.screenshot({ path: "/tmp/moedove-sflow-member.png" });
  const unrelatedContext = await browser.newContext();
  assert.equal(
    (
      await unrelatedContext.request.post(base + "/api/login", {
        data: { email: "sample-c@example.test", password },
      })
    ).status(),
    200,
  );
  assert.equal(
    (await api("/sflow/query", undefined, "GET", undefined, unrelatedContext))
      .items.length,
    0,
  );
  await unrelatedContext.close();
  await memberContext.close();
  console.log(
    "sFlow scoped token selectors, member peer aliases/private metadata, member token without switch reads, unrelated-member isolation passed",
  );

  await page.route(
    "**/api/sflow",
    async (route) => {
      const response = await route.fetch(),
        data = await response.json();
      await route.fulfill({
        response,
        json: {
          ...data,
          observedAt: new Date().toISOString(),
          staleAfterSeconds: 120,
          exporters: data.exporters.map((item) => ({
            ...item,
            receiveStatus: "fresh",
            lastReceivedAt: new Date(Date.now() - 300000).toISOString(),
          })),
        },
      });
    },
    { times: 1 },
  );
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await page.getByText("收包证据已过期", { exact: true }).first().waitFor();
  await page.route(
    "**/api/sflow/query?*",
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "采样归档暂不可用（本地故障测试）" }),
      }),
    { times: 1 },
  );
  await page.getByRole("button", { name: "查询采样", exact: true }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: "采样归档暂不可用" })
    .waitFor();
  assert.equal(await page.getByTestId("sflow-estimated-bytes").count(), 0);
  await querySamples();
  await page.getByTestId("sflow-estimated-bytes").first().waitFor();
  console.log(
    "sFlow aged-receipt downgrade and failed query clears stale report/recovery passed",
  );
  await page.screenshot({ path: "/tmp/moedove-sflow-desktop.png" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => window.scrollTo(0, 0));
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
  await page.screenshot({ path: "/tmp/moedove-sflow-mobile.png" });
  assert.deepEqual(errors, []);
  console.log("sFlow390px and no page errors passed");
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
