// Synthetic local inventory only. SMTP is disabled; no external mail is sent.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const root = fileURLToPath(new URL("../../", import.meta.url));
const dir = await mkdtemp(`${root}.cache/tmp/note-ui-`),
  socket = net.createServer();
await new Promise((r) => socket.listen(0, "127.0.0.1", r));
const port = socket.address().port;
await new Promise((r) => socket.close(r));
const base = `http://127.0.0.1:${port}`,
  password = "note-subscription-fixture-password";
const child = spawn(
  process.env.TEST_SERVER_BIN || `${root}.cache/moedove-note-ui-server`,
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "note-fixture-setup" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser,
  stderr = "";
child.stderr.on("data", (x) => (stderr = (stderr + x).slice(-3000)));
try {
  for (let i = 0; ; i++) {
    if (child.exitCode !== null) throw new Error(stderr);
    if (
      await fetch(base + "/api/setup")
        .then((r) => r.ok)
        .catch(() => false)
    )
      break;
    if (i > 100) throw new Error("note fixture startup timeout");
    await new Promise((r) => setTimeout(r, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const admin = await browser.newContext();
  assert.equal(
    (
      await admin.request.post(base + "/api/setup", {
        data: {
          setupToken: "note-fixture-setup",
          name: "Notes QA",
          email: "admin@example.test",
          password,
          ixpName: "Note QA",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const call = async (
    path,
    data,
    method = data ? "POST" : "GET",
    ctx = admin,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const headers = { "X-CSRF-Token": session.csrfToken };
    if (path.startsWith("/resources/") && ["PUT", "DELETE"].includes(method)) {
      const current = await (
        await ctx.request.get(base + "/api" + path)
      ).json();
      headers["If-Match"] = `"${current._etag}"`;
    }
    const r = await ctx.request.fetch(base + "/api" + path, {
      method,
      data,
      headers,
    });
    assert.ok(r.ok(), `${method} ${path}: ${r.status()} ${await r.text()}`);
    return r.json();
  };
  const a = await call("/resources/members", {
      name: "合成成员甲",
      asn: 64501,
      status: "active",
    }),
    b = await call("/resources/members", {
      name: "合成成员乙",
      asn: 64502,
      status: "active",
    });
  const noteA = await call("/resources/notes", {
      name: "甲成员维护窗口",
      memberId: a.id,
      visibility: "internal",
      body: "Synthetic internal note A",
    }),
    noteB = await call("/resources/notes", {
      name: "乙成员联络记录",
      memberId: b.id,
      visibility: "member",
      body: "Synthetic note B",
    });
  await call("/resources/users", {
    name: "只读员工",
    email: "viewer@example.test",
    password,
    role: "viewer",
  });
  await call("/resources/users", {
    name: "普通成员",
    email: "member@example.test",
    password,
    role: "member",
    memberId: a.id,
  });
  const login = async (email) => {
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    });
    assert.equal(
      (
        await ctx.request.post(base + "/api/login", {
          data: { email, password },
        })
      ).status(),
      200,
    );
    return ctx;
  };
  const viewer = await login("viewer@example.test"),
    page = await viewer.newPage(),
    errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(base + "/#security");
  await page
    .getByRole("button", { name: "备注通知与未读", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "备注通知与未读", exact: true })
    .waitFor();
  const mode = page.getByLabel("邮件通知范围", { exact: true }),
    save = page.getByRole("button", { name: "保存备注通知偏好", exact: true }),
    ack = page.getByRole("checkbox", {
      name: "我同意按所选范围，将后续备注变更发送到我的当前账号邮箱。",
    });
  assert.equal(await mode.inputValue(), "none");
  await page.getByText("viewer@example.test", { exact: true }).waitFor();
  await page.getByText(/邮件服务当前未启用/).waitFor();
  await mode.selectOption("watched");
  await page
    .getByRole("region", { name: "成员订阅选择", exact: true })
    .getByRole("button", { name: /合成成员甲.*订阅/ })
    .click();
  await page
    .getByRole("region", { name: "备注订阅选择", exact: true })
    .getByRole("button", { name: /乙成员联络记录.*订阅/ })
    .click();
  assert.equal(await save.isDisabled(), true);
  await ack.check();
  await save.click();
  await page.getByText("备注通知偏好已保存。", { exact: true }).waitFor();
  const prefs = await call(
    "/account/note-preferences",
    undefined,
    "GET",
    viewer,
  );
  assert.equal(prefs.mode, "watched");
  assert.equal(prefs.memberWatches[0].id, a.id);
  assert.equal(prefs.noteWatches[0].id, noteB.id);
  await page
    .getByLabel("按成员筛选备注摘要", { exact: true })
    .selectOption(b.id);
  await page
    .getByRole("button", { name: "将所选成员的当前备注标记已读", exact: true })
    .click();
  await page.getByRole("button", { name: "确认标记已读", exact: true }).click();
  await page.getByText("没有匹配的备注", { exact: true }).waitFor();
  const summary = await call("/account/note-summary", undefined, "GET", viewer);
  assert.equal(summary.items.find((n) => n.id === noteB.id).unread, false);
  assert.equal(summary.items.find((n) => n.id === noteA.id).unread, true);
  console.log(
    "Notes default-none/viewer self subscription/email consent/scoped mark-read passed",
  );
  const prefBody = (p, mode = p.mode) => ({
    revision: p.revision,
    mode,
    memberWatches: p.memberWatches.map(({ id, createdAt }) => ({
      id,
      createdAt,
    })),
    noteWatches: p.noteWatches.map(({ id, createdAt }) => ({ id, createdAt })),
  });
  await page.getByLabel("按成员筛选备注摘要", { exact: true }).selectOption("");
  await mode.selectOption("all");
  await ack.check();
  let remote = await call(
    "/account/note-preferences",
    undefined,
    "GET",
    viewer,
  );
  await call(
    "/account/note-preferences",
    prefBody(remote, "none"),
    "PUT",
    viewer,
  );
  await save.click();
  await page
    .getByRole("region", { name: "备注偏好冲突", exact: true })
    .waitFor();
  assert.equal(await mode.inputValue(), "all");
  assert.equal(
    await page
      .getByRole("button", { name: "取消订阅 合成成员甲", exact: true })
      .count(),
    1,
  );
  await page
    .getByRole("button", { name: "已核对，保留草稿继续编辑", exact: true })
    .click();
  assert.equal(await save.isDisabled(), true);
  await ack.check();
  await save.click();
  await page.getByText("备注通知偏好已保存。", { exact: true }).waitFor();
  assert.equal(
    (await call("/account/note-preferences", undefined, "GET", viewer)).mode,
    "all",
  );
  // A second stale tab can deliberately discard its draft, never auto-merge.
  await mode.selectOption("watched");
  await ack.check();
  remote = await call("/account/note-preferences", undefined, "GET", viewer);
  await call(
    "/account/note-preferences",
    prefBody(remote, "none"),
    "PUT",
    viewer,
  );
  await save.click();
  await page
    .getByRole("button", { name: "放弃草稿，采用服务器偏好", exact: true })
    .click();
  assert.equal(await mode.inputValue(), "none");
  assert.equal(await save.isDisabled(), true);
  await mode.selectOption("watched");
  await ack.check();
  await save.click();
  await page.getByText("备注通知偏好已保存。", { exact: true }).waitFor();
  // Confirmation captures the viewed summary; a new edit must remain unread.
  await page.getByRole("button", { name: "全部标记已读", exact: true }).click();
  await call(
    `/resources/notes/${noteA.id}`,
    {
      name: noteA.name,
      memberId: a.id,
      visibility: "internal",
      body: "Synthetic edit made while read confirmation is open",
    },
    "PUT",
  );
  await page.getByRole("button", { name: "确认标记已读", exact: true }).click();
  await page
    .getByText(
      "备注摘要已有新变化，未覆盖新版本的未读状态。请核对刷新后的摘要，再次确认标记已读。",
      { exact: true },
    )
    .waitFor();
  assert.equal(
    (await call("/account/note-summary", undefined, "GET", viewer)).items.find(
      (n) => n.id === noteA.id,
    ).unread,
    true,
  );
  await page.getByRole("button", { name: "全部标记已读", exact: true }).click();
  await page.getByRole("button", { name: "确认标记已读", exact: true }).click();
  await page.getByText("0 条未读", { exact: true }).waitFor();
  console.log(
    "Notes preference CAS draft/server choices and stale read-summary rejection passed",
  );
  // Deleted/recreated objects retain an inert old binding until explicitly removed.
  await call(`/resources/notes/${noteB.id}`, undefined, "DELETE");
  await page.reload();
  await page.getByText(/对象已删除或身份已变化/).waitFor();
  await mode.selectOption("none");
  await save.click();
  await page.getByText("备注通知偏好已保存。", { exact: true }).waitFor();
  assert.equal(
    (await call("/account/note-preferences", undefined, "GET", viewer))
      .noteWatches.length,
    1,
  );
  await call("/import", {
    version: 1,
    resources: {
      notes: [
        {
          id: noteB.id,
          name: "新身份的乙成员备注",
          memberId: b.id,
          visibility: "member",
          body: "New source identity with the same record ID",
        },
      ],
    },
  });
  await page.reload();
  await page.getByText(/对象已删除或身份已变化/).waitFor();
  const notePicker = page.getByRole("region", {
    name: "备注订阅选择",
    exact: true,
  });
  assert.equal(
    await notePicker
      .getByRole("button", { name: /新身份的乙成员备注.*订阅/ })
      .count(),
    0,
  );
  await notePicker
    .getByRole("button", { name: `取消订阅 ${noteB.id}`, exact: true })
    .click();
  await notePicker
    .getByRole("button", { name: /新身份的乙成员备注.*订阅/ })
    .click();
  await save.click();
  await page.getByText("备注通知偏好已保存。", { exact: true }).waitFor();
  const replaced = await call(
    "/account/note-preferences",
    undefined,
    "GET",
    viewer,
  );
  assert.notEqual(replaced.noteWatches[0].createdAt, noteB.createdAt);
  assert.equal(replaced.noteWatches[0].stale, false);
  assert.equal(replaced.mode, "none");
  // An intentional fetch failure is visible and never marks new data read.
  await page.route("**/api/account/note-summary", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: "synthetic summary unavailable" }),
    }),
  );
  await page.getByRole("button", { name: "刷新摘要", exact: true }).click();
  await page
    .getByText("synthetic summary unavailable", { exact: true })
    .waitFor();
  await page.unroute("**/api/account/note-summary");
  await page.getByRole("button", { name: "刷新摘要", exact: true }).click();
  const member = await login("member@example.test"),
    memberPage = await member.newPage(),
    noteRequests = [];
  memberPage.on("request", (r) => {
    if (/\/api\/account\/note-/.test(r.url())) noteRequests.push(r.url());
  });
  await memberPage.goto(base + "/#security?section=notes");
  await memberPage
    .getByRole("heading", { name: "账号安全", exact: true })
    .waitFor();
  assert.equal(
    await memberPage
      .getByRole("button", { name: "备注通知与未读", exact: true })
      .count(),
    0,
  );
  assert.deepEqual(noteRequests, []);
  assert.equal(
    (await member.request.get(base + "/api/account/note-preferences")).status(),
    403,
  );
  assert.equal(
    (await member.request.get(base + "/api/account/note-summary")).status(),
    403,
  );
  const outbox = await call("/notifications");
  assert.equal(outbox.deliveryEnabled, false);
  assert.equal(outbox.items.length, 0);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => window.scrollTo(0, 0));
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  await page.screenshot({ path: "/tmp/moedove-note-preferences-mobile.png" });
  await page
    .getByRole("heading", { name: "备注未读摘要", exact: false })
    .scrollIntoViewIfNeeded();
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  await page.screenshot({ path: "/tmp/moedove-note-summary-mobile.png" });
  console.log(
    "Notes stale entity explicit resubscription / disabled mail / member no-fetch boundary /500 recovery /390px passed",
  );
  assert.deepEqual(errors, []);
} finally {
  if (browser) await browser.close();
  if (child.exitCode === null) {
    child.kill("SIGTERM");
    await Promise.race([
      new Promise((r) => child.once("exit", r)),
      new Promise((r) => setTimeout(r, 5000)),
    ]);
    if (child.exitCode === null) {
      child.kill("SIGKILL");
      await new Promise((r) => child.once("exit", r));
    }
  }
  await rm(dir, { recursive: true, force: true });
  console.log("Notes browser/service/synthetic files cleaned up");
}
