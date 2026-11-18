// Private local provider fixture only; never reaches PeeringDB or sends mail.
import assert from "node:assert/strict";
import http from "node:http";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const workspace = await mkdtemp("/tmp/moedove-provider-browser-"),
  password = "Provider-peering-password-123",
  requests = [];
const network = {
  id: 101,
  asn: 64513,
  name: "PeeringDB Network A",
  website: "https://remote-a.example",
  irr_as_set: "AS-REMOTE-A",
  info_prefixes4: null,
  info_prefixes6: 50,
  status: "ok",
};
const netixlan = {
  id: 201,
  net_id: 101,
  ix_id: 22,
  ixlan_id: 222,
  asn: 64513,
  name: "Fixture IX",
  ipaddr4: "192.0.2.10",
  ipaddr6: "2001:db8:1::99",
  speed: 1000,
  is_rs_peer: true,
  operational: true,
  status: "ok",
};
const exchange = {
    id: 22,
    name: "Fixture Exchange",
    city: "Shanghai",
    country: "CN",
    website: "https://ix.example",
    status: "ok",
  },
  facility = {
    id: 33,
    name: "Fixture Facility",
    city: "Shanghai",
    country: "CN",
    address1: "Test Road",
    address2: "",
    zipcode: "200000",
    website: "https://dc.example",
    status: "ok",
  };
