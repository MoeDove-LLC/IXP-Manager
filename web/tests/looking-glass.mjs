// The runner submits typed local fixtures through real leases. No router is queried.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-lg-browser-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "LG-fixture-password-123",
  child = spawn(
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
      env: { ...process.env, IXP_SETUP_TOKEN: "lg-fixture-token" },
      stdio: ["ignore", "ignore", "pipe"],
    },
  );
let browser;
try {
  for (let attempt = 0; ; attempt++) {
    if (child.exitCode !== null) throw new Error("LG fixture manager exited");
    if (
      await fetch(base + "/api/setup")
        .then((response) => response.ok)
        .catch(() => false)
    )
      break;
    if (attempt > 100) throw new Error("LG fixture startup timeout");
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
  const call = async (
    path,
    body,
    method = body ? "POST" : "GET",
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: { "X-CSRF-Token": session.csrfToken || "" },
      ...(body ? { data: body } : {}),
    });
    return { status: response.status(), data: await response.json() };
  };
  const api = async (...args) => {
    const response = await call(...args);
    assert.ok(
      response.status < 300,
      `${args[0]}: ${response.status} ${response.data.error || ""}`,
    );
    return response.data;
  };
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "lg-fixture-token",
          name: "LG QA",
          email: "admin@example.test",
          password,
          ixpName: "Looking Glass Test",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const a = await api("/resources/members", {
      name: "LG Network A",
      asn: 64501,
      status: "active",
    }),
    b = await api("/resources/members", {
      name: "LG Network B",
      asn: 64502,
      status: "active",
    }),
    dc = await api("/resources/locations", { name: "LG DC" }),
    sw = await api("/resources/switches", {
      name: "LG switch",
      locationId: dc.id,
    }),
    vlan = await api("/resources/vlans", {
      name: "LG LAN",
      tag: 100,
      ipv4Prefix: "192.0.2.0/24",
    }),
    router = await api("/resources/routers", {
      name: "LG route server",
      platform: "bird2",
      asn: 64500,
      routerId: "192.0.2.1",
      localIpv4: "192.0.2.1",
      vlanId: vlan.id,
    });
  const peers = [];
  for (const [index, member] of [a, b].entries()) {
    const p = await api("/resources/ports", {
        name: `LG port ${index}`,
        switchId: sw.id,
        status: "active",
        speed: 1000,
      }),
      address = `192.0.2.${index ? 20 : 10}`;
    await api("/resources/connections", {
      name: `LG link ${index}`,
      memberId: member.id,
      portId: p.id,
      vlanId: vlan.id,
      ipv4: address,
      status: "active",
    });
    peers.push(
      await api("/resources/peers", {
        name: `LG peer ${index ? "B" : "A"}`,
        routerId: router.id,
        memberId: member.id,
        address,
        asn: member.asn,
        enabled: true,
        maxPrefixes: 100,
        allowedPrefixes: ["198.51.100.0/24", "203.0.113.0/24"],
      }),
    );
  }
  await api("/resources/users", {
    name: "LG member",
    email: "member@example.test",
    password,
    role: "member",
    memberId: a.id,
    memberIds: [a.id, b.id],
  });
  await page.goto(base + "/#security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  let dialog = page.getByRole("dialog");
  await dialog.getByLabel("令牌名称").fill("Local LG fixture worker");
  await dialog
    .getByRole("checkbox", {
      name: "采集器执行指定路由器的固定查询",
      exact: true,
    })
    .check();
  await dialog
    .getByRole("checkbox", { name: "LG route server", exact: true })
    .check();
  await dialog.getByLabel("当前密码", { exact: true }).fill(password);
  const issuedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/account/tokens") &&
      response.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "继续", exact: true }).click();
  const issued = await (await issuedResponse).json();
  assert.deepEqual(issued.item.routerIds, [router.id]);
  const worker = async (path, body) => {
    const response = await fetch(base + "/api/looking-glass/" + path, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${issued.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    });
    const data = await response.json();
    assert.ok(response.ok, `${path}: ${response.status} ${data.error || ""}`);
    return data;
  };
  const submit = async (peer, view, prefix) => {
    await page.goto(base + "/#looking-glass");
    await page
      .getByLabel("可访问的 BGP 会话", { exact: true })
      .selectOption(peer.id);
    await page.getByLabel("查询视图", { exact: true }).selectOption(view);
    await page.getByLabel("前缀（可选）", { exact: true }).fill(prefix);
    await page.getByLabel("最多返回行数", { exact: true }).fill("1");
    const response = page.waitForResponse(
      (value) =>
        new URL(value.url()).pathname === "/api/looking-glass/queries" &&
        value.request().method() === "POST",
    );
    await page.getByRole("button", { name: "发起查询", exact: true }).click();
    const result = await response;
    assert.ok(result.status() === 200 || result.status() === 202);
    return result.json();
  };
  const complete = async (id, { old = false, unsupported = false } = {}) => {
    const { lease } = await worker("claim", { routerId: router.id });
    assert.equal(lease.id, id);
    const result = {
      query: lease.query,
      observedAt: new Date(Date.now() - (old ? 25000 : 0)).toISOString(),
      status: unsupported ? "unsupported" : "truncated",
      complete: false,
      truncated: !unsupported,
      identity: {
        address: lease.query.neighborAddress,
        asn: lease.query.neighborAsn,
        state: "Established",
      },
      routes: unsupported
        ? []
        : [
            {
              prefix: lease.query.prefix || "198.51.100.0/24",
              nextHops: [lease.query.neighborAddress],
              asPath: [
                { type: "sequence", asns: [lease.query.neighborAsn] },
                { type: "set", asns: [64503, 64504] },
              ],
              communities: null,
              largeCommunities: null,
              unavailableAttributes: ["communities", "largeCommunities"],
            },
          ],
      ...(unsupported ? { errorCode: "unsupported-view" } : {}),
    };
    await worker("complete", {
      leaseId: lease.id,
      leaseToken: lease.token,
      result,
    });
  };
  const first = await submit(peers[0], "pre-policy", "198.51.100.0/24");
  await page.getByText("等待对应采集器处理。", { exact: true }).waitFor();
  await complete(first.item.id);
  await page
    .getByRole("cell", { name: "198.51.100.0/24", exact: true })
    .waitFor();
  await page.getByText("过滤状态未知", { exact: true }).waitFor();
  assert.ok(
    (await page.locator("main").innerText()).includes("{64503, 64504}"),
  );
  await page.getByText("结果已截断", { exact: false }).waitFor();
  const cached = await submit(peers[0], "pre-policy", "198.51.100.0/24");
  assert.equal(cached.cached, true);
  assert.equal(cached.item.id, first.item.id);
  await page.getByText("服务端返回已有查询记录", { exact: false }).waitFor();
  await page
    .getByRole("button", { name: "查看路由 198.51.100.0/24", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  assert.equal(
    await dialog.getByText("此原生视图未提供", { exact: true }).count(),
    2,
  );
  assert.ok(!(await dialog.innerText()).includes("返回 0 项"));
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  console.log(
    "real queue/lease, cached response, AS_SET, unknown filter and missing communities passed",
  );
  const stale = await submit(peers[0], "accepted", "203.0.113.0/24");
  await complete(stale.item.id, { old: true });
  await page
    .getByRole("cell", { name: "203.0.113.0/24", exact: true })
    .waitFor();
  await page
    .getByText("采样已过期", { exact: true })
    .waitFor({ timeout: 15000 });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "/tmp/moedove-looking-glass-desktop.png",
    fullPage: true,
  });
  console.log(
    "sample becomes stale by actual elapsed time without treating task completion as live health",
  );
  const unsupported = await submit(peers[0], "filtered", "198.51.100.0/24");
  await complete(unsupported.item.id, { unsupported: true });
  await page.getByText("不支持此视图。", { exact: false }).waitFor();
  assert.equal(
    (await call(`/looking-glass/queries/${unsupported.item.id}/result`)).status,
    409,
  );
  const cancelled = await submit(peers[0], "exported", "198.51.100.0/24");
  await page.getByRole("button", { name: "取消查询", exact: true }).click();
  await page.getByText("已取消", { exact: true }).first().waitFor();
  assert.equal(
    (await api(`/looking-glass/queries/${cancelled.item.id}`)).status,
    "cancelled",
  );
  const foreign = await submit(peers[1], "accepted", "198.51.100.0/24");
  await complete(foreign.item.id);
  await page
    .getByRole("cell", { name: "198.51.100.0/24", exact: true })
    .waitFor();
  const memberContext = await browser.newContext();
  assert.equal(
    (
      await memberContext.request.post(base + "/api/login", {
        data: { email: "member@example.test", password },
      })
    ).status(),
    200,
  );
  const memberPage = await memberContext.newPage();
  memberPage.on("pageerror", (error) => errors.push(error.message));
  await memberPage.goto(base + "/#looking-glass");
  await memberPage.getByLabel("可访问的 BGP 会话", { exact: true }).waitFor();
  assert.equal(
    await memberPage
      .getByRole("radio", { name: "按路由器表", exact: true })
      .count(),
    0,
  );
  assert.ok(
    !(await memberPage.locator("#lg-target").innerText()).includes("LG peer B"),
  );
  assert.ok(
    [403, 404].includes(
      (
        await call(
          `/looking-glass/queries/${foreign.item.id}`,
          undefined,
          "GET",
          memberContext,
        )
      ).status,
    ),
  );
  const deniedTable = await call(
    "/looking-glass/queries",
    { routerId: router.id, family: 4, view: "table" },
    "POST",
    memberContext,
  );
  assert.ok([400, 403, 404].includes(deniedTable.status));
  assert.ok(deniedTable.data.error && !deniedTable.data.item);
  assert.equal(
    (await call("/atlas", undefined, "GET", memberContext)).status,
    403,
  );
  await memberPage
    .getByLabel("当前成员网络", { exact: true })
    .selectOption(b.id);
  await memberPage.waitForFunction(() =>
    document.querySelector("#lg-target")?.textContent.includes("LG peer B"),
  );
  assert.ok(
    !(await memberPage.locator("#lg-target").innerText()).includes("LG peer A"),
  );
  assert.ok(
    [403, 404].includes(
      (
        await call(
          `/looking-glass/queries/${first.item.id}/result`,
          undefined,
          "GET",
          memberContext,
        )
      ).status,
    ),
  );
  console.log(
    "member peer/table boundaries and tenant switch discard prior-member access passed",
  );
  await page.locator(`[data-query-id="${first.item.id}"]`).click();
  await page.getByRole("button", { name: "清理结果文件", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "确认清理", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  await page.getByText("此查询的结果文件已清理", { exact: false }).waitFor();
  assert.equal(
    (await call(`/looking-glass/queries/${first.item.id}/result`)).status,
    410,
  );
  await page.locator(`[data-query-id="${stale.item.id}"]`).click();
  await page.getByText("采样已过期", { exact: true }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-looking-glass-mobile.png",
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  console.log("LG retention, unavailable results and 390px layout passed");
  console.log(`LG fixture: ${dir}`);
} finally {
  await browser?.close();
  if (child.exitCode === null) {
    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(timer);
  }
}
