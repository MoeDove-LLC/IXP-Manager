// Counter-source review/CAS only. Isolated inventory, no external collectors.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-traffic-source-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "Traffic-source-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-traffic-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "traffic-source-bootstrap" },
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
      throw new Error(`Source fixture exited: ${stderr}`);
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (attempt > 100) throw new Error("Source fixture startup timeout");
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
    page = await context.newPage(),
    errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const setup = await context.request.post(base + "/api/setup", {
    data: {
      setupToken: "traffic-source-bootstrap",
      name: "Source QA",
      email: "admin@example.test",
      password,
      ixpName: "Traffic Source QA",
      asn: 64500,
    },
  });
  assert.equal(setup.status(), 200);
  const call = async (path, body, ctx = context) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + path, {
      method: body ? "POST" : "GET",
      headers: { "X-CSRF-Token": session.csrfToken || "" },
      ...(body ? { data: body } : {}),
    });
    return { status: response.status(), data: await response.json() };
  };
  const api = async (...args) => {
    const response = await call(...args);
    assert.ok(
      response.status < 300,
      `${args[0]} ${response.status} ${JSON.stringify(response.data)}`,
    );
    return response.data;
  };
  const location = await api("/resources/locations", { name: "Source DC" }),
    sw = await api("/resources/switches", {
      name: "Source switch",
      locationId: location.id,
    });
  const physical = await api("/resources/ports", {
    name: "Reviewed source port",
    switchId: sw.id,
    speed: 10000,
    status: "active",
  });
  const member = await api("/resources/members", {
    name: "Source member",
    asn: 64510,
    status: "active",
  });
  const vlan = await api("/resources/vlans", {
    name: "Source LAN",
    tag: 411,
    ipv4Prefix: "192.0.2.0/24",
  });
  await api("/resources/connections", {
    name: "Source connection",
    memberId: member.id,
    vlanId: vlan.id,
    portId: physical.id,
    ipv4: "192.0.2.10",
    status: "active",
  });
  for (const [name, address] of [
    ["归档采集 A", "192.0.2.1"],
    ["归档采集 B", "192.0.2.2"],
  ])
    await api("/resources/routers", {
      name,
      asn: 64500,
      routerId: address,
      localIpv4: address,
      vlanId: vlan.id,
      platform: "bird2",
    });
  const getTarget = async () =>
    (await api("/traffic/catalog")).targets.find(
      (target) => target.scope === "port" && target.id === physical.id,
    );
  const initial = await getTarget(),
    routers = initial.sourceControl.choices.filter((choice) =>
      choice.value.startsWith("router:"),
    );
  assert.equal(initial.sourceControl.status, "ambiguous");
  assert.equal(routers.length, 2);
  const initialRead = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/traffic/query",
  );
  await page.goto(base + "/#traffic");
  assert.equal((await initialRead).status(), 200);
  await page.locator("#traffic-target").selectOption(initial.key);
  await page.locator("#traffic-source").selectOption(routers[0].value);
  const saveSource = async (expectedStatus) => {
    const response = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/traffic/source" &&
        response.request().method() === "POST",
    );
    const refreshed =
      expectedStatus === 200
        ? page.waitForResponse(
            (response) =>
              new URL(response.url()).pathname === "/api/traffic/query",
          )
        : null;
    await page
      .getByRole("button", { name: "保存归档来源", exact: true })
      .click();
    const result = await response;
    assert.equal(result.status(), expectedStatus, await result.text());
    if (refreshed) assert.equal((await refreshed).status(), 200);
    return result.request().postDataJSON();
  };
  const first = await saveSource(200);
  assert.equal(first.expectedRevision, initial.sourceControl.revision);
  const selected = await getTarget();
  assert.equal(selected.sourceControl.configured, routers[0].value);
  assert.equal(selected.sourceControl.status, "explicit");
  assert.equal(
    await page.locator("#traffic-source").inputValue(),
    routers[0].value,
  );
  await page.locator("#traffic-source").selectOption("none");
  await api("/traffic/source", {
    portId: physical.id,
    selection: routers[1].value,
    expectedRevision: selected.sourceControl.revision,
  });
  const stale = await saveSource(409);
  assert.equal(stale.expectedRevision, selected.sourceControl.revision);
  assert.equal(stale.selection, "none");
  await page
    .getByRole("button", { name: "按当前设置重新核对", exact: true })
    .waitFor();
  assert.equal(await page.locator("#traffic-source").inputValue(), "none");
  assert.equal((await getTarget()).sourceControl.configured, routers[1].value);
  await page
    .getByRole("button", { name: "按当前设置重新核对", exact: true })
    .click();
  assert.equal(await page.locator("#traffic-source").inputValue(), "none");
  const reviewed = await saveSource(200);
  assert.notEqual(reviewed.expectedRevision, stale.expectedRevision);
  assert.equal((await getTarget()).sourceControl.configured, "none");
  assert.match(
    await page.getByTestId("traffic-source-control").innerText(),
    /已停用归档/,
  );
  console.log(
    "Traffic source ambiguity → explicit choice → real CAS409 preserves draft → separate review/save passed",
  );

  for (const [email, role] of [
    ["source-viewer@example.test", "viewer"],
    ["source-member@example.test", "member"],
  ]) {
    await api("/resources/users", {
      name: email,
      email,
      password,
      role,
      ...(role === "member" ? { memberId: member.id } : {}),
    });
    const ctx = await browser.newContext();
    assert.equal(
      (
        await ctx.request.post(base + "/api/login", {
          data: { email, password },
        })
      ).status(),
      200,
    );
    const catalog = await api("/traffic/catalog", undefined, ctx),
      target = catalog.targets.find(
        (target) => target.scope === "port" && target.id === physical.id,
      );
    assert.ok(target);
    if (role === "member") assert.equal(target.sourceControl, undefined);
    else assert.equal(target.sourceControl.editable, false);
    const forbidden = await call(
      "/traffic/source",
      {
        portId: physical.id,
        selection: "auto",
        expectedRevision: (await getTarget()).sourceControl.revision,
      },
      ctx,
    );
    assert.equal(forbidden.status, 403);
    const rolePage = await ctx.newPage();
    await rolePage.goto(base + "/#traffic");
    await rolePage.locator("#traffic-target").selectOption(target.key);
    assert.equal(await rolePage.locator("#traffic-source").count(), 0);
    await ctx.close();
  }
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
  await page.screenshot({ path: "/tmp/moedove-traffic-source-mobile.png" });
  assert.deepEqual(errors, []);
  console.log(
    "Traffic source staff/member controls, forbidden writes and390px passed",
  );

  // Counter32 packet deltas are conditional lower bounds, not precise totals/rates.
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.locator("#traffic-source").selectOption(routers[0].value);
  await saveSource(200);
  const routerId = routers[0].value.slice("router:".length);
  const token = await api("/account/tokens", {
    name: "Local modulo-counter fixture",
    scopes: ["monitoring:write"],
    routerIds: [routerId],
    currentPassword: password,
  });
  const authorization = { Authorization: `Bearer ${token.token}` };
  const bindingResponse = await fetch(
    `${base}/api/monitoring/bindings?routerId=${routerId}`,
    { headers: authorization },
  );
  assert.equal(bindingResponse.status, 200);
  const binding = (await bindingResponse.json()).ports.find(
    (item) => item.portId === physical.id,
  );
  assert.ok(binding);
  const sampledAt = Date.now();
  for (const [offset, inCount, outCount] of [
    [0, "4294967291", "1000"],
    [2000, "10", "1020"],
  ]) {
    const response = await fetch(base + "/api/monitoring/ingest", {
      method: "POST",
      headers: { ...authorization, "Content-Type": "application/json" },
      body: JSON.stringify({
        routerId,
        observedAt: new Date(sampledAt + offset).toISOString(),
        ports: [
          {
            portId: physical.id,
            ownershipRevision: binding.ownershipRevision,
            counterEpoch: "conditional-wrap",
            counterBits: 64,
            inOctets: String(1000 + offset),
            outOctets: String(2000 + offset),
            operStatus: "up",
            unicast: { in: inCount, out: outCount, bits: 32 },
          },
        ],
        sessions: [],
      }),
    });
    assert.equal(response.status, 200, await response.text());
  }
  await new Promise((resolve) =>
    setTimeout(resolve, Math.max(0, sampledAt + 3100 - Date.now())),
  );
  await page.locator("#traffic-metric").selectOption("unicast");
  await page.locator("#traffic-to").fill(new Date().toISOString().slice(0, 19));
  const read = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/traffic/query",
  );
  await page.getByRole("button", { name: "查询归档", exact: true }).click();
  const response = await read,
    report = await response.json();
  assert.equal(response.status(), 200);
  assert.equal(report.summary.in.total, null);
  assert.equal(report.summary.in.averageRate, null);
  assert.equal(report.summary.in.lowerBoundTotal, "15");
  assert.ok(report.summary.in.ambiguousIntervals > 0);
  await page
    .getByTestId("traffic-in-total")
    .filter({ hasText: "无有效区间" })
    .waitFor();
  await page
    .getByTestId("traffic-in-lower-bound")
    .filter({ hasText: "至少 15 包（未排除多次回绕）" })
    .waitFor();
  await page
    .getByTestId("traffic-out-lower-bound")
    .filter({ hasText: "至少 20 包（未排除多次回绕）" })
    .waitFor();
  await page
    .getByRole("heading", { name: "所选范围没有有效速率区间", exact: true })
    .waitFor();
  assert.equal(
    await page.getByRole("img", { name: /物理计数器.*平均速率/ }).count(),
    0,
  );
  await page.getByTestId("traffic-in-lower-bound").scrollIntoViewIfNeeded();
  await page.screenshot({ path: "/tmp/moedove-traffic-lower-bound.png" });
  assert.deepEqual(errors, []);
  console.log(
    "Traffic real Counter32 lower bounds15/20 displayed separately; exact totals/rates remain null passed",
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
  console.log("Traffic source isolated browser and manager stopped");
}
