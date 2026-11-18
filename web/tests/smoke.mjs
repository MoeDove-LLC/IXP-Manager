// Browser integration test. Run against an empty, isolated Go instance.
// PLAYWRIGHT_MODULE=/path/to/playwright/index.mjs node tests/smoke.mjs
import assert from "node:assert/strict";
const { chromium } = await import(
  process.env.PLAYWRIGHT_MODULE || "playwright"
);
const browser = await chromium.launch({
  headless: true,
  executablePath: process.env.CHROMIUM_PATH,
  args: ["--no-sandbox"],
});
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
const base = process.env.TEST_URL || "http://127.0.0.1:18080";
const token = process.env.TEST_SETUP_TOKEN || "moedove-browser-test-token";
const go = async (kind) => {
  await page.goto(`${base}/#${kind}`);
  await page.locator("main").waitFor();
};
const create = async (kind, values) => {
  await go(kind);
  await page.locator(".page-heading button").click();
  const dialog = page.getByRole("dialog");
  await dialog.waitFor();
  for (const [key, value] of Object.entries(values)) {
    if (kind === "users" && key === "memberId") {
      await dialog
        .locator("#field-memberIds")
        .getByRole("checkbox", { name: String(value), exact: true })
        .check();
      continue;
    }
    if (kind === "connections" && key === "speed") continue;
    if (kind === "connections" && key === "portId") {
      await dialog
        .getByRole("checkbox", { name: new RegExp(String(value)) })
        .check();
      continue;
    }
    const field = dialog.locator(`#field-${key}`);
    const tag = await field.evaluate((e) => e.tagName);
    if (tag === "SELECT") await field.selectOption({ label: value });
    else if (typeof value === "boolean") await field.setChecked(value);
    else await field.fill(String(value));
  }
  await dialog.getByRole("button", { name: "保存", exact: true }).click();
  await dialog.waitFor({ state: "hidden" });
  await page.getByRole("cell", { name: values.name, exact: true }).waitFor();
};
try {
  await page.goto(base);
  await page
    .getByLabel("交换中心名称", { exact: true })
    .fill("MoeDove Test IXP");
  await page.getByLabel("交换中心 ASN").fill("64512");
  await page.getByLabel("初始化令牌", { exact: true }).fill(token);
  await page.getByLabel("管理员姓名").fill("测试管理员");
  await page.getByLabel("邮箱地址").fill("admin@example.test");
  await page
    .getByLabel("密码", { exact: true })
    .fill("Browser-test-password-123");
  await page.getByRole("button", { name: "创建工作空间" }).click();
  await page.locator("main").waitFor();
  console.log("setup passed");
  await create("members", {
    name: "Example Network",
    asn: 64513,
    email: "noc@example.test",
  });
  await create("locations", {
    name: "Test DC",
    city: "Shanghai",
    country: "CN",
  });
  await create("switches", {
    name: "sw-01",
    locationId: "Test DC",
    managementIp: "192.0.2.250",
  });
  await create("ports", {
    name: "Ethernet1",
    switchId: "sw-01",
    speed: 10000,
    memberId: "Example Network",
  });
  await create("vlans", {
    name: "Peering LAN",
    tag: 100,
    ipv4Prefix: "192.0.2.0/24",
    ipv6Prefix: "2001:db8::/64",
  });
  await create("connections", {
    name: "Example connection",
    memberId: "Example Network",
    portId: "Ethernet1",
    vlanId: "Peering LAN",
    ipv4: "192.0.2.10",
    speed: 10000,
  });
  await create("routers", {
    name: "rs-01",
    asn: 64512,
    routerId: "192.0.2.1",
    vlanId: "Peering LAN",
    localIpv4: "192.0.2.1",
  });
  await create("peers", {
    name: "Example IPv4",
    routerId: "rs-01",
    memberId: "Example Network",
    asn: 64513,
    address: "192.0.2.10",
    enabled: true,
    allowedPrefixes: "198.51.100.0/24",
  });
  console.log("resource graph CRUD passed");
  await go("members");
  await page.getByRole("button", { name: "编辑 Example Network" }).click();
  await page.locator("#field-name").fill("Example Network Updated");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "保存", exact: true })
    .click();
  await page
    .getByRole("cell", { name: "Example Network Updated", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "删除 Example Network Updated" })
    .click();
  await page.getByRole("button", { name: "确认删除" }).click();
  await page.getByRole("dialog").getByRole("alert").waitFor();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  console.log("edit and referential delete protection passed");
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page
    .getByLabel("路由服务器", { exact: true })
    .selectOption({ label: "rs-01" });
  await page.getByRole("button", { name: "生成预览", exact: true }).click();
  await page.locator("pre").waitFor();
  assert.match(await page.locator("pre").innerText(), /198\.51\.100\.0\/24/);
  await page.getByRole("button", { name: "变更对比", exact: true }).click();
  await page
    .getByText("尚无已批准版本，全部内容为新增", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "完整配置", exact: true }).click();
  await page.getByRole("button", { name: "校验配置", exact: true }).click();
  await page.getByRole("button", { name: "批准版本", exact: true }).waitFor();
  await page.getByRole("button", { name: "批准版本", exact: true }).click();
  await page.getByRole("button", { name: "确认批准", exact: true }).click();
  await page.getByText("已批准", { exact: true }).waitFor();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "下载配置", exact: true }).click();
  assert.match((await download).suggestedFilename(), /bird2-.*\.conf/);
  console.log("BIRD preview, native validation, approval, download passed");

  await create("routers", {
    name: "ros-01",
    platform: "RouterOS 7",
    asn: 64512,
    routerId: "192.0.2.2",
    vlanId: "Peering LAN",
    localIpv4: "192.0.2.2",
  });
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page
    .getByLabel("路由服务器", { exact: true })
    .selectOption({ label: "ros-01" });
  await page.getByRole("button", { name: "生成预览", exact: true }).click();
  await page.locator("pre").waitFor();
  await page
    .getByText("当前平台暂不支持自动语法校验、批准与生产配置下载。", {
      exact: true,
    })
    .waitFor();
  assert.equal(
    await page
      .getByRole("button", { name: "校验配置", exact: true })
      .isDisabled(),
    true,
  );
  assert.equal(
    await page
      .getByRole("button", { name: "下载配置", exact: true })
      .isDisabled(),
    true,
  );
  await create("routers", {
    name: "junos-01",
    platform: "Junos",
    asn: 64512,
    routerId: "192.0.2.3",
    vlanId: "Peering LAN",
    localIpv4: "192.0.2.3",
  });
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page
    .getByLabel("路由服务器", { exact: true })
    .selectOption({ label: "junos-01" });
  await page.getByRole("button", { name: "生成预览", exact: true }).click();
  await page.locator("pre").waitFor();
  await page.getByRole("button", { name: "检查配置结构", exact: true }).click();
  await page.getByRole("button", { name: "批准版本", exact: true }).waitFor();
  await create("routers", {
    name: "unsupported-01",
    platform: "BIRD 3（待适配）",
    asn: 64512,
    routerId: "192.0.2.4",
    vlanId: "Peering LAN",
    localIpv4: "192.0.2.4",
  });
  await go("deployments");
  await page.locator(".page-heading button").click();
  await page
    .getByLabel("路由服务器", { exact: true })
    .selectOption({ label: "unsupported-01" });
  assert.equal(
    await page
      .getByRole("button", { name: "生成预览", exact: true })
      .isDisabled(),
    true,
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "取消", exact: true })
    .click();
  console.log(
    "Multi-platform capabilities and blocked unsupported actions passed",
  );
  await create("users", {
    name: "Member User",
    email: "member@example.test",
    role: "成员",
    memberId: "Example Network Updated",
    password: "Member-test-password-123",
  });
  await go("settings");
  await page.getByLabel("交换中心简称").fill("MD-TEST");
  await page.getByLabel("国家 / 地区代码").fill("CN");
  await page.getByLabel("IX-F 交换中心 ID").fill("100");
  await page.getByRole("button", { name: "保存设置", exact: true }).click();
  await page.getByText("已保存", { exact: true }).waitFor();
  const ixf = page.waitForEvent("download");
  await page.getByRole("button", { name: "导出 IX-F", exact: true }).click();
  assert.equal((await ixf).suggestedFilename(), "ixf-members.json");
  const exported = page.waitForEvent("download");
  await page.getByRole("button", { name: "导出资源 JSON" }).click();
  assert.match((await exported).suggestedFilename(), /moedove-resources/);
  await page.locator("input[type=file]").setInputFiles({
    name: "import.json",
    mimeType: "application/json",
    buffer: Buffer.from(
      JSON.stringify({
        version: 1,
        resources: {
          vendors: [{ id: "browser-vendor", name: "Browser Vendor" }],
        },
      }),
    ),
  });
  await page.getByRole("button", { name: "确认导入", exact: true }).click();
  await page.getByText("成功导入 1 条记录。", { exact: true }).waitFor();
  await go("vendors");
  await page
    .getByRole("cell", { name: "Browser Vendor", exact: true })
    .waitFor();
  console.log("settings, export, atomic import passed");
  await go("dashboard");
  await page.screenshot({ path: "/tmp/moedove-dashboard.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "打开导航" }).click();
  await page.locator(".sidebar.open").waitFor();
  await page
    .locator(".sidebar.open")
    .getByRole("button", { name: "成员管理", exact: true })
    .click();
  await page.locator("main").waitFor();
  assert.equal(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
    true,
    "mobile page overflows",
  );
  await page.screenshot({ path: "/tmp/moedove-mobile.png", fullPage: true });
  console.log("mobile navigation/layout passed");
  await page.getByRole("button", { name: "退出登录" }).click();
  await page.getByLabel("邮箱地址").fill("member@example.test");
  await page
    .getByLabel("密码", { exact: true })
    .fill("Member-test-password-123");
  await page.getByRole("button", { name: "登录控制台" }).click();
  await go("dashboard");
  await page.getByRole("heading", { name: "成员工作空间" }).waitFor();
  await go("members");
  await page
    .getByRole("cell", { name: "Example Network Updated", exact: true })
    .waitFor();
  assert.equal(
    await page.getByRole("button", { name: "新增成员", exact: true }).count(),
    0,
  );
  await go("settings");
  await page.getByText("你没有此页面的访问权限。", { exact: true }).waitFor();
  console.log("member login, read-only portal and navigation access passed");
  assert.deepEqual(errors, []);
  console.log("All browser checks passed; no uncaught page errors.");
} finally {
  await browser.close();
}
