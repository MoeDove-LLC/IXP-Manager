// Synthetic images and loopback-only isolated app. Never uses external URLs.
import assert from "node:assert/strict";
import net from "node:net";
import { spawn, execFileSync } from "node:child_process";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const dir = await mkdtemp("/tmp/moedove-member-logo-ui-"),
  socket = net.createServer();
await new Promise((r) => socket.listen(0, "127.0.0.1", r));
const port = socket.address().port;
await new Promise((r) => socket.close(r));
const base = `http://127.0.0.1:${port}`,
  password = "member-logo-fixture-password";
const child = spawn(
  process.env.TEST_SERVER_BIN || "/tmp/moedove-logo-server",
  [
    "-listen",
    `127.0.0.1:${port}`,
    "-data",
    `${dir}/data`,
    "-web",
    fileURLToPath(new URL("../dist", import.meta.url)),
  ],
  {
    env: { ...process.env, IXP_SETUP_TOKEN: "logo-fixture-setup" },
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
    if (i > 100) throw new Error("fixture startup timeout");
    await new Promise((r) => setTimeout(r, 100));
  }
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  const context = await browser.newContext({
      viewport: { width: 1440, height: 1000 },
    }),
    page = await context.newPage(),
    errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  assert.equal(
    (
      await context.request.post(base + "/api/setup", {
        data: {
          setupToken: "logo-fixture-setup",
          name: "Logo QA",
          email: "admin@example.test",
          password,
          ixpName: "Logo QA IXP",
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
    ctx = context,
  ) => {
    const session = await (await ctx.request.get(base + "/api/session")).json();
    const r = await ctx.request.fetch(base + "/api" + path, {
      method,
      data,
      headers: { "X-CSRF-Token": session.csrfToken },
    });
    assert.ok(r.ok(), `${method} ${path}: ${r.status()} ${await r.text()}`);
    return r.json();
  };
  const a = await call("/resources/members", {
      name: "合成成员 Logo A",
      asn: 64501,
      status: "active",
    }),
    b = await call("/resources/members", {
      name: "合成成员 B",
      asn: 64502,
      status: "active",
    });
  await call("/resources/users", {
    name: "成员管理员",
    email: "member@example.test",
    password,
    role: "member",
    memberIds: [a.id, b.id],
    memberAdminIds: [a.id, b.id],
  });
  await call("/resources/users", {
    name: "只读成员",
    email: "reader@example.test",
    password,
    role: "member",
    memberId: a.id,
  });
  await call("/resources/users", {
    name: "平台查看者",
    email: "viewer@example.test",
    password,
    role: "viewer",
  });
  await page.goto(base + "/#members");
  await page
    .getByRole("button", { name: `成员 Logo ${a.name}`, exact: true })
    .click();
  const png = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = 120;
      c.height = 60;
      const x = c.getContext("2d");
      x.fillStyle = "#047857";
      x.fillRect(0, 0, 120, 60);
      x.fillStyle = "white";
      x.fillRect(12, 12, 96, 36);
      return c.toDataURL("image/png").split(",")[1];
    }),
    "base64",
  );
  const input = page.getByLabel("上传或替换图片", { exact: true });
  await input.setInputFiles({
    name: "synthetic.png",
    mimeType: "image/png",
    buffer: png,
  });
  await page.getByRole("button", { name: "保存图片", exact: true }).click();
  await page.getByText("成员 Logo 已保存。", { exact: true }).waitFor();
  assert.equal(
    (await fetch(base + `/api/public/members/${a.id}/logo`)).status,
    404,
  );
  await page.getByRole("checkbox", { name: /允许通过公开链接/ }).check();
  await page.getByRole("button", { name: "保存发布设置" }).click();
  await page.getByRole("link", { name: "查看当前公开图片" }).waitFor();
  assert.equal(
    (await fetch(base + `/api/public/members/${a.id}/logo`)).status,
    200,
  );
  console.log("Logo UI upload/private/explicit publication passed");
  // A concurrent publication edit must retain the pending file for review.
  const meta = await call(`/members/${a.id}/logo`);
  await call(
    `/members/${a.id}/logo`,
    { expectedRevision: meta.revision, published: false },
    "PUT",
  );
  await input.setInputFiles({
    name: "pending-replacement.png",
    mimeType: "image/png",
    buffer: png,
  });
  await page.getByRole("button", { name: "保存图片", exact: true }).click();
  await page
    .getByText(
      "服务器已有更新，所选文件和发布选择已保留。请核对最新版本再重试。",
      { exact: true },
    )
    .waitFor();
  assert.equal(
    await input.evaluate((e) => e.files[0].name),
    "pending-replacement.png",
  );
  await page.getByRole("button", { name: "已核对，使用该版本重试" }).click();
  await page.getByRole("button", { name: "保存图片", exact: true }).click();
  await page.getByText("成员 Logo 已保存。", { exact: true }).waitFor();
  await input.setInputFiles({
    name: "active.svg",
    mimeType: "image/svg+xml",
    buffer: Buffer.from("<svg><script/></svg>"),
  });
  await page.getByRole("button", { name: "保存图片", exact: true }).click();
  await page.getByText(/valid PNG or JPEG image is required/).waitFor();
  await input.setInputFiles([]);
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  await page.screenshot({ path: "/tmp/moedove-member-logo-mobile.png" });
  await page.keyboard.press("Escape");
  const login = async (email) => {
    const ctx = await browser.newContext({
      viewport: { width: 390, height: 844 },
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
  const memberContext = await login("member@example.test"),
    memberPage = await memberContext.newPage();
  await memberPage.goto(base + "/#member-profile");
  await memberPage
    .getByRole("heading", { name: "成员 Logo", exact: true })
    .waitFor();
  await memberPage
    .getByRole("button", { name: "删除图片", exact: true })
    .click();
  await memberPage.getByRole("button", { name: "确认删除 Logo" }).click();
  await memberPage.getByText("成员 Logo 已删除，公开访问已撤销。").waitFor();
  assert.equal(
    (await fetch(base + `/api/public/members/${a.id}/logo`)).status,
    404,
  );
  const reader = await login("reader@example.test"),
    readerPage = await reader.newPage();
  await readerPage.goto(base + "/#member-profile");
  await readerPage
    .getByText("只读查看；需要成员管理员或平台运营权限才能修改。", {
      exact: true,
    })
    .waitFor();
  assert.equal(
    await readerPage.getByLabel("上传或替换图片", { exact: true }).count(),
    0,
  );
  assert.equal(
    (await reader.request.get(base + `/api/members/${b.id}/logo`)).status(),
    404,
  );
  const viewer = await login("viewer@example.test"),
    viewerPage = await viewer.newPage();
  await viewerPage.goto(base + "/#members");
  await viewerPage
    .getByRole("button", { name: `成员 Logo ${a.name}`, exact: true })
    .click();
  await viewerPage
    .getByText("只读查看；需要成员管理员或平台运营权限才能修改。", {
      exact: true,
    })
    .waitFor();
  console.log(
    "Logo CAS preserved draft / invalid image / member-admin deletion / reader+viewer boundary /390px passed",
  );
  // Real offline converter -> reviewed private import, all inputs synthetic.
  const stored = createHash("sha1").update(png).digest("hex") + ".png",
    sourceRoot = `${dir}/legacy-public`,
    sourceFile = `${dir}/source.json`,
    bundleFile = `${dir}/logos.json`;
  await mkdir(`${sourceRoot}/logos/${stored[0]}/${stored[1]}`, {
    recursive: true,
  });
  await writeFile(
    `${sourceRoot}/logos/${stored[0]}/${stored[1]}/${stored}`,
    png,
    { mode: 0o600 },
  );
  await writeFile(
    sourceFile,
    JSON.stringify({
      tables: {
        cust: [{ id: 77, name: "迁移图片组织", autsys: 64577, status: 1 }],
        logos: [
          {
            id: 7,
            customer_id: 77,
            stored_name: stored,
            original_name: "source.png",
            uploaded_by: "旧来源展示文字",
            created_at: "2018-01-01",
            width: 120,
            height: 60,
            type: "image/png",
          },
        ],
      },
    }),
    { mode: 0o600 },
  );
  execFileSync(
    process.env.TEST_MIGRATE_BIN || "/tmp/moedove-logo-migrate",
    [
      "--input",
      sourceFile,
      "--logo-bundle",
      "--logo-public-root",
      sourceRoot,
      "--output",
      bundleFile,
    ],
    { stdio: "pipe" },
  );
  await call("/import", {
    version: 1,
    resources: {
      members: [
        {
          id: "legacy-members-77",
          name: "迁移图片组织",
          asn: 64577,
          status: "active",
        },
      ],
    },
  });
  await page.goto(base + "/#settings");
  await page
    .getByLabel("Logo 迁移包", { exact: true })
    .setInputFiles(bundleFile);
  await page
    .getByRole("button", { name: "预览 Logo 导入", exact: true })
    .click();
  await page.getByRole("region", { name: "Logo 导入审核" }).waitFor();
  await page
    .getByRole("checkbox", {
      name: "已核对目标成员与图片来源；导入后逐成员审核公开发布。",
    })
    .check();
  await page
    .getByRole("button", { name: "确认私有导入 Logo", exact: true })
    .click();
  await page
    .getByText("已导入 1 个成员 Logo，全部保持私有。", { exact: true })
    .waitFor();
  assert.equal(
    (await fetch(base + "/api/public/members/legacy-members-77/logo")).status,
    404,
  );
  await page.screenshot({ path: "/tmp/moedove-member-logo-import-mobile.png" });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    ),
    false,
  );
  assert.deepEqual(errors, []);
  console.log(
    "Logo synthetic CLI bundle / preview / reviewed private import passed",
  );
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
  console.log("Logo browser/service/synthetic files cleaned up");
}
