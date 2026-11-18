// Isolated fake loopback SMTP only. Never contacts an external mail server.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, writeFile, readFile, mkdir, realpath } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createHmac } from "node:crypto";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const binary = process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  workspace = await mkdtemp("/tmp/moedove-mail-browser-"),
  web = fileURLToPath(new URL("../dist", import.meta.url));
const password = "Mail-fixture-password-123",
  newPassword = "Recovered-fixture-password-456",
  smtpMessages = [],
  rejected = new Set(),
  sockets = new Set();
let processHandle = null,
  base = "",
  browser;
const serverPorts = new Map();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const until = async (fn, timeout = 15000) => {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const result = await fn();
    if (result) return result;
    await sleep(150);
  }
  throw new Error("Timed out waiting for local test state");
};
const smtp = net.createServer((socket) => {
  sockets.add(socket);
  socket.on("close", () => sockets.delete(socket));
  socket.setEncoding("utf8");
  socket.write("220 isolated.test ESMTP\r\n");
  let buffer = "",
    data = false,
    lines = [],
    recipient = "";
  socket.on("data", (chunk) => {
    buffer += chunk;
    let newline;
    while ((newline = buffer.indexOf("\n")) >= 0) {
      let line = buffer.slice(0, newline).replace(/\r$/, "");
      buffer = buffer.slice(newline + 1);
      if (data) {
        if (line === ".") {
          data = false;
          const raw = lines.join("\r\n");
          const encoded = raw
            .split("\r\n\r\n")
            .slice(1)
            .join("\r\n\r\n")
            .replace(/\s/g, "");
          smtpMessages.push({
            to: recipient,
            text: Buffer.from(encoded, "base64").toString("utf8"),
          });
          lines = [];
          socket.write("250 accepted by isolated fixture\r\n");
        } else lines.push(line.startsWith("..") ? line.slice(1) : line);
        continue;
      }
      if (/^(EHLO|HELO)/i.test(line)) socket.write("250 isolated.test\r\n");
      else if (/^MAIL FROM:/i.test(line)) socket.write("250 sender ok\r\n");
      else if (/^RCPT TO:/i.test(line)) {
        recipient = line.match(/<([^>]+)>/)?.[1] ?? "";
        socket.write(
          rejected.has(recipient)
            ? "550 fixture rejects this recipient\r\n"
            : "250 recipient ok\r\n",
        );
      } else if (/^DATA$/i.test(line)) {
        data = true;
        lines = [];
        socket.write("354 send data\r\n");
      } else if (/^QUIT$/i.test(line)) {
        socket.end("221 done\r\n");
      } else socket.write("250 ok\r\n");
    }
  });
});
await new Promise((r) => smtp.listen(0, "127.0.0.1", r));
const smtpPort = smtp.address().port;
const freePort = async () => {
  const s = net.createServer();
  await new Promise((r) => s.listen(0, "127.0.0.1", r));
  const p = s.address().port;
  await new Promise((r) => s.close(r));
  return p;
};
const stop = async () => {
  if (!processHandle) return;
  const child = processHandle;
  processHandle = null;
  const exited = new Promise((r) => child.once("exit", r));
  child.kill("SIGTERM");
  const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
  await exited;
  clearTimeout(timer);
};
const start = async (label, enabled) => {
  const port = serverPorts.get(label) ?? (await freePort());
  serverPorts.set(label, port);
  base = `http://127.0.0.1:${port}`;
  const data = `${workspace}/${label}`;
  await mkdir(data, { recursive: true, mode: 0o700 });
  const args = ["-listen", `127.0.0.1:${port}`, "-data", data, "-web", web];
  if (enabled) {
    const config = `${workspace}/${label}-smtp.json`;
    await writeFile(
      config,
      JSON.stringify({
        enabled: true,
        host: "127.0.0.1",
        port: smtpPort,
        mode: "plain",
        allowLoopbackPlain: true,
        from: "ixp@example.test",
        baseURL: base,
      }),
      { mode: 0o600 },
    );
    args.push("-smtp-config", config);
  }
  processHandle = spawn(binary, args, {
    env: { ...process.env, IXP_SETUP_TOKEN: "mail-fixture-setup-token" },
    stdio: ["ignore", "ignore", "pipe"],
  });
  let failure = "";
  processHandle.stderr.on("data", (d) => {
    failure += d;
  });
  await until(async () => {
    if (processHandle.exitCode != null)
      throw new Error(
        "Local Go test server exited: " +
          failure.replace(/token[^\n]*/gi, "token <redacted>"),
      );
    try {
      return (await fetch(base + "/api/setup")).ok;
    } catch {
      return false;
    }
  });
  return data;
};
const json = async (
  page,
  path,
  body,
  method = body ? "POST" : "GET",
  check = true,
) =>
  page.evaluate(
    async ({ path, body, method, check }) => {
      const s = await (await fetch("/api/session")).json();
      // Test fixture setup deliberately reads a current baseline; production editors never do this before saving.
      const fixtureHeaders = {};
      if (
        ["PUT", "DELETE"].includes(method) &&
        /^\/resources\/(?!provisioning-profiles\/)[^/]+\/[^/]+$/.test(path)
      ) {
        const baselineResponse = await fetch(`/api${path}`);
        if (baselineResponse.ok) {
          const baseline = await baselineResponse.json();
          if (baseline._etag)
            fixtureHeaders["If-Match"] = JSON.stringify(baseline._etag);
        }
        if (body && typeof body === "object" && !Array.isArray(body)) {
          const { _etag, ...values } = body;
          body = values;
        }
      }
      const r = await fetch(`/api${path}`, {
        method,
        headers: {
          ...fixtureHeaders,
          "Content-Type": "application/json",
          "X-CSRF-Token": s.csrfToken ?? "",
        },
        body: body ? JSON.stringify(body) : undefined,
      });
      const d = await r.json();
      if (check && !r.ok) throw new Error(d.error);
      return check ? d : { status: r.status, data: d };
    },
    { path, body, method, check },
  );
