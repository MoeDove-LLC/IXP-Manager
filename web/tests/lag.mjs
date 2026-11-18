// Targeted LAG browser workflow. Run against an empty isolated Go instance.
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
const base = process.env.TEST_URL || "http://127.0.0.1:18080";
const request = async (path, body, method = "POST") =>
  page.evaluate(
    async ({ path, body, method }) => {
      const session = await (await fetch("/api/session")).json();
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
          "X-CSRF-Token": session.csrfToken,
        },
        body: body ? JSON.stringify(body) : undefined,
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error);
      return data;
    },
    { path, body, method },
  );
const open = async (name, member, vlan) => {
  await page.locator(".page-heading button").click();
  await page.locator("#field-name").fill(name);
  await page.locator("#field-memberId").selectOption(member);
  await page.locator("#field-vlanId").selectOption(vlan);
};
const choose = async (id, checked = true) =>
  page.locator(`#field-portIds input[value="${id}"]`).setChecked(checked);
const save = async () => {
  const response = page.waitForResponse(
    (r) =>
      r.url().includes("/api/resources/connections") &&
      ["POST", "PUT"].includes(r.request().method()),
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  return response;
};
try {
  await page.goto(base);
  await page.getByLabel("交换中心名称", { exact: true }).fill("LAG Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill(process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token");
  await page.getByLabel("管理员姓名").fill("链路管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill("Lag-test-password-123");
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
  const a = await request("/resources/members", {
      name: "Member A",
      asn: 64513,
      status: "active",
    }),
    b = await request("/resources/members", {
      name: "Member B",
      asn: 64514,
      status: "active",
    });
  const loc = await request("/resources/locations", { name: "LAG DC" }),
    s1 = await request("/resources/switches", {
      name: "switch-a",
      locationId: loc.id,
    }),
    s2 = await request("/resources/switches", {
      name: "switch-b",
      locationId: loc.id,
    });
  const ports = [];
  for (const p of [
    { name: "Ethernet1", switchId: s1.id, speed: 10000, status: "active" },
    { name: "Ethernet2", switchId: s1.id, speed: 10000, status: "active" },
    { name: "Ethernet1", switchId: s2.id, speed: 10000, status: "active" },
    { name: "UnknownSpeed", switchId: s1.id, status: "active" },
    { name: "DisabledPort", switchId: s1.id, speed: 10000, status: "inactive" },
  ])
    ports.push(await request("/resources/ports", p));
  const v1 = await request("/resources/vlans", {
      name: "LAN 100",
      tag: 100,
      ipv4Prefix: "192.0.2.0/24",
    }),
    v2 = await request("/resources/vlans", {
      name: "LAN 200",
      tag: 200,
      ipv4Prefix: "198.51.100.0/24",
    });
  await page.goto(`${base}/#connections`);
  await page.locator("main h1").waitFor();
  await open("Cross-switch LAG", a.id, v1.id);
  await choose(ports[0].id);
  await page.getByLabel("搜索物理端口").fill("switch-b");
  await choose(ports[2].id);
  await page.getByLabel("搜索物理端口").fill("");
  await page.locator("#field-ipv4").fill("192.0.2.10");
  await page.locator("#field-mtu").fill("9000");
  assert.equal(await page.locator("#field-linkMode").inputValue(), "lag");
  assert.equal(await page.locator("#field-speed").inputValue(), "20000");
  assert.equal(await page.locator("#field-speed").getAttribute("readonly"), "");
  await page.screenshot({ path: "/tmp/moedove-lag.png", fullPage: true });
  let response = await save();
  assert.equal(response.status(), 201);
  let payload = response.request().postDataJSON();
  assert.equal("portId" in payload, false);
  assert.equal("speed" in payload, false);
  assert.equal(payload.portIds.length, 2);
  const lag = await response.json();
  assert.deepEqual(lag.portIds, [ports[0].id, ports[2].id]);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("row")
    .filter({
      has: page.getByRole("cell", { name: "Cross-switch LAG", exact: true }),
    })
    .getByRole("cell", { name: "20000", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "编辑 Cross-switch LAG", exact: true })
    .click();
  await choose(ports[2].id, false);
  assert.equal(await page.locator("#field-linkMode").inputValue(), "lag");
  assert.equal(await page.locator("#field-speed").inputValue(), "10000");
  response = await save();
  assert.equal(response.status(), 200);
  assert.equal((await response.json()).linkMode, "lag");
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  console.log(
    "Cross-switch LAG selection, derived speed/MTU and explicit single-member LAG preservation passed",
  );
  await request("/resources/connections", {
    name: "Legacy single",
    memberId: a.id,
    portId: ports[1].id,
    vlanId: v1.id,
    ipv4: "192.0.2.11",
    status: "active",
  });
  await page.reload();
  await page
    .getByRole("button", { name: "编辑 Legacy single", exact: true })
    .click();
  assert.equal(
    await page
      .locator(`#field-portIds input[value="${ports[1].id}"]`)
      .isChecked(),
    true,
  );
  await page.locator("#field-name").fill("Legacy edited");
  response = await save();
  assert.equal(response.status(), 200);
  payload = response.request().postDataJSON();
  assert.deepEqual(payload.portIds, [ports[1].id]);
  assert.equal("portId" in payload, false);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await open("Unknown capacity", a.id, v1.id);
  await choose(ports[3].id);
  assert.equal(await page.locator("#field-speed").inputValue(), "");
  response = await save();
  assert.equal(response.status(), 201);
  assert.equal("speed" in response.request().postDataJSON(), false);
  assert.ok((await response.json()).speed == null);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  console.log(
    "Legacy portId migration and no fabricated capacity for unknown port speed passed",
  );
  await open("Rejected member reuse", b.id, v2.id);
  await choose(ports[0].id);
  response = await save();
  assert.equal(response.status(), 400);
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await open("Disabled physical port", a.id, v1.id);
  await choose(ports[4].id);
  response = await save();
  assert.equal(response.status(), 400);
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page.locator("#field-status").selectOption("disabled");
  response = await save();
  assert.equal(response.status(), 201);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  console.log(
    "Cross-member port reuse and active connection on inactive physical port rejected; draft retained for correction",
  );
  await page.setViewportSize({ width: 390, height: 844 });
  await page
    .getByRole("button", { name: "编辑 Cross-switch LAG", exact: true })
    .click();
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "/tmp/moedove-lag-mobile.png",
    fullPage: true,
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await request("/resources/users", {
    name: "Member A User",
    email: "member@example.test",
    password: "Member-lag-password-123",
    role: "member",
    memberId: a.id,
  });
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await page.getByLabel("邮箱地址").fill("member@example.test");
  await page
    .getByLabel("密码", { exact: true })
    .fill("Member-lag-password-123");
  await page.getByRole("button", { name: "登录控制台", exact: true }).click();
  await page.locator("main").waitFor();
  await page.goto(`${base}/#connections`);
  await page
    .getByRole("cell", { name: "Cross-switch LAG", exact: true })
    .waitFor();
  const memberRow = page.getByRole("row").filter({
    has: page.getByRole("cell", { name: "Cross-switch LAG", exact: true }),
  });
  await memberRow
    .getByRole("cell", { name: "switch-a / Ethernet1", exact: true })
    .waitFor();
  await memberRow.getByRole("cell", { name: "10000", exact: true }).waitFor();
  assert.equal(
    await page.getByRole("button", { name: "新增接入", exact: true }).count(),
    0,
  );
  assert.deepEqual(
    (await request("/resources/ports", undefined, "GET")).items,
    [],
  );
  const projection = (
    await request("/resources/connections", undefined, "GET")
  ).items.find((c) => c.id === lag.id);
  assert.equal(projection.capacityKnown, true);
  assert.equal(projection.capacityMbps, 10000);
  assert.equal("managementIp" in projection.portDetails[0], false);
  console.log(
    "Member portal displays own safe physical labels/capacity without exposing port inventory",
  );
  assert.deepEqual(errors, []);
  console.log(
    "Targeted LAG browser suite passed without uncaught page errors.",
  );
} finally {
  await browser.close();
}
