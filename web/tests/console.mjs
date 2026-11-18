// Inventory/serial workflow only: no terminal, credential or device connections.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-console-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "console-inventory-fixture-password";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-console-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "console-fixture-setup" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser,
  stderr = "";
child.stderr.on("data", (data) => {
  stderr = (stderr + data.toString()).slice(-3000);
});
try {
  for (let n = 0; ; n++) {
    if (child.exitCode !== null) throw new Error(stderr);
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (n > 100) throw new Error("console fixture startup timeout");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    }),
    page = await context.newPage(),
    errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "console-fixture-setup",
          name: "Console QA",
          email: "admin@example.test",
          password,
          ixpName: "Console Inventory QA",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const call = async (
    path,
    body,
    method = body ? "POST" : "GET",
    tag,
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: {
        "X-CSRF-Token": session.csrfToken || "",
        ...(tag ? { "If-Match": JSON.stringify(tag) } : {}),
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
  const loc = await api("/resources/locations", { name: "Console DC" }),
    rack = await api("/resources/racks", {
      name: "Console rack",
      locationId: loc.id,
    }),
    vendor = await api("/resources/vendors", { name: "Serial vendor" }),
    member = await api("/resources/members", {
      name: "Console equipment owner",
      asn: 64510,
      status: "active",
    });
  const oldServer = await api("/resources/console-servers", {
    name: "Old IP console",
    locationId: loc.id,
    managementIp: "192.0.2.20",
  });
  const oldConnection = await api("/resources/console-connections", {
    name: "Old numeric record",
    consoleServerId: oldServer.id,
    memberId: member.id,
    port: 7,
  });
  assert.equal(oldConnection.portLabel, "7");
  const go = async (route) => {
    await page.goto(base + "/#" + route);
    await page.reload();
    await page.locator(".page-heading").waitFor();
  };
  const saveForm = async (kind, method) => {
    const pending = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.startsWith(`/api/resources/${kind}`) &&
        response.request().method() === method,
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "保存", exact: true })
      .click();
    const response = await pending,
      result = await response.json();
    assert.ok(response.status() < 300, JSON.stringify(result));
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    return result;
  };
  await go("console-servers");
  await page
    .locator(".page-heading")
    .getByRole("button", { name: "新增控制台服务器", exact: true })
    .click();
  await page.locator("#field-name").fill("Console 主设备");
  await page.locator("#field-hostname").fill("bücher.example");
  await page.locator("#field-rackId").selectOption(rack.id);
  assert.equal(await page.locator("#field-locationId").inputValue(), loc.id);
  await page.locator("#field-vendorId").selectOption(vendor.id);
  await page.locator("#field-model").fill("Serial inventory model");
  await page.locator("#field-serialNumber").fill("SERIAL-001");
  await page.locator("#field-notes").fill("Inventory notes only");
  const server = await saveForm("console-servers", "POST");
  assert.equal(server.hostname, "xn--bcher-kva.example");
  assert.equal(server.managementIp ?? "", "");
  assert.equal(server.connectionCount, 0);
  const serverRow = page.getByRole("row").filter({
    has: page.getByRole("button", {
      name: "查看控制台记录 Console 主设备",
      exact: true,
    }),
  });
  await serverRow
    .getByRole("link", { name: "查看 0 条接入", exact: true })
    .click();
  await page
    .getByRole("button", { name: "新增控制台连接", exact: true })
    .first()
    .click();
  assert.equal(
    await page.locator("#field-consoleServerId").inputValue(),
    server.id,
  );
  await page.locator("#field-name").fill("Member serial console");
  await page
    .locator("#field-description")
    .fill("Customer equipment console inventory");
  await page.locator("#field-memberId").selectOption(member.id);
  await page.locator("#field-portLabel").fill("01");
  await page.locator("#field-autobaud").selectOption("false");
  await page.locator("#field-baudRate").selectOption("9600");
  await page.locator("#field-parity").selectOption("even");
  await page.locator("#field-stopBits").selectOption("1");
  await page.locator("#field-flowControl").selectOption("rts-cts");
  const connection = await saveForm("console-connections", "POST");
  assert.equal(connection.portLabel, "01");
  assert.equal(connection.baudRate, 9600);
  assert.equal(connection.parity, "even");
  await api("/resources/console-connections", {
    name: "Distinct numeric1",
    consoleServerId: server.id,
    memberId: member.id,
    port: 1,
  });
  assert.equal(
    (await api(`/resources/console-connections?consoleServerId=${server.id}`))
      .items.length,
    2,
  );
  const incompatible = await call("/resources/console-connections", {
    name: "Ambiguous label",
    consoleServerId: server.id,
    memberId: member.id,
    port: 1,
    portLabel: "01",
  });
  assert.equal(incompatible.status, 400);
  console.log(
    "Console hostname-only IDNA, assets/location, relation filter and leading-zero labels passed",
  );
  const openConnection = async () => {
    await go(`console-connections?id=${connection.id}`);
    await page
      .getByRole("button", { name: `编辑 ${connection.name}`, exact: true })
      .click();
  };
  await openConnection();
  await page.locator("#field-autobaud").selectOption("true");
  assert.equal(await page.locator("#field-baudRate").count(), 0);
  await saveForm("console-connections", "PUT");
  assert.equal(
    (await api(`/resources/console-connections/${connection.id}`)).baudRate,
    9600,
  );
  await openConnection();
  await page.locator("#field-autobaud").selectOption("false");
  assert.equal(await page.locator("#field-baudRate").inputValue(), "9600");
  await page.locator("#field-baudRate").selectOption("");
  await saveForm("console-connections", "PUT");
  assert.equal(
    (await api(`/resources/console-connections/${connection.id}`)).baudRate,
    null,
  );
  await go(`console-connections?id=${oldConnection.id}`);
  await page
    .getByRole("button", { name: "编辑 Old numeric record", exact: true })
    .click();
  assert.equal(await page.locator("#field-portLabel").inputValue(), "7");
  assert.equal(await page.locator("#field-autobaud").inputValue(), "unknown");
  await page.locator("#field-portLabel").fill("007");
  await saveForm("console-connections", "PUT");
  const upgraded = await api(
    `/resources/console-connections/${oldConnection.id}`,
  );
  assert.equal(upgraded.portLabel, "007");
  assert.equal(upgraded.port, undefined);
  console.log(
    "Console autobaud preserves manual values, explicit unknown clearing and old numeric edit compatibility passed",
  );
  await openConnection();
  await page.locator("#field-name").fill("Preserved console draft");
  const latest = await api(`/resources/console-connections/${connection.id}`);
  await api(
    `/resources/console-connections/${connection.id}`,
    {
      name: latest.name,
      consoleServerId: latest.consoleServerId,
      memberId: latest.memberId,
      description: "Changed by another operator",
    },
    "PUT",
    latest._etag,
  );
  const conflict = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname ===
        `/api/resources/console-connections/${connection.id}` &&
      response.request().method() === "PUT",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  assert.equal((await conflict).status(), 412);
  assert.equal(
    await page.locator("#field-name").inputValue(),
    "Preserved console draft",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "放弃草稿并关闭", exact: true })
    .click();
  await go(`console-servers?id=${server.id}`);
  await page
    .getByRole("button", { name: `删除 ${server.name}`, exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByText(/仍有 2 条关联接入/)
    .waitFor();
  assert.equal(
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "确认删除", exact: true })
      .isDisabled(),
    true,
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  const currentServer = await api(`/resources/console-servers/${server.id}`);
  assert.equal(
    (
      await call(
        `/resources/console-servers/${server.id}`,
        undefined,
        "DELETE",
        currentServer._etag,
      )
    ).status,
    400,
  );
  assert.equal(
    (await api(`/resources/console-connections?consoleServerId=${server.id}`))
      .items.length,
    2,
  );
  console.log(
    "Console stale edit retains draft and referenced server cannot cascade-delete passed",
  );
  await page
    .getByRole("button", { name: `查看控制台记录 ${server.name}`, exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByText("SERIAL-001", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "关闭详情", exact: true }).click();
  for (const [role, email] of [
    ["viewer", "console-viewer@example.test"],
    ["member", "console-member@example.test"],
  ]) {
    await api("/resources/users", {
      name: role,
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
    const rolePage = await ctx.newPage();
    rolePage.on("pageerror", (error) =>
      errors.push(`${role}: ${error.message}`),
    );
    await rolePage.goto(base + "/#console-connections");
    if (role === "member") {
      assert.equal(
        (
          await call(
            "/resources/console-connections",
            undefined,
            "GET",
            undefined,
            ctx,
          )
        ).data.items.length,
        0,
      );
      assert.equal(
        (
          await call(
            `/resources/console-connections/${connection.id}`,
            undefined,
            "GET",
            undefined,
            ctx,
          )
        ).status,
        404,
      );
      assert.equal(
        await rolePage
          .getByRole("button", { name: "新增控制台连接", exact: true })
          .count(),
        0,
      );
    } else {
      await rolePage
        .getByRole("button", {
          name: `查看控制台记录 ${connection.name}`,
          exact: true,
        })
        .click();
      await rolePage
        .getByRole("dialog")
        .getByText("Changed by another operator", { exact: true })
        .waitFor();
      assert.equal(
        await rolePage
          .getByRole("button", { name: "新增控制台连接", exact: true })
          .count(),
        0,
      );
    }
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
  await page.screenshot({ path: "/tmp/moedove-console-mobile.png" });
  await page
    .getByRole("button", { name: `查看控制台记录 ${server.name}`, exact: true })
    .click();
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    ),
    false,
  );
  await page.screenshot({ path: "/tmp/moedove-console-detail-mobile.png" });
  assert.deepEqual(errors, []);
  console.log("Console viewer details/member isolation and390px passed");
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
  await rm(dir, { recursive: true, force: true });
  console.log("Console isolated browser/service/synthetic data cleaned up");
}
