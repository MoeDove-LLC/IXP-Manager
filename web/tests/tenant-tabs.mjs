// Focused same-origin two-tab tenant synchronization; uses an initialized isolated fixture.
import assert from "node:assert/strict";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  }),
  base = process.env.TEST_URL || "http://127.0.0.1:18080",
  adminPassword =
    process.env.TEST_ADMIN_PASSWORD || "Fabric-device-test-password-123",
  password = "Cross-tab-test-password-123";
const errors = [];
const api = async (page, path, body, method = body ? "POST" : "GET") =>
  page.evaluate(
    async ({ path, body, method }) => {
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
          "X-CSRF-Token": s.csrfToken,
        },
        body: body ? JSON.stringify(body) : undefined,
      });
      const data = await r.json();
      if (!r.ok) throw new Error(data.error);
      return data;
    },
    { path, body, method },
  );
try {
  const admin = await browser.newPage();
  await admin.goto(base);
  await admin.getByLabel("邮箱地址").fill("admin@example.test");
  await admin.getByLabel("密码", { exact: true }).fill(adminPassword);
  await admin.getByRole("button", { name: "登录控制台", exact: true }).click();
  await admin.locator("main").waitFor();
  const usedASNs = new Set(
    (await api(admin, "/resources/members")).items.map((m) => m.asn),
  );
  let asn = 64601;
  while (usedASNs.has(asn) || usedASNs.has(asn + 1)) asn += 2;
  assert.ok(asn < 65534);
  const memberEmail = `tabs-${Date.now()}@example.test`;
  const a = await api(admin, "/resources/members", {
      name: `Tabs Network A ${memberEmail}`,
      asn,
      status: "active",
      phone: "A initial",
    }),
    b = await api(admin, "/resources/members", {
      name: `Tabs Network B ${memberEmail}`,
      asn: asn + 1,
      status: "active",
      phone: "B initial",
    });
  await api(admin, "/resources/users", {
    name: "Two-tab user",
    email: memberEmail,
    password,
    role: "member",
    memberIds: [a.id, b.id],
    memberAdminIds: [a.id, b.id],
  });
  const aColleague = "A-only roster colleague";
  const aEmail = `roster-a-${Date.now()}@example.test`;
  const bColleague = "B-only roster colleague";
  const bEmail = `roster-b-tenant-support-${"long-".repeat(5)}${Date.now()}@example.test`;
  const colleagues = [];
  for (const [member, name, email] of [[a, aColleague, aEmail], [b, bColleague, bEmail]]) {
    colleagues.push(await api(admin, "/resources/users", {
      name,
      email,
      password,
      role: "member",
      memberId: member.id,
    }));
  }
  const context = await browser.newContext();
  await context.addInitScript(() => {
    window.__sessionSignals = [];
    const observer = new BroadcastChannel("moedove-session-context");
    observer.onmessage = (e) => window.__sessionSignals.push(e.data);
  });
  const one = await context.newPage();
  one.on("pageerror", (e) => errors.push(e.message));
  await one.goto(base);
  await one.getByLabel("邮箱地址").fill(memberEmail);
  await one.getByLabel("密码", { exact: true }).fill(password);
  await one.getByRole("button", { name: "登录控制台", exact: true }).click();
  await one.locator("main").waitFor();
  const two = await context.newPage();
  two.on("pageerror", (e) => errors.push(e.message));
  await two.goto(base + "/#member-profile");
  await two.getByRole("heading", { name: a.name, exact: true }).waitFor();
  const roster = (page) => page.getByRole("region", { name: "组织账号", exact: true });
  const refreshRoster = (page) => roster(page).getByRole("button", { name: "刷新名册", exact: true });
  await roster(two).getByText(aColleague, { exact: true }).waitFor();
  assert.equal(await roster(two).getByText(bColleague, { exact: true }).count(), 0);
  const capturedRoster = Promise.withResolvers();
  await two.route("**/api/member/users", async (route) => {
    try {
      const response = await route.fetch({ timeout: 10000 });
      const body = await response.json();
      assert.equal(response.status(), 200);
      assert.equal(body.memberId, a.id);
      assert.ok(body.items.some((item) => item.email === aEmail));
      capturedRoster.resolve({ route, response });
    } catch (error) {
      capturedRoster.reject(error);
      await route.abort();
    }
  }, { times: 1 });
  const refreshStarted = two.waitForRequest("**/api/member/users", { timeout: 10000 });
  await refreshRoster(two).click();
  await refreshStarted;
  const delayedRoster = await capturedRoster.promise;
  assert.equal(await roster(two).getByText(aColleague, { exact: true }).count(), 0);
  await two.getByLabel("电话", { exact: true }).fill("UNSAVED A DRAFT");
  const old = await api(two, "/session");
  const delayedSchema = [];
  await two.route("**/api/schema", (route) => {
    delayedSchema.push(route);
  });
  await one.goto(base + "/#member-profile");
  await roster(one).getByText(aColleague, { exact: true }).waitFor();
  await one.getByLabel("电话", { exact: true }).fill("DRAFT DURING FAILED SWITCH");
  const selection = one.getByLabel("当前成员网络", { exact: true });
  const refusedSwitch = "组织切换未完成（测试）";
  await one.route("**/api/memberships/select", (route) => route.fulfill({
    status: 409,
    json: { error: refusedSwitch },
  }), { times: 1 });
  await selection.selectOption(b.id);
  await one.getByRole("alert").filter({ hasText: refusedSwitch }).waitFor();
  await roster(one).getByText(aColleague, { exact: true }).waitFor();
  assert.equal(await one.getByLabel("电话", { exact: true }).inputValue(), "DRAFT DURING FAILED SWITCH");

  const completedSwitch = Promise.withResolvers();
  await one.route("**/api/memberships/select", async (route) => {
    try {
      const response = await route.fetch({ timeout: 10000 });
      assert.equal(response.status(), 200);
      assert.equal((await response.json()).user.memberId, b.id);
      completedSwitch.resolve({ route, response });
    } catch (error) {
      completedSwitch.reject(error);
      await route.abort();
    }
  }, { times: 1 });
  const switchStarted = one.waitForRequest("**/api/memberships/select", { timeout: 10000 });
  await selection.selectOption(b.id);
  await switchStarted;
  const switched = await completedSwitch.promise;
  await one.getByText("正在切换工作空间…", { exact: true }).waitFor();
  assert.equal(await roster(one).count(), 0, "pending switch must hide the previous roster");
  await switched.route.fulfill({ response: switched.response });
  await two.getByRole("heading", { name: b.name, exact: true }).waitFor();
  await roster(two).getByText(bColleague, { exact: true }).waitFor();
  try {
    await delayedRoster.route.fulfill({ response: delayedRoster.response });
  } catch (error) {
    assert.equal(delayedRoster.route.request().failure()?.errorText, "net::ERR_ABORTED", String(error));
  }
  await two.evaluate(() => new Promise((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await two.getByText(aColleague, { exact: true }).count(), 0);
  assert.equal(await roster(two).getByText(bEmail, { exact: true }).count(), 1);
  const unavailable = "名册验证期间暂时不可用";
  await two.route("**/api/member/users", (route) => route.fulfill({
    status: 503,
    json: { error: unavailable },
  }), { times: 1 });
  await refreshRoster(two).click();
  await roster(two).getByRole("alert").filter({ hasText: unavailable }).waitFor();
  assert.equal(await roster(two).getByText(bColleague, { exact: true }).count(), 0);
  await refreshRoster(two).click();
  await roster(two).getByText(bColleague, { exact: true }).waitFor();

  const mobile = await context.newPage();
  await mobile.setViewportSize({ width: 390, height: 844 });
  mobile.on("pageerror", (e) => errors.push(e.message));
  await mobile.goto(base + "/#member-profile");
  await roster(mobile).getByText(bEmail, { exact: true }).waitFor();
  assert.ok(await mobile.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "roster must fit 390px viewport");
  if (process.env.MEMBER_ROSTER_SCREENSHOT) {
    await roster(mobile).screenshot({ path: process.env.MEMBER_ROSTER_SCREENSHOT });
  }
  await roster(mobile).getByRole("link", { name: "邀请成员", exact: true }).click();
  await mobile.getByRole("heading", { name: "成员邀请", exact: true }).waitFor();
  await mobile.close();

  const ordinary = await browser.newContext();
  try {
    const login = await ordinary.request.post(base + "/api/login", { data: { email: aEmail, password } });
    assert.equal(login.status(), 200);
    assert.equal((await ordinary.request.get(base + "/api/member/users")).status(), 403);
    const ordinaryPage = await ordinary.newPage();
    ordinaryPage.on("pageerror", (e) => errors.push(e.message));
    let rosterRequests = 0;
    ordinaryPage.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/member/users") rosterRequests++;
    });
    await ordinaryPage.goto(base + "/#member-profile");
    await ordinaryPage.getByRole("heading", { name: a.name, exact: true }).waitFor();
    assert.equal(await roster(ordinaryPage).count(), 0);
    assert.equal(rosterRequests, 0);
  } finally {
    await ordinary.close();
  }
  console.log("PASS roster isolation while switching, stale response rejection, retry after failed refresh, ordinary member denial, mobile layout and invitation link");
  assert.equal(
    await two.getByLabel("当前成员网络", { exact: true }).inputValue(),
    b.id,
  );
  assert.equal(
    await two.getByLabel("电话", { exact: true }).inputValue(),
    "B initial",
  );
  const current = await api(two, "/session");
  assert.ok(
    current.csrfToken !== old.csrfToken,
    "tenant change must rotate CSRF",
  );
  const rejected = await two.request.post(base + "/api/member/contacts", {
    headers: { "X-CSRF-Token": old.csrfToken },
    data: { name: "Wrong stale tenant", email: "wrong@example.test" },
  });
  assert.equal(rejected.status(), 403);
  await two.getByLabel("电话", { exact: true }).fill("B updated after sync");
  await two.getByRole("button", { name: "保存网络资料", exact: true }).click();
  await two.getByText("网络资料已更新。", { exact: true }).waitFor();
  assert.equal(
    (await api(admin, `/resources/members/${a.id}`)).phone,
    "A initial",
  );
  assert.equal(
    (await api(admin, `/resources/members/${b.id}`)).phone,
    "B updated after sync",
  );
  await two.getByLabel("当前成员网络", { exact: true }).selectOption(a.id);
  await one.waitForFunction(
    (id) => document.querySelector("#active-membership")?.value === id,
    a.id,
  );
  await one.goto(base + "/#member-profile");
  await one.getByRole("heading", { name: a.name, exact: true }).waitFor();
  await one
    .getByLabel("电话", { exact: true })
    .fill("A updated with returned CSRF");
  await one.getByRole("button", { name: "保存网络资料", exact: true }).click();
  await one.getByText("网络资料已更新。", { exact: true }).waitFor();
  const removeMember = (name) => roster(one).getByRole("button", {
    name: `移出组织 ${name}`, exact: true,
  });
  const removal = one.getByRole("dialog", { name: "移出当前组织", exact: true });
  const confirmRemoval = removal.getByRole("button", { name: "确认移出组织", exact: true });
  await removeMember("Two-tab user").click();
  await removal.getByText(/最后一位有效成员管理员/).waitFor();
  assert.equal(await confirmRemoval.isDisabled(), true);
  await removal.getByRole("button", { name: "取消", exact: true }).click();

  await one.setViewportSize({ width: 390, height: 844 });
  await removeMember(aColleague).click();
  await removal.getByText(a.name, { exact: true }).waitFor();
  await removal.getByText(aEmail, { exact: true }).waitFor();
  assert.equal(await confirmRemoval.isEnabled(), true);
  await removal.getByText(/最后一条组织关系/).waitFor();
  assert.ok(await one.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "removal must fit 390px viewport");
  if (process.env.MEMBER_REMOVAL_SCREENSHOT) {
    await removal.screenshot({ path: process.env.MEMBER_REMOVAL_SCREENSHOT });
  }
  const renamed = "Organization colleague after review";
  await api(admin, `/resources/users/${colleagues[0].id}`, {
    name: renamed, email: aEmail, role: "member", memberId: a.id,
  }, "PUT");
  await confirmRemoval.click();
  await removal.getByRole("alert").filter({ hasText: /关系或权限已变化/ }).waitFor();
  assert.equal(await confirmRemoval.isDisabled(), true);
  await removal.getByRole("button", { name: "重新读取移除预览", exact: true }).click();
  await removal.getByText(renamed, { exact: true }).waitFor();

  const target = await browser.newContext({ viewport: { width: 390, height: 844 } });
  try {
    const signIn = () => target.request.post(base + "/api/login", { data: { email: aEmail, password } });
    assert.equal((await signIn()).status(), 200);
    assert.equal((await target.request.get(base + "/api/member/profile")).status(), 200);
    await confirmRemoval.click();
    await removal.waitFor({ state: "hidden" });
    await roster(one).getByText("Two-tab user", { exact: true }).waitFor();
    assert.equal(await roster(one).getByText(aEmail, { exact: true }).count(), 0);
    assert.equal((await target.request.get(base + "/api/session")).status(), 401);
    assert.equal((await signIn()).status(), 200);
    assert.equal((await target.request.get(base + "/api/member/profile")).status(), 403);
    const targetPage = await target.newPage();
    targetPage.on("pageerror", (e) => errors.push(e.message));
    await targetPage.goto(base + "/#member-profile");
    await targetPage.getByRole("heading", { name: "当前账号没有组织", exact: true }).waitFor();
    assert.equal(await roster(targetPage).count(), 0);
    await targetPage.getByRole("button", { name: "退出登录", exact: true }).click();
    await targetPage.getByLabel("邮箱地址").waitFor();
  } finally {
    await target.close();
  }
  await one.setViewportSize({ width: 1280, height: 720 });
  console.log("PASS final administrator guard, 390px removal review, real stale preview rejection, one confirmed removal and restricted sign-in with the same account");
  const signals = await two.evaluate(() => window.__sessionSignals);
  assert.ok(signals.length > 0);
  assert.ok(
    signals.every(
      (signal) =>
        Object.keys(signal).length === 1 &&
        signal.type === "session-context-changed",
    ),
  );
  await one.getByRole("button", { name: "退出登录", exact: true }).click();
  await two.getByLabel("邮箱地址").waitFor();
  assert.equal(await two.locator("main").count(), 0);
  const signalsAfterLogout = await two.evaluate(
    () => window.__sessionSignals.length,
  );
  assert.ok(delayedSchema.length > 0);
  for (const route of delayedSchema) {
    await route.continue();
    const response = await route.request().response();
    assert.equal(response.status(), 401);
    await response.finished();
  }
  await two.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  assert.equal(await two.getByLabel("邮箱地址").isVisible(), true);
  assert.equal(await two.getByRole("alert").count(), 0);
  assert.equal(
    await two.evaluate(() => window.__sessionSignals.length),
    signalsAfterLogout,
    "late old-context responses must not broadcast new invalidations",
  );
  assert.deepEqual(errors, []);
  await context.close();
  console.log(
    "Cross-tab tenant reload, no-secret broadcast, stale CSRF rejection, same-tab returned CSRF, synchronized logout and late old-context response isolation passed",
  );
} finally {
  await browser.close();
}
