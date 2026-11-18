// Real application HTTP flows against a literal-loopback fake Atlas provider.
// No request is sent to RIPE Atlas and no network measurement is executed.
import assert from "node:assert/strict";
import http from "node:http";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-atlas-browser-"),
  key = "fixture-only-private-key",
  password = "Atlas-fixture-password-123",
  measurements = new Map(),
  providerErrors = [],
  requests = [];
let nextID = 5001,
  failNextCreate = false,
  rejectNextCreate = false,
  allowFind = false,
  child,
  browser;
const json = (response, status, data) => {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(data));
};
const fake = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://fixture.invalid");
    requests.push({ method: request.method, path: url.pathname });
    if (request.headers.authorization !== `Key ${key}`)
      throw new Error("fixture authorization mismatch");
    if (url.searchParams.has("key"))
      throw new Error("credential was placed in URL");
    if (request.method === "GET" && url.pathname === "/api/v2/probes/") {
      const asn = Number(
          url.searchParams.get("asn_v4") || url.searchParams.get("asn_v6"),
        ),
        prefix = (asn - 64500) * 100;
      const rows = [1, 2].map((offset) => ({
        id: prefix + offset,
        asn_v4: asn,
        asn_v6: asn,
        address_v4: `198.51.100.${prefix + offset}`,
        address_v6: `2001:db8::${prefix + offset}`,
        country_code: "NL",
        is_anchor: offset === 1,
        is_public: true,
        status: { id: 1, name: "Connected" },
        last_connected: Math.floor(Date.now() / 1000),
      }));
      json(response, 200, {
        count: 203,
        next: "https://must-not-follow.invalid/probes/",
        results: rows,
      });
      return;
    }
    if (request.method === "POST" && url.pathname === "/api/v2/measurements/") {
      let raw = "";
      for await (const chunk of request) raw += chunk;
      const body = JSON.parse(raw),
        definition = body.definitions[0],
        id = nextID++;
      if (rejectNextCreate) {
        rejectNextCreate = false;
        json(response, 400, { error: "synthetic definite rejection" });
        return;
      }
      measurements.set(id, {
        id,
        type: definition.type,
        af: definition.af,
        target_ip: definition.target,
        description: definition.description,
        is_public: definition.is_public,
        is_oneoff: definition.is_oneoff,
        interval: definition.interval ?? 0,
        start_time: body.start_time,
        stop_time: body.stop_time ?? null,
        status: { id: 2, name: "Ongoing" },
        probeIds: body.probes[0].value.split(",").map(Number),
        receivedDefinition: definition,
      });
      if (failNextCreate) {
        failNextCreate = false;
        measurements.get(id).errorOnly = true;
        json(response, 503, { error: "synthetic uncertain response" });
        return;
      }
      json(response, 201, { measurements: [id] });
      return;
    }
    if (
      request.method === "GET" &&
      url.pathname === "/api/v2/measurements/my/"
    ) {
      const results = allowFind
        ? [...measurements.values()].filter(
            (item) => item.description === url.searchParams.get("description"),
          )
        : [];
      json(response, 200, { results, next: null });
      return;
    }
    const match = url.pathname.match(
      /^\/api\/v2\/measurements\/(\d+)\/(results\/)?$/,
    );
    if (match) {
      const item = measurements.get(Number(match[1]));
      if (!item) {
        json(response, 404, { error: "missing fixture measurement" });
        return;
      }
      if (request.method === "DELETE" && !match[2]) {
        item.status = { id: 4, name: "Stopped" };
        if (item.is_oneoff) item.stop_time = Math.floor(Date.now() / 1000);
        response.writeHead(204);
        response.end();
        return;
      }
      if (request.method === "GET" && !match[2]) {
        json(response, 200, item);
        return;
      }
      if (request.method === "GET" && match[2]) {
        const timestamp = Math.max(
            item.start_time,
            Math.floor(Date.now() / 1000) - 1,
          ),
          common = {
            msm_id: item.id,
            prb_id: item.probeIds[0],
            type: item.type,
            af: item.af,
            timestamp,
            dst_addr: item.target_ip,
            src_addr: `198.51.100.${item.probeIds[0]}`,
          };
        if (item.errorOnly) {
          const failed = {
            ...common,
            error: { message: "fixture probe initialization failed" },
          };
          delete failed.dst_addr;
          delete failed.src_addr;
          json(response, 200, [failed]);
          return;
        }
        json(response, 200, [
          item.type === "ping"
            ? { ...common, sent: 3, rcvd: 0, min: -1, avg: -1, max: -1 }
            : {
                ...common,
                result: [
                  {
                    hop: 1,
                    result: [
                      { from: "192.0.2.10", rtt: 1.2 },
                      { from: "192.0.2.20", rtt: 1.7 },
                      { x: "*" },
                    ],
                  },
                  {
                    hop: 2,
                    result: [{ from: item.target_ip, rtt: 3.4 }, { x: "*" }],
                  },
                ],
              },
        ]);
        return;
      }
    }
    throw new Error(
      `unexpected fixture request ${request.method} ${url.pathname}`,
    );
  } catch (error) {
    providerErrors.push(error.message);
    json(response, 500, { error: "fixture failure" });
  }
});
await new Promise((resolve) => fake.listen(0, "127.0.0.1", resolve));
const providerPort = fake.address().port;
const socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`;
await writeFile(
  dir + "/atlas.json",
  JSON.stringify({
    enabled: true,
    baseURL: `http://127.0.0.1:${providerPort}`,
    apiKey: key,
    allowLoopbackHTTP: true,
  }),
  { mode: 0o600 },
);
const until = async (check, timeout = 30000) => {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = await check();
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error("Timed out waiting for isolated Atlas fixture");
};
try {
  child = spawn(
    process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
    [
      "-listen",
      `127.0.0.1:${port}`,
      "-data",
      dir + "/state",
      "-web",
      fileURLToPath(new URL("../dist", import.meta.url)),
      "-atlas-config",
      dir + "/atlas.json",
    ],
    {
      env: { ...process.env, IXP_SETUP_TOKEN: "atlas-fixture-token" },
      stdio: ["ignore", "ignore", "pipe"],
    },
  );
  await until(async () => {
    if (child.exitCode !== null)
      throw new Error("Atlas fixture manager exited");
    return fetch(base + "/api/setup")
      .then((response) => response.ok)
      .catch(() => false);
  }, 10000);
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
          setupToken: "atlas-fixture-token",
          name: "Atlas QA",
          email: "admin@example.test",
          password,
          ixpName: "Atlas Isolated Test",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const members = [];
  for (const [offset, name] of ["Atlas Network A", "Atlas Network B"].entries())
    members.push(
      await api("/resources/members", {
        name,
        asn: 64501 + offset,
        status: "active",
      }),
    );
  const dc = await api("/resources/locations", { name: "Atlas DC" }),
    sw = await api("/resources/switches", {
      name: "Atlas switch",
      locationId: dc.id,
    }),
    vlan = await api("/resources/vlans", {
      name: "Atlas shared LAN",
      tag: 100,
      ipv4Prefix: "192.0.2.0/24",
    });
  const connections = [];
  for (const [offset, member] of members.entries()) {
    const physical = await api("/resources/ports", {
      name: `port-${offset}`,
      switchId: sw.id,
      status: "active",
      speed: 1000,
    });
    connections.push(
      await api("/resources/connections", {
        name: `Atlas link ${offset}`,
        memberId: member.id,
        portId: physical.id,
        vlanId: vlan.id,
        ipv4: `192.0.2.${offset ? 20 : 10}`,
        status: "active",
      }),
    );
  }
  const go = async () => {
    await page.goto(base + "/#atlas");
    await page.reload();
    await page
      .getByRole("heading", { name: "RIPE Atlas", exact: true })
      .waitFor();
  };
  await go();
  await page.getByRole("button", { name: "探针快照", exact: true }).click();
  for (const member of members) {
    await page.locator("#atlas-probe-member").selectOption(member.id);
    const response = page.waitForResponse((value) =>
      value.url().endsWith("/api/atlas/probes/discover"),
    );
    await page
      .getByRole("button", { name: "发现 / 更新探针", exact: true })
      .click();
    assert.equal((await response).status(), 200);
    await page.getByText("有界部分探针", { exact: false }).waitFor();
  }
  assert.equal(measurements.size, 0);
  assert.ok(!JSON.stringify(await api("/atlas")).includes(key));
  console.log(
    "real fake-provider discovery, bounded snapshots and no browser credential exposure passed",
  );
  const startNow = async (dialog) => {
    const local = await page.evaluate(() => {
      const now = new Date();
      return new Date(now.getTime() - now.getTimezoneOffset() * 60000)
        .toISOString()
        .slice(0, 19);
    });
    await dialog.locator("#atlas-start").fill(local);
  };
  const proposal = async (
    name,
    matrix = false,
    publicResult = false,
    explicitStart = true,
  ) => {
    await go();
    await page
      .getByRole("button", { name: "新建测量提案", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.locator("#atlas-name").fill(name);
    assert.equal(
      await dialog
        .getByRole("checkbox", { name: /提议公开此测量的结果/ })
        .isChecked(),
      false,
    );
    if (matrix) {
      await dialog.locator("#atlas-mode").selectOption("matrix");
      await dialog.locator("#atlas-type").selectOption("traceroute");
      await dialog.locator("#atlas-vlan").selectOption(vlan.id);
      for (const [index, member] of members.entries()) {
        await dialog
          .getByRole("checkbox", { name: new RegExp(`^${member.name}`) })
          .check();
        const group = dialog.locator(`[data-atlas-participant="${member.id}"]`),
          probeId = index ? 201 : 101;
        await group
          .getByRole("checkbox", {
            name: `选择 Atlas 探针 ${probeId}`,
            exact: true,
          })
          .check();
        await group
          .locator(`#atlas-participant-target-${member.id}`)
          .selectOption(String(probeId));
      }
    } else {
      await dialog.locator("#atlas-source").selectOption(members[0].id);
      await dialog.locator("#atlas-target-member").selectOption(members[1].id);
      await dialog
        .getByRole("checkbox", { name: "选择 Atlas 探针 101", exact: true })
        .check();
      await dialog.locator("#atlas-target-type").selectOption("connection");
      await dialog
        .locator("#atlas-target-connection")
        .selectOption(connections[1].id);
    }
    if (publicResult)
      await dialog
        .getByRole("checkbox", { name: /提议公开此测量的结果/ })
        .check();
    if (explicitStart) await startNow(dialog);
    const response = page.waitForResponse(
      (value) =>
        value.url().endsWith("/api/atlas/runs") &&
        value.request().method() === "POST",
    );
    await dialog
      .getByRole("button", { name: "创建待审核提案", exact: true })
      .click();
    const result = await response;
    assert.equal(result.status(), 201);
    const run = await result.json();
    await dialog.waitFor({ state: "hidden" });
    await page.locator(`[data-atlas-run="${run.id}"]`).waitFor();
    return run;
  };
  const approve = async (run) => {
    const before = measurements.size;
    await page
      .getByRole("button", { name: "审核额度与范围", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.locator('input[name="ackCredits"]').check();
    if (run.isPublic) {
      assert.equal(
        await dialog.locator('input[name="ackPublic"]').isChecked(),
        false,
      );
      await dialog.locator('input[name="ackPublic"]').check();
    } else
      assert.equal(await dialog.locator('input[name="ackPublic"]').count(), 0);
    await dialog.locator("#atlas-approval-password").fill(password);
    const response = page.waitForResponse((value) =>
      value.url().endsWith(`/api/atlas/runs/${run.id}/approve`),
    );
    await dialog.getByRole("button", { name: "确认审批", exact: true }).click();
    assert.equal((await response).status(), 200);
    await dialog.waitFor({ state: "hidden" });
    assert.equal(measurements.size, before);
  };
  const execute = async (run) => {
    await page
      .getByRole("button", { name: "提交外部测量", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("checkbox").check();
    const response = page.waitForResponse((value) =>
      value.url().endsWith(`/api/atlas/runs/${run.id}/execute`),
    );
    await dialog
      .getByRole("button", { name: "确认提交外部测量", exact: true })
      .click();
    assert.equal((await response).status(), 202);
    await dialog.waitFor({ state: "hidden" });
  };
  const sync = async (run) => {
    const response = page.waitForResponse((value) =>
      value.url().endsWith(`/api/atlas/runs/${run.id}/refresh`),
    );
    await page
      .getByRole("button", { name: "同步 Atlas 状态与结果", exact: true })
      .click();
    assert.equal((await response).status(), 200);
  };
  const stop = async (run) => {
    await page
      .getByRole("button", { name: "请求停止 / 取消未发送项", exact: true })
      .click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("checkbox").check();
    const response = page.waitForResponse((value) =>
      value.url().endsWith(`/api/atlas/runs/${run.id}/stop`),
    );
    await dialog
      .getByRole("button", { name: "确认停止 / 取消意图", exact: true })
      .click();
    assert.equal((await response).status(), 202);
    await dialog.waitFor({ state: "hidden" });
    await until(async () => {
      const current = await api(`/atlas/runs/${run.id}`);
      return current.items.every((item) =>
        ["completed", "stopped", "cancelled", "failed", "archived"].includes(
          item.status,
        ),
      );
    });
  };
  if (process.env.ATLAS_REAPPROVAL_ONLY === "1") {
    const renewal = await proposal(
      "Expired partial approval fixture",
      true,
      false,
      false,
    );
    await approve(renewal);
    rejectNextCreate = true;
    await execute(renewal);
    const partial = await api(`/atlas/runs/${renewal.id}`);
    assert.equal(partial.status, "partial");
    assert.equal(partial.remainingCount, 1);
    assert.equal(
      partial.items.filter((item) => item.status === "failed").length,
      1,
    );
    assert.equal(measurements.size, 0);
    const launch = child.spawnargs.slice();
    const stopped = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGTERM");
    await stopped;
    // Age only the approval in our stopped, private test fixture; no production clock/API bypass exists.
    const statePath = dir + "/state/state.json",
      state = JSON.parse(await readFile(statePath, "utf8"));
    const stored = state.atlas.runs.find((item) => item.id === renewal.id);
    stored.approvedAt = new Date(Date.now() - 16 * 60 * 1000).toISOString();
    await writeFile(statePath, JSON.stringify(state), { mode: 0o600 });
    child = spawn(launch[0], launch.slice(1), {
      env: { ...process.env, IXP_SETUP_TOKEN: "atlas-fixture-token" },
      stdio: ["ignore", "ignore", "pipe"],
    });
    await until(async () => {
      if (child.exitCode !== null)
        throw new Error("Aged Atlas fixture failed startup");
      return fetch(base + "/api/setup")
        .then((response) => response.ok)
        .catch(() => false);
    }, 10000);
    assert.equal(
      (
        await context.request.post(base + "/api/login", {
          data: { email: "admin@example.test", password },
        })
      ).status(),
      200,
    );
    await go();
    await page.locator(`[data-atlas-run="${renewal.id}"]`).click();
    await page.getByText("原审批已过期", { exact: false }).waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "继续未发送项", exact: true })
        .count(),
      0,
    );
    await page
      .getByRole("button", { name: "重新批准剩余项", exact: true })
      .click();
    let renewalDialog = page.getByRole("dialog");
    assert.ok(
      (await renewalDialog.innerText()).includes("本次剩余 1 项估算上界"),
    );
    await renewalDialog.locator('input[name="ackCredits"]').check();
    await renewalDialog.locator("#atlas-approval-password").fill(password);
    const approved = page.waitForResponse((response) =>
      response.url().endsWith(`/api/atlas/runs/${renewal.id}/approve`),
    );
    await renewalDialog
      .getByRole("button", { name: "确认重新批准剩余项", exact: true })
      .click();
    assert.equal((await approved).status(), 200);
    await renewalDialog.waitFor({ state: "hidden" });
    const renewed = await api(`/atlas/runs/${renewal.id}`);
    assert.equal(renewed.resumeRequired, true);
    assert.equal(renewed.approvalExpired, false);
    assert.equal(renewed.remainingCount, 1);
    assert.ok(renewed.remainingEstimatedCredits < renewed.estimatedCredits);
    assert.equal(measurements.size, 0);
    assert.equal(
      await page
        .getByRole("button", { name: "提交外部测量", exact: true })
        .count(),
      0,
    );
    await page
      .getByRole("button", { name: "继续未发送项", exact: true })
      .click();
    renewalDialog = page.getByRole("dialog");
    await renewalDialog.getByRole("checkbox").check();
    const resumed = page.waitForResponse((response) =>
      response.url().endsWith(`/api/atlas/runs/${renewal.id}/resume`),
    );
    await renewalDialog
      .getByRole("button", { name: "确认仅继续未发送项", exact: true })
      .click();
    assert.equal((await resumed).status(), 202);
    await renewalDialog.waitFor({ state: "hidden" });
    await until(async () => {
      const record = await api(`/atlas/runs/${renewal.id}`);
      return (
        record.remainingCount === 0 &&
        record.items.some((item) => item.ownershipConfirmed)
      );
    });
    const final = await api(`/atlas/runs/${renewal.id}`);
    assert.equal(
      final.items.filter((item) => item.status === "failed").length,
      1,
    );
    assert.equal(measurements.size, 1);
    assert.equal(
      requests.filter(
        (request) =>
          request.method === "POST" && request.path === "/api/v2/measurements/",
      ).length,
      2,
    );
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({
      path: "/tmp/moedove-atlas-reapproval.png",
      fullPage: true,
    });
    await stop(renewal);
    assert.deepEqual(errors, []);
    assert.deepEqual(providerErrors, []);
    console.log(
      "expired approval reauthenticated for remaining frozen items; separate resume creates only unattempted work",
    );
    console.log(`Atlas reapproval fixture: ${dir}`);
  } else {
    const matrix = await proposal("Matrix traceroute fixture", true);
    assert.equal(matrix.mode, "matrix");
    assert.equal(matrix.measurementCount, 2);
    assert.equal(matrix.isPublic, false);
    assert.ok(matrix.estimatedCredits > 0);
    assert.equal(measurements.size, 0);
    await approve(matrix);
    await execute(matrix);
    await until(async () => {
      const run = await api(`/atlas/runs/${matrix.id}`);
      return run.items.every(
        (item) =>
          item.ownershipConfirmed && item.providerId > 0 && item.hasResults,
      );
    });
    await sync(matrix);
    await page.getByRole("table", { name: "Atlas 有向测量矩阵" }).waitFor();
    await page
      .getByRole("button", {
        name: /查看 Atlas Network A 到 Atlas Network B 的结果/,
      })
      .click();
    await page
      .getByRole("button", { name: /查看探针 101 的样本/ })
      .first()
      .click();
    let dialog = page.getByRole("dialog");
    const trace = await dialog.innerText();
    assert.ok(
      trace.includes("192.0.2.10") &&
        trace.includes("192.0.2.20") &&
        trace.includes("超时"),
    );
    assert.ok(!trace.includes("0 ms"));
    await page.screenshot({
      path: "/tmp/moedove-atlas-traceroute-details.png",
      fullPage: true,
    });
    await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    assert.equal(
      [...measurements.values()].filter((item) => item.is_public).length,
      0,
    );
    console.log(
      "private matrix, explicit cost approval/execute and per-hop multi-response/timeout preservation passed",
    );
    await stop(matrix);
    await page
      .getByRole("button", { name: "刷新控制台记录", exact: true })
      .click();
    await page
      .getByRole("button", { name: "清理终态结果快照", exact: true })
      .waitFor();
    await page
      .getByRole("button", { name: "清理终态结果快照", exact: true })
      .click();
    dialog = page.getByRole("dialog");
    await dialog
      .locator('input[name="reason"]')
      .fill("Completed local fixture retention");
    await dialog.getByRole("checkbox").check();
    await dialog.getByRole("button", { name: "确认清理", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    await page.getByText("结果快照已清理", { exact: false }).first().waitFor();
    const ping = await proposal("Explicit public ping fixture", false, true);
    const denied = await call(`/atlas/runs/${ping.id}/approve`, {
      checksum: ping.checksum,
      acknowledgeCredits: true,
      acknowledgePublic: false,
      currentPassword: password,
    });
    assert.ok(denied.status >= 400);
    await approve(ping);
    await execute(ping);
    await until(async () => {
      const run = await api(`/atlas/runs/${ping.id}`);
      return run.items[0].hasResults && run.items[0].ownershipConfirmed;
    });
    await sync(ping);
    await page.getByRole("cell", { name: "0 / 3", exact: true }).waitFor();
    await page
      .getByRole("button", { name: /查看探针 101 的样本/ })
      .first()
      .click();
    dialog = page.getByRole("dialog");
    assert.ok((await dialog.innerText()).includes("未提供"));
    assert.ok(!(await dialog.innerText()).includes("0 ms"));
    await dialog.getByRole("button", { name: "关闭", exact: true }).click();
    const publicRows = [...measurements.values()].filter(
      (item) => item.is_public,
    );
    assert.equal(publicRows.length, 1);
    await stop(ping);
    console.log(
      "public results require explicit approval; lost ping replies remain missing, never zero latency",
    );
    const uncertain = await proposal("Uncertain creation fixture");
    await approve(uncertain);
    const before = measurements.size;
    failNextCreate = true;
    await execute(uncertain);
    await until(
      async () =>
        (await api(`/atlas/runs/${uncertain.id}`)).status === "uncertain",
    );
    await page.getByText("提交结果不确定", { exact: true }).first().waitFor();
    assert.equal(
      await page
        .getByRole("button", { name: "提交外部测量", exact: true })
        .count(),
      0,
    );
    assert.equal(
      await page
        .getByRole("button", { name: "继续未发送项", exact: true })
        .count(),
      0,
    );
    await sync(uncertain);
    assert.equal(measurements.size, before + 1);
    allowFind = true;
    await sync(uncertain);
    await until(
      async () =>
        (await api(`/atlas/runs/${uncertain.id}`)).items[0].ownershipConfirmed,
    );
    assert.equal(measurements.size, before + 1);
    await page.getByText("目标未观测", { exact: true }).waitFor();
    await stop(uncertain);
    console.log(
      "uncertain creation is never reposted; safe ownership reconciliation enables owned stop only",
    );
    const untouched = await proposal("Cancel unsent fixture");
    const beforeCancel = measurements.size;
    await stop(untouched);
    assert.equal(measurements.size, beforeCancel);
    console.log("unsent proposal cancellation creates no external measurement");
    for (const role of ["operator", "viewer"]) {
      await api("/resources/users", {
        name: `Atlas ${role}`,
        email: `${role}@example.test`,
        role,
        password,
      });
      const restricted = await browser.newContext();
      assert.equal(
        (
          await restricted.request.post(base + "/api/login", {
            data: { email: `${role}@example.test`, password },
          })
        ).status(),
        200,
      );
      const tab = await restricted.newPage();
      await tab.goto(base + "/#atlas");
      await tab
        .getByRole("heading", { name: "RIPE Atlas", exact: true })
        .waitFor();
      assert.equal(
        await tab
          .getByRole("button", { name: "提交外部测量", exact: true })
          .count(),
        0,
      );
      assert.equal(
        await tab
          .getByRole("button", { name: "审核额度与范围", exact: true })
          .count(),
        0,
      );
      if (role === "viewer")
        assert.equal(
          await tab
            .getByRole("button", { name: "新建测量提案", exact: true })
            .count(),
          0,
        );
      assert.equal(
        (
          await call(
            `/atlas/runs/${uncertain.id}/execute`,
            { checksum: uncertain.checksum },
            "POST",
            restricted,
          )
        ).status,
        403,
      );
      await restricted.close();
    }
    await go();
    await page.locator(`[data-atlas-run="${ping.id}"]`).click();
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({
      path: "/tmp/moedove-atlas-desktop.png",
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await page.screenshot({
      path: "/tmp/moedove-atlas-mobile.png",
      fullPage: true,
    });
    await page.getByRole("button", { name: "探针快照", exact: true }).click();
    await page.locator("#atlas-probe-member").selectOption(members[0].id);
    await page
      .getByRole("button", { name: "清除探针缓存", exact: true })
      .click();
    dialog = page.getByRole("dialog");
    await dialog
      .locator('input[name="reason"]')
      .fill("Remove finished test probe snapshot");
    await dialog.getByRole("checkbox").check();
    await dialog
      .getByRole("button", { name: "确认清除探针缓存", exact: true })
      .click();
    await dialog.waitFor({ state: "hidden" });
    await page.getByText("没有缓存的可用探针", { exact: true }).waitFor();
    assert.deepEqual(errors, []);
    assert.deepEqual(providerErrors, []);
    assert.ok(requests.every((request) => request.path.startsWith("/api/v2/")));
    assert.ok(!JSON.stringify(await api("/atlas")).includes(key));
    console.log(
      "staff roles, retention, no secret exposure and 390px layout passed",
    );
    console.log(`Atlas fake-provider fixture: ${dir}`);
  }
} finally {
  await browser?.close();
  if (child && child.exitCode === null) {
    const exited = new Promise((resolve) => child.once("exit", resolve));
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(timer);
  }
  await new Promise((resolve) => fake.close(resolve));
  fake.closeAllConnections?.();
}
