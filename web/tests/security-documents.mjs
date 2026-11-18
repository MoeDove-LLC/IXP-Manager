// Uses a fresh isolated Go service. No production router changes are made.
import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.CHROMIUM_PATH,
  args: ["--no-sandbox"],
});
const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
  }),
  page = await context.newPage(),
  errors = [];
page.on("pageerror", (e) => errors.push(e.message));
const base = process.env.TEST_URL || "http://127.0.0.1:18080",
  password = "Security-test-password-123",
  newPassword = "Changed-test-password-456";
const go = async (path) => {
  await page.goto(`${base}/#${path}`);
  await page.locator("main").waitFor();
};
const json = async (path, body, method = "POST") =>
  page.evaluate(
    async ({ path, body, method }) => {
      const session = await (await fetch("/api/session")).json();
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
          "X-CSRF-Token": session.csrfToken,
        },
        body: JSON.stringify(body),
      });
      const d = await r.json();
      if (!r.ok) throw new Error(d.error);
      return d;
    },
    { path, body, method },
  );
const totp = (secret, offset = 0) => {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret)
    bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000) + offset));
  const h = createHmac("sha1", key).update(counter).digest(),
    o = h[19] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, "0");
};
const finishSecret = async () => {
  await page.getByLabel("我已妥善保存").check();
  await page.getByRole("button", { name: "完成", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
};
const reauth = async (code) => {
  await page
    .getByRole("dialog")
    .getByLabel("当前密码", { exact: true })
    .fill(password);
  if (code)
    await page
      .getByRole("dialog")
      .getByLabel("验证器代码或恢复码", { exact: true })
      .fill(code);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
};
const login = async (target, email, pwd, code) => {
  await target.goto(base);
  await target.getByLabel("邮箱地址").fill(email);
  await target.getByLabel("密码", { exact: true }).fill(pwd);
  await target.getByRole("button", { name: "登录控制台", exact: true }).click();
  if (code) {
    await target.getByLabel("验证器代码或恢复码", { exact: true }).fill(code);
    await target
      .getByRole("button", { name: "登录控制台", exact: true })
      .click();
  }
  await target.locator("main").waitFor();
};
try {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("Security Docs Test");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page
    .getByLabel("初始化令牌", { exact: true })
    .fill(process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token");
  await page.getByLabel("管理员姓名").fill("资料管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page.getByLabel("密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "创建工作空间", exact: true }).click();
  await page.locator("main").waitFor();
  const a = await json("/resources/members", {
      name: "Member A",
      asn: 64513,
      status: "active",
    }),
    b = await json("/resources/members", {
      name: "Member B",
      asn: 64514,
      status: "active",
    });
  await json("/resources/users", {
    name: "Member A User",
    email: "a@example.test",
    password,
    role: "member",
    memberId: a.id,
  });
  await json("/resources/users", {
    name: "Member B User",
    email: "b@example.test",
    password,
    role: "member",
    memberId: b.id,
  });
  await go("documents");
  await page.getByRole("button", { name: "新建目录", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("成员资料");
  await page.getByLabel("访问范围", { exact: true }).selectOption("member");
  await page.getByLabel("关联成员", { exact: false }).selectOption(a.id);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("button", { name: "成员资料", exact: true }).click();
  await page.getByRole("button", { name: "上传文件", exact: true }).click();
  await page.locator("#doc-file").setInputFiles({
    name: "member-guide.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("original member text"),
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("button", { name: "下载 member-guide.txt", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "编辑文本 member-guide.txt", exact: true })
    .click();
  await page.getByLabel("文档文本内容").fill("updated member text");
  await page.getByRole("button", { name: "保存文本", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const docListing = await page.evaluate(
    async () => await (await fetch("/api/documents")).json(),
  );
  const memberDoc = docListing.items.find((d) => d.name === "member-guide.txt");
  assert.ok(memberDoc);
  const downloaded = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "下载 member-guide.txt", exact: true })
    .click();
  assert.equal((await downloaded).suggestedFilename(), "member-guide.txt");
  await page
    .getByRole("button", { name: "编辑文本 member-guide.txt", exact: true })
    .click();
  await page
    .getByLabel("文档文本内容")
    .fill("draft while another editor saves");
  const current = await page.evaluate(
    async (id) => await (await fetch(`/api/documents/${id}/text`)).json(),
    memberDoc.id,
  );
  await json(
    `/documents/${memberDoc.id}/text`,
    { content: "concurrent saved text", checksum: current.checksum },
    "PUT",
  );
  await page.getByRole("button", { name: "保存文本", exact: true }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  assert.equal(
    await page.getByLabel("文档文本内容").inputValue(),
    "draft while another editor saves",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  const chooserPromise = page.waitForEvent("filechooser");
  await page
    .getByRole("button", { name: "替换内容 member-guide.txt", exact: true })
    .click();
  const replacementResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith(`/documents/${memberDoc.id}/content`) &&
      r.request().method() === "POST",
  );
  await (
    await chooserPromise
  ).setFiles({
    name: "replacement.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("replacement member text"),
  });
  assert.equal((await replacementResponse).status(), 200);
  await page
    .locator("main")
    .getByRole("button", { name: "文档库", exact: true })
    .click();
  await page
    .getByRole("button", { name: "删除目录 成员资料", exact: true })
    .click();
  await page.getByRole("button", { name: "确认删除", exact: true }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  await page.getByRole("button", { name: "上传文件", exact: true }).click();
  await page.locator("#doc-file").setInputFiles({
    name: "public-guide.md",
    mimeType: "text/markdown",
    buffer: Buffer.from("# Public guide"),
  });
  await page.getByLabel("访问范围", { exact: true }).selectOption("public");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("button", { name: "下载 public-guide.md", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "上传文件", exact: true }).click();
  await page.locator("#doc-file").setInputFiles({
    name: "internal.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("staff only"),
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("button", { name: "下载 internal.txt", exact: true })
    .waitFor();
  await page.getByRole("button", { name: "新建目录", exact: true }).click();
  await page.getByLabel("名称", { exact: true }).fill("共享资料");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("button", { name: "共享资料", exact: true }).click();
  await page.getByRole("button", { name: "上传文件", exact: true }).click();
  await page.locator("#doc-file").setInputFiles({
    name: "shared.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("shared guide"),
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("button", { name: "下载 shared.txt", exact: true })
    .waitFor();
  await page
    .locator("main")
    .getByRole("button", { name: "文档库", exact: true })
    .click();
  await page
    .getByRole("button", { name: "编辑目录 共享资料", exact: true })
    .click();
  await page
    .getByLabel("访问范围", { exact: true })
    .selectOption("authenticated");
  await page
    .getByLabel(
      "我理解此修改会同步到目录下全部文件和子目录，并改变它们的访问范围。",
      { exact: true },
    )
    .check();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  const cascadeDoc = await page.evaluate(async () => {
    const d = await (await fetch("/api/documents")).json();
    return d.items.find((f) => f.name === "shared.txt");
  });
  assert.equal(cascadeDoc.visibility, "authenticated");
  await page.getByRole("button", { name: "共享资料", exact: true }).click();
  await page
    .getByRole("button", { name: "编辑文件信息 shared.txt", exact: true })
    .click();
  await page.getByLabel("名称", { exact: true }).fill("shared-guide.txt");
  await page.getByLabel("说明", { exact: true }).fill("共享接入指南");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("button", { name: "下载 shared-guide.txt", exact: true })
    .waitFor();
  await page
    .locator("main")
    .getByRole("button", { name: "文档库", exact: true })
    .click();
  await page.getByLabel("搜索文档", { exact: true }).fill("共享接入指南");
  await page
    .getByRole("button", { name: "下载 shared-guide.txt", exact: true })
    .waitFor();
  await page.getByLabel("搜索文档", { exact: true }).fill("");
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "/tmp/moedove-documents-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log(
    "Directory permission cascade, metadata update/search and mobile document layout passed",
  );
  await page.screenshot({ path: "/tmp/moedove-documents.png", fullPage: true });
  console.log(
    "Documents hierarchy, upload, download, replace, optimistic text conflict and protected directory delete passed",
  );
  await go("security");
  await page
    .getByRole("button", { name: "设置身份验证器", exact: true })
    .click();
  await reauth();
  await page.getByLabel("手动设置密钥", { exact: true }).waitFor();
  const secret = await page
    .getByLabel("手动设置密钥", { exact: true })
    .inputValue();
  assert.match(
    await page.getByAltText("用于绑定身份验证器的二维码").getAttribute("src"),
    /^data:image\/png/,
  );
  await page.getByLabel("6 位验证代码", { exact: true }).fill(totp(secret));
  await page.getByRole("button", { name: "验证并开启", exact: true }).click();
  let recovery = (await page.getByLabel("恢复码列表").innerText())
    .trim()
    .split(/\s+/);
  assert.equal(recovery.length, 10);
  await finishSecret();
  await page.getByText("已开启", { exact: true }).waitFor();
  await page
    .getByRole("button", { name: "创建 API 令牌", exact: true })
    .click();
  await page.getByLabel("令牌名称", { exact: true }).fill("browser-read-token");
  await reauth(recovery.shift());
  const raw = await page.getByLabel("API 令牌", { exact: true }).innerText();
  await finishSecret();
  assert.equal(
    (
      await fetch(`${base}/api/resources/members`, {
        headers: { Authorization: `Bearer ${raw}` },
      })
    ).status,
    200,
  );
  assert.equal(
    (
      await fetch(`${base}/api/settings`, {
        headers: { Authorization: `Bearer ${raw}` },
      })
    ).status,
    403,
  );
  await page
    .getByRole("button", { name: "撤销令牌 browser-read-token", exact: true })
    .click();
  await page.getByRole("button", { name: "确认撤销", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert.equal(
    (
      await fetch(`${base}/api/resources/members`, {
        headers: { Authorization: `Bearer ${raw}` },
      })
    ).status,
    401,
  );
  await page.getByRole("button", { name: "更新恢复码", exact: true }).click();
  await reauth(recovery.shift());
  recovery = (await page.getByLabel("恢复码列表").innerText())
    .trim()
    .split(/\s+/);
  await finishSecret();
  const second = await browser.newContext(),
    secondPage = await second.newPage();
  await login(secondPage, "admin@example.test", password, totp(secret, 1));
  await page.reload();
  await page.getByRole("button", { name: "退出其他设备", exact: true }).click();
  await reauth(recovery.shift());
  await page.getByText("其他设备已退出登录。", { exact: true }).waitFor();
  assert.equal(
    (await secondPage.request.get(`${base}/api/session`)).status(),
    401,
  );
  await second.close();
  await page.screenshot({ path: "/tmp/moedove-security.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    true,
  );
  await page.screenshot({
    path: "/tmp/moedove-security-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  console.log(
    "TOTP QR enrollment, one-time recovery codes, scoped token creation/revocation and session revocation passed",
  );
  await page.getByLabel("当前密码", { exact: true }).fill(password);
  await page.getByLabel("新密码", { exact: true }).fill(newPassword);
  await page.getByLabel("确认新密码", { exact: true }).fill(newPassword);
  await page
    .getByLabel("验证器代码或恢复码", { exact: true })
    .fill(recovery.shift());
  await page
    .getByRole("button", { name: "修改密码并重新登录", exact: true })
    .click();
  await page.getByLabel("邮箱地址").waitFor();
  await login(page, "admin@example.test", newPassword, recovery.shift());
  await go("security");
  await page.getByRole("button", { name: "关闭双重验证", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByLabel("当前密码", { exact: true })
    .fill(newPassword);
  await page
    .getByRole("dialog")
    .getByLabel("验证器代码或恢复码", { exact: true })
    .fill(recovery.shift());
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "继续", exact: true })
    .click();
  await page.getByLabel("邮箱地址").waitFor();
  await login(page, "admin@example.test", newPassword);
  console.log(
    "MFA login, password rotation with forced logout, and disabling MFA passed",
  );
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await login(page, "a@example.test", password);
  await go("documents");
  await page.getByRole("button", { name: "成员资料", exact: true }).click();
  await page
    .getByRole("button", { name: "下载 member-guide.txt", exact: true })
    .waitFor();
  assert.equal(
    await page.getByRole("button", { name: "上传文件", exact: true }).count(),
    0,
  );
  await page
    .locator("main")
    .getByRole("button", { name: "文档库", exact: true })
    .click();
  assert.equal(
    await page
      .getByRole("button", { name: "下载 internal.txt", exact: true })
      .count(),
    0,
  );
  await page.getByRole("button", { name: "退出登录", exact: true }).click();
  await login(page, "b@example.test", password);
  await go("documents");
  await page.getByRole("button", { name: "共享资料", exact: true }).waitFor();
  assert.equal(
    await page.getByRole("button", { name: "成员资料", exact: true }).count(),
    0,
  );
  assert.equal(
    (
      await page.request.get(`${base}/api/documents/${memberDoc.id}/download`)
    ).status(),
    404,
  );
  await page
    .getByRole("button", { name: "下载 public-guide.md", exact: true })
    .waitFor();
  const anonymous = await browser.newContext(),
    publicPage = await anonymous.newPage();
  await publicPage.goto(`${base}/#public-documents`);
  await publicPage
    .getByRole("button", { name: "下载 public-guide.md", exact: true })
    .waitFor();
  assert.equal(
    await publicPage
      .getByRole("button", { name: "下载 internal.txt", exact: true })
      .count(),
    0,
  );
  assert.equal(
    (
      await publicPage.request.get(
        `${base}/api/public/documents/${memberDoc.id}/download`,
      )
    ).status(),
    404,
  );
  await anonymous.close();
  console.log(
    "Member-specific file isolation, read-only access and anonymous public document access passed",
  );
  assert.deepEqual(errors, []);
  console.log(
    "Security and document browser suite passed without uncaught page errors.",
  );
} finally {
  await browser.close();
}
