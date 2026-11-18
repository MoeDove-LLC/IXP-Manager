// Isolated browser fixture: no live router, mail, or external provider operations.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const workspace = await mkdtemp("/tmp/moedove-credentials-browser-");
const socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`;
const password = "Credential-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    workspace,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "credentials-fixture-bootstrap" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser;
const errors = [],
  previews = [];
let secretA = "",
  secretB = "";
const noSecrets = (value) => {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  assert.ok(
    !secretA || !text.includes(secretA),
    "Generated credential leaked into public response",
  );
  assert.ok(
    !secretB || !text.includes(secretB),
    "Manual credential leaked into public response",
  );
};
try {
  for (let n = 0; ; n++) {
    if (child.exitCode !== null) throw new Error("Isolated manager exited");
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (n > 100) throw new Error("Isolated manager startup timed out");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  });
  const page = await context.newPage();
  page.on("pageerror", (error) => errors.push(error.message));
  const request = async (
    path,
    body,
    method = body ? "POST" : "GET",
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    // Fixture-only rebasing: every intentional inventory setup update obtains its own current baseline.
    const fixtureHeaders = {};
    if (
      ["PUT", "DELETE"].includes(method) &&
      /^\/resources\/(?!provisioning-profiles\/)[^/]+\/[^/]+$/.test(path)
    ) {
      const baselineResponse = await ctx.request.get(base + "/api" + path);
      if (baselineResponse.ok()) {
        const baseline = await baselineResponse.json();
        if (baseline._etag)
          fixtureHeaders["If-Match"] = JSON.stringify(baseline._etag);
      }
      if (body && typeof body === "object" && !Array.isArray(body)) {
        const { _etag, ...values } = body;
        body = values;
      }
    }
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: { ...fixtureHeaders, "X-CSRF-Token": session.csrfToken || "" },
      ...(body ? { data: body } : {}),
    });
    return { status: response.status(), data: await response.json() };
  };
  const api = async (path, body, method) => {
    const r = await request(path, body, method);
    assert.ok(r.status < 300, `${method || "GET/POST"} ${path}: ${r.status}`);
    return r.data;
  };
  const setup = await context.request.post(base + "/api/setup", {
    data: {
      setupToken: "credentials-fixture-bootstrap",
      name: "Credential QA",
      email: "admin@example.test",
      password,
      ixpName: "Credential Test IXP",
      asn: 64500,
    },
  });
  assert.equal(setup.status(), 200);
  const create = (kind, data) => api(`/resources/${kind}`, data);
  const member = await create("members", {
    name: "Credential Test Network",
    asn: 64501,
    status: "active",
  });
  const location = await create("locations", {
    name: "Test DC",
    city: "Shanghai",
    country: "CN",
  });
  const sw = await create("switches", {
    name: "sw-fixture",
    locationId: location.id,
  });
  const portRow = await create("ports", {
    name: "Ethernet1",
    switchId: sw.id,
    speed: 10000,
    status: "active",
  });
  const vlan = await create("vlans", {
    name: "Test LAN",
    tag: 100,
    ipv4Prefix: "192.0.2.0/24",
  });
  await create("connections", {
    name: "Test link",
    memberId: member.id,
    portId: portRow.id,
    vlanId: vlan.id,
    ipv4: "192.0.2.10",
    status: "active",
  });
  const router = await create("routers", {
    name: "rs-junos",
    platform: "junos",
    asn: 64500,
    routerId: "192.0.2.1",
    localIpv4: "192.0.2.1",
    vlanId: vlan.id,
  });
  const otherRouter = await create("routers", {
    name: "rs-other",
    platform: "junos",
    asn: 64500,
    routerId: "192.0.2.2",
    localIpv4: "192.0.2.2",
    vlanId: vlan.id,
  });
  const peer = await create("peers", {
    name: "Test IPv4 peer",
    maxPrefixes: 100,
    routerId: router.id,
    memberId: member.id,
    address: "192.0.2.10",
    asn: 64501,
    enabled: true,
    allowedPrefixes: ["198.51.100.0/24"],
  });
  const go = async (kind) => {
    await page.goto(base + "/#" + kind);
    await page.locator("main").waitFor();
  };
  const openCredentials = async () => {
    await go("peers");
    await page
      .getByRole("button", {
        name: "管理 BGP 凭据 Test IPv4 peer",
        exact: true,
      })
      .click();
    await page.locator("#peer-credential-password").waitFor();
    return page.getByRole("dialog");
  };
  let dialog = await openCredentials();
  await dialog
    .locator("#peer-credential-password")
    .fill("incorrect-fixture-password");
  await dialog
    .getByRole("button", { name: "生成并保存凭据", exact: true })
    .click();
  await dialog.getByRole("alert").waitFor();
  assert.equal((await request("/session")).status, 200);
  await dialog.locator("#peer-credential-password").fill(password);
  await dialog
    .getByRole("button", { name: "生成并保存凭据", exact: true })
    .click();
  await page.locator("#generated-peer-secret").waitFor();
  secretA = await page.locator("#generated-peer-secret").inputValue();
  assert.ok(secretA.length >= 32);
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "完成", exact: true }).click();
  const generatedMeta = await api(`/peer-credentials?peerId=${peer.id}`);
  assert.equal(generatedMeta.configured, true);
  assert.ok(!Object.hasOwn(generatedMeta, "generatedSecret"));
  noSecrets(generatedMeta);
  console.log(
    "credential generation, one-time management display, reauthentication passed",
  );

  // Concurrent revision rotation is rejected without silently submitting against the new revision.
  dialog = await openCredentials();
  assert.equal(await page.locator("#generated-peer-secret").count(), 0);
  secretB = "Manual_fixture_TCP_MD5_key_456";
  const concurrent = await api(
    `/peer-credentials/${peer.id}`,
    {
      secret: secretB,
      expectedRevision: generatedMeta.revision,
      expectedPeerChecksum: generatedMeta.peerChecksum,
      currentPassword: password,
    },
    "PUT",
  );
  noSecrets(concurrent);
  await dialog.locator("#peer-credential-password").fill(password);
  await dialog
    .getByRole("button", { name: "生成并保存凭据", exact: true })
    .click();
  await dialog.getByRole("button", { name: "关闭并刷新会话列表" }).waitFor();
  assert.equal(
    (await api(`/peer-credentials?peerId=${peer.id}`)).revision,
    concurrent.revision,
  );
  await dialog.getByRole("button", { name: "关闭并刷新会话列表" }).click();
  dialog = await openCredentials();
  await dialog.getByRole("radio", { name: "输入已有共享凭据" }).check();
  await dialog.locator("#manual-peer-secret").fill(secretB);
  await dialog.locator("#peer-credential-password").fill(password);
  await dialog
    .getByRole("button", { name: "保存共享凭据", exact: true })
    .click();
  await dialog.getByRole("status").waitFor();
  noSecrets(await dialog.innerText());
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .first()
    .click();
  console.log(
    "manual entry does not echo and stale credential revision is rejected",
  );

  let previewRequests = [];
  page.on("response", async (response) => {
    const path = new URL(response.url()).pathname;
    if (
      /^\/api\/deployments(?:\/[^/]+(?:\/diff)?)?$/.test(path) &&
      response.status() === 200 &&
      response.request().method() === "GET"
    ) {
      const value = await response.json().catch(() => null);
      if (value) previews.push(value);
      previewRequests.push(path);
    }
  });
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page.locator("#deployment-router").selectOption(router.id);
  await page.getByRole("button", { name: "生成预览", exact: true }).click();
  await page
    .getByRole("button", { name: "检查配置结构", exact: true })
    .waitFor();
  await page.locator("pre code").waitFor();
  const first = (await api("/deployments")).items[0];
  assert.ok(!Object.hasOwn(first, "config"));
  assert.equal(first.checksumKind, "hmac-review-token");
  assert.ok(previewRequests.includes(`/api/deployments/${first.id}`));
  noSecrets(await page.locator("main").innerText());
  await page.getByRole("button", { name: "变更对比", exact: true }).click();
  await page.getByRole("heading", { name: "BGP 凭据修订变化" }).waitFor();
  await page.getByText("新增认证", { exact: true }).waitFor();
  noSecrets(await page.locator("main").innerText());
  await page.getByRole("button", { name: "检查配置结构", exact: true }).click();
  await page.getByRole("button", { name: "批准版本", exact: true }).click();
  await page.getByRole("button", { name: "确认批准", exact: true }).click();
  await page.getByRole("button", { name: "下载配置", exact: true }).waitFor();
  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载配置", exact: true }).click();
  const download = await downloadPromise;
  const bytes = await readFile(await download.path());
  assert.ok(
    bytes.includes(secretB),
    "Authorized private artifact must contain the fixture credential",
  );
  const actualSha = createHash("sha256").update(bytes).digest("hex");
  await page.getByLabel("下载文件真实 SHA-256").waitFor();
  assert.equal(
    await page.getByLabel("下载文件真实 SHA-256").innerText(),
    actualSha,
  );
  assert.notEqual(first.checksum, actualSha);
  await page.getByText("在目标路由器安全拉取配置", { exact: true }).click();
  const command = await page.getByLabel("私密配置安全拉取命令").innerText();
  assert.ok(command.includes("ixp-agent fetch"));
  assert.ok(!command.includes(first.checksum));
  for (const response of previews) noSecrets(response);
  console.log(
    "metadata-only list, lazy redacted detail/diff, approval, authenticated actual-SHA download passed",
  );

  // Delete credential keeps a tombstone revision; old immutable artifact remains confidential.
  dialog = await openCredentials();
  await dialog.getByRole("radio", { name: "移除此会话的凭据" }).check();
  await dialog.locator("#peer-credential-password").fill(password);
  await dialog
    .getByRole("button", { name: "确认移除凭据", exact: true })
    .click();
  await dialog.getByRole("status").waitFor();
  const removed = await api(`/peer-credentials?peerId=${peer.id}`);
  assert.equal(removed.configured, false);
  assert.ok(removed.revision.length > 0);
  await dialog
    .getByRole("button", { name: "关闭", exact: true })
    .first()
    .click();
  const second = await api("/deployments", { routerId: router.id });
  await go("deployments");
  await page.locator(`[data-deployment-id="${second.id}"]`).click();
  await page.getByRole("button", { name: "变更对比", exact: true }).click();
  await page.getByText("移除认证", { exact: true }).waitFor();
  assert.equal(await page.getByLabel("下载文件真实 SHA-256").count(), 0);
  noSecrets(await page.locator("main").innerText());
  await page.screenshot({
    path: "/tmp/moedove-credentials-diff-desktop.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "检查配置结构", exact: true }).click();
  await page.getByRole("button", { name: "批准版本", exact: true }).click();
  await page.getByRole("button", { name: "确认批准", exact: true }).click();
  await page.locator(`[data-deployment-id="${first.id}"]`).click();
  await page.getByRole("button", { name: "清理不再需要的配置文件" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("checkbox").check();
  await dialog.getByRole("button", { name: "确认清理配置文件" }).click();
  await dialog.waitFor({ state: "hidden" });
  await page
    .getByText("此版本配置文件已明确清理。", { exact: false })
    .waitFor();
  await page.locator(`[data-deployment-id="${second.id}"]`).click();
  await page.getByText("旧基线文件已清理", { exact: false }).waitFor();
  console.log(
    "credential removal, tombstone revision, pruned baseline warning passed",
  );

  // Viewer cannot access credential metadata or actual configuration bytes.
  await create("users", {
    name: "Read Only",
    email: "viewer@example.test",
    role: "viewer",
    password,
  });
  const viewerContext = await browser.newContext();
  const login = await viewerContext.request.post(base + "/api/login", {
    data: { email: "viewer@example.test", password },
  });
  assert.equal(login.status(), 200);
  const viewer = await viewerContext.newPage();
  await viewer.goto(base + "/#peers");
  await viewer
    .getByRole("cell", {
      name: "Test IPv4 peer",
      maxPrefixes: 100,
      exact: true,
    })
    .waitFor();
  assert.equal(
    await viewer.getByRole("button", { name: /管理 BGP 凭据/ }).count(),
    0,
  );
  assert.equal(
    (
      await request(
        `/peer-credentials?peerId=${peer.id}`,
        undefined,
        "GET",
        viewerContext,
      )
    ).status,
    403,
  );
  await viewer.goto(base + "/#deployments");
  await viewer.locator("pre code").waitFor();
  assert.equal(
    await viewer.getByRole("button", { name: "下载配置", exact: true }).count(),
    0,
  );
  assert.equal(
    (
      await viewerContext.request.get(
        base + `/api/deployments/${second.id}/config`,
      )
    ).status(),
    403,
  );
  noSecrets(await viewer.locator("main").innerText());

  await go("security");
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  dialog = page.getByRole("dialog");
  await dialog.getByLabel("令牌名称").fill("router-bound private pull");
  await dialog
    .getByRole("checkbox", { name: "拉取指定路由器的私密配置", exact: true })
    .check();
  await dialog.getByText("绑定的路由服务器（必选）", { exact: true }).waitFor();
  await dialog.getByRole("checkbox", { name: "rs-junos", exact: true }).check();
  await dialog.getByLabel("当前密码", { exact: true }).fill(password);
  const tokenResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/account/tokens") &&
      r.request().method() === "POST",
  );
  await dialog.getByRole("button", { name: "继续", exact: true }).click();
  const issued = await (await tokenResponse).json();
  assert.deepEqual(issued.item.routerIds, [router.id]);
  assert.ok(issued.item.scopes.includes("deployments:pull"));
  const tokenGet = (path) =>
    fetch(base + "/api" + path, {
      headers: { Authorization: `Bearer ${issued.token}` },
    });
  assert.equal(
    (await tokenGet(`/deployments/${second.id}/config`)).status,
    200,
  );
  const other = await api("/deployments", { routerId: otherRouter.id });
  await api(`/deployments/${other.id}/validate`, {});
  await api(`/deployments/${other.id}/approve`, { checksum: other.checksum });
  assert.equal((await tokenGet(`/deployments/${other.id}/config`)).status, 403);
  console.log(
    "viewer cannot download; pull token UI enforces selected router binding",
  );

  // Narrow layout screenshot only after secret/token dialogs have been discarded.
  await go("deployments");
  await page.locator(`[data-deployment-id="${second.id}"]`).click();
  await page.getByRole("button", { name: "变更对比", exact: true }).click();
  await page.getByText("旧基线文件已清理", { exact: false }).waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
    "Deployment page overflows narrow viewport",
  );
  await page.screenshot({
    path: "/tmp/moedove-credentials-deployments-mobile.png",
    fullPage: true,
  });
  dialog = await openCredentials();
  assert.ok(
    await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
    "Credential dialog overflows narrow viewport",
  );
  await page.screenshot({
    path: "/tmp/moedove-credentials-dialog-mobile.png",
    fullPage: true,
  });
  assert.deepEqual(errors, []);
  console.log(
    "390px deployment and credential dialog layout passed; no page errors",
  );
  console.log(`Fixture: ${workspace}`);
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
