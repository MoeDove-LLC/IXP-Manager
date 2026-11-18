// Isolated Core workspace smoke; no device configuration is applied.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-core-workspace-"),
  socket = net.createServer();
await new Promise((r) => socket.listen(0, "127.0.0.1", r));
const port = socket.address().port;
await new Promise((r) => socket.close(r));
const base = `http://127.0.0.1:${port}`,
  password = "Core-workspace-fixture-password-123";
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
    env: { ...process.env, IXP_SETUP_TOKEN: "core-workspace-bootstrap" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser;
try {
  for (let i = 0; ; i++) {
    if (child.exitCode !== null) throw new Error("Core fixture exited");
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (i > 100) throw new Error("Core fixture startup timeout");
    await new Promise((r) => setTimeout(r, 100));
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
  page.on("pageerror", (e) => errors.push(e.message));
  const call = async (
    path,
    body,
    method = body ? "POST" : "GET",
    etag,
    ctx = context,
  ) => {
    const s = await (await ctx.request.get(base + "/api/session")).json();
    const r = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: {
        "X-CSRF-Token": s.csrfToken || "",
        ...(etag ? { "If-Match": JSON.stringify(etag) } : {}),
      },
      ...(body ? { data: body } : {}),
    });
    return { status: r.status(), data: await r.json() };
  };
  const api = async (...args) => {
    const r = await call(...args);
    assert.ok(r.status < 300, `${args[0]} ${r.status}: ${r.data.error || ""}`);
    return r.data;
  };
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "core-workspace-bootstrap",
          name: "Core QA",
          email: "admin@example.test",
          password,
          ixpName: "Core Workspace Test",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const dc = await api("/resources/locations", { name: "Core DC" }),
    a = await api("/resources/switches", {
      name: "Core A",
      locationId: dc.id,
      asn: 64510,
      loopbackIpv4: "10.255.0.1",
      loopbackName: "lo0",
    }),
    b = await api("/resources/switches", {
      name: "Core B",
      locationId: dc.id,
      asn: 64520,
      loopbackIpv4: "10.255.0.2",
      loopbackName: "lo0",
    }),
    ports = [];
  for (const [sw, prefix] of [
    [a, "A"],
    [b, "B"],
  ])
    for (let i = 1; i <= 3; i++)
      ports.push(
        await api("/resources/ports", {
          name: `${prefix}-${i}`,
          switchId: sw.id,
          status: "active",
          ...(i < 3 ? { speed: 10000 } : {}),
        }),
      );
  const pa = ports.filter((p) => p.switchId === a.id),
    pb = ports.filter((p) => p.switchId === b.id);
  const go = async () => {
    await page.goto(base + "/#core");
    await page.reload();
    await page
      .getByRole("heading", { name: "核心互联", exact: true })
      .waitFor();
  };
  const next = () =>
    page
      .getByRole("dialog")
      .getByRole("button", { name: "下一步", exact: true })
      .click();
  const preview = async () => {
    const r = page.waitForResponse((r) =>
      r.url().endsWith("/api/core/preview"),
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "检查并生成预览", exact: true })
      .click();
    const response = await r;
    const result = await response.json();
    assert.equal(response.status(), 200, JSON.stringify(result));
    await page
      .getByRole("checkbox", { name: /我已核对双端、所有链路/ })
      .waitFor();
    return result;
  };
  const commit = async (method) => {
    const r = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname.startsWith("/api/core/bundles") &&
        r.request().method() === method,
    );
    await page
      .getByRole("checkbox", { name: /我已核对双端、所有链路/ })
      .check();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "保存这份库存方案", exact: true })
      .click();
    const response = await r;
    const result = await response.json();
    assert.ok(response.status() < 300, JSON.stringify(result));
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    return result;
  };
  await go();
  await page
    .getByRole("button", { name: "新增核心 Bundle", exact: true })
    .first()
    .click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#core-name").fill("ECMP smoke");
  await dialog.locator("#core-type").selectOption("ecmp");
  await dialog.locator("#core-sideA-switch").selectOption(a.id);
  await dialog.locator("#core-sideB-switch").selectOption(b.id);
  await dialog.locator("#core-sideA-mtu").fill("9000");
  await dialog.locator("#core-sideB-mtu").fill("9000");
  await next();
  await dialog.locator("#core-link-0-sideA").selectOption(pa[0].id);
  await dialog.locator("#core-link-0-sideB").selectOption(pb[0].id);
  await next();
  await dialog.locator("#core-link-ipv4-0").fill("10.77.0.0/31");
  await dialog.locator("#core-link-ipv6-0").fill("2001:db8:77::/127");
  const plan = await preview();
  assert.ok(plan.bundle.id && plan.bundle.links[0].sideA.id);
  assert.equal(plan.derived.addresses[0].ipv4.sideA, "10.77.0.0");
  let bundle = await commit("POST");
  assert.equal(bundle.type, "ecmp");
  assert.equal((await api("/core")).items.length, 1);
  console.log("Core create/read/derived dual-stack preview passed");
  await page.getByRole("button", { name: "编辑完整方案", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#core-name").fill("Reviewed ECMP");
  await next();
  await next();
  await preview();
  bundle = await commit("PUT");
  assert.equal(bundle.name, "Reviewed ECMP");
  assert.equal((await api(`/core/bundles/${bundle.id}`)).name, "Reviewed ECMP");
  console.log(
    "Core aggregate edit with reviewed revision and dedicated port tokens passed",
  );

  // Preserve a complete draft when a dependency changes after preview.
  await page.getByRole("button", { name: "编辑完整方案", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#core-name").fill("CAS preserved core");
  await next();
  assert.equal(
    await dialog
      .getByRole("button", { name: "移除此链路", exact: true })
      .isDisabled(),
    true,
  );
  await dialog
    .getByRole("button", { name: "添加一对端口", exact: true })
    .click();
  await dialog.locator("#core-link-1-sideA").selectOption(pa[1].id);
  await dialog.locator("#core-link-1-sideB").selectOption(pb[1].id);
  await next();
  await dialog.locator("#core-link-ipv4-1").fill("10.77.0.2/31");
  await dialog.locator("#core-link-ipv6-1").fill("2001:db8:77::2/127");
  await preview();
  const changing = await api(`/resources/ports/${pa[0].id}`);
  await api(
    `/resources/ports/${pa[0].id}`,
    {
      name: "A-1 reviewed change",
      switchId: a.id,
      speed: 10000,
      status: "active",
      coreReserved: true,
    },
    "PUT",
    changing._etag,
  );
  let writeResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/api/core/bundles/${bundle.id}`) &&
      r.request().method() === "PUT",
  );
  await page.getByRole("checkbox", { name: /我已核对双端、所有链路/ }).check();
  await dialog
    .getByRole("button", { name: "保存这份库存方案", exact: true })
    .click();
  assert.equal((await writeResponse).status(), 409);
  const conflict = dialog.locator('[aria-label="核心互联版本冲突"]');
  await conflict
    .getByText("整份草稿仍在，先核对服务器变化", { exact: true })
    .waitFor();
  await conflict.getByRole("checkbox").check();
  await conflict
    .getByRole("button", { name: "保留整份草稿，重新核对端口", exact: true })
    .click();
  await dialog.getByRole("button", { name: "上一步", exact: true }).click();
  assert.equal(
    await dialog.locator("#core-name").inputValue(),
    "CAS preserved core",
  );
  await next();
  await next();
  await preview();
  bundle = await commit("PUT");
  assert.equal(bundle.links.length, 2);
  assert.equal(bundle.name, "CAS preserved core");
  console.log(
    "Core dependency CAS rejected stale save and retained full draft/new link",
  );
  // Remove one link explicitly, retaining both physical records and reservations.
  await page.getByRole("button", { name: "编辑完整方案", exact: true }).click();
  dialog = page.getByRole("dialog");
  await next();
  await dialog
    .locator('[data-core-link-index="1"]')
    .getByRole("button", { name: "移除此链路", exact: true })
    .click();
  await next();
  await preview();
  bundle = await commit("PUT");
  assert.equal(bundle.links.length, 1);
  assert.equal((await api(`/resources/ports/${pa[1].id}`)).coreReserved, true);
  const legacy = await api("/resources/core-links", {
    name: "Legacy L2 inventory",
    type: "layer2",
    portAId: pa[2].id,
    portBId: pb[2].id,
    notes: "preserve original legacy notes",
  });
  await go();
  await page.getByRole("button", { name: /未归组兼容库存/ }).click();
  await page
    .getByRole("checkbox", {
      name: "归组选择 Legacy L2 inventory",
      exact: true,
    })
    .check();
  await page.getByRole("button", { name: /核对并归组选中的 1 条链路/ }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#core-type").waitFor();
  assert.equal(await dialog.locator("#core-type").inputValue(), "");
  await dialog.locator("#core-type").selectOption("l2-lag");
  for (const side of ["sideA", "sideB"]) {
    await dialog.locator(`#core-${side}-interface`).fill("ae");
    await dialog
      .locator(`#core-${side}-group`)
      .fill(side === "sideA" ? "0" : "1");
  }
  await next();
  assert.equal(
    await dialog.locator("#core-link-0-sideA").inputValue(),
    pa[2].id,
  );
  await next();
  await preview();
  const adopted = await commit("POST");
  assert.equal(adopted.type, "l2-lag");
  assert.equal((await api("/core")).unbundledLinks.length, 0);
  assert.equal(adopted.derived.capacityKnown, false);
  assert.ok(
    JSON.stringify(await api("/audit")).includes(
      "preserve original legacy notes",
    ),
  );
  await page.getByRole("button", { name: "删除 Bundle", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog
    .locator("#core-delete-reason")
    .fill("Reviewed local fixture decommission");
  await dialog.getByRole("checkbox").check();
  await dialog
    .getByRole("button", { name: "确认删除完整 Bundle", exact: true })
    .click();
  await dialog.waitFor({ state: "hidden" });
  assert.equal((await api(`/resources/ports/${pa[2].id}`)).coreReserved, true);
  assert.equal((await call(`/core/bundles/${adopted.id}`)).status, 404);
  console.log(
    "Explicit L2 adoption, unknown capacity, link removal and whole deletion preserve reservations",
  );
  await go();
  await page.locator(`[data-core-bundle="${bundle.id}"]`).click();
  await page.locator('[data-core-layer="physical"]').waitFor();
  const observed = await api(`/core/bundles/${bundle.id}/observations`);
  assert.equal(observed.physical.status, "unknown");
  assert.equal(observed.bgp.status, "unsupported");
  // Fault injection models a stale cached server response; production data still comes only from API.
  const stale = structuredClone(observed),
    old = new Date(Date.now() - 121000).toISOString();
  stale.physical = {
    ...stale.physical,
    status: "up",
    observedAt: old,
    identityVerified: true,
  };
  for (const link of stale.links) {
    link.physical = {
      ...link.physical,
      status: "up",
      observedAt: old,
      identityVerified: true,
    };
    for (const key of ["sideA", "sideB"])
      link[key] = {
        ...link[key],
        status: "fresh",
        observedAt: old,
        identityVerified: true,
        operStatus: "up",
        inBps: 123000,
        outBps: 234000,
      };
  }
  for (const key of ["sideA", "sideB"])
    stale.traffic[key] = {
      ...stale.traffic[key],
      inBps: 123000,
      outBps: 234000,
      sourceFrom: old,
      sourceTo: old,
    };
  const observationPath = `**/api/core/bundles/${bundle.id}/observations`;
  await page.route(observationPath, (route) =>
    route.fulfill({ status: 200, json: stale }),
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await page
    .locator('[data-core-layer="physical"]')
    .getByText("观测已过期", { exact: true })
    .waitFor();
  assert.ok(!(await page.locator("main").innerText()).includes("123.00 Kbps"));
  await page.unroute(observationPath);
  await page.route(observationPath, (route) =>
    route.fulfill({
      status: 500,
      json: { error: "fixture observation unavailable" },
    }),
  );
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await page
    .getByRole("alert")
    .filter({ hasText: "fixture observation unavailable" })
    .waitFor();
  assert.equal(await page.getByText("端口报告 Up", { exact: true }).count(), 0);
  await page.unroute(observationPath);
  await page.getByRole("button", { name: "刷新观测", exact: true }).click();
  await page
    .locator('[data-core-layer="bgp"]')
    .getByText("尚无独立观测适配", { exact: true })
    .waitFor();
  console.log(
    "Unknown protocols, stale cached Up and refresh500 never claim current health/rates",
  );
  const member = await api("/resources/members", {
    name: "Core portal fixture",
    asn: 64530,
    status: "active",
  });
  for (const role of ["viewer", "member"]) {
    await api("/resources/users", {
      name: `Core ${role}`,
      email: `${role}@example.test`,
      role,
      password,
      ...(role === "member" ? { memberIds: [member.id] } : {}),
    });
    const ctx = await browser.newContext();
    assert.equal(
      (
        await ctx.request.post(base + "/api/login", {
          data: { email: `${role}@example.test`, password },
        })
      ).status(),
      200,
    );
    const other = await ctx.newPage();
    await other.goto(base + "/#core");
    if (role === "viewer") {
      await other
        .getByRole("heading", { name: "核心互联", exact: true })
        .waitFor();
      assert.equal(
        await other
          .getByRole("button", { name: "新增核心 Bundle", exact: true })
          .count(),
        0,
      );
      assert.equal(
        await other
          .getByRole("button", { name: "编辑完整方案", exact: true })
          .count(),
        0,
      );
      assert.ok(
        (await call("/core/bundles", { bundle: {} }, "POST", undefined, ctx))
          .status >= 400,
      );
    } else {
      await other
        .getByText("你没有此页面的访问权限。", { exact: true })
        .waitFor();
      assert.equal(
        (await call("/core", undefined, "GET", undefined, ctx)).status,
        403,
      );
    }
    await ctx.close();
  }
  await page.goto(base + "/#security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("令牌名称").fill("Core A-only read");
  await dialog
    .getByRole("checkbox", { name: "读取网络资源", exact: true })
    .uncheck();
  await dialog
    .getByRole("checkbox", {
      name: "读取授权交换机双端可见的核心互联",
      exact: true,
    })
    .check();
  await dialog.getByRole("checkbox", { name: "Core A", exact: true }).check();
  await dialog.getByLabel("当前密码", { exact: true }).fill(password);
  const tokenResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/account/tokens") &&
      r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "继续", exact: true }).click();
  const issued = await (await tokenResponse).json();
  assert.deepEqual(issued.item.switchIds, [a.id]);
  const restricted = await fetch(base + `/api/core/bundles/${bundle.id}`, {
    headers: { Authorization: `Bearer ${issued.token}` },
  });
  assert.ok([403, 404].includes(restricted.status));
  console.log("Viewer/member and both-switch scoped token boundaries passed");
  await go();
  await page.locator(`[data-core-bundle="${bundle.id}"]`).click();
  await page.locator('[data-core-layer="physical"]').waitFor();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "/tmp/moedove-core-workspace-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-core-workspace-mobile.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "编辑完整方案", exact: true }).click();
  dialog = page.getByRole("dialog");
  await dialog.locator("#core-name").waitFor();
  assert.ok(
    await dialog.evaluate((node) => node.scrollWidth <= node.clientWidth),
  );
  await page.screenshot({
    path: "/tmp/moedove-core-editor-mobile.png",
    fullPage: true,
  });
  console.log("Core workspace/editor390px layouts passed");

  assert.deepEqual(errors, []);
  console.log(`Core fixture: ${dir}`);
} finally {
  await browser?.close();
  if (child.exitCode === null) {
    const ended = new Promise((r) => child.once("exit", r));
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await ended;
    clearTimeout(timer);
  }
}
