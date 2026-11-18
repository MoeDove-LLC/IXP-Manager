// Real UI and synthetic local data. SMTP is exclusively a namespace-loopback fixture.
import assert from "node:assert/strict";
import net from "node:net";
import path from "node:path";
import { tmpdir } from "node:os";
import { spawn } from "node:child_process";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const repo = fileURLToPath(new URL("../..", import.meta.url));
const dir = await mkdtemp(path.join(tmpdir(), "patch-mail-browser-"));
const password = "Patch-notification-browser-fixture-123", messages = [], sockets = new Set();
const until = async (check, label) => {
  for (let i = 0; i < 200; i++) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 100)); }
  assert.fail(label);
};
const freePort = async () => {
  const server = net.createServer(); await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const port = server.address().port; await new Promise(resolve => server.close(resolve)); return port;
};
const smtp = net.createServer(socket => {
  sockets.add(socket); socket.on("close", () => sockets.delete(socket)); socket.on("error", () => {});
  socket.setEncoding("utf8"); socket.write("220 isolated.test ESMTP\r\n");
  let buffer = "", data = false, lines = [], recipients = [];
  socket.on("data", chunk => {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      const line = buffer.slice(0, newline).replace(/\r$/, ""); buffer = buffer.slice(newline + 1);
      if (data) {
        if (line === ".") { data = false; messages.push({ recipients, raw: lines.join("\r\n") }); lines = []; socket.write("250 accepted by isolated fixture\r\n"); }
        else lines.push(line.startsWith("..") ? line.slice(1) : line);
      } else if (/^(EHLO|HELO)/i.test(line)) socket.write("250 isolated.test\r\n");
      else if (/^MAIL FROM:/i.test(line)) { recipients = []; socket.write("250 sender ok\r\n"); }
      else if (/^RCPT TO:/i.test(line)) {
        const email = line.match(/<([^>]+)>/)?.[1] ?? ""; recipients.push(email);
        socket.write(email === "reject@example.test" ? "550 fixture rejects this recipient\r\n" : "250 recipient ok\r\n");
      } else if (/^DATA$/i.test(line)) { data = true; lines = []; socket.write("354 send data\r\n"); }
      else if (/^QUIT$/i.test(line)) socket.end("221 done\r\n");
      else socket.write("250 ok\r\n");
    }
  });
});
let child, browser, stderr = "";
const stop = async () => {
  if (!child || child.exitCode !== null) return;
  const running = child, exited = new Promise(resolve => running.once("exit", resolve));
  running.kill("SIGTERM"); const timer = setTimeout(() => running.kill("SIGKILL"), 5000); await exited; clearTimeout(timer);
};
try {
  await new Promise(resolve => smtp.listen(0, "127.0.0.1", resolve));
  const port = await freePort(), base = `http://127.0.0.1:${port}`;
  const smtpPath = path.join(dir, "smtp.json");
  await writeFile(smtpPath, JSON.stringify({ enabled: true, host: "127.0.0.1", port: smtp.address().port, mode: "plain", allowLoopbackPlain: true, from: "ixp@example.test", baseURL: base }), { mode: 0o600 });
  const start = async enabled => {
    stderr = "";
    child = spawn(process.env.TEST_SERVER_BIN || path.join(repo, ".cache/patch-notifications-service/ixp-manager"), ["-listen", `127.0.0.1:${port}`, "-data", path.join(dir, "data"), "-web", path.join(repo, "web/dist"), ...(enabled ? ["-smtp-config", smtpPath] : [])], { env: { ...process.env, IXP_SETUP_TOKEN: "patch-mail-setup" }, stdio: ["ignore", "ignore", "pipe"] });
    child.stderr.on("data", value => { stderr = (stderr + value).slice(-4000); });
    await until(async () => {
      if (child.exitCode !== null) throw new Error(`fixture service stopped: ${stderr}`);
      return fetch(base + "/api/setup").then(response => response.ok).catch(() => false);
    }, "local service startup timed out");
  };
  await start(true);
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const admin = await browser.newContext({ viewport: { width: 1440, height: 1000 } }), page = await admin.newPage(), errors = [];
  page.setDefaultTimeout(10000); page.on("pageerror", error => errors.push(error.message));
  const setup = await admin.request.post(base + "/api/setup", { data: { setupToken: "patch-mail-setup", name: "配线通知管理员", email: "admin@example.test", password, ixpName: "MoeDove 配线通知测试", asn: 64500 } });
  assert.equal(setup.status(), 200, await setup.text());
  const api = async (ctx, route, data, method = data ? "POST" : "GET") => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + route, { method, headers: { "X-CSRF-Token": session.csrfToken || "" }, ...(data ? { data } : {}) });
    assert.ok(response.ok(), `${route}: ${response.status()} ${await response.text()}`); return response.json();
  };
  await api(admin, "/settings", { name: "MoeDove 配线通知测试", asn: 64500, supportEmail: "support@example.test" }, "PUT");
  const member = await api(admin, "/resources/members", { name: "当前成员甲", asn: 64501, status: "active", nocEmail: "noc@example.test", email: "member@example.test" });
  const location = await api(admin, "/resources/locations", { name: "合成机房" });
  const rack = await api(admin, "/resources/racks", { name: "合成机架", locationId: location.id, units: 42 });
  const sw = await api(admin, "/resources/switches", { name: "合成交换机", locationId: location.id, rackId: rack.id });
  const physical = await api(admin, "/resources/ports", { name: "Ethernet fixture", switchId: sw.id, memberId: member.id, status: "active", speed: 10000 });
  const panel = await api(admin, "/resources/patch-panels", { name: "合成配线架", rackId: rack.id, portCount: 12 });
  const portRow = await api(admin, "/resources/patch-ports", { name: "合成光纤 1", panelId: panel.id, number: 1 });
  const patchPort = async () => (await api(admin, "/patching/ports")).items.find(item => item.id === portRow.id);
  const transition = async (action, extra = {}) => api(admin, `/patching/ports/${portRow.id}/transition`, { action, revision: (await patchPort()).revision, reason: "Synthetic browser notification fixture", ...extra });
  const upload = async (name, visibility, content) => {
    const session = await (await admin.request.get(base + "/api/session")).json();
    const response = await admin.request.post(base + "/api/documents", { headers: { "X-CSRF-Token": session.csrfToken }, multipart: { file: { name, mimeType: "text/plain", buffer: Buffer.from(content) }, visibility, memberId: member.id } });
    assert.equal(response.status(), 201, await response.text()); return response.json();
  };
  const document = await upload("授权工单.txt", "member", "CURRENT AUTHORIZED WORK ORDER");
  const privateDoc = await upload("内部私有工单.txt", "staff", "PRIVATE-STAFF-NEVER-MAIL");
  await transition("reserve", { memberId: member.id, switchPortId: physical.id, circuitId: "FIXTURE-CIRCUIT", documentIds: [document.id, privateDoc.id] });
  const loa = await api(admin, `/patching/ports/${portRow.id}/loas`, { revision: (await patchPort()).revision, reason: "Synthetic LOA for isolated browser test", expiresAt: new Date(Date.now() + 86400000).toISOString() });
  const endpoint = base + `/api/patching/ports/${portRow.id}/notifications`;
  const go = async () => { await page.goto(base + `/#patching?id=${portRow.id}`); await page.getByRole("heading", { name: "合成光纤 1 · 配线详情", exact: true }).waitFor(); };
  await go(); await page.getByRole("button", { name: "配线通知邮件", exact: true }).click();
  const dialog = page.getByRole("dialog"), review = page.getByRole("region", { name: "配线通知发送预览", exact: true });
  await dialog.locator("#patch-mail-body").waitFor();
  assert.equal(await dialog.locator("#patch-mail-to").inputValue(), "noc@example.test");
  assert.equal(await dialog.locator("#patch-mail-bcc").inputValue(), "support@example.test");
  assert.equal(await dialog.getByText("内部私有工单.txt", { exact: true }).count(), 0);
  assert.equal(messages.length, 0, "opening the composer sent mail");
  await dialog.locator("#patch-mail-kind").selectOption("connect");
  await until(async () => (await dialog.locator("#patch-mail-loa").inputValue()) === loa.id, "connect did not explicitly default its valid LOA");
  await dialog.locator("#patch-mail-loa").selectOption("");
  assert.equal(await dialog.locator("#patch-mail-loa").inputValue(), "");
  await dialog.locator("#patch-mail-kind").selectOption("loa");
  await until(async () => (await dialog.locator("#patch-mail-loa").inputValue()) === loa.id, "LOA notification did not select its required PDF");
  await dialog.locator("#patch-mail-kind").selectOption("info");
  await until(async () => (await dialog.locator("#patch-mail-kind").inputValue()) === "info" && !(await dialog.locator("#patch-mail-subject").isDisabled()), "info template did not load");
  await dialog.locator("#patch-mail-to").fill("noc@example.test, engineer@example.test");
  await dialog.locator("#patch-mail-cc").fill("cc@example.test");
  await dialog.locator("#patch-mail-bcc").fill("hidden@example.test");
  await dialog.locator("#patch-mail-subject").fill("人工审阅配线通知");
  const text = "保留手工正文\n<img src=x onerror=window.__patchMailInjected=true>\n请按工单核对机房信息。";
  await dialog.locator("#patch-mail-body").fill(text);
  await dialog.locator("#patch-mail-loa").selectOption(loa.id);
  await dialog.getByRole("checkbox", { name: /授权工单.txt/ }).check();
  await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click();
  await review.getByText("人工审阅配线通知", { exact: true }).waitFor();
  assert.equal(await review.locator("pre").innerText(), text);
  assert.equal(await page.evaluate(() => window.__patchMailInjected), undefined);
  await review.getByText(/LOA.*application\/pdf|application\/pdf/).waitFor();
  await review.getByText(/授权工单.txt/).waitFor();
  assert.equal(await review.getByRole("button", { name: "确认加入发送队列", exact: true }).isDisabled(), true);
  assert.equal(messages.length, 0, "preview sent mail");
  assert.equal((await api(admin, "/notifications")).items.length, 0, "preview added outbox entries");
  console.log("PASS explicit composer, editable To/CC/BCC, real attachment preview and no preview delivery");

  await transition("request-connect");
  await review.getByRole("checkbox").check();
  const conflict = page.waitForResponse(response => response.url() === endpoint + "/queue");
  await review.getByRole("button", { name: "确认加入发送队列", exact: true }).click();
  assert.equal((await conflict).status(), 409); await review.waitFor({ state: "hidden" });
  assert.equal(await dialog.locator("#patch-mail-body").inputValue(), text);
  assert.equal(await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).isDisabled(), true);
  await dialog.getByRole("button", { name: "重新读取，保留编辑", exact: true }).click();
  await until(() => dialog.getByRole("button", { name: "预览通知邮件", exact: true }).isEnabled(), "explicit reload did not recover stale composer");
  // The reservation remains current while awaiting connection; the reviewer
  // explicitly keeps the same valid PDF and authorized work order.
  assert.equal(await dialog.locator("#patch-mail-loa").inputValue(), loa.id);
  await page.clock.install({ time: new Date() });
  await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click(); await review.waitFor();
  await page.clock.fastForward(601000);
  await review.waitFor({ state: "hidden" }); await page.clock.setSystemTime(new Date());
  assert.equal(await dialog.locator("#patch-mail-body").inputValue(), text);
  await dialog.getByText(/审阅已过期/).waitFor();
  await dialog.getByRole("button", { name: "重新读取，保留编辑", exact: true }).click();
  await until(() => dialog.getByRole("button", { name: "预览通知邮件", exact: true }).isEnabled(), "expiry reload did not recover preview");
  await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click(); await review.waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "patch notification dialog overflowed 390px");
  assert.ok(await dialog.evaluate(element => element.scrollWidth <= element.clientWidth), "notification content overflowed inside the dialog");
  assert.ok((await dialog.boundingBox()).width >= 330, "mobile composer width collapsed");
  await review.scrollIntoViewIfNeeded();
  if (process.env.PATCH_MAIL_SCREENSHOT) await page.screenshot({ path: process.env.PATCH_MAIL_SCREENSHOT, animations: "disabled" });
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log("PASS stale allocation and expiry preserve the draft, require fresh review and fit 390px");

  let submitted;
  await page.route(endpoint + "/queue", async route => { submitted = route.request().postDataJSON(); await route.fetch(); await route.abort("failed"); }, { times: 1 });
  await review.getByRole("checkbox").check(); await review.getByRole("button", { name: "确认加入发送队列", exact: true }).click();
  await dialog.getByText(/未取得本次提交结果/).waitFor();
  const queuedCount = (await api(admin, "/notifications")).items.length;
  assert.ok(queuedCount > 0);
  await review.getByRole("checkbox").check();
  const retry = page.waitForRequest(request => request.url() === endpoint + "/queue");
  await review.getByRole("button", { name: "重试本次确认", exact: true }).click();
  assert.deepEqual((await retry).postDataJSON(), submitted, "uncertain outcome changed the frozen retry request");
  await dialog.getByText("已找到本次排队结果，没有重复创建邮件。", { exact: true }).waitFor();
  assert.equal((await api(admin, "/notifications")).items.length, queuedCount);
  await until(() => messages.length > 0, "loopback SMTP did not receive the explicitly confirmed notification");
  assert.deepEqual([...new Set(messages.flatMap(entry => entry.recipients))].sort(), ["cc@example.test", "engineer@example.test", "hidden@example.test", "noc@example.test"], "SMTP recipients differed from the explicitly reviewed To/CC/BCC");
  assert.ok(messages.every(entry => !/^bcc:/im.test(entry.raw)), "BCC header leaked into a delivered copy");
  assert.ok(messages.every(entry => !entry.raw.split("\r\n\r\n")[0].includes("hidden@example.test")), "BCC address leaked into headers");
  assert.ok(messages.some(entry => entry.recipients.includes("hidden@example.test")), "explicit BCC was not in the SMTP envelope");
  assert.ok(messages.some(entry => entry.raw.includes("application/pdf") && entry.raw.includes("JVBERi0")), "real LOA PDF bytes were not attached");
  assert.ok(messages.some(entry => entry.raw.includes(Buffer.from("CURRENT AUTHORIZED WORK ORDER").toString("base64"))), "authorized document bytes were not attached");
  assert.ok(messages.every(entry => !entry.raw.includes(Buffer.from("PRIVATE-STAFF-NEVER-MAIL").toString("base64"))));
  console.log("PASS queue lost-response recovery is idempotent and BCC remains private in loopback SMTP");

  await dialog.getByRole("button", { name: "撰写另一封通知", exact: true }).click();
  await dialog.locator("#patch-mail-to").fill("reject@example.test"); await dialog.locator("#patch-mail-cc").fill(""); await dialog.locator("#patch-mail-bcc").fill("");
  await dialog.locator("#patch-mail-subject").fill("合成投递错误");
  await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click(); await review.waitFor();
  await review.getByRole("checkbox").check(); await review.getByRole("button", { name: "确认加入发送队列", exact: true }).click();
  await dialog.getByRole("region", { name: "配线通知排队结果", exact: true }).waitFor();
  await until(async () => (await api(admin, "/notifications")).items.some(item => item.subject === "合成投递错误" && item.attempts > 0 && item.lastError), "SMTP failure did not expose a bounded delivery error");
  await dialog.getByRole("link", { name: "查看发件箱投递状态", exact: true }).click();
  await page.getByRole("heading", { name: "通知与后台任务", exact: true }).waitFor();
  await page.getByText("投递未完成，请检查传输状态。", { exact: true }).waitFor();
  console.log("PASS queued result links to actual delivery errors without claiming success");

  for (const role of ["operator", "viewer", "member"]) {
    await api(admin, "/resources/users", { name: `${role} QA`, email: `${role}@example.test`, password, role, ...(role === "member" ? { memberId: member.id } : {}) });
    const ctx = await browser.newContext(), nonAdmin = await ctx.newPage(), requests = [];
    const login = await ctx.request.post(base + "/api/login", { data: { email: `${role}@example.test`, password } }); assert.equal(login.status(), 200);
    nonAdmin.on("request", request => requests.push(request.url()));
    await nonAdmin.goto(base + `/#patching?id=${portRow.id}`); await nonAdmin.getByRole("heading", { name: "合成光纤 1 · 配线详情", exact: true }).waitFor();
    assert.equal(await nonAdmin.getByRole("button", { name: "配线通知邮件", exact: true }).count(), 0);
    assert.ok(!requests.some(url => url.startsWith(endpoint)));
    assert.equal((await ctx.request.get(endpoint + "?type=info")).status(), 403);
    await ctx.close();
  }
  await transition("connect");
  await go(); await page.getByRole("button", { name: "配线通知邮件", exact: true }).click();
  await dialog.locator("#patch-mail-body").waitFor();
  await dialog.locator("#patch-mail-kind").selectOption("loa");
  await dialog.getByText("此类通知需要有效 LOA，请先签发并明确选择授权文件。", { exact: true }).waitFor();
  assert.equal(await dialog.locator(`#patch-mail-loa option[value="${loa.id}"]`).count(), 0, "connected stale LOA became a valid mail attachment");
  await dialog.getByRole("button", { name: "关闭", exact: true }).click();
  for (const [action, title] of [["suspend", "暂停会员拆线通知"], ["close", "关闭会员拆线通知"]]) {
    await api(admin, `/members/${member.id}/${action}`, { reason: "Synthetic inactive-member cease notification fixture" });
    await go(); await page.getByRole("button", { name: "配线通知邮件", exact: true }).click();
    await dialog.locator("#patch-mail-body").waitFor();
    // Information remains reviewable while the existing allocation is retained.
    await dialog.getByRole("checkbox", { name: /授权工单.txt/ }).check();
    await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click(); await review.waitFor();
    await review.getByText(/授权工单.txt/).waitFor();
    await review.getByRole("button", { name: "返回编辑", exact: true }).click();
    await dialog.locator("#patch-mail-kind").selectOption("cease");
    await until(async () => (await dialog.locator("#patch-mail-kind").inputValue()) === "cease" && !(await dialog.locator("#patch-mail-subject").isDisabled()), "inactive cease template did not load");
    await dialog.locator("#patch-mail-subject").fill(title);
    await dialog.getByRole("checkbox", { name: /授权工单.txt/ }).check();
    await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click(); await review.waitFor();
    await review.getByRole("checkbox").check(); await review.getByRole("button", { name: "确认加入发送队列", exact: true }).click();
    await dialog.getByRole("region", { name: "配线通知排队结果", exact: true }).waitFor();
    await until(async () => (await api(admin, "/notifications")).items.some(item => item.subject === title && item.status === "sent"), "inactive cease did not reach isolated SMTP");
    await dialog.getByRole("button", { name: "完成", exact: true }).click();
  }
  await api(admin, `/members/${member.id}/activate`, { reason: "Restore synthetic member for disabled-preview check" });
  console.log("PASS connected stale LOA exclusion and retained-allocation info/cease for suspended and closed members");
  await stop(); await start(false);
  assert.equal((await admin.request.post(base + "/api/login", { data: { email: "admin@example.test", password } })).status(), 200);
  // The fixture logs in through the API after restart; remount the app to read
  // the new session's CSRF token instead of reusing the pre-restart JS module.
  await go(); await page.reload();
  await page.getByRole("button", { name: "配线通知邮件", exact: true }).click();
  await dialog.getByText("邮件投递未启用", { exact: true }).waitFor();
  const disabledPreview = page.waitForResponse(response => response.url() === endpoint + "/review");
  await dialog.getByRole("button", { name: "预览通知邮件", exact: true }).click();
  const disabledResponse = await disabledPreview;
  assert.equal(disabledResponse.status(), 200, await disabledResponse.text());
  const disabledReview = await disabledResponse.json();
  assert.ok(Date.parse(disabledReview.expiresAt) > await page.evaluate(() => Date.now()), "virtual fixture clock unexpectedly remained ahead after restart");
  await review.waitFor();
  assert.equal(await review.getByRole("button", { name: "确认加入发送队列", exact: true }).isDisabled(), true);
  await review.getByText("邮件投递尚未启用。可以预览，管理员配置 SMTP 后才能加入发送队列。", { exact: true }).waitFor();
  assert.deepEqual(errors, []);
  console.log("PASS administrator-only entry and SMTP-disabled preview; patch notification browser suite passed");
} finally {
  if (browser) await browser.close(); await stop();
  for (const socket of sockets) socket.destroy();
  await new Promise(resolve => smtp.close(resolve));
  await rm(dir, { recursive: true, force: true });
}