const provider = http.createServer((req, res) => {
  const url = new URL(req.url, "http://fixture.local");
  requests.push({ method: req.method, path: url.pathname, query: url.search });
  const rows =
    url.pathname === "/api/net"
      ? [network]
      : url.pathname === "/api/netixlan"
        ? [netixlan]
        : url.pathname === "/api/ix/22"
          ? [exchange]
          : url.pathname === "/api/fac/33"
            ? [facility]
            : null;
  res.setHeader("Content-Type", "application/json");
  if (req.method !== "GET" || !rows) {
    res.statusCode = 404;
    res.end(JSON.stringify({ error: "unexpected fixture request" }));
    return;
  }
  res.end(JSON.stringify({ data: rows }));
});
await new Promise((r) => provider.listen(0, "127.0.0.1", r));
const providerPort = provider.address().port;
const free = net.createServer();
await new Promise((r) => free.listen(0, "127.0.0.1", r));
const port = free.address().port;
await new Promise((r) => free.close(r));
const base = `http://127.0.0.1:${port}`;
await writeFile(
  `${workspace}/provider.json`,
  JSON.stringify({
    enabled: true,
    baseURL: `http://127.0.0.1:${providerPort}/api`,
    allowedOrigins: [`http://127.0.0.1:${providerPort}`],
    allowLoopbackHTTP: true,
    requestIntervalSeconds: 5,
  }),
  { mode: 0o600 },
);
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    `${workspace}/state`,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
    "-provider-config",
    `${workspace}/provider.json`,
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "provider-fixture-setup-token" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let serverError = "";
child.stderr.on("data", (d) => (serverError += d));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let browser;
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
  for (let i = 0; i < 80; i++) {
    if (child.exitCode != null) throw new Error(serverError);
    try {
      if ((await fetch(base + "/api/setup")).ok) break;
    } catch {}
    await sleep(100);
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    }),
    errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("Provider Peering Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill("provider-fixture-setup-token");
  await page.getByLabel("管理员姓名").fill("互联管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
  const members = [];
  for (const [index, name] of [
    "Local A",
    "Network B",
    "Network C",
    "Private D",
  ].entries())
    members.push(
      await call(page, "/resources/members", {
        name,
        asn: 64513 + index,
        status: "active",
        type: "full",
        email: `private-noc-${index}@example.test`,
        website: index === 0 ? "https://local-a.example" : "",
        excludeIxf: true,
      }),
    );
  const [a, b, c, d] = members;
  const fabric = await call(page, "/resources/infrastructures", {
      name: "Public Fabric",
      shortName: "PF",
      peeringdbId: 22,
      excludeIxf: true,
    }),
    loc = await call(page, "/resources/locations", { name: "Test DC" }),
    sw = await call(page, "/resources/switches", {
      name: "Public switch",
      locationId: loc.id,
      infrastructureId: fabric.id,
    }),
    publicVlan = await call(page, "/resources/vlans", {
      name: "Shared public VLAN",
      tag: 100,
      infrastructureId: fabric.id,
      ipv4Prefix: "192.0.2.0/24",
      ipv6Prefix: "2001:db8:1::/64",
      excludeIxf: true,
    }),
    privateVlan = await call(page, "/resources/vlans", {
      name: "Private management VLAN",
      tag: 200,
      infrastructureId: fabric.id,
      ipv4Prefix: "198.51.100.0/24",
      private: true,
    });
  const ports = [],
    connections = [];
  for (const [index, m] of members.entries()) {
    ports.push(
      await call(page, "/resources/ports", {
        name: `Port ${index + 1}`,
        switchId: sw.id,
        memberId: m.id,
        speed: 10000,
        status: "active",
      }),
    );
    if (index < 3)
      connections.push(
        await call(page, "/resources/connections", {
          name: `Connection ${index + 1}`,
          memberId: m.id,
          portIds: [ports[index].id],
          vlanId: publicVlan.id,
          ipv4: `192.0.2.${(index + 1) * 10}`,
          ipv6: `2001:db8:1::${(index + 1) * 10}`,
          status: "active",
        }),
      );
  }
  await call(page, "/resources/connections", {
    name: "A private connection",
    memberId: a.id,
    portIds: [ports[0].id],
    vlanId: privateVlan.id,
    ipv4: "198.51.100.10",
    status: "active",
  });
  await call(page, "/resources/connections", {
    name: "D private connection",
    memberId: d.id,
    portIds: [ports[3].id],
    vlanId: privateVlan.id,
    ipv4: "198.51.100.40",
    status: "active",
  });
  const router = await call(page, "/resources/routers", {
    name: "Shared RS",
    platform: "bird2",
    asn: 64512,
    routerId: "192.0.2.1",
    localIpv4: "192.0.2.1",
    vlanId: publicVlan.id,
  });
  for (const [index, m] of members.slice(0, 3).entries())
    await call(page, "/resources/peers", {
      name: `RS member ${index + 1}`,
      routerId: router.id,
      memberId: m.id,
      address: `192.0.2.${(index + 1) * 10}`,
      asn: m.asn,
      enabled: true,
      maxPrefixes: 100,
      allowedPrefixes: [`203.0.${index + 1}.0/24`],
    });
  for (const [email, m, admin] of [
    ["a@example.test", a, false],
    ["a2@example.test", a, false],
    ["b@example.test", b, false],
    ["badmin@example.test", b, true],
    ["cadmin@example.test", c, true],
  ])
    await call(page, "/resources/users", {
      name: email.split("@")[0],
      email,
      password,
      role: "member",
      memberIds: [m.id],
      memberAdminIds: admin ? [m.id] : [],
    });
  const schema = await call(page, "/schema");
  const edit = async (kind, id, values) => {
    const record = await call(page, `/resources/${kind}/${id}`),
      body = {};
    for (const key of Object.keys(schema[kind]))
      if (
        record[key] !== undefined &&
        ![
          "peeringdbId",
          "peeringdbNetixlanId",
          "snmpIfIndex",
          "ownershipSince",
        ].includes(key)
      )
        body[key] = record[key];
    return call(
      page,
      `/resources/${kind}/${id}`,
      { ...body, ...values },
      "PUT",
    );
  };
  await go(page, "integrations");
  await page
    .getByRole("button", { name: "获取 PeeringDB 提案", exact: true })
    .click();
  await page.getByLabel("本地成员", { exact: true }).selectOption(a.id);
  await page
    .getByLabel("关联接入（可选）", { exact: true })
    .selectOption(connections[0].id);
  await page
    .getByLabel("PeeringDB Facility ID（可选）", { exact: true })
    .fill("33");
  const fetchedResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/integrations/peeringdb/propose") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建审查提案", exact: true }).click();
  const fetched = await fetchedResponse;
  assert.equal(fetched.status(), 201, await fetched.text());
  const first = await fetched.json();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("checkbox", { name: "应用 成员名称", exact: true })
    .check();
  assert.equal(
    await page
      .getByRole("checkbox", { name: "应用 接入 IPv6", exact: true })
      .count(),
    0,
  );
  assert.equal(
    await page
      .getByRole("checkbox", { name: "应用 接入速率", exact: true })
      .count(),
    0,
  );
  assert.ok(requests.length >= 2);
  assert.ok(
    requests.every((r) => r.method === "GET" && r.path.startsWith("/api/")),
  );
  assert.ok(
    requests.some(
      (r) =>
        r.path === "/api/net" &&
        new URLSearchParams(r.query).get("asn") === "64513",
    ),
  );
  assert.ok(requests.some((r) => r.path === "/api/fac/33"));
  await edit("members", a.id, { name: "Locally changed A" });
  await page
    .getByRole("button", { name: "应用选中的 1 个字段", exact: true })
    .click();
  await page.getByRole("button", { name: "确认应用资料", exact: true }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.getByRole("button", { name: "继续检查", exact: true }).click();
  const raw = JSON.stringify(
      {
        version: 1,
        provider: "peeringdb",
        fetchedAt: new Date().toISOString(),
        network,
        netixlans: [netixlan],
        exchanges: [exchange],
        facilities: [facility],
      },
      null,
      2,
    ).replace(/\n/g, "\r\n"),
    checksum = createHash("sha256").update(raw, "utf8").digest("hex");
  await page.getByRole("button", { name: "导入快照", exact: true }).click();
  await page.getByLabel("本地成员", { exact: true }).selectOption(a.id);
  await page
    .getByLabel("关联接入（可选）", { exact: true })
    .selectOption(connections[0].id);
  await page.getByLabel("类型化 JSON 文件", { exact: true }).setInputFiles({
    name: "provider.json",
    mimeType: "application/json",
    buffer: Buffer.from(raw),
  });
  const importedResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/integrations/proposals/import") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "创建审查提案", exact: true }).click();
  const imported = await importedResponse;
  assert.equal(imported.status(), 201, await imported.text());
  assert.equal(imported.request().postDataJSON().checksum, checksum);
  const second = await imported.json();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  for (const name of [
    "成员名称",
    "成员网站",
    "PeeringDB 网络 ID",
    "PeeringDB 接入 ID",
  ])
    await page
      .getByRole("checkbox", { name: `应用 ${name}`, exact: true })
      .check();
  await page
    .getByRole("button", { name: "应用选中的 4 个字段", exact: true })
    .click();
  await page.getByRole("button", { name: "确认应用资料", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const changed = await call(page, `/resources/members/${a.id}`),
    linked = await call(page, `/resources/connections/${connections[0].id}`);
  assert.equal(changed.name, network.name);
  assert.equal(changed.website, network.website);
  assert.equal(changed.peeringdbId, 101);
  assert.equal(linked.peeringdbNetixlanId, 201);
  assert.equal(linked.ipv6, "2001:db8:1::10");
  assert.equal(linked.capacityMbps, 10000);
  const listed = await call(page, "/integrations/proposals?limit=1");
  assert.equal(listed.items[0].snapshot, undefined);
  assert.equal(listed.hasMore, true);
  assert.equal(
    (await call(page, "/integrations/proposals?offset=1&limit=1")).items[0].id,
    first.id,
  );
  assert.ok(
    (await call(page, `/integrations/proposals/${second.id}`)).snapshot,
  );
  await page
    .getByRole("button", { name: new RegExp(`Local A.*AS64513`) })
    .click();
  await page
    .getByRole("button", { name: "保持本地并关闭提案", exact: true })
    .click();
  await page
    .getByLabel("审查说明（可选）", { exact: true })
    .fill("Stale proposal; current reviewed import already applied");
  await page.getByRole("button", { name: "确认保持本地", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/integrations/proposals/${first.id}`)).status,
    "dismissed",
  );
  await page.screenshot({
    path: "/tmp/moedove-integrations.png",
    fullPage: true,
  });
  await go(page, "members");
  await page
    .getByRole("button", { name: `编辑 ${network.name}`, exact: true })
    .click();
  assert.equal(
    await page.locator("#field-peeringdbId").getAttribute("readonly"),
    "",
  );
  await page.locator("#field-phone").fill("+86 123456");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/resources/members/${a.id}`)).peeringdbId,
    101,
  );
  await go(page, "connections");
  await page
    .getByRole("button", { name: "编辑 Connection 1", exact: true })
    .click();
  assert.equal(
    await page.locator("#field-peeringdbNetixlanId").getAttribute("readonly"),
    "",
  );
  await page.locator("#field-name").fill("Connection A reviewed");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, `/resources/connections/${connections[0].id}`))
      .peeringdbNetixlanId,
    201,
  );
  console.log(
    "Loopback provider fetch, exact raw checksum import, protected diffs, stale-review rejection, selective apply/dismiss and managed-ID preservation passed",
  );
  const contexts = [];
  const memberPage = async (email) => {
    const context = await browser.newContext();
    contexts.push(context);
    const p = await context.newPage();
    p.on("pageerror", (e) => errors.push(e.message));
    await login(p, email);
    return p;
  };
  const pa = await memberPage("a@example.test"),
    pa2 = await memberPage("a2@example.test"),
    pb = await memberPage("b@example.test"),
    pba = await memberPage("badmin@example.test"),
    pca = await memberPage("cadmin@example.test");
  const third = await call(pba, "/peering/requests", {
    toMemberId: c.id,
    vlanId: publicVlan.id,
    message: "Third-party private agreement",
    notify: false,
  });
  await call(pca, `/peering/requests/${third.id}/decision`, {
    decision: "accept",
    notify: false,
  });
  await go(pa, "peering");
  const partners = await call(pa, "/peering/partners");
  assert.ok(partners.members.some((m) => m.id === b.id));
  assert.ok(partners.members.some((m) => m.id === c.id));
  assert.equal(
    partners.members.some((m) => m.id === d.id),
    false,
  );
  await pa.getByRole("button", { name: "发起对等请求", exact: true }).click();
  await pa.getByLabel("对方网络", { exact: true }).selectOption(b.id);
  await pa
    .getByLabel("共享交换 VLAN", { exact: true })
    .selectOption(publicVlan.id);
  assert.equal(
    await pa
      .getByRole("checkbox", { name: "通过邮件通知对方", exact: true })
      .isDisabled(),
    true,
  );
  await pa
    .getByLabel("互联说明（可选）", { exact: true })
    .fill("Administrative peering proposal");
  const pendingResponse = pa.waitForResponse(
    (r) =>
      r.url().endsWith("/api/peering/requests") &&
      r.request().method() === "POST",
  );
  await pa.getByRole("button", { name: "确认发起请求", exact: true }).click();
  const pending = await (await pendingResponse).json();
  await pa.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(pa2, "/peering/requests")).items.find(
      (r) => r.id === pending.id,
    ).canRevoke,
    false,
  );
  await pa
    .getByRole("button", { name: `撤回对等请求 ${pending.id}`, exact: true })
    .click();
  await pa
    .getByRole("button", { name: "确认更新对等记录", exact: true })
    .click();
  await pa.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(pa, "/peering/requests")).items.find((r) => r.id === pending.id)
      .status,
    "revoked",
  );
  await pa.getByRole("button", { name: "发起对等请求", exact: true }).click();
  await pa.getByLabel("对方网络", { exact: true }).selectOption(b.id);
  await pa
    .getByLabel("共享交换 VLAN", { exact: true })
    .selectOption(publicVlan.id);
  const secondRequestResponse = pa.waitForResponse(
    (r) =>
      r.url().endsWith("/api/peering/requests") &&
      r.request().method() === "POST",
  );
  await pa.getByRole("button", { name: "确认发起请求", exact: true }).click();
  const relationship = await (await secondRequestResponse).json();
  await pa.getByRole("dialog").waitFor({ state: "hidden" });
  await go(pb, "peering");
  assert.equal(
    await pb
      .getByRole("button", {
        name: `接受对等请求 ${relationship.id}`,
        exact: true,
      })
      .count(),
    0,
  );
  await go(pba, "peering");
  await pba
    .getByRole("button", {
      name: `接受对等请求 ${relationship.id}`,
      exact: true,
    })
    .click();
  await pba
    .getByRole("button", { name: "确认更新对等记录", exact: true })
    .click();
  await pba.getByRole("dialog").waitFor({ state: "hidden" });
  await pa.getByRole("button", { name: "刷新", exact: true }).click();
  await pa
    .getByRole("button", {
      name: `查看对等请求 ${relationship.id}`,
      exact: true,
    })
    .click();
  await pa
    .getByRole("dialog")
    .getByText("2001:db8:1::10", { exact: true })
    .waitFor();
  assert.equal(
    await pa
      .getByRole("dialog")
      .getByText("2001:db8:1::99", { exact: true })
      .count(),
    0,
  );
  await pa
    .getByRole("dialog")
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  const matrix = await call(pa, "/peering/matrix");
  assert.equal(
    matrix.bilateral.some((r) => r.id === third.id),
    false,
  );
  assert.ok(matrix.routeServer.every((r) => r.memberId === a.id));
  assert.equal(
    matrix.members.some((m) => m.id === d.id),
    false,
  );
  const ownRequests = await call(pa, "/peering/requests");
  assert.equal(JSON.stringify(ownRequests).includes("private-noc"), false);
  assert.ok(
    ownRequests.items.every((r) => r.events.every((e) => !("actorId" in e))),
  );
  assert.equal((await call(page, "/notifications")).items.length, 0);
  await pa.screenshot({
    path: "/tmp/moedove-peering-member.png",
    fullPage: true,
  });
  await pa.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await pa.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
    true,
  );
  await pa.screenshot({
    path: "/tmp/moedove-peering-member-mobile.png",
    fullPage: true,
  });
  console.log(
    "Ordinary-member proposals/own withdrawal, recipient-admin decision, authoritative addresses and private third-party matrix boundaries passed",
  );
  await edit("members", c.id, {
    peeringManagerEnabled: false,
    peeringMatrixEnabled: true,
  });
  assert.equal(
    (await call(pa, "/peering/partners")).members.some((m) => m.id === c.id),
    false,
  );
  assert.equal(
    (await call(pa, "/peering/matrix")).members.some((m) => m.id === c.id),
    true,
  );
  await edit("members", c.id, { peeringMatrixEnabled: false });
  assert.equal(
    (await call(pa, "/peering/matrix")).members.some((m) => m.id === c.id),
    false,
  );
  assert.equal(
    (await call(page, "/peering/matrix")).members.some((m) => m.id === c.id),
    true,
  );
  await edit("connections", connections[0].id, { status: "disabled" });
  await pa.getByRole("button", { name: "刷新", exact: true }).click();
  const invalid = (await call(pa, "/peering/requests")).items.find(
    (r) => r.id === relationship.id,
  );
  assert.equal(invalid.topologyValid, false);
  assert.deepEqual(invalid.fromAddresses, []);
  assert.deepEqual(invalid.toAddresses, []);
  await pa
    .getByRole("button", {
      name: `查看对等请求 ${relationship.id}`,
      exact: true,
    })
    .click();
  await pa
    .getByText(
      "当前拓扑已变化，互联地址不再提供。请检查双方有效接入与 VLAN 权限。",
      { exact: true },
    )
    .waitFor();
  await pa
    .getByRole("dialog")
    .getByRole("button", { name: "关闭", exact: true })
    .click();
  await go(page, "peering");
  await page
    .getByRole("button", {
      name: `撤回对等请求 ${relationship.id}`,
      exact: true,
    })
    .click();
  await page
    .getByRole("button", { name: "确认更新对等记录", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(page, "/peering/requests")).items.find(
      (r) => r.id === relationship.id,
    ).status,
    "revoked",
  );
  assert.deepEqual((await call(page, "/deployments")).items, []);
  await go(page, "integrations");
  await page
    .getByRole("button", { name: new RegExp("Local A.*AS64513") })
    .click();
  await page
    .getByRole("button", { name: "清理终态快照数据", exact: true })
    .click();
  const backedUp = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "导出当前快照数据", exact: true })
    .click();
  assert.match(
    (await backedUp).suggestedFilename(),
    /peeringdb-snapshot-.*\.json/,
  );
  await page
    .getByLabel("我已保存所需资料，理解此次清理不会修改本地网络配置。", {
      exact: true,
    })
    .check();
  await page
    .getByRole("button", { name: "确认清理快照数据", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const pruned = await call(page, `/integrations/proposals/${first.id}`);
  assert.equal(pruned.snapshotPruned, true);
  assert.equal(pruned.snapshotUnavailable, true);
  assert.equal(pruned.snapshot, undefined);
  assert.ok(pruned.diff.length > 0);
  assert.equal(
    await page
      .getByRole("button", { name: "导出快照数据", exact: true })
      .isDisabled(),
    true,
  );
  await page
    .getByRole("button", { name: "删除已清理提案记录", exact: true })
    .click();
  await page
    .getByLabel("我已保存所需资料，理解此次清理不会修改本地网络配置。", {
      exact: true,
    })
    .check();
  await page
    .getByLabel("删除原因", { exact: true })
    .fill("Offline audit record retained");
  await page
    .getByRole("button", { name: "确认删除提案记录", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (
      await page.request.get(`${base}/api/integrations/proposals/${first.id}`)
    ).status(),
    404,
  );
  assert.equal(
    (await call(page, `/resources/members/${a.id}`)).peeringdbId,
    101,
  );
  console.log(
    "Terminal provider snapshot export/prune and audited metadata cleanup preserved current inventory",
  );
  for (const context of contexts) await context.close();
  assert.deepEqual(errors, []);
  console.log(
    "Independent peering opt-outs, topology invalidation/address hiding and administrative-only agreements passed; no external writes or mail.",
  );
} catch (error) {
  throw new Error(
    String(error?.stack ?? error).replace(
      /token=[^\s"'<>]+/g,
      "token=<redacted>",
    ),
  );
} finally {
  if (browser) await browser.close();
  const exited = new Promise((r) => child.once("exit", r));
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
  if (child.exitCode == null) await exited;
  clearTimeout(timer);
  await new Promise((r) => provider.close(r));
}
