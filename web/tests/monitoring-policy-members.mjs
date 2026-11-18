// New observability, policy and tenant workflows; use an empty isolated Go instance.
import assert from "node:assert/strict";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.CHROMIUM_PATH,
  args: ["--no-sandbox"],
});
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } }),
  errors = [];
page.on("pageerror", (e) => errors.push(e.message));
const base = process.env.TEST_URL || "http://127.0.0.1:18080",
  password = "Integrated-test-password-123";
const call = async (path, body, method = body ? "POST" : "GET", check = true) =>
  page.evaluate(
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
      const data = await r.json();
      if (check && !r.ok) throw new Error(data.error);
      return check ? data : { status: r.status, data };
    },
    { path, body, method, check },
  );
const go = async (route) => {
  await page.goto(`${base}/#${route}`);
  await page.locator("main").waitFor();
};
const login = async (email) => {
  await page.goto(base);
  await page.getByLabel("邮箱地址").fill(email);
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "登录控制台", exact: true }).click();
  await page.locator("main").waitFor();
};
const logout = async () => {
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await page.getByLabel("邮箱地址").waitFor();
};
const importSnapshot = async (kind, snapshot) => {
  await page.getByRole("button", { name: "导入快照", exact: true }).click();
  await page.getByLabel("快照类型", { exact: true }).selectOption(kind);
  await page
    .getByLabel("快照 JSON", { exact: true })
    .fill(JSON.stringify(snapshot));
  const response = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/policy/snapshots") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "验证并导入", exact: true }).click();
  const r = await response;
  assert.equal(r.status(), 201);
  const data = await r.json();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  return data;
};
try {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("Policy Monitoring Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill(process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token");
  await page.getByLabel("管理员姓名").fill("运维管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
  const a = await call("/resources/members", {
      name: "Network A",
      asn: 64513,
      status: "active",
    }),
    b = await call("/resources/members", {
      name: "Network B",
      asn: 64514,
      status: "active",
    }),
    c = await call("/resources/members", {
      name: "Unassigned Network",
      asn: 64515,
      status: "active",
    });
  const loc = await call("/resources/locations", { name: "Test DC" }),
    sw = await call("/resources/switches", {
      name: "sw-core",
      locationId: loc.id,
    });
  const pa = await call("/resources/ports", {
      name: "port-a",
      switchId: sw.id,
      speed: 10000,
      status: "active",
    }),
    pb = await call("/resources/ports", {
      name: "port-b",
      switchId: sw.id,
      speed: 10000,
      status: "active",
    }),
    pu = await call("/resources/ports", {
      name: "not-yet-sampled",
      switchId: sw.id,
      speed: 10000,
      status: "active",
    });
  const vlan = await call("/resources/vlans", {
    name: "Peering",
    tag: 100,
    ipv4Prefix: "192.0.2.0/24",
  });
  const ca = await call("/resources/connections", {
    name: "A connection",
    memberId: a.id,
    portIds: [pa.id],
    vlanId: vlan.id,
    ipv4: "192.0.2.10",
    status: "active",
  });
  await call("/resources/connections", {
    name: "B connection",
    memberId: b.id,
    portIds: [pb.id],
    vlanId: vlan.id,
    ipv4: "192.0.2.11",
    status: "active",
  });
  const router = await call("/resources/routers", {
    name: "rs-test",
    platform: "bird2",
    asn: 64512,
    routerId: "192.0.2.1",
    localIpv4: "192.0.2.1",
    vlanId: vlan.id,
  });
  const peerBody = {
      name: "A peer",
      routerId: router.id,
      memberId: a.id,
      address: "192.0.2.10",
      asn: 64513,
      enabled: true,
      maxPrefixes: 100,
      allowedPrefixes: ["198.51.100.0/24"],
    },
    peer = await call("/resources/peers", peerBody),
    peerB = await call("/resources/peers", {
      name: "B peer",
      routerId: router.id,
      memberId: b.id,
      address: "192.0.2.11",
      asn: 64514,
      enabled: true,
      maxPrefixes: 100,
      allowedPrefixes: ["203.0.113.0/24"],
    });
  await go("users");
  await page.locator(".page-heading button").click();
  await page.locator("#field-name").fill("Multi Network User");
  await page.locator("#field-email").fill("member@example.test");
  await page.locator("#field-role").selectOption("member");
  await page.locator("#field-password").fill(password);
  await page.locator(`#field-memberIds input[value="${a.id}"]`).check();
  await page.locator(`#field-memberIds input[value="${b.id}"]`).check();
  await page.locator(`#field-memberAdminIds input[value="${a.id}"]`).check();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await call("/resources/users", {
    name: "Read Only",
    email: "viewer@example.test",
    password,
    role: "viewer",
  });
  await go("security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await page.getByLabel("令牌名称", { exact: true }).fill("Bound collector");
  await page
    .getByRole("checkbox", { name: "读取网络资源", exact: true })
    .uncheck();
  await page
    .getByRole("checkbox", { name: "采集器上报监测数据", exact: true })
    .check();
  await page.getByRole("checkbox", { name: "rs-test", exact: true }).check();
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
  const bindingsResponse = await fetch(
    `${base}/api/monitoring/bindings?routerId=${router.id}`,
    { headers: { Authorization: `Bearer ${collector}` } },
  );
  assert.equal(bindingsResponse.status, 200);
  const bindings = await bindingsResponse.json();
  const now = Date.now(),
    large = 9007199254740993n;
  const ingest = async (offset, ports, sessions = []) => {
    const r = await fetch(`${base}/api/monitoring/ingest`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${collector}`,
      },
      body: JSON.stringify({
        routerId: router.id,
        observedAt: new Date(now + offset).toISOString(),
        ports: ports.map((p) => ({
          ...p,
          ownershipRevision: bindings.ports.find((v) => v.portId === p.portId)
            ?.ownershipRevision,
        })),
        sessions: sessions.map((p) => ({
          ...p,
          neighborAddress: [peer, peerB].find((item) => item.id === p.peerId)
            ?.address,
          neighborAsn: [peer, peerB].find((item) => item.id === p.peerId)?.asn,
          ownershipRevision: bindings.sessions.find(
            (v) => v.peerId === p.peerId,
          )?.ownershipRevision,
        })),
      }),
    });
    assert.equal(r.status, 200, await r.text());
  };
  await ingest(
    0,
    [
      {
        portId: pb.id,
        inOctets: "1000",
        outOctets: "2000",
        operStatus: "up",
        counterBits: 64,
        counterEpoch: "boot1",
      },
    ],
    [{ peerId: peerB.id, state: "Idle", receivedPrefixes: 0 }],
  );
  await ingest(1000, [
    {
      portId: pa.id,
      inOctets: String(large),
      outOctets: String(large),
      operStatus: "up",
      counterBits: 64,
      counterEpoch: "boot1",
    },
  ]);
  await ingest(2000, [
    {
      portId: pa.id,
      inOctets: String(large + 125n),
      outOctets: String(large + 250n),
      operStatus: "up",
      counterBits: 64,
      counterEpoch: "boot1",
    },
  ]);
  await ingest(3000, [
    {
      portId: pa.id,
      inOctets: "5",
      outOctets: "5",
      operStatus: "up",
      counterBits: 64,
      counterEpoch: "boot2",
    },
  ]);
  await ingest(
    4000,
    [
      {
        portId: pa.id,
        inOctets: "130",
        outOctets: "255",
        operStatus: "up",
        counterBits: 64,
        counterEpoch: "boot2",
      },
    ],
    [{ peerId: peer.id, state: "Established", receivedPrefixes: 42 }],
  );
  await page.waitForTimeout(Math.max(0, now + 4100 - Date.now()));
  assert.equal(
    (
      await call(
        "/monitoring/ingest",
        {
          routerId: router.id,
          observedAt: new Date().toISOString(),
          ports: [],
          sessions: [],
        },
        "POST",
        false,
      )
    ).status,
    403,
  );
  await go("monitoring");
  await page.getByLabel("监测端口", { exact: true }).selectOption(pa.id);
  await page.getByRole("img", { name: /真实端口流量采样/ }).waitFor();
  await page.getByText("查看采样明细", { exact: true }).click();
  await page
    .getByRole("cell", { name: String(large), exact: true })
    .first()
    .waitFor();
  await page.getByRole("cell", { name: "重置", exact: true }).waitFor();
  assert.ok(
    (await page.getByRole("cell", { name: "1.00 Kbps", exact: true }).count()) >
      0,
  );
  await page.getByLabel("监测时间范围", { exact: true }).selectOption("24");
  await page.getByRole("img", { name: /真实端口流量采样/ }).waitFor();
  await page.getByLabel("监测时间范围", { exact: true }).selectOption("1");
  await page.getByRole("img", { name: /真实端口流量采样/ }).waitFor();
  await page.screenshot({
    path: "/tmp/moedove-monitoring.png",
    fullPage: true,
  });
  await page.getByLabel("监测端口", { exact: true }).selectOption(pb.id);
  const summary = await call("/monitoring/summary");
  assert.equal(summary.ports.find((p) => p.portId === pb.id).status, "fresh");
  assert.equal(summary.ports.find((p) => p.portId === pb.id).inBps, null);
  await page.getByLabel("监测端口", { exact: true }).selectOption(pu.id);
  await page.getByText("所选时段没有采样", { exact: true }).waitFor();
  console.log(
    "Bound collector token, exact >2^53 counters, ownership-bound genuine rates/reset, unknown and 24h chart range passed",
  );
  await go("policy");
  const timing = {
    version: 1,
    fetchedAt: new Date(now - 60000).toISOString(),
    expiresAt: new Date(now + 3600000).toISOString(),
  };
  const irr = await importSnapshot("irr", {
    ...timing,
    source: "TEST-IRR",
    asSet: "AS-MEMBERS",
    asns: [64513, 64496],
    routes: [
      { prefix: "198.51.100.0/24", originAsn: 64513 },
      { prefix: "203.0.113.0/24", originAsn: 64496 },
    ],
  });
  const rpki = await importSnapshot("rpki", {
    ...timing,
    source: "TEST-ROA",
    roas: [
      { prefix: "198.51.100.0/24", maxLength: 24, asn: 64513 },
      { prefix: "203.0.113.0/24", maxLength: 24, asn: 64496 },
    ],
  });
  await page.getByLabel("BGP 会话", { exact: true }).selectOption(peer.id);
  await page.getByLabel("IRR 快照", { exact: true }).selectOption(irr.id);
  await page.getByLabel("ROA 快照", { exact: true }).selectOption(rpki.id);
  await page
    .getByRole("checkbox", { name: "要求 IRR 来源匹配", exact: true })
    .check();
  await page
    .getByLabel("待检查路由", { exact: true })
    .fill("198.51.100.0/24 64513\n198.51.100.0/24 64514 64513 64514");
  await page.getByRole("button", { name: "模拟路由", exact: true }).click();
  await page.getByRole("heading", { name: "模拟结果", exact: true }).waitFor();
  await page.getByText("ROA 有效", { exact: true }).waitFor();
  await page.getByText("ROA 无效", { exact: true }).waitFor();
  await page
    .getByRole("checkbox", {
      name: "候选白名单包含 AS-SET 下游起源 ASN",
      exact: true,
    })
    .check();
  await page
    .getByRole("button", { name: "生成白名单候选", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "白名单变更审查", exact: true })
    .waitFor();
  await page.getByText("候选完整白名单（2 个前缀）", { exact: true }).click();
  await page.getByText(/203\.0\.113\.0\/24  AS64496/).waitFor();
  await call(
    `/resources/peers/${peer.id}`,
    { ...peerBody, allowedPrefixes: ["198.51.100.0/24", "192.0.2.0/24"] },
    "PUT",
  );
  await page
    .getByRole("button", { name: "保存已审查的白名单", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认保存白名单", exact: true })
    .click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.getByRole("button", { name: "继续检查", exact: true }).click();
  await page
    .getByRole("button", { name: "生成白名单候选", exact: true })
    .click();
  await page
    .getByRole("button", { name: "保存已审查的白名单", exact: true })
    .click();
  await page
    .getByRole("button", { name: "确认保存白名单", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const applied = await call(`/resources/peers/${peer.id}`);
  assert.equal(applied.allowedRoutes.length, 2);
  assert.deepEqual((await call("/deployments")).items, []);
  await page
    .getByLabel("待检查路由", { exact: true })
    .fill("203.0.113.0/24 64496 64513 64496");
  await page.getByRole("button", { name: "模拟路由", exact: true }).click();
  await page.getByText("允许", { exact: true }).waitFor();
  await page.screenshot({ path: "/tmp/moedove-policy.png", fullPage: true });
  await go("peers");
  await page.getByRole("button", { name: "编辑 A peer", exact: true }).click();
  assert.equal(
    await page.locator("#field-allowedPrefixes").getAttribute("readonly"),
    "",
  );
  await page.locator("#field-name").fill("A peer reviewed");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (await call(`/resources/peers/${peer.id}`)).allowedRoutes.length,
    2,
  );
  console.log(
    "Typed IRR/ROA imports, AS-path simulation, origin-bound AS-SET proposal, stale-review rejection and protected peer edit passed",
  );
  await page.locator(".page-heading button").click();
  await page.locator("#field-name").fill("Awaiting policy review");
  await page.locator("#field-routerId").selectOption(router.id);
  await page.locator("#field-memberId").selectOption(a.id);
  await page.locator("#field-asn").fill("64513");
  await page.locator("#field-address").fill("192.0.2.12");
  assert.equal(
    await page.locator("#field-allowedPrefixes").evaluate((e) => e.required),
    false,
  );
  await page.locator("#field-enabled").check();
  assert.equal(
    await page.locator("#field-allowedPrefixes").evaluate((e) => e.required),
    true,
  );
  await page.locator("#field-enabled").uncheck();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await go("members");
  await page
    .getByRole("button", { name: "编辑 Network A", exact: true })
    .click();
  await page.locator("#field-billingName").fill("Network A Billing");
  await page.locator("#field-billingEmail").fill("billing-a@example.test");
  await page.locator("#field-joinedAt").fill("2026-11-01");
  assert.equal(
    await page.locator("#field-status").getAttribute("readonly"),
    "",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await logout();
  await login("member@example.test");
  await page.getByLabel("当前成员网络", { exact: true }).selectOption(a.id);
  await go("member-profile");
  await page.getByLabel("电话", { exact: true }).fill("+86 12345678");
  await page.getByRole("button", { name: "保存网络资料", exact: true }).click();
  await page.getByText("网络资料已更新。", { exact: true }).waitFor();
  await page.getByRole("button", { name: "新增联系人", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByLabel("姓名", { exact: true })
    .fill("Alice NOC");
  await page
    .getByRole("dialog")
    .getByLabel("邮箱", { exact: true })
    .fill("alice@example.test");
  await page.getByRole("button", { name: "保存联系人", exact: true }).click();
  await page.getByRole("cell", { name: "Alice NOC", exact: true }).waitFor();
  await page.getByLabel("当前成员网络", { exact: true }).selectOption(b.id);
  await page.getByRole("heading", { name: "Network B", exact: true }).waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "保存网络资料", exact: true })
      .count(),
    0,
  );
  assert.equal(
    await page.getByRole("button", { name: "新增联系人", exact: true }).count(),
    0,
  );
  assert.equal(
    await page.getByRole("cell", { name: "Alice NOC", exact: true }).count(),
    0,
  );
  assert.equal(
    (await call("/member/profile", { name: "unauthorized" }, "PUT", false))
      .status,
    403,
  );
  assert.equal(
    (await call("/memberships/select", { memberId: c.id }, "POST", false))
      .status,
    403,
  );
  await go("monitoring");
  await page.getByLabel("监测端口", { exact: true }).waitFor();
  assert.equal(
    await page.getByLabel("监测端口", { exact: true }).inputValue(),
    pb.id,
  );
  assert.equal(
    await page
      .locator(`select[aria-label="监测端口"] option[value="${pa.id}"]`)
      .count(),
    0,
  );
  await page.getByLabel("当前成员网络", { exact: true }).selectOption(a.id);
  await page.getByRole("img", { name: /真实端口流量采样/ }).waitFor();
  assert.equal(
    await page.getByLabel("监测端口", { exact: true }).inputValue(),
    pa.id,
  );
  await page.getByText("查看采样明细", { exact: true }).click();
  assert.equal(
    await page
      .getByRole("columnheader", { name: "入口计数", exact: true })
      .count(),
    0,
  );
  const memberSeries = await call(`/monitoring/series?portId=${pa.id}`);
  assert.equal(memberSeries.points[0].inOctets, undefined);
  assert.equal(
    (await call(`/monitoring/series?portId=${pb.id}`, undefined, "GET", false))
      .status,
    404,
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(
    () => document.querySelector('main svg[role="img"]')?.clientHeight >= 200,
  );
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "/tmp/moedove-monitoring-member-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await go("policy");
  await page.getByText("你没有此页面的访问权限。", { exact: true }).waitFor();
  console.log(
    "Multi-membership switching remounts tenant data; per-tenant administrator selfservice, monitoring privacy and staff boundaries passed",
  );
  await logout();
  await login("admin@example.test");
  await go("members");
  await page
    .getByLabel("成员状态操作 Network A", { exact: true })
    .selectOption("suspend");
  await page
    .getByLabel("操作原因", { exact: true })
    .fill("Scheduled test suspension");
  await page.getByRole("button", { name: "确认更新状态", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal((await call(`/resources/peers/${peer.id}`)).enabled, false);
  assert.equal(
    (await call(`/resources/connections/${ca.id}`)).status,
    "disabled",
  );
  await page.getByRole("link", { name: "前往配置中心", exact: true }).waitFor();
  await page
    .getByLabel("成员状态操作 Network A", { exact: true })
    .selectOption("activate");
  await page
    .getByLabel("操作原因", { exact: true })
    .fill("Return from planned suspension");
  await page.getByRole("button", { name: "确认更新状态", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal((await call(`/resources/peers/${peer.id}`)).enabled, false);
  assert.equal(
    (await call(`/resources/connections/${ca.id}`)).status,
    "disabled",
  );
  console.log(
    "Registered/billing fields and suspend/activate lifecycle preserve explicit configuration review passed",
  );
  await logout();
  await login("viewer@example.test");
  await go("policy");
  assert.equal(
    await page.getByRole("button", { name: "导入快照", exact: true }).count(),
    0,
  );
  await page.getByLabel("BGP 会话", { exact: true }).selectOption(peer.id);
  await page
    .getByLabel("待检查路由", { exact: true })
    .fill("198.51.100.0/24 64513");
  await page.getByRole("button", { name: "模拟路由", exact: true }).click();
  await page.getByRole("heading", { name: "模拟结果", exact: true }).waitFor();
  assert.deepEqual(errors, []);
  console.log(
    "Viewer read-only simulation passed. New integration suite completed without uncaught page errors.",
  );
} finally {
  await browser.close();
}
