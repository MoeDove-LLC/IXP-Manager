// Targeted physical patch lifecycle/LOA + address allocation. Isolated empty server only.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
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
  password = "Patch-fixture-password-123";
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
const ports = async () => (await call(page, "/patching/ports")).items;
const select = async (name) =>
  page
    .getByRole("button", { name: `查看配线端口 ${name}`, exact: true })
    .click();
const perform = async (action, values = {}) => {
  await page.getByRole("button", { name: action, exact: true }).click();
  for (const [key, value] of Object.entries(values)) {
    const input = page.locator(`#${key}`);
    if (typeof value === "boolean") await input.setChecked(value);
    else if ((await input.evaluate((e) => e.tagName)) === "SELECT")
      await input.selectOption(value);
    else await input.fill(value);
  }
  await page
    .getByLabel("操作原因 / 工单说明 *", { exact: true })
    .fill(`Browser fixture: ${action}`);
  const pending = page.waitForResponse(
    (r) =>
      r.url().includes("/api/patching/ports/") &&
      r.url().endsWith("/transition") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "确认配线操作", exact: true }).click();
  const response = await pending;
  assert.equal(response.status(), 200, await response.text());
  await page.getByRole("dialog").waitFor({ state: "hidden" });
};
const issue = async () => {
  await page.getByRole("button", { name: "签发 LOA", exact: true }).click();
  const date = new Date(Date.now() + 86400000);
  await page
    .getByLabel("授权截止时间", { exact: true })
    .fill(
      new Date(date.getTime() - date.getTimezoneOffset() * 60000)
        .toISOString()
        .slice(0, 16),
    );
  await page
    .getByLabel("签发原因", { exact: true })
    .fill("Synthetic local authorization for browser verification");
  await page.getByRole("button", { name: "确认签发 LOA", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  return (await call(page, "/patching/loas")).items.findLast(
    (l) => l.status === "issued",
  );
};
try {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("MoeDove 配线测试");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill(process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token");
  await page.getByLabel("管理员姓名").fill("配线管理员");
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
  const loc = await call(page, "/resources/locations", {
      name: "测试机房",
      city: "上海",
      country: "CN",
    }),
    rack = await call(page, "/resources/racks", {
      name: "Rack A",
      locationId: loc.id,
      units: 42,
    }),
    sw = await call(page, "/resources/switches", {
      name: "switch-a",
      locationId: loc.id,
      rackId: rack.id,
    }),
    spa = await call(page, "/resources/ports", {
      name: "Ethernet A",
      switchId: sw.id,
      speed: 10000,
      status: "active",
      memberId: a.id,
    }),
    spb = await call(page, "/resources/ports", {
      name: "Ethernet B",
      switchId: sw.id,
      speed: 10000,
      status: "active",
      memberId: b.id,
    }),
    sp2 = await call(page, "/resources/ports", {
      name: "Ethernet A2",
      switchId: sw.id,
      speed: 10000,
      status: "active",
      memberId: a.id,
    }),
    panel = await call(page, "/resources/patch-panels", {
      name: "Panel A",
      rackId: rack.id,
      portCount: 12,
    });
  const p1 = await call(page, "/resources/patch-ports", {
      name: "Fibre 1",
      panelId: panel.id,
      number: 1,
    }),
    p2 = await call(page, "/resources/patch-ports", {
      name: "Fibre 2",
      panelId: panel.id,
      number: 2,
    });
  const doc = await page.evaluate(async (memberId) => {
    const s = await (await fetch("/api/session")).json();
    const form = new FormData();
    form.set(
      "file",
      new File(["Synthetic private work order"], "A Work Order.txt", {
        type: "text/plain",
      }),
    );
    form.set("visibility", "member");
    form.set("memberId", memberId);
    const r = await fetch("/api/documents", {
      method: "POST",
      headers: { "X-CSRF-Token": s.csrfToken },
      body: form,
    });
    if (!r.ok) throw new Error((await r.json()).error);
    return r.json();
  }, a.id);
  await go(page, "patching");
  await select("Fibre 1");
  assert.equal(
    await page.getByRole("button", { name: "确认连接", exact: true }).count(),
    0,
  );
  await page.getByRole("button", { name: "预留端口", exact: true }).click();
  await page.getByLabel("预留成员", { exact: true }).selectOption(a.id);
  await page.locator("#patch-pair").selectOption(p2.id);
  const partner = (await ports()).find((p) => p.id === p2.id);
  await call(page, `/patching/ports/${p2.id}/transition`, {
    action: "prewire",
    revision: partner.revision,
    switchPortId: spa.id,
    reason: "Concurrent prewire fixture",
  });
  await page
    .getByLabel("操作原因 / 工单说明 *", { exact: true })
    .fill("Stale pair review");
  const staleResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/patching/ports/${p1.id}/transition`) &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "确认配线操作", exact: true }).click();
  const stale = await staleResponse;
  assert.equal(stale.status(), 409);
  assert.equal(
    stale.request().postDataJSON().duplexPeerRevision,
    partner.revision,
  );
  assert.match((await stale.json()).error, /duplex partner changed/);
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await page.getByText("已预布线", { exact: true }).first().waitFor();
  await page.getByRole("button", { name: "预留端口", exact: true }).click();
  await page.getByLabel("预留成员", { exact: true }).selectOption(a.id);
  await page.locator("#patch-pair").selectOption(p2.id);
  await page.locator("#patch-switch").selectOption(spa.id);
  await page.locator("#patch-circuit").fill("CIR-A-001");
  await page.getByLabel("更新关联资料", { exact: true }).check();
  await page
    .getByRole("checkbox", { name: "A Work Order.txt", exact: true })
    .check();
  await page
    .getByLabel("操作原因 / 工单说明 *", { exact: true })
    .fill("Reserve duplex with private work order");
  const reservedResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/patching/ports/${p1.id}/transition`) &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "确认配线操作", exact: true }).click();
  const reserveResult = await reservedResponse;
  assert.equal(reserveResult.status(), 200, await reserveResult.text());
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const reserved = await ports();
  assert.equal(reserved.find((p) => p.id === p1.id).status, "reserved");
  assert.equal(reserved.find((p) => p.id === p2.id).memberId, a.id);
  assert.equal(
    reserved.find((p) => p.id === p1.id).allocationId,
    reserved.find((p) => p.id === p2.id).allocationId,
  );
  const firstLOA = await issue();
  assert.ok(firstLOA);
  await page.screenshot({ path: "/tmp/moedove-patching.png", fullPage: true });
  const aContext = await browser.newContext(),
    aPage = await aContext.newPage();
  await login(aPage, "a@example.test");
  await go(aPage, "patching");
  await aPage
    .getByRole("button", { name: "查看配线端口 Fibre 1", exact: true })
    .click();
  assert.equal(
    await aPage.getByRole("button", { name: "预留端口", exact: true }).count(),
    0,
  );
  assert.equal(
    await aPage.getByText("不可变配线历史", { exact: true }).count(),
    0,
  );
  assert.equal(
    (
      await aPage.request.get(`${base}/api/patching/history?portId=${p1.id}`)
    ).status(),
    403,
  );
  const pdfDownload = aPage.waitForEvent("download");
  await aPage.getByRole("button", { name: "PDF", exact: true }).click();
  const pdf = await readFile(await (await pdfDownload).path());
  assert.equal(pdf.subarray(0, 5).toString(), "%PDF-");
  const html = await (
    await aPage.request.get(
      `${base}/api/patching/loas/${firstLOA.id}/download?format=html`,
    )
  ).text();
  assert.ok(html.includes("Network A"));
  assert.ok(html.includes("data:image/"));
  await aPage.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await aPage.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  await aPage.screenshot({
    path: "/tmp/moedove-patching-member-mobile.png",
    fullPage: true,
  });
  await perform("申请连接");
  await perform("确认连接");
  assert.equal(
    (await call(page, "/patching/loas")).items.find((l) => l.id === firstLOA.id)
      .status,
    "stale",
  );
  await aPage.reload();
  await aPage.getByText("没有可显示的 LOA", { exact: true }).waitFor();
  assert.ok(
    (
      await aPage.request.get(
        `${base}/api/patching/loas/${firstLOA.id}/download?format=pdf`,
      )
    ).status() >= 400,
  );
  await perform("申请终止");
  await perform("确认终止");
  await page.getByRole("button", { name: "释放端口", exact: true }).click();
  await page.getByLabel(/保留交换机绑定与双芯关系/).check();
  await page
    .getByLabel("操作原因 / 工单说明 *", { exact: true })
    .fill("Release current allocation, keep prewire");
  await page.getByRole("button", { name: "确认配线操作", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal((await ports()).find((p) => p.id === p1.id).status, "prewired");
  await aPage.reload();
  await aPage.getByText("没有匹配的配线端口", { exact: true }).waitFor();
  await perform("预留端口", {
    "patch-member": b.id,
    "patch-switch": spb.id,
    "patch-circuit": "CIR-B-001",
  });
  const secondLOA = await issue();
  assert.equal(secondLOA.memberId, b.id);
  const bContext = await browser.newContext(),
    bPage = await bContext.newPage();
  await login(bPage, "b@example.test");
  await go(bPage, "patching");
  assert.equal((await call(bPage, "/patching/loas")).items.length, 1);
  assert.ok(
    (
      await bPage.request.get(
        `${base}/api/patching/loas/${firstLOA.id}/download?format=pdf`,
      )
    ).status() >= 400,
  );
  assert.equal(
    (await call(bPage, "/documents")).items.some((d) => d.id === doc.id),
    false,
  );
  await page.getByRole("button", { name: "撤销", exact: true }).click();
  await page
    .getByLabel("撤销原因", { exact: true })
    .fill("Synthetic authorization revoked");
  await page.getByRole("button", { name: "确认撤销授权", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await bPage.reload();
  await bPage.getByText("没有可显示的 LOA", { exact: true }).waitFor();
  await perform("释放端口");
  const free = await ports();
  assert.equal(free.find((p) => p.id === p1.id).status, "available");
  assert.equal(free.find((p) => p.id === p2.id).duplexPeerId ?? "", "");
  await perform("标记故障");
  await perform("恢复可用");
  assert.ok(
    (await call(page, `/patching/history?portId=${p1.id}`)).items.length >= 8,
  );
  await aContext.close();
  await bContext.close();
  console.log(
    "Duplex optimistic revisions, complete lifecycle, private documents, real PDF/HTML LOA and current-only tenant isolation passed",
  );
  const vlan = await call(page, "/resources/vlans", {
    name: "IPAM LAN",
    tag: 100,
    ipv4Prefix: "192.0.2.0/29",
    ipv6Prefix: "2001:db8:100::/64",
  });
  await call(page, "/resources/routers", {
    name: "IPAM rs",
    platform: "bird2",
    asn: 64512,
    routerId: "192.0.2.1",
    localIpv4: "192.0.2.1",
    vlanId: vlan.id,
  });
  await call(page, "/resources/connections", {
    name: "Existing connection",
    memberId: a.id,
    portIds: [spa.id],
    vlanId: vlan.id,
    ipv4: "192.0.2.2",
    status: "active",
  });
  await go(page, "ipam");
  await page.getByLabel("交换 VLAN", { exact: true }).selectOption(vlan.id);
  await page.getByRole("button", { name: "分配地址", exact: true }).click();
  await page.getByLabel("成员网络", { exact: true }).selectOption(a.id);
  await page.getByLabel("地址族", { exact: true }).selectOption("4");
  await page.getByLabel("数量（最多 64）", { exact: true }).fill("2");
  await page.getByRole("button", { name: "确认预留地址", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const pool = await call(page, `/ipam?vlanId=${vlan.id}`),
    allocated = pool.items.filter(
      (i) => i.state === "reserved" && i.memberId === a.id,
    );
  assert.equal(allocated.length, 2);
  assert.ok(
    allocated.every(
      (i) =>
        !["192.0.2.0", "192.0.2.1", "192.0.2.2", "192.0.2.7"].includes(
          i.address,
        ),
    ),
  );
  await call(page, "/resources/connections", {
    name: "IPAM assigned connection",
    memberId: a.id,
    portIds: [sp2.id],
    vlanId: vlan.id,
    ipv4: allocated[0].address,
    status: "active",
  });
  await page.getByRole("button", { name: "刷新分配状态", exact: true }).click();
  const usedRow = page.getByRole("row").filter({
    has: page.getByRole("cell", { name: allocated[0].address, exact: true }),
  });
  await usedRow.getByText("连接正在使用", { exact: true }).waitFor();
  assert.equal(
    await usedRow
      .getByRole("button", { name: "释放预留", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(
    (
      await call(
        page,
        "/ipam/release",
        { id: allocated[0].id, expectedMemberId: a.id },
        "POST",
        false,
      )
    ).status,
    409,
  );
  const spareRow = page.getByRole("row").filter({
    has: page.getByRole("cell", { name: allocated[1].address, exact: true }),
  });
  await spareRow.getByRole("button", { name: "释放预留", exact: true }).click();
  await call(page, "/ipam/release", {
    id: allocated[1].id,
    expectedMemberId: a.id,
  });
  await call(page, "/ipam/allocate", {
    vlanId: vlan.id,
    memberId: b.id,
    family: 4,
    address: allocated[1].address,
  });
  await page.getByRole("button", { name: "确认释放地址", exact: true }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await page.getByRole("button", { name: "刷新分配状态", exact: true }).click();
  await spareRow.getByText("Network B", { exact: true }).waitFor();
  await spareRow.getByRole("button", { name: "释放预留", exact: true }).click();
  await page.getByRole("button", { name: "确认释放地址", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "分配地址", exact: true }).click();
  await page.getByLabel("成员网络", { exact: true }).selectOption(a.id);
  await page.getByLabel("地址族", { exact: true }).selectOption("6");
  await page
    .getByLabel("指定地址（可选）", { exact: true })
    .fill("2001:db8:100::10");
  await page.getByRole("button", { name: "确认预留地址", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("cell", { name: "2001:db8:100::10", exact: true })
    .waitFor();
  const latest = await call(page, `/ipam?vlanId=${vlan.id}`),
    v6 = latest.prefixes.find((p) => p.family === 6);
  await page
    .getByText(BigInt(v6.available).toLocaleString("zh-CN"), { exact: true })
    .waitFor();
  await page.screenshot({ path: "/tmp/moedove-ipam.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    true,
  );
  assert.deepEqual(errors, []);
  console.log(
    "IPAM automatic reservations, assigned/router guards, stale-owner release protection and exact IPv6 capacity passed",
  );
} finally {
  await browser.close();
}
