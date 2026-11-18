// Real two-writer CAS checks against a fresh local manager. No production data.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dataDir = await mkdtemp("/tmp/moedove-resource-cas-");
const socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "Resource-CAS-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-browser-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    dataDir,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "resource-cas-fixture" },
    stdio: ["ignore", "ignore", "pipe"],
  },
);
let browser;
try {
  for (let attempt = 0; ; attempt++) {
    if (child.exitCode !== null) throw new Error("Fixture manager exited");
    if (
      await fetch(base + "/api/setup")
        .then((response) => response.ok)
        .catch(() => false)
    )
      break;
    if (attempt > 100) throw new Error("Fixture startup timeout");
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox"],
  });
  const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    }),
    page = await context.newPage(),
    errors = [],
    writes = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("request", (request) => {
    if (
      request.url().includes("/api/resources/") &&
      ["PUT", "DELETE"].includes(request.method())
    )
      writes.push({
        method: request.method(),
        path: new URL(request.url()).pathname,
        etag: request.headers()["if-match"],
        body: request.postDataJSON(),
      });
  });
  // Every call supplies its own explicit baseline. This helper never refreshes it.
  const request = async (
    path,
    body,
    method = body ? "POST" : "GET",
    etag,
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const response = await ctx.request.fetch(base + "/api" + path, {
      method,
      headers: {
        "X-CSRF-Token": session.csrfToken || "",
        ...(etag === undefined ? {} : { "If-Match": etag }),
      },
      ...(body ? { data: body } : {}),
    });
    return {
      status: response.status(),
      data: await response.json(),
      headers: response.headers(),
    };
  };
  const api = async (...args) => {
    const response = await request(...args);
    assert.ok(
      response.status < 300,
      `${args[0]}: ${response.status} ${response.data.error || ""}`,
    );
    return response.data;
  };
  const create = (kind, values) => api(`/resources/${kind}`, values);
  const fields = (row) =>
    Object.fromEntries(
      Object.entries(row).filter(
        ([key]) => !["id", "createdAt", "updatedAt", "_etag"].includes(key),
      ),
    );
  const update = (kind, row, changes) =>
    api(
      `/resources/${kind}/${row.id}`,
      { ...fields(row), ...changes },
      "PUT",
      JSON.stringify(row._etag),
    );
  const remove = (kind, row) =>
    api(
      `/resources/${kind}/${row.id}`,
      undefined,
      "DELETE",
      JSON.stringify(row._etag),
    );
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "resource-cas-fixture",
          name: "CAS QA",
          email: "admin@example.test",
          password,
          ixpName: "CAS Test IXP",
          asn: 64500,
        },
      })
    ).status(),
    200,
  );
  const go = async (kind) => {
    await page.goto(base + "/#" + kind);
    await page.locator("main").waitFor();
  };
  const open = async (kind, name) => {
    await go(kind);
    await page.reload();
    await page
      .getByRole("button", { name: `编辑 ${name}`, exact: true })
      .click();
    await page.locator("#field-name").waitFor();
    return page.getByRole("dialog");
  };
  const save = async () => {
    const response = page.waitForResponse(
      (value) =>
        value.url().includes("/api/resources/") &&
        value.request().method() === "PUT",
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "保存", exact: true })
      .click();
    return (await response).status();
  };
  const accept = async (mode = "edit") => {
    const panel = page.getByRole("region", {
      name: mode === "edit" ? "编辑版本冲突" : "删除版本冲突",
      exact: true,
    });
    await panel.getByRole("checkbox").check();
    await panel
      .getByRole("button", {
        name: mode === "edit" ? "采用方案并返回编辑" : "使用核对后的删除版本",
        exact: true,
      })
      .click();
    await panel.waitFor({ state: "hidden" });
  };
  let location = await create("locations", {
    name: "CAS DC",
    city: "Shanghai",
    country: "CN",
    address: "Initial address",
  });
  assert.match(location._etag, /^[a-f0-9]{64}$/);
  const read = await request(`/resources/locations/${location.id}`);
  assert.equal(read.headers.etag, JSON.stringify(location._etag));
  assert.equal(
    (
      await request(
        `/resources/locations/${location.id}`,
        fields(location),
        "PUT",
      )
    ).status,
    428,
  );
  assert.equal(
    (await request(`/resources/locations/${location.id}`, undefined, "DELETE"))
      .status,
    428,
  );
  assert.ok(
    (
      await request(
        `/resources/locations/${location.id}`,
        fields(location),
        "PUT",
        "*",
      )
    ).status >= 400,
  );
  assert.ok(
    (
      await request(
        `/resources/locations/${location.id}`,
        fields(location),
        "PUT",
        `W/"${location._etag}"`,
      )
    ).status >= 400,
  );
  let dialog = await open("locations", location.name);
  await dialog.locator("#field-city").fill("Hangzhou");
  const openedEtag = location._etag;
  location = await update("locations", location, { country: "JP" });
  assert.equal(await save(), 412);
  let panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.getByText("服务器更新时间：", { exact: false }).waitFor();
  assert.equal(await dialog.locator("#field-city").inputValue(), "Hangzhou");
  const firstWrite = writes.at(-1);
  assert.equal(firstWrite.etag, JSON.stringify(openedEtag));
  assert.ok(!Object.hasOwn(firstWrite.body, "_etag"));
  const beforeResolve = writes.length;
  await accept();
  assert.equal(writes.length, beforeResolve);
  assert.equal(await dialog.locator("#field-city").inputValue(), "Hangzhou");
  assert.equal(await dialog.locator("#field-country").inputValue(), "JP");
  assert.equal(await save(), 200);
  await dialog.waitFor({ state: "hidden" });
  assert.equal(writes.at(-1).etag, JSON.stringify(location._etag));
  location = await api(`/resources/locations/${location.id}`);
  assert.equal(location.city, "Hangzhou");
  assert.equal(location.country, "JP");
  console.log(
    "strong If-Match required; nonoverlapping changes are reviewed before a separate save",
  );

  let member = await create("members", {
    name: "CAS Network",
    asn: 64501,
    status: "active",
    email: "initial@example.test",
    phone: "initial phone",
  });
  dialog = await open("members", member.name);
  await dialog.locator("#field-name").fill("My Network Name");
  await dialog.locator("#field-email").fill("mine@example.test");
  member = await update("members", member, {
    name: "Server Network Name",
    email: "server@example.test",
    phone: "first server phone",
  });
  assert.equal(await save(), 412);
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.locator('[data-conflict-field="name"]').waitFor();
  assert.equal(
    await panel
      .getByRole("button", { name: "采用方案并返回编辑", exact: true })
      .isDisabled(),
    true,
  );
  await panel
    .locator('[data-conflict-field="name"]')
    .getByRole("radio", { name: "保留我的修改", exact: true })
    .check();
  await panel
    .locator('[data-conflict-field="email"]')
    .getByRole("radio", { name: "采用服务器值", exact: true })
    .check();
  await page.screenshot({
    path: "/tmp/moedove-resource-conflict-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await dialog.evaluate(
      (element) => element.scrollWidth <= element.clientWidth,
    ),
  );
  await page.screenshot({
    path: "/tmp/moedove-resource-conflict-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await accept();
  assert.equal(
    await dialog.locator("#field-name").inputValue(),
    "My Network Name",
  );
  assert.equal(
    await dialog.locator("#field-email").inputValue(),
    "server@example.test",
  );
  const reviewedTag = member._etag;
  member = await update("members", member, { phone: "second server phone" });
  assert.equal(await save(), 412);
  assert.equal(writes.at(-1).etag, JSON.stringify(reviewedTag));
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.getByText("second server phone", { exact: true }).waitFor();
  await accept();
  assert.equal(
    await dialog.locator("#field-phone").inputValue(),
    "second server phone",
  );
  assert.equal(await save(), 200);
  await dialog.waitFor({ state: "hidden" });
  member = await api(`/resources/members/${member.id}`);
  assert.equal(member.name, "My Network Name");
  assert.equal(member.email, "server@example.test");
  assert.equal(member.phone, "second server phone");
  console.log(
    "overlapping fields need explicit choices and a second concurrent edit remains protected",
  );

  let vlan = await create("vlans", {
    name: "CAS VLAN",
    tag: 100,
    prefixes: ["198.51.100.0/24"],
  });
  dialog = await open("vlans", vlan.name);
  await dialog
    .locator("#field-prefixes")
    .fill("198.51.100.0/24\n203.0.113.0/24");
  vlan = await update("vlans", vlan, { name: "Server VLAN" });
  assert.equal(await save(), 412);
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.locator('[data-conflict-field="prefixes"]').waitFor();
  await accept();
  assert.equal(await save(), 200);
  await dialog.waitFor({ state: "hidden" });
  vlan = await api(`/resources/vlans/${vlan.id}`);
  assert.deepEqual(vlan.prefixes, ["198.51.100.0/24", "203.0.113.0/24"]);
  assert.equal(vlan.name, "Server VLAN");
  console.log("array-valued draft survives the reviewed merge");

  let user = await create("users", {
    name: "CAS Viewer",
    email: "viewer@example.test",
    role: "viewer",
    password,
  });
  dialog = await open("users", user.name);
  await dialog.locator("#field-name").fill("My Viewer Name");
  const unsavedPassword = "Unsaved-password-must-not-reuse-456";
  await dialog.locator("#field-password").fill(unsavedPassword);
  user = await update("users", user, { name: "Server Viewer Name" });
  assert.equal(await save(), 412);
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.getByText("密码字段已清空", { exact: false }).waitFor();
  assert.equal(await dialog.locator("#field-password").inputValue(), "");
  assert.ok(!(await panel.innerText()).includes(unsavedPassword));
  await panel
    .locator('[data-conflict-field="name"]')
    .getByRole("radio", { name: "保留我的修改", exact: true })
    .check();
  await accept();
  assert.equal(await dialog.locator("#field-password").inputValue(), "");
  assert.equal(await save(), 200);
  assert.ok(!Object.hasOwn(writes.at(-1).body, "password"));
  await dialog.waitFor({ state: "hidden" });
  const viewerContext = await browser.newContext();
  assert.equal(
    (
      await viewerContext.request.post(base + "/api/login", {
        data: { email: "viewer@example.test", password },
      })
    ).status(),
    200,
  );
  await viewerContext.close();
  console.log(
    "user password draft is cleared on conflict and never silently reused",
  );

  user = await api(`/resources/users/${user.id}`);
  user = await update("users", user, {
    role: "member",
    memberIds: [member.id],
  });
  dialog = await open("users", user.name);
  await dialog.locator("#field-name").fill("Reviewed member account");
  const newMember = await create("members", {
    name: "Created during review",
    asn: 64502,
    status: "active",
  });
  user = await update("users", user, { memberIds: [member.id, newMember.id] });
  assert.equal(await save(), 412);
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.locator('[data-conflict-field="memberIds"]').waitFor();
  await accept();
  assert.equal(
    await dialog
      .locator("#field-memberIds")
      .getByRole("checkbox", { name: "Created during review", exact: true })
      .isChecked(),
    true,
  );
  assert.equal(await save(), 200);
  await dialog.waitFor({ state: "hidden" });
  const membershipRecord = await api(`/resources/users/${user.id}`);
  assert.deepEqual(membershipRecord.memberIds, [member.id, newMember.id]);
  console.log(
    "reference choices refresh for explicit review without silently replacing the record baseline",
  );

  await go("locations");
  await page.getByRole("button", { name: "删除 CAS DC", exact: true }).click();
  dialog = page.getByRole("dialog");
  const deleteBaseline = location._etag;
  location = await update("locations", location, {
    address: "Changed before deletion",
  });
  let response = page.waitForResponse(
    (value) =>
      value.request().method() === "DELETE" &&
      value.url().includes("/api/resources/locations/"),
  );
  await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
  assert.equal((await response).status(), 412);
  assert.equal(writes.at(-1).etag, JSON.stringify(deleteBaseline));
  panel = page.getByRole("region", { name: "删除版本冲突", exact: true });
  await panel.getByText("Changed before deletion", { exact: true }).waitFor();
  const deletes = writes.length;
  await accept("delete");
  assert.equal(writes.length, deletes);
  assert.equal(
    (await request(`/resources/locations/${location.id}`)).status,
    200,
  );
  response = page.waitForResponse(
    (value) =>
      value.request().method() === "DELETE" &&
      value.url().includes("/api/resources/locations/"),
  );
  await dialog.getByRole("button", { name: "确认删除", exact: true }).click();
  assert.equal((await response).status(), 200);
  assert.equal(writes.at(-1).etag, JSON.stringify(location._etag));
  await dialog.waitFor({ state: "hidden" });
  let gone = await create("locations", {
    name: "Deleted while editing",
    city: "Original",
  });
  dialog = await open("locations", gone.name);
  await dialog.locator("#field-city").fill("Retained draft city");
  await remove("locations", gone);
  assert.equal(await save(), 404);
  panel = page.getByRole("region", { name: "编辑版本冲突", exact: true });
  await panel.getByText("这条记录已被删除", { exact: true }).waitFor();
  assert.equal(
    await dialog.locator("#field-city").inputValue(),
    "Retained draft city",
  );
  const downloaded = page.waitForEvent("download");
  await panel
    .getByRole("button", { name: "保存非密码草稿副本", exact: true })
    .click();
  const draft = JSON.parse(
    await readFile(await (await downloaded).path(), "utf8"),
  );
  assert.equal(draft.values.city, "Retained draft city");
  assert.ok(!Object.hasOwn(draft.values, "_etag"));
  await dialog
    .getByRole("button", { name: "放弃草稿并关闭", exact: true })
    .click();
  console.log(
    "delete conflicts require a reviewed baseline; externally deleted records preserve an exportable draft",
  );
  assert.deepEqual(errors, []);
  console.log(`CAS fixture: ${dataDir}`);
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
