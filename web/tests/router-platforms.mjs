// New-platform UI checks use native parsers only; no daemon/application is started.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-platform-browser-");
const socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "Platform-fixture-password-123",
  bird3 =
    process.env.BIRD3_BIN ||
    "/tmp/moedove-bird3-native/extracted/usr/sbin/bird",
  openbgpd =
    process.env.OPENBGPD_BIN ||
    "/tmp/moedove-openbgpd-native/extracted/usr/sbin/bgpd";
let child, browser;
const start = async (missing = false) => {
  child = spawn(
    process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
    [
      "-listen",
      `127.0.0.1:${port}`,
      "-data",
      dir,
      "-web",
      fileURLToPath(new URL("../dist", import.meta.url)),
      "-bird3",
      missing ? "/not-installed/bird3" : bird3,
      "-openbgpd",
      missing ? "/not-installed/openbgpd" : openbgpd,
    ],
    {
      env: { ...process.env, IXP_SETUP_TOKEN: "platform-fixture-bootstrap" },
      stdio: ["ignore", "ignore", "pipe"],
    },
  );
  for (let attempt = 0; ; attempt++) {
    if (child.exitCode !== null)
      throw new Error("Platform fixture manager exited");
    if (
      await fetch(base + "/api/setup")
        .then((response) => response.ok)
        .catch(() => false)
    )
      return;
    if (attempt > 100) throw new Error("Platform fixture startup timeout");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
};
const stop = async () => {
  if (!child || child.exitCode !== null) return;
  const running = child,
    exited = new Promise((resolve) => running.once("exit", resolve));
  running.kill("SIGTERM");
  const timer = setTimeout(() => running.kill("SIGKILL"), 5000);
  await exited;
  clearTimeout(timer);
};
try {
  await start();
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
  const api = async (path, body, method = body ? "POST" : "GET", etag) => {
    const session = await (
      await context.request.get(base + "/api/session")
    ).json();
    const response = await context.request.fetch(base + "/api" + path, {
      method,
      headers: {
        "X-CSRF-Token": session.csrfToken || "",
        ...(etag ? { "If-Match": JSON.stringify(etag) } : {}),
      },
      ...(body ? { data: body } : {}),
    });
    const data = await response.json();
    assert.ok(
      response.ok(),
      `${path}: ${response.status()} ${data.error || ""}`,
    );
    return data;
  };
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "platform-fixture-bootstrap",
          name: "Platform QA",
          email: "admin@example.test",
          password,
          ixpName: "Native Platform Test",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const member = await api("/resources/members", {
      name: "Platform Network",
      asn: 64501,
      status: "active",
    }),
    location = await api("/resources/locations", { name: "Platform DC" }),
    sw = await api("/resources/switches", {
      name: "Platform switch",
      locationId: location.id,
    }),
    physical = await api("/resources/ports", {
      name: "eth1",
      switchId: sw.id,
      status: "active",
      speed: 10000,
    }),
    vlan = await api("/resources/vlans", {
      name: "Platform LAN",
      tag: 100,
      ipv4Prefix: "192.0.2.0/24",
    });
  await api("/resources/connections", {
    name: "Platform link",
    memberId: member.id,
    portId: physical.id,
    vlanId: vlan.id,
    ipv4: "192.0.2.10",
    status: "active",
  });
  const go = async (pageName) => {
    await page.goto(base + "/#" + pageName);
    await page.reload();
    await page.locator("main").waitFor();
  };
  const createRouter = async (platform, name, address) => {
    await go("routers");
    await page.locator(".page-heading button").click();
    const form = page.getByRole("dialog");
    await form.locator("#field-name").fill(name);
    await form.locator("#field-platform").selectOption(platform);
    await form.locator("#field-asn").fill("64500");
    await form.locator("#field-routerId").fill(address);
    await form.locator("#field-localIpv4").fill(address);
    await form.locator("#field-vlanId").selectOption(vlan.id);
    assert.equal(
      await form.locator("#field-rfc1997Passthrough").inputValue(),
      "native",
    );
    assert.ok(
      (
        await form
          .locator("#field-rfc1997Passthrough option:checked")
          .innerText()
      ).includes(platform === "openbgpd" ? "保留给客户端" : "由服务器执行"),
    );
    await form.getByRole("button", { name: "保存", exact: true }).click();
    await form.waitFor({ state: "hidden" });
    const router = (await api("/resources/routers")).items.find(
      (row) => row.name === name,
    );
    assert.ok(!Object.hasOwn(router, "rfc1997Passthrough"));
    return router;
  };
  let b3 = await createRouter("bird3", "BIRD3 native", "192.0.2.1"),
    ob = await createRouter("openbgpd", "OpenBGPD native", "192.0.2.2");
  const editRfc = async (router, value) => {
    await go("routers");
    await page
      .getByRole("button", { name: `编辑 ${router.name}`, exact: true })
      .click();
    const form = page.getByRole("dialog");
    await form.locator("#field-rfc1997Passthrough").selectOption(value);
    await form.getByRole("button", { name: "保存", exact: true }).click();
    await form.waitFor({ state: "hidden" });
    return api(`/resources/routers/${router.id}`);
  };
  b3 = await editRfc(b3, "true");
  assert.equal(b3.rfc1997Passthrough, true);
  b3 = await editRfc(b3, "native");
  assert.ok(!Object.hasOwn(b3, "rfc1997Passthrough"));
  ob = await editRfc(ob, "false");
  assert.equal(ob.rfc1997Passthrough, false);
  let switcher = await createRouter(
    "bird3",
    "Platform switch fixture",
    "192.0.2.3",
  );
  switcher = await editRfc(switcher, "true");
  await go("routers");
  await page
    .getByRole("button", { name: `编辑 ${switcher.name}`, exact: true })
    .click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("#field-platform").selectOption("frr");
  assert.equal(await dialog.locator("#field-rfc1997Passthrough").count(), 0);
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  assert.ok(
    !Object.hasOwn(
      await api(`/resources/routers/${switcher.id}`),
      "rfc1997Passthrough",
    ),
  );
  console.log(
    "RFC1997 native/true/false semantics and omission on unsupported platforms passed",
  );
  for (const router of [b3, ob])
    await api("/resources/peers", {
      name: `Peer ${router.name}`,
      routerId: router.id,
      memberId: member.id,
      address: "192.0.2.10",
      asn: 64501,
      enabled: true,
      maxPrefixes: 100,
      allowedPrefixes: ["198.51.100.0/24"],
    });
  const caps = (await api("/router-platforms")).items;
  for (const platform of ["bird3", "openbgpd"]) {
    const cap = caps.find((c) => c.platform === platform);
    assert.equal(cap.render, true);
    assert.equal(cap.validate, true);
    assert.equal(cap.nativeCheckerAvailable, true);
    assert.equal(cap.fetch, true);
  }
  const generate = async (router) => {
    await go("deployments");
    await page.locator(".page-heading button").click();
    await page.locator("#deployment-router").selectOption(router.id);
    const response = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/deployments") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "生成预览", exact: true }).click();
    const value = await (await response).json();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.locator("pre code").waitFor();
    return value;
  };
  for (const router of [b3, ob]) {
    const revision = await generate(router);
    assert.equal(revision.platform, router.platform);
    assert.equal(revision.rfc1997Passthrough, false);
    assert.equal(
      revision.rfc1997Mode,
      router.platform === "bird3" ? "native" : "explicit",
    );
    await page.getByLabel("此版本 RFC 1997 语义").waitFor();
    assert.ok(
      (await page.getByLabel("此版本 RFC 1997 语义").innerText()).includes(
        "由服务器执行",
      ),
    );
    const validated = page.waitForResponse((r) =>
      r.url().endsWith(`/api/deployments/${revision.id}/validate`),
    );
    await page.getByRole("button", { name: "校验配置", exact: true }).click();
    assert.equal((await validated).status(), 200);
    await page.getByRole("button", { name: "批准版本", exact: true }).click();
    await page.getByRole("button", { name: "确认批准", exact: true }).click();
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载配置", exact: true }).click();
    await download;
    await page.getByLabel("下载文件真实 SHA-256").waitFor();
    await page.getByText("在目标路由器安全拉取配置", { exact: true }).click();
    assert.ok(
      (await page.getByLabel("私密配置安全拉取命令").innerText()).includes(
        `--platform '${router.platform}'`,
      ),
    );
    if (router.platform === "openbgpd")
      await page.screenshot({
        path: "/tmp/moedove-openbgpd-native-review.png",
        fullPage: true,
      });
    console.log(
      `${router.platform} real native validation, approval, checked download and capability-driven fetch passed`,
    );
  }
  // Missing executable flags change capability availability, never validation claims.
  await stop();
  await start(true);
  assert.equal(
    (
      await context.request.post(base + "/api/login", {
        data: { email: "admin@example.test", password },
      })
    ).status(),
    200,
  );
  await generate(b3);
  assert.equal(
    await page
      .getByRole("button", { name: "校验配置", exact: true })
      .isDisabled(),
    true,
  );
  await page
    .getByText("服务端尚未找到此平台的原生校验器", { exact: false })
    .waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-bird3-checker-missing-mobile.png",
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  console.log("missing native checker fails closed in UI; 390px layout passed");
  console.log(`Platform fixture: ${dir}`);
} finally {
  await browser?.close();
  await stop();
}
