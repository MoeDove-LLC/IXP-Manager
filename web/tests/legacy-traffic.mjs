// Isolated legacy report UI: no live collectors, no source database writes.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-legacy-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "SFlow-browser-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-legacy-ui-server",
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
  const requests = [];
  page.on("request", (r) => requests.push(new URL(r.url()).pathname));
  await page.goto(base + "/#traffic");
  await page
    .getByRole("button", { name: "旧统计（独立来源）", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "旧统计归档", exact: true })
    .waitFor();
  await page
    .getByRole("heading", { name: "暂无可访问的旧统计批次", exact: true })
    .waitFor();
  requests.length = 0;
  await page
    .getByRole("button", { name: "预览旧统计导入", exact: true })
    .click();
  await page.locator("#legacy-input-name").fill("Historical MRTG fixture");
  await page.locator("#legacy-input-timezone").fill("Europe/Dublin");
  await page
    .locator("#legacy-input-file")
    .setInputFiles(
      fileURLToPath(
        new URL(
          "../../internal/traffic/testdata/legacy-traffic-daily.json",
          import.meta.url,
        ),
      ),
    );
  const previewed = page.waitForResponse((r) =>
    r.url().endsWith("/traffic/legacy/preview"),
  );
  await page.getByRole("button", { name: "解析并预览", exact: true }).click();
  const previewResponse = await previewed,
    preview = await previewResponse.json();
  assert.equal(previewResponse.status(), 200, JSON.stringify(preview));
  assert.equal(preview.rowCount, 4);
  assert.equal(preview.staffOnlyRows, 4);
  await page
    .getByRole("dialog")
    .getByText("9,007,199,254,740,993 bits", { exact: true })
    .waitFor();
  await page
    .getByRole("dialog")
    .getByText("原样本 NaN", { exact: true })
    .waitFor();
  assert.equal((await api("/traffic/legacy/catalog")).batches.length, 0);
  assert.equal(
    await page
      .getByRole("button", { name: "确认导入此批次", exact: true })
      .isDisabled(),
    true,
  );
  await page.locator("#legacy-import-ack").check();
  await page.locator("#legacy-import-password").fill(password);
  const imported = page.waitForResponse((r) =>
    r.url().endsWith("/traffic/legacy/import"),
  );
  await page
    .getByRole("button", { name: "确认导入此批次", exact: true })
    .click();
  const importResponse = await imported;
  assert.ok(importResponse.ok(), await importResponse.text());
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page.locator("#legacy-period").selectOption("day");
  const queried = page.waitForResponse(
    (r) => new URL(r.url()).pathname === "/api/traffic/legacy/query",
  );
  await page.getByRole("button", { name: "查询旧统计", exact: true }).click();
  const queryResponse = await queried,
    report = await queryResponse.json();
  assert.equal(queryResponse.status(), 200, JSON.stringify(report));
  assert.equal(report.sourceKind, "legacy-report");
  assert.equal(report.noAggregation, true);
  assert.equal(report.rows.length, 1);
  assert.equal(report.rows[0].in.total, "9007199254740993");
  assert.equal(report.rows[0].window.start, null);
  assert.equal(report.rows[0].out.average, null);
  await page
    .getByTestId("legacy-traffic-row")
    .getByText("9,007,199,254,740,993 bits", { exact: true })
    .waitFor();
  assert.equal(
    requests.includes("/api/traffic/query"),
    false,
    "legacy tab fetched live counter query",
  );
  assert.equal(
    requests.includes("/api/sflow/query"),
    false,
    "legacy tab fetched sampled query",
  );
  console.log(
    "legacy local file preview -> explicit admin import -> independent exact decimal/null/unknown-boundary query passed",
  );
  await page
    .getByText("按已知实际窗口筛选（UTC，可选）", { exact: true })
    .click();
  await page.locator("#legacy-window-from").fill("2024-05-30T00:00:00");
  await page.locator("#legacy-window-to").fill("2024-06-02T00:00:00");
  const knownWindows = page.waitForResponse(
    (r) => new URL(r.url()).pathname === "/api/traffic/legacy/query",
  );
  await page.getByRole("button", { name: "查询旧统计", exact: true }).click();
  const knownResponse = await knownWindows;
  assert.equal(knownResponse.status(), 200);
  assert.equal(
    new URL(knownResponse.url()).searchParams.get("windowFrom"),
    "2024-05-30T00:00:00.000Z",
  );
  assert.equal((await knownResponse.json()).rows.length, 0);
  await page
    .getByRole("heading", { name: "此批次没有符合条件的历史记录", exact: true })
    .waitFor();
  await page.locator("#legacy-window-from").fill("");
  await page.locator("#legacy-window-to").fill("");
  await page.getByRole("button", { name: "查询旧统计", exact: true }).click();
  await page.getByTestId("legacy-traffic-row").waitFor();
  console.log(
    "legacy UTC known-window filter excludes unknown rolling boundaries without fabricating intervals passed",
  );

  const member = await api("/resources/members", {
    name: "No historical proof",
    asn: 64599,
    status: "active",
  });
  await api("/resources/users", {
    name: "Historical member",
    email: "legacy-member@example.test",
    password,
    role: "member",
    memberId: member.id,
  });
  const memberContext = await browser.newContext();
  assert.equal(
    (
      await memberContext.request.post(base + "/api/login", {
        data: { email: "legacy-member@example.test", password },
      })
    ).status(),
    200,
  );
  const memberPage = await memberContext.newPage();
  memberPage.on("pageerror", (e) => errors.push(e.message));
  await memberPage.goto(base + "/#traffic");
  await memberPage
    .getByRole("button", { name: "旧统计（独立来源）", exact: true })
    .click();
  await memberPage
    .getByRole("heading", { name: "暂无可访问的旧统计批次", exact: true })
    .waitFor();
  assert.equal(
    await memberPage
      .getByRole("button", { name: "预览旧统计导入", exact: true })
      .count(),
    0,
  );
  const memberCatalog = await (
    await memberContext.request.get(base + "/api/traffic/legacy/catalog")
  ).json();
  assert.equal(memberCatalog.batches.length, 0);
  assert.equal(memberCatalog.bytes, undefined);
  await memberContext.close();
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
    path: "/tmp/moedove-legacy-mobile.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "删除此导入", exact: true }).click();
  await page
    .locator("#legacy-delete-reason")
    .fill("Reviewed fixture retirement");
  await page.getByRole("dialog").getByRole("checkbox").check();
  await page
    .getByRole("button", { name: "确认删除此导入", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  await page
    .getByRole("heading", { name: "暂无可访问的旧统计批次", exact: true })
    .waitFor();
  assert.deepEqual(errors, []);
  console.log(
    "legacy default staff-only member isolation, no global usage leak, 390px and reasoned deletion passed",
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
  console.log("legacy isolated browser and manager stopped");
}
