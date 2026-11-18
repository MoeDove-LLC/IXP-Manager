// Isolated counter archive UI test. Uses only loopback fixtures; no live devices.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-traffic-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "Traffic-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "traffic-bootstrap" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser,
  stderr = "";
child.stderr.on("data", (chunk) => {
  stderr = (stderr + chunk.toString()).slice(-4000);
});
try {
  for (let n = 0; ; n++) {
    if (child.exitCode !== null)
      throw new Error(`Traffic fixture exited: ${stderr}`);
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (n > 100) throw new Error("Traffic fixture startup timeout");
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
  });
  const page = await context.newPage(),
    errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const setup = await context.request.post(base + "/api/setup", {
    data: {
      setupToken: "traffic-bootstrap",
      name: "Traffic QA",
      email: "admin@example.test",
      password,
      ixpName: "Traffic QA",
      asn: 64500,
    },
  });
  assert.equal(setup.status(), 200);
  const call = async (
    path,
    body,
    method = body ? "POST" : "GET",
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
      ...(body ? { data: body } : {}),
    });
    return { status: response.status(), data: await response.json() };
  };
  const api = async (...args) => {
    const response = await call(...args);
    assert.ok(
      response.status < 300,
      `${args[0]} ${response.status}: ${JSON.stringify(response.data)}`,
    );
    return response.data;
  };
  const location = await api("/resources/locations", { name: "Archive DC" });
  const sw = await api("/resources/switches", {
    name: "Archive switch",
    locationId: location.id,
  });
  const physical = await api("/resources/ports", {
    name: "Archive physical",
    switchId: sw.id,
    speed: 10000,
    status: "active",
  });
  const catalog = await api("/traffic/catalog");
  assert.equal(catalog.sourceKind, "counter");
  assert.equal(catalog.timezone, "UTC");
  assert.ok(
    catalog.targets.some(
      (target) => target.scope === "port" && target.id === physical.id,
    ),
  );
  const initialQuery = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/traffic/query",
  );
  await page.goto(base + "/#traffic");
  await page.getByRole("heading", { name: "长期流量", exact: true }).waitFor();
  assert.equal((await initialQuery).status(), 200);
  const portTarget = catalog.targets.find(
    (target) => target.scope === "port" && target.id === physical.id,
  );
  await page
    .locator("#traffic-target")
    .selectOption(portTarget.key || `port:${physical.id}`);
  const query = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/traffic/query",
  );
  await page.getByRole("button", { name: "查询归档", exact: true }).click();
  const resultResponse = await query,
    result = await resultResponse.json();
  assert.equal(resultResponse.status(), 200);
  assert.equal(result.summary.in.total, null);
  await page
    .getByTestId("traffic-in-total")
    .filter({ hasText: "无有效区间" })
    .waitFor();
  await page
    .getByRole("heading", { name: "所选范围没有有效速率区间", exact: true })
    .waitFor();
  const request = new URL(resultResponse.url());
  assert.ok(request.searchParams.get("from").endsWith("Z"));
  assert.ok(request.searchParams.get("to").endsWith("Z"));
  assert.equal(request.searchParams.get("scope"), "port");
  assert.equal(request.searchParams.get("id"), physical.id);
  assert.equal(
    Date.parse(`${await page.locator("#traffic-from").inputValue()}Z`),
    Date.parse(request.searchParams.get("from")),
  );
  console.log(
    "Traffic real catalog → authorized filter → UTC query → null/empty report passed",
  );

  const member = await api("/resources/members", {
    name: "Archive member A",
    asn: 64510,
    status: "active",
  });
  const vlan = await api("/resources/vlans", {
    name: "Archive LAN",
    tag: 310,
    ipv4Prefix: "192.0.2.0/24",
  });
  const connection = await api("/resources/connections", {
    name: "Archive connection",
    memberId: member.id,
    vlanId: vlan.id,
    portId: physical.id,
    ipv4: "192.0.2.2",
    status: "active",
  });
  const router = await api("/resources/routers", {
    name: "Archive collector",
    asn: 64500,
    routerId: "10.255.0.1",
    localIpv4: "192.0.2.1",
    vlanId: vlan.id,
    platform: "bird2",
  });
  const token = await api("/account/tokens", {
    name: "Local archive fixture",
    scopes: ["monitoring:write"],
    routerIds: [router.id],
    currentPassword: password,
  });
  const authorization = { Authorization: `Bearer ${token.token}` };
  const bindingResponse = await fetch(
    `${base}/api/monitoring/bindings?routerId=${router.id}`,
    { headers: authorization },
  );
  assert.equal(bindingResponse.status, 200);
  const bindings = await bindingResponse.json(),
    binding = bindings.ports.find((p) => p.portId === physical.id);
  assert.ok(binding?.ownershipRevision);
  const now = Date.now(),
    large = 9007199254740993n;
  for (const [offset, delta] of [
    [0, 0n],
    [2000, 250n],
  ]) {
    const response = await fetch(base + "/api/monitoring/ingest", {
      method: "POST",
      headers: { ...authorization, "Content-Type": "application/json" },
      body: JSON.stringify({
        routerId: router.id,
        observedAt: new Date(now + offset).toISOString(),
        ports: [
          {
            portId: physical.id,
            ownershipRevision: binding.ownershipRevision,
            counterEpoch: "traffic-fixture-1",
            counterBits: 64,
            inOctets: String(large + delta),
            outOctets: String(large + delta * 2n),
            unicast: {
              in: String(large + delta),
              out: String(large),
              bits: 64,
            },
            operStatus: "up",
          },
        ],
        sessions: [],
      }),
    });
    assert.equal(response.status, 200, await response.text());
  }
  await new Promise((resolve) =>
    setTimeout(resolve, Math.max(0, now + 3100 - Date.now())),
  );
  const refreshedCatalog = page.waitForResponse(
    (response) => new URL(response.url()).pathname === "/api/traffic/query",
  );
  await page.getByRole("button", { name: "刷新可选对象", exact: true }).click();
  assert.equal((await refreshedCatalog).status(), 200);
  const refreshQuery = async () => {
    await page
      .locator("#traffic-to")
      .fill(new Date().toISOString().slice(0, 19));
    const pending = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/traffic/query",
    );
    await page.getByRole("button", { name: "查询归档", exact: true }).click();
    const response = await pending,
      report = await response.json();
    assert.equal(response.status(), 200, JSON.stringify(report));
    return report;
  };
  const sampled = await refreshQuery();
  assert.equal(sampled.summary.in.total, "250");
  assert.equal(sampled.summary.out.total, "500");
  await page
    .getByTestId("traffic-in-total")
    .filter({ hasText: "250 B" })
    .waitFor();
  await page.getByRole("img", { name: /物理计数器.*平均速率/ }).waitFor();
  assert.ok((await page.locator('svg[role="img"] circle').count()) > 0);
  assert.equal(sampled.summary.in.averageRate, 1000);
  await page.locator("#traffic-metric").selectOption("unicast");
  const packets = await refreshQuery();
  assert.equal(packets.rateUnit, "packets/s");
  assert.equal(packets.summary.in.total, "250");
  assert.equal(packets.summary.out.total, "0");
  await page
    .getByTestId("traffic-in-total")
    .filter({ hasText: "250 包" })
    .waitFor();
  await page
    .getByTestId("traffic-out-total")
    .filter({ hasText: "0 包" })
    .waitFor();
  assert.match(await page.locator("main").innerText(), /125 包\/秒/);
  console.log(
    "Traffic real bound ingest → exact counter delta → chart/table and packets/s; observed zero stays zero passed",
  );

  await page.getByTestId("traffic-in-total").scrollIntoViewIfNeeded();
  await page.evaluate(() => window.scrollBy(0, -150));
  await page.screenshot({
    path: "/tmp/moedove-traffic-desktop.png",
    fullPage: false,
  });
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
  await page.screenshot({
    path: "/tmp/moedove-traffic-mobile-form.png",
    fullPage: false,
  });
  await page.getByTestId("traffic-in-total").scrollIntoViewIfNeeded();
  await page.screenshot({
    path: "/tmp/moedove-traffic-mobile.png",
    fullPage: false,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });

  // An unobserved peer port makes aggregate coverage partial; never fabricate a group rate.
  await api("/resources/ports", {
    name: "Archive missing counter",
    switchId: sw.id,
    status: "active",
  });
  const refreshTargets = async () => {
    const pending = page.waitForResponse(
      (response) => new URL(response.url()).pathname === "/api/traffic/query",
    );
    await page
      .getByRole("button", { name: "刷新可选对象", exact: true })
      .click();
    assert.equal((await pending).status(), 200);
    return await api("/traffic/catalog");
  };
  const selectTarget = async (catalog, scope, id) => {
    const target = catalog.targets.find(
      (t) => t.scope === scope && t.id === id,
    );
    assert.ok(target);
    await page
      .locator("#traffic-target")
      .selectOption(target.key || `${scope}:${id}`);
    return target;
  };
  const updatedCatalog = await refreshTargets();
  await selectTarget(updatedCatalog, "switch", sw.id);
  await page.locator("#traffic-metric").selectOption("octets");
  const partial = await refreshQuery();
  assert.equal(partial.summary.in.total, "250");
  assert.equal(partial.summary.in.averageRate, null);
  assert.equal(partial.summary.expectedPorts, 2);
  assert.equal(partial.summary.partial, true);
  await page
    .getByRole("heading", { name: "所选范围没有有效速率区间", exact: true })
    .waitFor();
  assert.match(await page.locator("main").innerText(), /完整覆盖桶平均速率/);
  for (const resolution of ["1h", "1d"]) {
    await page.locator("#traffic-resolution").selectOption(resolution);
    const result = await refreshQuery();
    assert.equal(result.resolution, resolution);
    assert.equal(result.summary.in.total, "250");
  }
  console.log(
    "Traffic partial aggregate preserves totals with null rates; 5m/hour/day queries passed",
  );

  // Core sides are explicit, authorized targets, never added together by the UI.
  const remote = await api("/resources/switches", {
    name: "Archive core remote",
    locationId: location.id,
  });
  const ca = await api("/resources/ports", {
    name: "Archive core A",
    switchId: sw.id,
  });
  const cb = await api("/resources/ports", {
    name: "Archive core B",
    switchId: remote.id,
  });
  const plan = await api("/core/preview", {
    bundle: {
      name: "Archive core target",
      type: "l2-lag",
      enabled: true,
      sideA: { switchId: sw.id, interfaceName: "core-a", channelGroup: 0 },
      sideB: { switchId: remote.id, interfaceName: "core-b", channelGroup: 0 },
      links: [
        {
          enabled: true,
          sideA: { portId: ca.id, duplex: "full", autoneg: true },
          sideB: { portId: cb.id, duplex: "full", autoneg: true },
        },
      ],
    },
  });
  const inventory = await api("/core/inventory");
  const bundle = await api("/core/bundles", {
    bundle: plan.bundle,
    portEtags: Object.fromEntries(
      inventory.ports
        .filter((p) => [ca.id, cb.id].includes(p.id))
        .map((p) => [p.id, p._etag]),
    ),
  });
  const coreCatalog = await refreshTargets();
  await selectTarget(coreCatalog, "core-bundle", bundle.id);
  await page.locator("#traffic-side").selectOption("b");
  const coreResult = await refreshQuery();
  assert.equal(coreResult.coreSide || coreResult.side, "b");
  assert.equal(coreResult.summary.in.total, null);
  console.log("Traffic core target requires distinct A/B side passed");

  const memberB = await api("/resources/members", {
    name: "Archive member B",
    asn: 64511,
    status: "active",
  });
  for (const [email, memberId, role] of [
    ["archive-a@example.test", member.id, "member"],
    ["archive-b@example.test", memberB.id, "member"],
    ["archive-viewer@example.test", undefined, "viewer"],
  ])
    await api("/resources/users", {
      name: email,
      email,
      password,
      role,
      ...(memberId ? { memberId } : {}),
    });
  const loginContext = async (email) => {
    const ctx = await browser.newContext();
    const response = await ctx.request.post(base + "/api/login", {
      data: { email, password },
    });
    assert.equal(response.status(), 200);
    return ctx;
  };
  const memberContext = await loginContext("archive-a@example.test"),
    memberPage = await memberContext.newPage();
  const memberResources = [];
  memberPage.on("request", (req) => {
    if (new URL(req.url()).pathname.startsWith("/api/resources/"))
      memberResources.push(req.url());
  });
  const ownCatalog = (
    await call("/traffic/catalog", undefined, "GET", undefined, memberContext)
  ).data;
  assert.ok(
    ownCatalog.targets.every(
      (target) =>
        ["member", "port"].includes(target.scope) ||
        (target.scope === "connection" && target.id === connection.id),
    ),
  );
  assert.ok(!ownCatalog.targets.some((target) => target.id === memberB.id));
  await memberPage.goto(base + "/#traffic");
  await memberPage
    .getByTestId("traffic-in-total")
    .filter({ hasText: "250 B" })
    .waitFor();
  assert.deepEqual(memberResources, []);
  const oldConnection = await api(`/resources/connections/${connection.id}`);
  await api(
    `/resources/connections/${connection.id}`,
    {
      name: "Reassigned archive connection",
      memberId: memberB.id,
      vlanId: vlan.id,
      portId: physical.id,
      ipv4: "192.0.2.2",
      status: "active",
    },
    "PUT",
    oldConnection._etag,
  );
  const afterTransfer = (
    await call("/traffic/catalog", undefined, "GET", undefined, memberContext)
  ).data;
  assert.ok(
    afterTransfer.targets.every(
      (target) =>
        (target.scope === "member" && target.id === member.id) ||
        (target.scope === "connection" &&
          target.id === connection.id &&
          !target.label.includes("Reassigned archive connection")),
    ),
  );
  const ownTarget = afterTransfer.targets.find(
    (target) => target.scope === "member" && target.id === member.id,
  );
  assert.ok(ownTarget);
  const historicalQuery = new URLSearchParams({
    scope: "member",
    id: member.id,
    memberRevision: ownTarget.memberRevision,
    metric: "octets",
    resolution: "5m",
  });
  const oldHistory = (
    await call(
      `/traffic/query?${historicalQuery}`,
      undefined,
      "GET",
      undefined,
      memberContext,
    )
  ).data;
  assert.equal(oldHistory.summary.in.total, "250");
  assert.equal(
    (
      await call(
        `/traffic/query?scope=port&id=${physical.id}`,
        undefined,
        "GET",
        undefined,
        memberContext,
      )
    ).status,
    404,
  );
  assert.equal(
    (
      await call(
        `/traffic/query?scope=member&id=${memberB.id}`,
        undefined,
        "GET",
        undefined,
        memberContext,
      )
    ).status,
    404,
  );
  await memberPage
    .getByRole("button", { name: "刷新可选对象", exact: true })
    .click();
  await memberPage
    .getByTestId("traffic-in-total")
    .filter({ hasText: "250 B" })
    .waitFor();
  assert.equal(await memberPage.locator("#traffic-target option").count(), 2);
  assert.ok(
    !(await memberPage.locator("#traffic-target").innerText()).includes(
      "Reassigned archive connection",
    ),
  );
  const memberBContext = await loginContext("archive-b@example.test");
  const newPortHistory = await call(
    `/traffic/query?scope=port&id=${physical.id}&metric=octets&resolution=5m`,
    undefined,
    "GET",
    undefined,
    memberBContext,
  );
  assert.equal(newPortHistory.status, 200);
  assert.equal(newPortHistory.data.summary.in.total, null);
  const newConnectionHistory = await call(
    `/traffic/query?scope=connection&id=${connection.id}&metric=octets&resolution=5m`,
    undefined,
    "GET",
    undefined,
    memberBContext,
  );
  assert.equal(newConnectionHistory.status, 200);
  assert.equal(newConnectionHistory.data.summary.in.total, null);
  const scoped = (
    await call(
      "/account/tokens",
      {
        name: "Historical read",
        currentPassword: password,
        scopes: ["traffic:read"],
      },
      "POST",
      undefined,
      memberContext,
    )
  ).data;
  const tokenRead = await fetch(
    base + `/api/traffic/query?${historicalQuery}`,
    { headers: { Authorization: `Bearer ${scoped.token}` } },
  );
  assert.equal(tokenRead.status, 200);
  assert.equal((await tokenRead.json()).summary.in.total, "250");
  assert.equal(
    (
      await fetch(base + "/api/resources/ports", {
        headers: { Authorization: `Bearer ${scoped.token}` },
      })
    ).status,
    403,
  );
  const viewerContext = await loginContext("archive-viewer@example.test");
  assert.equal(
    (await call("/traffic/catalog", undefined, "GET", undefined, viewerContext))
      .status,
    200,
  );
  console.log(
    "Traffic member history survives reassignment; former current-port access denied; new owner cannot read past tenant; read token and viewer passed",
  );

  await page.locator("#traffic-resolution").selectOption("5m");
  await selectTarget(coreCatalog, "port", physical.id);
  await refreshQuery();
  await page.route(
    "**/api/traffic/query?*",
    (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "归档暂不可用（本地故障测试）" }),
      }),
    { times: 1 },
  );
  await page.getByRole("button", { name: "查询归档", exact: true }).click();
  await page.getByRole("alert").filter({ hasText: "归档暂不可用" }).waitFor();
  assert.equal(await page.getByTestId("traffic-in-total").count(), 0);
  assert.equal(
    await page.getByRole("img", { name: /物理计数器.*平均速率/ }).count(),
    0,
  );
  await refreshQuery();
  await page.getByTestId("traffic-in-total").waitFor();
  console.log("Traffic query error hides obsolete report and recovery passed");
  await memberContext.close();
  await memberBContext.close();
  await viewerContext.close();
  assert.deepEqual(errors, []);
  console.log("Traffic 390px layout and page errors passed");
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
  console.log("Traffic isolated browser and manager stopped");
}
