// Isolated real global search UI, scoped results, stable cursors and exact entity navigation.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-search-ui-"),
  socket = net.createServer();
await new Promise((resolve) => socket.listen(0, "127.0.0.1", resolve));
const port = socket.address().port;
await new Promise((resolve) => socket.close(resolve));
const base = `http://127.0.0.1:${port}`,
  password = "SFlow-browser-fixture-password-123";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-search-ui-server",
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
  // Navigation-only target checks. Run just this block with
  // TEST_SEARCH_TARGETS_ONLY=1; no generated configs or downloads are requested.
  const verifyExtraTargets = async () => {
    const rootDirectory = await api("/directories", {
      name: "NavFolderRoot",
      visibility: "staff",
    });
    const directory = await api("/directories", {
      name: "NavFolderTarget",
      parentId: rootDirectory.id,
    });
    const sibling = await api("/directories", {
      name: "NavFolderSibling",
      parentId: rootDirectory.id,
    });
    const upload = async (name, directoryId) => {
      const session = await api("/session");
      const response = await context.request.post(base + "/api/documents", {
        headers: { "X-CSRF-Token": session.csrfToken },
        multipart: {
          file: {
            name,
            mimeType: "text/plain",
            buffer: Buffer.from(
              "Synthetic navigation-only file; never downloaded by the browser.",
            ),
          },
          directoryId,
        },
      });
      const result = await response.json();
      assert.ok(response.ok(), JSON.stringify(result));
      return result;
    };
    const document = await upload("NavExactDocument.txt", directory.id);
    await upload("NavDocumentSibling.txt", directory.id);
    await upload("NavOutsideDocument.txt", sibling.id);
    await api("/resources/provisioning-profiles", {
      name: "NavProfileDecoy",
      options: { kind: "nagios", families: [4] },
    });
    const profile = await api("/resources/provisioning-profiles", {
      name: "NavProfileTarget",
      options: { kind: "nagios", families: [4] },
    });
    const loc = await api("/resources/locations", {
      name: "NavPhysicalLocation",
    });
    const rack = await api("/resources/racks", {
      name: "NavPhysicalRack",
      locationId: loc.id,
      units: 42,
    });
    const panel = await api("/resources/patch-panels", {
      name: "NavPatchPanel",
      rackId: rack.id,
      portCount: 12,
    });
    await api("/resources/patch-ports", {
      name: "Navigation Fibre One",
      panelId: panel.id,
      number: 1,
    });
    const patch = await api("/resources/patch-ports", {
      name: "Navigation Fibre Two",
      panelId: panel.id,
      number: 2,
    });
    const switches = [];
    for (const name of ["NavCoreSwitchA", "NavCoreSwitchB"])
      switches.push(
        await api("/resources/switches", { name, locationId: loc.id }),
      );
    // Reuse the minimal L2 core fixture already exercised by traffic.mjs.
    const bundles = [];
    for (let index = 0; index < 2; index++) {
      const corePorts = [];
      for (let side = 0; side < 2; side++)
        corePorts.push(
          await api("/resources/ports", {
            name: `core-${index}-${side}`,
            switchId: switches[side].id,
            speed: 10000,
            status: "active",
          }),
        );
      const plan = await api("/core/preview", {
        bundle: {
          name: `Navigation Core ${index === 0 ? "One" : "Two"}`,
          type: "l2-lag",
          enabled: false,
          sideA: {
            switchId: switches[0].id,
            interfaceName: `core-a-${index}`,
            channelGroup: index + 1,
          },
          sideB: {
            switchId: switches[1].id,
            interfaceName: `core-b-${index}`,
            channelGroup: index + 1,
          },
          links: [
            {
              enabled: true,
              sideA: { portId: corePorts[0].id, duplex: "full", autoneg: true },
              sideB: { portId: corePorts[1].id, duplex: "full", autoneg: true },
            },
          ],
        },
      });
      const inventory = await api("/core/inventory");
      bundles.push(
        await api("/core/bundles", {
          bundle: plan.bundle,
          portEtags: Object.fromEntries(
            inventory.ports
              .filter((p) => corePorts.some((selected) => selected.id === p.id))
              .map((p) => [p.id, p._etag]),
          ),
        }),
      );
    }
    const coreList = await api("/core");
    const core = coreList.items.find(
      (item) => item.id !== coreList.items[0].id,
    );
    assert.ok(core, "fixture needs a non-default core target");
    const patchBefore = (await api("/patching/ports")).items.find(
      (item) => item.id === patch.id,
    );
    const mutations = [],
      bodyReads = [],
      downloads = [];
    const recordRequest = (request) => {
      const path = new URL(request.url()).pathname;
      if (!path.startsWith("/api/")) return;
      if (!["GET", "HEAD"].includes(request.method()))
        mutations.push(`${request.method()} ${path}`);
      if (/\/(download|config|content|text)$/.test(path)) bodyReads.push(path);
    };
    const recordDownload = (download) =>
      downloads.push(download.suggestedFilename());
    page.on("request", recordRequest);
    page.on("download", recordDownload);
    const choose = async (term, kind, id) => {
      await page.getByRole("button", { name: "全局搜索", exact: true }).click();
      const searched = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === "/api/search" &&
          new URL(response.url()).searchParams.get("q") === term,
      );
      await page.locator("#global-search-query").fill(term);
      const response = await searched;
      assert.equal(response.status(), 200, await response.text());
      await page
        .locator(`[data-search-kind="${kind}"][data-search-id="${id}"]`)
        .click();
      await page.getByRole("dialog").waitFor({ state: "hidden" });
    };
    await page.goto(base);
    await choose(document.name, "documents", document.id);
    await page.getByText("已定位搜索结果", { exact: true }).waitFor();
    await page
      .locator("main tbody tr")
      .filter({ hasText: document.name })
      .waitFor();
    assert.equal(await page.locator("main tbody tr").count(), 1);
    let parameters = new URLSearchParams(
      (await page.evaluate(() => location.hash)).split("?")[1],
    );
    assert.equal(parameters.get("id"), document.id);
    assert.equal(parameters.get("directoryId"), directory.id);
    await page
      .getByRole("navigation", { name: "文档目录", exact: true })
      .getByRole("button", { name: directory.name, exact: true })
      .waitFor();
    await choose(directory.name, "directories", directory.id);
    await page
      .locator("main tbody tr")
      .filter({ hasText: "NavDocumentSibling.txt" })
      .waitFor();
    assert.equal(await page.locator("main tbody tr").count(), 2);
    assert.equal(
      await page
        .locator("main tbody tr")
        .filter({ hasText: "NavOutsideDocument.txt" })
        .count(),
      0,
    );
    parameters = new URLSearchParams(
      (await page.evaluate(() => location.hash)).split("?")[1],
    );
    assert.equal(parameters.get("directoryId"), directory.id);
    assert.equal(parameters.has("id"), false);
    console.log(
      "search document and nested directory targets show exact authorized record/contents without opening a file passed",
    );
    await choose(profile.name, "provisioning-profiles", profile.id);
    await page.getByText("已定位搜索结果", { exact: true }).waitFor();
    await page
      .locator("main tbody tr")
      .filter({ hasText: profile.name })
      .waitFor();
    assert.equal(await page.locator("main tbody tr").count(), 1);
    assert.equal(
      await page
        .getByRole("button", { name: "生成规则", exact: true })
        .getAttribute("aria-pressed"),
      "true",
    );
    parameters = new URLSearchParams(
      (await page.evaluate(() => location.hash)).split("?")[1],
    );
    assert.equal(parameters.get("profileId"), profile.id);
    console.log(
      "search provisioning target opens only its exact profile, without generating an artifact passed",
    );
    await choose(core.name, "core-bundles", core.id);
    await page.getByRole("heading", { name: core.name, exact: true }).waitFor();
    assert.ok(
      (
        await page
          .locator(`[data-core-bundle="${core.id}"]`)
          .getAttribute("class")
      ).includes("bg-primary/5"),
    );
    parameters = new URLSearchParams(
      (await page.evaluate(() => location.hash)).split("?")[1],
    );
    assert.equal(parameters.get("id"), core.id);
    await choose(patch.name, "patch-ports", patch.id);
    await page
      .getByRole("heading", { name: `${patch.name} · 配线详情`, exact: true })
      .waitFor();
    parameters = new URLSearchParams(
      (await page.evaluate(() => location.hash)).split("?")[1],
    );
    assert.equal(parameters.get("id"), patch.id);
    assert.deepEqual(
      mutations,
      [],
      "navigation unexpectedly wrote data or generated a plan",
    );
    assert.deepEqual(
      bodyReads,
      [],
      "navigation unexpectedly fetched file/config contents",
    );
    assert.deepEqual(downloads, [], "navigation triggered a download");
    page.off("request", recordRequest);
    page.off("download", recordDownload);
    assert.equal((await api("/provisioning")).items.length, 0);
    assert.equal(
      (await api(`/resources/provisioning-profiles/${profile.id}`)).revision,
      profile.revision,
    );
    assert.equal(
      (await api(`/core/bundles/${core.id}`)).revision,
      core.revision,
    );
    const patchAfter = (await api("/patching/ports")).items.find(
      (item) => item.id === patch.id,
    );
    assert.equal(patchAfter.revision, patchBefore.revision);
    const documentAfter = (await api("/documents")).items.find(
      (item) => item.id === document.id,
    );
    assert.equal(documentAfter.checksum, document.checksum);
    assert.deepEqual(errors, []);
    console.log(
      "search non-default Core/Patching targets select exact detail; all target navigation is read-only, no generation/download/mutation passed",
    );
  };
  await verifyExtraTargets();
  if (process.env.TEST_SEARCH_TARGETS_ONLY !== "1") {
    const member = await api("/resources/members", {
      name: "Visible Search Member",
      asn: 64510,
      status: "active",
    });
    const foreign = await api("/resources/members", {
      name: "Foreign Search Organization",
      asn: 64511,
      status: "active",
    });
    await api("/resources/notes", {
      name: "Private staff search note",
      memberId: member.id,
      body: "PRIVATESEARCHMARKER41",
      visibility: "internal",
    });
    for (let i = 0; i < 25; i++)
      await api("/resources/members", {
        name: `Bulk Catalog Member ${String(i).padStart(2, "0")}`,
        asn: 64600 + i,
        status: "active",
      });
    let searchRequests = 0;
    page.on("request", (r) => {
      if (new URL(r.url()).pathname === "/api/search") searchRequests++;
    });
    await page.goto(base);
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    await page.locator("#global-search-query").waitFor();
    assert.equal(
      searchRequests,
      0,
      "opening empty search enumerated inventory",
    );
    assert.equal(
      await page.locator('#global-search-type option[value="users"]').count(),
      1,
    );
    const query = async (term, targetPage = page) => {
      const pending = targetPage.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === "/api/search" &&
          new URL(r.url()).searchParams.get("q") === term,
      );
      await targetPage.locator("#global-search-query").fill(term);
      const response = await pending,
        data = await response.json();
      assert.equal(response.status(), 200, JSON.stringify(data));
      return data;
    };
    const answer = await query("AS64510");
    assert.equal(answer.mode, "asn");
    assert.ok(
      answer.groups.some((g) =>
        g.items.some((i) => i.kind === "members" && i.id === member.id),
      ),
    );
    await page
      .getByRole("option")
      .filter({ hasText: "Visible Search Member" })
      .waitFor();
    await page.locator("#global-search-query").press("Enter");
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.getByText("已定位搜索结果", { exact: true }).waitFor();
    assert.equal(
      new URLSearchParams(
        (await page.evaluate(() => location.hash)).split("?")[1],
      ).get("id"),
      member.id,
    );
    assert.equal(await page.locator("main tbody tr").count(), 1);
    console.log(
      "global empty-open makes no query; real ASN lookup + Enter opens exact resource record passed",
    );
    const focusedHash = await page.evaluate(() => location.hash);
    await page
      .locator('main input[aria-label^="搜索"]')
      .fill("NO LOCAL ROW MATCH");
    assert.equal(await page.locator("main tbody tr").count(), 0);
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    await query("AS64510");
    await page
      .locator(`[data-search-kind="members"][data-search-id="${member.id}"]`)
      .click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page.locator("main tbody tr").waitFor();
    assert.equal(await page.evaluate(() => location.hash), focusedHash);
    assert.equal(
      await page.locator('main input[aria-label^="搜索"]').inputValue(),
      "",
    );
    assert.equal(await page.locator("main tbody tr").count(), 1);
    console.log(
      "global same-hash resource navigation remounts and restores the exact target passed",
    );

    await page.keyboard.press("Control+k");
    await page.locator("#global-search-query").waitFor();
    assert.equal(
      await page
        .locator("#global-search-query")
        .evaluate((e) => document.activeElement === e),
      true,
    );
    const bulk = await query("Bulk Catalog");
    assert.equal(bulk.total, 25);
    assert.ok(bulk.nextCursor);
    await api("/resources/members", {
      name: "Bulk Catalog Member 25",
      asn: 64625,
      status: "active",
    });
    const stale = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/search" &&
        new URL(r.url()).searchParams.has("cursor"),
    );
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    assert.equal((await stale).status(), 409);
    await page
      .getByRole("button", { name: "从第一页重新搜索", exact: true })
      .waitFor();
    assert.equal(
      await page.locator("#global-search-results").getByRole("option").count(),
      0,
    );
    const restarted = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/search" &&
        !new URL(r.url()).searchParams.has("cursor"),
    );
    await page
      .getByRole("button", { name: "从第一页重新搜索", exact: true })
      .click();
    assert.equal((await restarted).status(), 200);
    const next = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/search" &&
        new URL(r.url()).searchParams.has("cursor"),
    );
    await page.getByRole("button", { name: "下一页", exact: true }).click();
    assert.equal((await next).status(), 200);
    await page.getByText("第 2 页", { exact: true }).waitFor();
    assert.equal(
      await page.locator("#global-search-results").getByRole("option").count(),
      6,
    );
    await page.getByRole("button", { name: "上一页", exact: true }).click();
    await page.getByText("第 1 页", { exact: true }).waitFor();
    assert.equal(
      await page.locator("#global-search-results").getByRole("option").count(),
      20,
    );
    await page.locator("#global-search-query").press("ArrowDown");
    assert.equal(
      await page
        .locator("#global-search-result-1")
        .getAttribute("aria-selected"),
      "true",
    );
    await page.keyboard.press("Escape");
    console.log(
      "global CtrlK, keyboard selection, stable cursor pages and changed-revision409 clears old results passed",
    );
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    let releaseHeld, captured, finished;
    const held = new Promise((resolve) => {
      releaseHeld = resolve;
    });
    const capturedRequest = new Promise((resolve) => {
      captured = resolve;
    });
    const retiredRequest = new Promise((resolve) => {
      finished = resolve;
    });
    const delayed = async (route) => {
      if (
        new URL(route.request().url()).searchParams.get("q") !== "Bulk Catalog"
      )
        return route.continue();
      try {
        const response = await route.fetch();
        captured();
        await held;
        await route.fulfill({ response });
      } catch {
        /* The old browser fetch is expected to be aborted. */
      } finally {
        finished();
      }
    };
    await page.route("**/api/search?*", delayed);
    await page.locator("#global-search-query").fill("Bulk Catalog");
    await capturedRequest;
    await query("AS64510");
    releaseHeld();
    await retiredRequest;
    await page.unroute("**/api/search?*", delayed);
    await page.evaluate(
      () =>
        new Promise((resolve) =>
          requestAnimationFrame(() => requestAnimationFrame(resolve)),
        ),
    );
    assert.equal(
      await page.locator("#global-search-query").inputValue(),
      "AS64510",
    );
    assert.equal(
      await page.locator("#global-search-results").getByRole("option").count(),
      1,
    );
    // Mock ONLY the client presentation of permissions being revoked mid-dialog.
    // Real tenant authorization and private-field matching are tested below.
    const revoked = async (route) =>
      route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ error: "fixture: staff permissions changed" }),
      });
    await page.route("**/api/search?*", revoked);
    const deniedPresentation = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/search" && r.status() === 403,
    );
    await page.locator("#global-search-type").selectOption("members");
    await deniedPresentation;
    await page.getByRole("alert").waitFor();
    assert.equal(
      await page.locator("#global-search-results").getByRole("option").count(),
      0,
    );
    await page.unroute("**/api/search?*", revoked);
    const permissionRecovery = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/search" && r.status() === 200,
    );
    await page
      .getByRole("button", { name: "从第一页重新搜索", exact: true })
      .click();
    await permissionRecovery;
    await page
      .locator(`[data-search-kind="members"][data-search-id="${member.id}"]`)
      .waitFor();
    console.log(
      "mocked staff permission-revocation403 clears client results; real endpoint recovery passed",
    );
    await page.keyboard.press("Escape");
    console.log(
      "global delayed old real search response cannot replace newer input/results passed",
    );

    const location = await api("/resources/locations", {
      name: "Search receiver DC",
    });
    const sw = await api("/resources/switches", {
      name: "Search receiver switch",
      locationId: location.id,
    });
    const port = await api("/resources/ports", {
      name: "Search receiver port",
      switchId: sw.id,
      status: "active",
    });
    const vlan = await api("/resources/vlans", {
      name: "Search receiver VLAN",
      tag: 100,
      ipv4Prefix: "192.0.2.0/24",
    });
    const connection = await api("/resources/connections", {
      name: "Search receiver access",
      memberId: member.id,
      portId: port.id,
      vlanId: vlan.id,
      ipv4: "192.0.2.10",
      status: "active",
    });
    const receiver = await api("/sflow/receivers", {
      name: "Exact Receiver Target",
      spec: {
        memberId: member.id,
        connectionIds: [connection.id],
        vlanIds: [vlan.id],
        address: "203.0.113.40",
        port: 6343,
        enabled: false,
      },
    });
    await api("/sflow/receivers", {
      name: "Other Receiver Target",
      spec: {
        memberId: member.id,
        connectionIds: [connection.id],
        vlanIds: [vlan.id],
        address: "203.0.113.41",
        port: 6343,
        enabled: false,
      },
    });
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    await query("Exact Receiver");
    await page
      .locator(
        `[data-search-kind="sflow-receivers"][data-search-id="${receiver.id}"]`,
      )
      .click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    const receiverCard = page.getByTestId("sflow-receiver");
    await receiverCard.waitFor();
    assert.equal(await receiverCard.count(), 1);
    assert.equal(
      await receiverCard.getAttribute("data-receiver-id"),
      receiver.id,
    );
    assert.equal(await receiverCard.getAttribute("data-located"), "true");
    assert.equal(
      (await api(`/sflow/receivers/${receiver.id}`)).status,
      "disabled",
    );
    // Create a different target while this same SFlow page still holds its old catalog.
    const freshReceiver = await api("/sflow/receivers", {
      name: "Fresh Remote Receiver",
      spec: {
        memberId: member.id,
        connectionIds: [connection.id],
        vlanIds: [vlan.id],
        address: "203.0.113.42",
        port: 6343,
        enabled: false,
      },
    });
    await page.getByRole("button", { name: "全局搜索", exact: true }).click();
    await query("Fresh Remote");
    await page
      .locator(
        `[data-search-kind="sflow-receivers"][data-search-id="${freshReceiver.id}"]`,
      )
      .click();
    await page.getByRole("dialog").waitFor({ state: "hidden" });
    await page
      .getByTestId("sflow-receiver")
      .filter({ hasText: "Fresh Remote Receiver" })
      .waitFor();
    assert.equal(await page.getByTestId("sflow-receiver").count(), 1);
    assert.equal(
      await page.getByTestId("sflow-receiver").getAttribute("data-receiver-id"),
      freshReceiver.id,
    );
    assert.equal(
      (await api(`/sflow/receivers/${freshReceiver.id}`)).status,
      "disabled",
    );
    console.log(
      "global same-page different receiver ID reloads latest catalog and targets newly created record passed",
    );
    await page.goto(base + "/#sflow?tab=receivers&receiverId=missing-target");
    await page
      .getByRole("heading", { name: "未找到指定接收器", exact: true })
      .waitFor();
    assert.equal(await page.getByTestId("sflow-receiver").count(), 0);
    console.log(
      "global receiver result targets exact disabled receiver; missing ID never falls back or auto-approves passed",
    );

    await api("/resources/users", {
      name: "Search member",
      email: "search-member@example.test",
      password,
      role: "member",
      memberId: member.id,
    });
    const memberContext = await browser.newContext({
      viewport: { width: 390, height: 844 },
    });
    assert.equal(
      (
        await memberContext.request.post(base + "/api/login", {
          data: { email: "search-member@example.test", password },
        })
      ).status(),
      200,
    );
    const memberPage = await memberContext.newPage();
    memberPage.on("pageerror", (e) => errors.push(e.message));
    await memberPage.goto(base);
    await memberPage
      .getByRole("button", { name: "全局搜索", exact: true })
      .click();
    assert.equal((await query("PRIVATESEARCHMARKER41", memberPage)).total, 0);
    assert.equal(
      (await query("Foreign Search Organization", memberPage)).total,
      0,
    );
    assert.equal((await query("Visible Search", memberPage)).total, 1);
    const permittedMemberTypes = new Set([
      "",
      "members",
      "connections",
      "contacts",
      "member-tags",
      "tags",
      "notes",
      "equipment",
      "documents",
      "directories",
      "sflow-receivers",
      "patch-ports",
    ]);
    const memberOptions = await memberPage
      .locator("#global-search-type option")
      .evaluateAll((options) => options.map((o) => o.value));
    assert.ok(memberOptions.every((value) => permittedMemberTypes.has(value)));
    assert.equal(memberOptions.includes("users"), false);
    assert.equal(await memberPage.locator("#global-search-switch").count(), 0);
    // This is an actual backend request, not the presentation mock above.
    const forbidden = await memberContext.request.get(
      base + "/api/search?q=Visible%20Search&types=users",
    );
    assert.equal(forbidden.status(), 403);
    await memberPage
      .locator("#global-search-results")
      .getByRole("option")
      .filter({ hasText: "Visible Search Member" })
      .waitFor();
    assert.equal(
      await memberPage.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      ),
      false,
    );
    await memberPage.screenshot({
      path: "/tmp/moedove-search-mobile.png",
      fullPage: true,
    });
    await memberContext.close();
    await api("/resources/users", {
      name: "Search staff viewer",
      email: "search-viewer@example.test",
      password,
      role: "viewer",
    });
    const viewerContext = await browser.newContext();
    assert.equal(
      (
        await viewerContext.request.post(base + "/api/login", {
          data: { email: "search-viewer@example.test", password },
        })
      ).status(),
      200,
    );
    const viewerPage = await viewerContext.newPage();
    viewerPage.on("pageerror", (e) => errors.push(e.message));
    await viewerPage.goto(base);
    await viewerPage
      .getByRole("button", { name: "全局搜索", exact: true })
      .click();
    await viewerPage.locator("#global-search-query").waitFor();
    assert.equal(
      await viewerPage
        .locator('#global-search-type option[value="users"]')
        .count(),
      0,
    );
    assert.equal(await viewerPage.locator("#global-search-switch").count(), 1);
    await viewerContext.close();
    assert.deepEqual(errors, []);
    console.log(
      "global role-aware controls, real member403/private-field isolation,390px and no page errors passed",
    );
  }
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
  console.log("search isolated browser and manager stopped");
}
