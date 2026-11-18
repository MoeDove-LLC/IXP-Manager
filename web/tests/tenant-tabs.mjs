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
  await two.getByLabel("电话", { exact: true }).fill("UNSAVED A DRAFT");
  const old = await api(two, "/session");
  const delayedSchema = [];
  await two.route("**/api/schema", (route) => {
    delayedSchema.push(route);
  });
  await one.getByLabel("当前成员网络", { exact: true }).selectOption(b.id);
  await two.getByRole("heading", { name: b.name, exact: true }).waitFor();
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