const setup = async (page) => {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("Local Mail Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill("mail-fixture-setup-token");
  await page.getByLabel("管理员姓名").fill("测试管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
};
const login = async (page, email, pwd = password, code) => {
  await page.goto(base);
  await page.getByLabel("邮箱地址").fill(email);
  await page.getByLabel("密码", { exact: true }).fill(pwd);
  await page.getByRole("button", { name: "登录控制台", exact: true }).click();
  if (code) {
    await page.getByLabel("验证器代码或恢复码", { exact: true }).fill(code);
    await page.getByRole("button", { name: "登录控制台", exact: true }).click();
  }
  await page.locator("main").waitFor();
};
const go = async (page, route) => {
  await page.goto(`${base}/#${route}`);
  await page.locator("main").waitFor();
};
const linkFor = async (to, route) =>
  until(
    () =>
      smtpMessages
        .findLast((m) => m.to === to && m.text.includes(`#${route}?token=`))
        ?.text.match(
          new RegExp(`http://[^\\s]+/#${route}\\?token=[^\\s]+`),
        )?.[0],
  );
const createInvite = async (page, email, memberId) => {
  await go(page, "invitations");
  await page.getByRole("button", { name: "邀请用户", exact: true }).click();
  await page.getByLabel("受邀邮箱", { exact: true }).fill(email);
  await page.getByLabel("成员网络", { exact: true }).selectOption(memberId);
  await page.getByLabel("当前密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "发送成员邀请", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
};

const checkSchedulerHealth = async (page) => {
  await go(page, "notifications");
  const panel = page.getByRole("region", { name: "后台调度", exact: true });
  const refresh = page.getByRole("button", { name: "刷新", exact: true });
  await panel.getByText("运行中", { exact: true }).waitFor();
  const live = await json(page, "/jobs");
  assert.equal(live.scheduler.status, "running");
  assert.ok(live.scheduler.lastCompletedAt, "the real manager must have completed a background cycle");
  assert.equal(live.scheduler.stallAfterSeconds, 600);

  await page.route("**/api/jobs", async (route) => {
    const response = await route.fetch();
    const observation = await response.json();
    const old = new Date(Date.parse(observation.checkedAt) - 11 * 60_000).toISOString();
    await route.fulfill({
      response,
      json: {
        ...observation,
        scheduler: {
          ...observation.scheduler, status: "stalled", startedAt: old,
          lastCompletedAt: old, cycleStartedAt: old, currentPhase: "mail",
        },
        items: observation.items.map((job) => job.kind === "cleanup"
          ? { ...job, status: "running", health: "lease-expired", leaseUntil: old }
          : { ...job, health: "overdue", nextRunAt: old }),
      },
    });
  }, { times: 1 });
  await refresh.click();
  await panel.getByText("调度停滞", { exact: true }).waitFor();
  const cleanup = page.locator('[data-job-kind="cleanup"]');
  await cleanup.getByText("执行租约已过期", { exact: true }).waitFor();
  assert.equal(await cleanup.getByRole("button", { name: "提交执行", exact: true }).isEnabled(), true);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "scheduler panel must fit 390px viewport");
  if (process.env.SCHEDULER_SCREENSHOT) await panel.screenshot({ path: process.env.SCHEDULER_SCREENSHOT });

  await page.route("**/api/jobs", (route) => route.fulfill({ status: 503, json: { error: "计划任务暂不可用（测试）" } }), { times: 1 });
  await refresh.click();
  await panel.getByText("读取失败，以下仅为上次快照。", { exact: true }).waitFor();
  assert.equal(await panel.getByText("调度停滞", { exact: true }).count(), 0, "failed refresh must not present old state as current");
  await refresh.click();
  await panel.getByText("运行中", { exact: true }).waitFor();
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log("PASS actual Go scheduler heartbeat, browser stalled/expired-lease presentation, failed-poll invalidation and 390px layout");
};

const migrateLegacyAccount = async (page, memberA, memberB) => {
  const email = "migrated-member@example.test";
  const sourceUser = "9007199254740993";
  const fixtureDir = await realpath(workspace);
  const source = `${fixtureDir}/account-source.json`;
  const archive = `${fixtureDir}/account-archive.json`;
  const sourceData = {
    tables: {
      user: [{ id: sourceUser, email, name: "Migrated member fixture", username: "literal-username", disabled: 0, custid: "42" }],
      customer_to_users: [
        { id: "91", user_id: sourceUser, customer_id: "41", privs: 1 },
        { id: "92", user_id: sourceUser, customer_id: "42", privs: 2 },
      ],
    },
  };
  // The file carries an unneeded unusual scalar to check that the browser preserves raw archive bytes.
  await writeFile(source, JSON.stringify(sourceData).replace('"literal-username"', "1e+05"), { mode: 0o600 });
  const converted = spawnSync(process.env.TEST_MIGRATOR_BIN, ["--account-archive", "--input", source, "--output", archive], { encoding: "utf8" });
  assert.equal(converted.status, 0, `isolated account converter failed: ${converted.stderr ?? converted.error?.message ?? ""}`);
  assert.match(await readFile(archive, "utf8"), /"username":1e\+05/);

  await go(page, "invitations");
  await page.getByRole("button", { name: "旧账号批量审核", exact: true }).click();
  const panel = page.locator('[aria-label="旧账号批量审核"]');
  await panel.locator('input[type="file"]').setInputFiles(archive);
  await panel.getByRole("checkbox", { name: `选择旧账号 ${sourceUser}`, exact: true }).check();
  await panel.getByLabel("旧组织 #41", { exact: true }).selectOption(memberA.id);
  const secondMapping = panel.getByLabel("旧组织 #42", { exact: true });
  await secondMapping.selectOption(memberB.id);
  const preview = panel.getByRole("button", { name: "读取审核预览", exact: true });
  const confirm = panel.getByRole("button", { name: "确认创建激活邀请", exact: true });
  await preview.click();
  await confirm.waitFor();
  await secondMapping.selectOption(memberA.id);
  await confirm.waitFor({ state: "hidden" });
  await secondMapping.selectOption(memberB.id);
  await preview.click();
  await confirm.waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "batch review must fit 390px viewport");
  if (process.env.LEGACY_ACCOUNT_SCREENSHOT) {
    await panel.screenshot({ path: process.env.LEGACY_ACCOUNT_SCREENSHOT });
  }
  assert.equal(smtpMessages.some((message) => message.to === email), false);
  await confirm.click();
  const confirmation = page.getByRole("dialog", { name: "确认创建激活邀请", exact: true });
  await confirmation.getByLabel("当前密码", { exact: true }).fill(password);
  await confirmation.getByRole("button", { name: "创建并进入邮件队列", exact: true }).click();
  const link = await linkFor(email, "accept-invitation");
  assert.ok(!(await json(page, "/resources/users")).items.some((user) => user.email === email), "queueing cannot activate an account");

  const recipient = await browser.newContext({ viewport: { width: 390, height: 844 } });
  try {
    const view = await recipient.newPage();
    await view.goto(link);
    await view.getByText(memberA.name, { exact: true }).waitFor();
    assert.ok(await view.getByText(memberB.name, { exact: true }).count() > 0, "recipient must see both organizations");
    assert.ok(await view.getByText(/成员管理员.*默认组织/).count() > 0);
    const renamed = `${memberB.name} updated during review`;
    await json(page, `/resources/members/${memberB.id}`, {
      name: renamed, asn: memberB.asn, status: "active",
    }, "PUT");
    const accept = async () => {
      await view.getByLabel("你的姓名", { exact: true }).fill("Migrated member fixture");
      await view.getByLabel("账号密码", { exact: true }).fill(password);
      await view.getByLabel("确认密码", { exact: true }).fill(password);
      await view.getByRole("button", { name: "确认接受邀请", exact: true }).click();
    };
    await accept();
    await view.getByRole("alert").filter({ hasText: "重新读取邀请" }).waitFor();
    await view.getByRole("button", { name: "重新读取邀请", exact: true }).click();
    await view.getByText(renamed, { exact: true }).waitFor();
    await accept();
    await view.getByText("邀请已接受。请重新登录，可在已授权的组织之间切换。", { exact: true }).waitFor();
    await view.getByRole("button", { name: "返回登录", exact: true }).click();
    await login(view, email);
    const memberships = await json(view, "/memberships");
    assert.equal(memberships.activeMemberId, memberB.id);
    assert.deepEqual(Object.fromEntries(memberships.items.map((item) => [item.id, item.role])), {
      [memberA.id]: "member", [memberB.id]: "member-admin",
    });
  } finally {
    await recipient.close();
    await page.setViewportSize({ width: 1440, height: 1000 });
  }
  console.log("PASS real legacy converter, archive preview at 390px, isolated SMTP, stale recipient reinspection and two reviewed organizations with old default");
};

const totp = (secret) => {
  const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const bits = [...secret]
    .map((c) => chars.indexOf(c).toString(2).padStart(5, "0"))
    .join("");
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2))),
    counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const h = createHmac("sha1", key).update(counter).digest(),
    o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, "0");
};
try {
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const page = await browser.newPage({
      viewport: { width: 1440, height: 1000 },
    }),
    errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await start("disabled", false);
  await setup(page);
  const aDisabled = await json(page, "/resources/members", {
    name: "Disabled Delivery Network",
    asn: 64513,
    status: "active",
  });
  await go(page, "notifications");
  await page.getByText("未启用邮件投递", { exact: true }).waitFor();
  await checkSchedulerHealth(page);
  await go(page, "invitations");
  await page.getByRole("button", { name: "邀请用户", exact: true }).click();
  await page.getByLabel("受邀邮箱", { exact: true }).fill("never@example.test");
  await page.getByLabel("成员网络", { exact: true }).selectOption(aDisabled.id);
  await page.getByLabel("当前密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "发送成员邀请", exact: true }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  const known = await fetch(base + "/api/forgot-password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "admin@example.test" }),
    }),
    unknown = await fetch(base + "/api/forgot-password", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ email: "unknown@example.test" }),
    });
  assert.equal(known.status, 202);
  assert.deepEqual(await known.json(), await unknown.json());
  assert.equal(smtpMessages.length, 0);
  await stop();
  console.log(
    "Disabled-delivery readiness and non-enumerating recovery responses passed",
  );
  const enabledDir = await start("enabled", true);
  await page.context().clearCookies();
  await setup(page);
  const a = await json(page, "/resources/members", {
      name: "Network A",
      asn: 64513,
      status: "active",
    }),
    b = await json(page, "/resources/members", {
      name: "Network B",
      asn: 64514,
      status: "active",
    });
  await json(page, "/resources/users", {
    name: "Existing Member",
    email: "existing@example.test",
    role: "member",
    memberId: a.id,
    password,
  });
  await json(page, "/resources/users", {
    name: "Recover Member",
    email: "reset@example.test",
    role: "member",
    memberId: a.id,
    password,
  });
  await createInvite(page, "new@example.test", a.id);
  const inviteLink = await linkFor("new@example.test", "accept-invitation"),
    newContext = await browser.newContext(),
    newPage = await newContext.newPage();
  await newPage.goto(inviteLink);
  await newPage.getByLabel("你的姓名", { exact: true }).fill("New Member");
  assert.equal(newPage.url().includes("token="), false);
  await newPage.getByLabel("账号密码", { exact: true }).fill(password);
  await newPage.getByLabel("确认密码", { exact: true }).fill(password);
  await newPage
    .getByRole("button", { name: "确认接受邀请", exact: true })
    .click();
  await newPage
    .getByText("邀请已接受。请重新登录，进入受邀成员网络。", { exact: true })
    .waitFor();
  await newPage.getByRole("button", { name: "返回登录", exact: true }).click();
  await login(newPage, "new@example.test");
  assert.equal((await json(newPage, "/session")).user.role, "member");
  await newContext.close();
  await createInvite(page, "existing@example.test", b.id);
  const existingLink = await linkFor(
      "existing@example.test",
      "accept-invitation",
    ),
    existingContext = await browser.newContext(),
    existingPage = await existingContext.newPage();
  await existingPage.goto(existingLink);
  await existingPage
    .getByRole("button", { name: "使用受邀账号登录", exact: true })
    .click();
  await existingPage.getByLabel("邮箱地址").fill("existing@example.test");
  await existingPage.getByLabel("密码", { exact: true }).fill(password);
  await existingPage
    .getByRole("button", { name: "登录控制台", exact: true })
    .click();
  await existingPage.getByLabel("当前密码", { exact: true }).fill(password);
  assert.equal(existingPage.url().includes("token="), false);
  await existingPage
    .getByRole("button", { name: "确认接受邀请", exact: true })
    .click();
  await existingPage
    .getByText("邀请已接受。请重新登录，进入受邀成员网络。", { exact: true })
    .waitFor();
  await existingPage
    .getByRole("button", { name: "返回登录", exact: true })
    .click();
  await login(existingPage, "existing@example.test");
  assert.equal((await json(existingPage, "/memberships")).items.length, 2);
  await existingContext.close();
  if (process.env.TEST_MIGRATOR_BIN) await migrateLegacyAccount(page, a, b);
  await go(page, "notifications");
  await page.getByText("已启用邮件投递", { exact: true }).waitFor();
  const metadata = await json(page, "/notifications");
  for (const item of metadata.items) {
    for (const key of ["text", "body", "html", "token", "sealed", "ciphertext"])
      assert.equal(key in item, false);
    assert.equal(
      (await page.getByText(item.subject, { exact: true }).count()) > 0,
      true,
    );
  }
  const tokenText = new URL(inviteLink).hash.split("token=")[1];
  assert.equal(
    (await page.locator("body").innerText()).includes(tokenText),
    false,
  );
  assert.equal(
    await page.getByRole("button", { name: "重新排队", exact: true }).count(),
    0,
  );
  await page
    .getByRole("button", { name: "提交执行", exact: true })
    .first()
    .click();
  await page
    .getByText("任务已提交，将按后台执行状态更新结果。", { exact: true })
    .waitFor();
  await page.screenshot({
    path: "/tmp/moedove-notifications.png",
    fullPage: true,
  });
  console.log(
    "New/existing invitation acceptance, scrubbed fragment tokens, metadata-only outbox and fixed jobs passed",
  );
  // Restart resets only in-memory test sessions; durable invitations/outbox are retained.
  await stop();
  await start("enabled", true);
  await page.context().clearCookies();
  await login(page, "admin@example.test");
  const resetContext = await browser.newContext(),
    resetPage = await resetContext.newPage();
  await login(resetPage, "reset@example.test");
  const enrollment = await json(resetPage, "/account/totp/enroll", {
    currentPassword: password,
  });
  const codes = (
    await json(resetPage, "/account/totp/verify", {
      code: totp(enrollment.secret),
    })
  ).recoveryCodes;
  await resetPage
    .getByRole("button", { name: "退出登录", exact: true })
    .click();
  await resetPage
    .getByRole("link", { name: "忘记密码？", exact: true })
    .click();
  await resetPage
    .getByLabel("登录邮箱", { exact: true })
    .fill("reset@example.test");
  await resetPage
    .getByRole("button", { name: "发送恢复邮件", exact: true })
    .click();
  await resetPage.getByRole("status").waitFor();
  const resetLink = await linkFor("reset@example.test", "reset-password");
  await resetPage.goto(resetLink);
  await resetPage.getByLabel("新密码", { exact: true }).fill(newPassword);
  await resetPage.getByLabel("确认密码", { exact: true }).fill(newPassword);
  assert.equal(resetPage.url().includes("token="), false);
  await resetPage
    .getByRole("button", { name: "更新密码", exact: true })
    .click();
  await resetPage.getByRole("alert").waitFor();
  await resetPage
    .getByLabel("验证器代码或恢复码（已开启时填写）", { exact: true })
    .fill(codes[0]);
  await resetPage
    .getByRole("button", { name: "更新密码", exact: true })
    .click();
  await resetPage
    .getByText("密码已更新，原有会话和 API 令牌已失效。请重新登录。", {
      exact: true,
    })
    .waitFor();
  await resetPage
    .getByRole("button", { name: "返回登录", exact: true })
    .click();
  await login(resetPage, "reset@example.test", newPassword, codes[1]);
  assert.equal((await json(resetPage, "/account/security")).mfaEnabled, true);
  const replay = await fetch(base + "/api/reset-password", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      token: new URLSearchParams(new URL(resetLink).hash.split("?")[1]).get(
        "token",
      ),
      newPassword: password,
      code: codes[2],
    }),
  });
  assert.equal(replay.status, 400);
  await resetContext.close();
  console.log(
    "Password recovery requires MFA, preserves MFA, invalidates sessions and consumes link once",
  );
  rejected.add("retry@example.test");
  await createInvite(page, "retry@example.test", a.id);
  const failing = await until(async () => {
    const data = await json(page, "/notifications");
    return data.items.find(
      (i) =>
        i.to === "retry@example.test" &&
        i.attempts > 0 &&
        i.status === "pending",
    );
  });
  await go(page, "notifications");
  assert.equal(
    await page.getByRole("button", { name: "重新排队", exact: true }).count(),
    0,
  );
  await stop();
  // Valid terminal-failure fixture avoids waiting 7.5 minutes of exponential backoff.
  // Core queue tests cover that backoff; this exercises the real persisted retry API/UI.
  const statePath = enabledDir + "/state.json",
    state = JSON.parse(await readFile(statePath, "utf8"));
  const entry = state.outbox.entries.find((e) => e.id === failing.id);
  entry.status = "failed";
  entry.attempts = 5;
  entry.lastError = "投递重试次数已达上限";
  await writeFile(statePath, JSON.stringify(state), { mode: 0o600 });
  rejected.delete("retry@example.test");
  await start("enabled", true);
  await page.context().clearCookies();
  await login(page, "admin@example.test");
  await go(page, "notifications");
  await page.getByRole("button", { name: "重新排队", exact: true }).click();
  await until(async () => {
    const data = await json(page, "/notifications");
    return data.items.find((i) => i.id === failing.id)?.status === "sent";
  });
  await page.getByRole("button", { name: "刷新", exact: true }).click();
  assert.equal(
    await page.getByRole("button", { name: "重新排队", exact: true }).count(),
    0,
  );
  await linkFor("retry@example.test", "accept-invitation");
  assert.deepEqual(errors, []);
  console.log(
    "Failed-only outbox retry delivered to fake SMTP; mail browser suite passed without uncaught page errors.",
  );
} catch (e) {
  throw new Error(
    String(e?.stack ?? e).replace(/token=[^\s"'<>]+/g, "token=<redacted>"),
  );
} finally {
  if (browser) await browser.close();
  await stop();
  for (const socket of sockets) socket.destroy();
  await new Promise((r) => smtp.close(r));
}
