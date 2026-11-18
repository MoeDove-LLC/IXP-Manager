// Isolated MAC FDB review UI. Typed local SNMP fixtures, no device network polling.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-mac-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "SFlow-browser-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-mac-ui-server",
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
  const location = await api("/resources/locations", {
    name: "MAC learning DC",
  });
  const sw = await api("/resources/switches", {
    name: "FDB switch",
    locationId: location.id,
  });
  const vlan = await api("/resources/vlans", {
    name: "FDB peering",
    tag: 100,
    ipv4Prefix: "192.0.2.0/24",
  });
  const members = [],
    ports = [],
    connections = [];
  for (let i = 0; i < 2; i++) {
    const member = await api("/resources/members", {
      name: `FDB member ${i + 1}`,
      asn: 64510 + i,
      status: "active",
    });
    const port = await api("/resources/ports", {
      name: `Ethernet${i + 1}`,
      switchId: sw.id,
      status: "active",
      speed: 1000,
    });
    const connection = await api("/resources/connections", {
      name: `FDB connection ${i + 1}`,
      memberId: member.id,
      portId: port.id,
      vlanId: vlan.id,
      ipv4: `192.0.2.${10 + i}`,
      status: "active",
    });
    members.push(member);
    ports.push(port);
    connections.push(connection);
  }
  const conflict = await api("/resources/mac-addresses", {
    name: "Already registered member2",
    connectionId: connections[1].id,
    address: "02:00:00:00:00:02",
  });
  const token = await api("/account/tokens", {
    name: "FDB fixture collector",
    scopes: ["devices:read", "devices:write"],
    switchIds: [sw.id],
    currentPassword: password,
  });
  const agent = async (path, data) => {
    const response = await fetch(base + "/api" + path, {
      method: data ? "POST" : "GET",
      headers: {
        Authorization: `Bearer ${token.token}`,
        ...(data ? { "Content-Type": "application/json" } : {}),
      },
      ...(data ? { body: JSON.stringify(data) } : {}),
    });
    const result = await response.json();
    assert.ok(
      response.ok,
      `${path}: ${response.status} ${JSON.stringify(result)}`,
    );
    return result;
  };
  const fdb = [
    {
      fdbId: 1,
      vlanIds: [100],
      mac: "02:00:00:00:00:01",
      bridgePort: 1,
      ifIndex: 1,
      status: "learned",
    },
    {
      fdbId: 1,
      vlanIds: [100],
      mac: "02:00:00:00:00:03",
      bridgePort: 1,
      ifIndex: 1,
      status: "learned",
    },
    {
      fdbId: 1,
      vlanIds: [100],
      mac: "02:00:00:00:00:02",
      bridgePort: 1,
      ifIndex: 1,
      status: "learned",
    },
    {
      fdbId: 1,
      vlanIds: [100, 101],
      mac: "02:00:00:00:00:04",
      bridgePort: 1,
      ifIndex: 1,
      status: "learned",
    },
    {
      fdbId: 1,
      vlanIds: [],
      mac: "02:00:00:00:00:09",
      bridgePort: 99,
      ifIndex: 0,
      status: "learned",
    },
  ];
  // Keep the deep-linked target beyond the first page without enumerating it in the UI.
  for (let i = 0; i < 101; i++)
    fdb.push({
      fdbId: 1,
      vlanIds: [100],
      mac: `02:00:00:01:${Math.floor(i / 256)
        .toString(16)
        .padStart(2, "0")}:${(i % 256).toString(16).padStart(2, "0")}`,
      bridgePort: 1,
      ifIndex: 1,
      status: "learned",
    });
  let uptime = 10000;
  const snapshot = () => ({
    version: 1,
    switchId: sw.id,
    observedAt: new Date().toISOString(),
    security: "authPriv",
    engineIdentity: "a".repeat(64),
    engineEpoch: "fixture",
    identity: { name: "FDB switch" },
    uptimeTicks: String((uptime += 100)),
    interfaces: ports.map((p, i) => ({
      ifIndex: i + 1,
      name: p.name,
      type: 6,
      mtu: 1500,
      speedMbps: 1000,
      adminStatus: "up",
      operStatus: "up",
      physicalAddress: `02:aa:00:00:00:0${i + 1}`,
      inOctets: "1000",
      outOctets: "2000",
      counterBits: 64,
      discontinuityTicks: "0",
      lastChangeTicks: "0",
    })),
    entities: [],
    sensors: [],
    fdb,
    warnings: [],
  });
  const discovery = await agent("/devices/discoveries", snapshot());
  const preview = await api(`/devices/discoveries/${discovery.id}/preview`);
  await api(`/devices/discoveries/${discovery.id}/apply`, {
    baseChecksum: preview.baseChecksum,
    ports: ports.map((p, i) => ({ ifIndex: i + 1, portId: p.id })),
  });
  let lastObservation = 0;
  const observe = async () => {
    const delay = Math.max(0, lastObservation + 1100 - Date.now());
    if (delay) await new Promise((resolve) => setTimeout(resolve, delay));
    const binding = await agent(`/devices/bindings?switchId=${sw.id}`),
      data = snapshot();
    data.interfaces = data.interfaces.map((i) => {
      const b = binding.mappings.find((m) => m.ifIndex === i.ifIndex);
      return { ...i, portId: b.portId, ownershipRevision: b.ownershipRevision };
    });
    lastObservation = Date.parse(data.observedAt);
    return agent("/devices/observations", data);
  };
  await observe();
  assert.equal(
    (await api("/resources/mac-addresses")).items.length,
    1,
    "FDB observation automatically changed registered MACs",
  );
  const catalog = await api(`/devices/mac-learning?switchId=${sw.id}`);
  const candidate = catalog.items.find((r) => r.mac === "02:00:00:00:00:01");
  assert.equal(candidate.registrationStatus, "candidate");
  const deepQuery = new URLSearchParams({
    tab: "mac",
    switchId: sw.id,
    candidateId: candidate.id,
  });
  await page.goto(base + "/#ipam?" + deepQuery);
  await page
    .getByRole("heading", { name: "MAC 登记与学习对照", exact: true })
    .waitFor();
  const card = page
    .getByTestId("learned-mac")
    .filter({ hasText: "02:00:00:00:00:01" });
  await card.waitFor();
  assert.equal(await card.getAttribute("data-located"), "true");
  assert.equal(await page.locator("#mac-learning-switch").inputValue(), sw.id);
  for (const mac of [
    "02:00:00:00:00:02",
    "02:00:00:00:00:04",
    "02:00:00:00:00:09",
  ])
    assert.equal(
      await page
        .getByTestId("learned-mac")
        .filter({ hasText: mac })
        .getByRole("button", { name: "审阅并登记", exact: true })
        .count(),
      0,
    );
  await card.getByRole("button", { name: "审阅并登记", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByText("FDB member 1", { exact: true })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "确认登记 MAC", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(
    await page.getByRole("dialog").locator("select").count(),
    0,
    "ambiguous ownership must not be resolved by hand-selection",
  );
  await page
    .locator("#mac-registration-name")
    .fill("Reviewed FDB registration");
  await page.locator("#mac-registration-ack").check();
  const applied = page.waitForResponse((r) =>
    r.url().endsWith("/devices/mac-learning/apply"),
  );
  await page.getByRole("button", { name: "确认登记 MAC", exact: true }).click();
  const appliedResponse = await applied,
    result = await appliedResponse.json();
  assert.equal(appliedResponse.status(), 201, JSON.stringify(result));
  assert.equal(result.registered.connectionId, connections[0].id);
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByTestId("registered-mac")
    .filter({ hasText: "Reviewed FDB registration" })
    .waitFor();
  assert.equal(
    (await api(`/resources/mac-addresses/${conflict.id}`)).connectionId,
    connections[1].id,
  );
  console.log(
    "MAC actual mapped SNMP/FDB -> deep-linked candidate -> explicit reviewed registration; conflict/ambiguous/unmapped never auto-adopted passed",
  );
  await observe();
  await page
    .getByRole("button", { name: "刷新 MAC 证据", exact: true })
    .click();
  const nextCard = page
    .getByTestId("learned-mac")
    .filter({ hasText: "02:00:00:00:00:03" });
  await nextCard
    .getByRole("button", { name: "审阅并登记", exact: true })
    .click();
  await page.locator("#mac-registration-name").fill("PRESERVED MAC DRAFT");
  await observe();
  await page.locator("#mac-registration-ack").check();
  const stale = page.waitForResponse((r) =>
    r.url().endsWith("/devices/mac-learning/apply"),
  );
  await page.getByRole("button", { name: "确认登记 MAC", exact: true }).click();
  assert.equal((await stale).status(), 409);
  assert.equal(
    await page.locator("#mac-registration-name").inputValue(),
    "PRESERVED MAC DRAFT",
  );
  await page
    .getByRole("button", { name: "保留名称并刷新证据", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await nextCard
    .getByRole("button", { name: "审阅并登记", exact: true })
    .click();
  assert.equal(
    await page.locator("#mac-registration-name").inputValue(),
    "PRESERVED MAC DRAFT",
  );
  assert.equal(await page.locator("#mac-registration-ack").isChecked(), false);
  await page.locator("#mac-registration-ack").check();
  await page.getByRole("button", { name: "确认登记 MAC", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByTestId("registered-mac")
    .filter({ hasText: "PRESERVED MAC DRAFT" })
    .waitFor();
  console.log(
    "MAC stale observation409 retains name, fresh evidence selection and new explicit acknowledgement passed",
  );
  const later = await api(
    `/devices/mac-learning?switchId=${sw.id}&offset=100&limit=100`,
  );
  const laterTarget = later.items[later.items.length - 1];
  await page.goto(
    base +
      "/#ipam?" +
      new URLSearchParams({
        tab: "mac",
        switchId: sw.id,
        candidateId: laterTarget.id,
      }),
  );
  const located = page
    .getByTestId("learned-mac")
    .filter({ hasText: laterTarget.mac });
  await located.waitFor();
  assert.equal(await located.getAttribute("data-located"), "true");
  assert.ok((await page.getByTestId("learned-mac").count()) < 100);
  await api("/resources/users", {
    name: "Read-only FDB",
    email: "mac-viewer@example.test",
    password,
    role: "viewer",
  });
  const viewerContext = await browser.newContext();
  assert.equal(
    (
      await viewerContext.request.post(base + "/api/login", {
        data: { email: "mac-viewer@example.test", password },
      })
    ).status(),
    200,
  );
  const viewerPage = await viewerContext.newPage();
  viewerPage.on("pageerror", (e) => errors.push(e.message));
  await viewerPage.goto(
    base +
      "/#ipam?" +
      new URLSearchParams({
        tab: "mac",
        switchId: sw.id,
        candidateId: laterTarget.id,
      }),
  );
  await viewerPage
    .getByTestId("learned-mac")
    .filter({ hasText: laterTarget.mac })
    .waitFor();
  assert.equal(
    await viewerPage
      .getByRole("button", { name: "审阅并登记", exact: true })
      .count(),
    0,
  );
  await viewerContext.close();
  console.log(
    "MAC candidate deep-link selects its actual page; viewer cannot review/apply passed",
  );
  const openSearchTarget = async () => {
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    const pending = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/search" &&
        new URL(r.url()).searchParams.get("q") === laterTarget.mac,
    );
    await page.locator("#global-search-query").fill(laterTarget.mac);
    assert.equal((await pending).status(), 200);
    await page
      .locator(
        `[data-search-kind="learned-macs"][data-search-id="${laterTarget.id}"]`,
      )
      .click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page
      .getByTestId("learned-mac")
      .filter({ hasText: laterTarget.mac })
      .waitFor();
  };
  await openSearchTarget();
  const searchHash = await page.evaluate(() => location.hash);
  const previous = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === "/api/devices/mac-learning" &&
      new URL(r.url()).searchParams.get("offset") === "0",
  );
  await page.getByRole("button", { name: "上一页", exact: true }).click();
  assert.equal((await previous).status(), 200);
  await page
    .getByTestId("learned-mac")
    .filter({ hasText: "02:00:00:00:00:01" })
    .waitFor();
  assert.equal(
    await page
      .getByTestId("learned-mac")
      .filter({ hasText: laterTarget.mac })
      .count(),
    0,
  );
  await openSearchTarget();
  assert.equal(await page.evaluate(() => location.hash), searchHash);
  assert.equal(
    await page
      .getByTestId("learned-mac")
      .filter({ hasText: laterTarget.mac })
      .getAttribute("data-located"),
    "true",
  );
  assert.ok((await page.getByTestId("learned-mac").count()) < 100);
  console.log(
    "global learned-MAC result and repeated same-hash navigation restore the precise later FDB page passed",
  );
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
    path: "/tmp/moedove-mac-mobile.png",
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  console.log("MAC390px and no page errors passed");
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
  console.log("MAC isolated browser and manager stopped");
}
