// Run using a synthetic data directory and loopback HTTPS. No external services.
import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { chromium } from "playwright";

const dir = await mkdtemp(path.join(tmpdir(), "remembered-devices-"));
const host = "127.0.0.1";
const email = "remembered@example.test";
const password = "Browser-remembered-device-fixture";
const setupToken = "remembered-device-fixture-setup";
const listen = server => new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, host, () => resolve(server.address().port));
});
const close = server => new Promise(resolve => server.close(() => resolve()));
let child, proxy, browser;
try {
  const key = path.join(dir, "key.pem"), cert = path.join(dir, "cert.pem"), config = path.join(dir, "openssl.cnf");
  await writeFile(config, "[req]\nprompt=no\ndistinguished_name=dn\nx509_extensions=fixture\n[dn]\nCN=127.0.0.1\n[fixture]\nsubjectAltName=IP:127.0.0.1\n", { mode: 0o600 });
  const generated = spawnSync("openssl", ["req", "-config", config, "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", cert, "-days", "1"], { encoding: "utf8" });
  assert.equal(generated.status, 0, `generate loopback HTTPS fixture certificate: ${generated.error?.message ?? generated.stderr}`);
  const reservation = net.createServer();
  const upstreamPort = await listen(reservation);
  await close(reservation);
  child = spawn(process.env.TEST_SERVER_BIN, ["-listen", `${host}:${upstreamPort}`, "-data", path.join(dir, "data"), "-web", path.resolve("dist"), "-secure-cookie"], { env: { ...process.env, IXP_SETUP_TOKEN: setupToken }, stdio: "ignore" });
  let ready = false;
  for (let i = 0; i < 200; i++) {
    if (child.exitCode !== null) throw new Error("manager stopped during startup");
    if (await fetch(`http://${host}:${upstreamPort}/api/setup`).then(r => r.ok).catch(() => false)) {
      ready = true;
      break;
    }
    await new Promise(resolve => setTimeout(resolve, 50));
  }
  assert.ok(ready, "manager startup timeout");
  proxy = https.createServer({ key: await readFile(key), cert: await readFile(cert) }, (request, response) => {
    const upstream = http.request({ hostname: host, port: upstreamPort, method: request.method, path: request.url, headers: request.headers, agent: false }, reply => {
      response.writeHead(reply.statusCode, reply.headers);
      reply.pipe(response);
    });
    upstream.on("error", () => {
      if (!response.headersSent) response.writeHead(502);
      response.end();
    });
    request.pipe(upstream);
  });
  const base = `https://${host}:${await listen(proxy)}`;
  browser = await chromium.launch({ headless: true, executablePath: process.env.CHROMIUM_PATH, args: ["--no-sandbox", "--disable-dev-shm-usage"] });
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 390, height: 844 } });
  const setup = await context.request.post(`${base}/api/setup`, { data: { setupToken, name: "Browser fixture", email, password, ixpName: "Browser fixture IXP", asn: 64500 } });
  assert.equal(setup.status(), 200);
  await context.clearCookies();
  const errors = [];
  const newPage = async ctx => {
    const page = await ctx.newPage();
    page.setDefaultTimeout(10000);
    page.on("pageerror", error => errors.push(error.message));
    return page;
  };
  const login = async (page, remember) => {
    await page.goto(`${base}/#dashboard`);
    const checkbox = page.getByRole("checkbox", { name: /记住这台设备/ });
    await checkbox.waitFor();
    assert.equal(await checkbox.isChecked(), false, "device opt-in must default off");
    if (remember) await checkbox.check();
    await page.getByLabel("邮箱地址").fill(email);
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "登录控制台" }).click();
    await page.locator("main h1").waitFor();
  };
  const persistent = async ctx => (await ctx.cookies()).find(cookie => cookie.name === "__Host-moedove_remember");
  const first = await newPage(context);
  await login(first, true);
  const grant = await persistent(context);
  assert.ok(grant, "expected a persistent HttpOnly/Secure cookie");
  assert.ok(grant.httpOnly && grant.secure);
  assert.equal(grant.sameSite, "Strict");
  assert.ok(grant.expires > Date.now() / 1000 + 29 * 86400);
  await context.clearCookies({ name: "moedove_session" });

  let restorations = 0;
  await context.route("**/api/session/restore", route => {
    restorations++;
    return route.continue();
  });
  const second = await newPage(context);
  await Promise.all([first.goto(`${base}/?tab=first#security`), second.goto(`${base}/#security`)]);
  const deviceCard = page => page.getByRole("region", { name: "已记住的设备" });
  await Promise.all([deviceCard(first).getByText("当前设备", { exact: true }).waitFor(), deviceCard(second).getByText("当前设备", { exact: true }).waitFor()]);
  assert.equal(restorations, 1, "two tabs should share one completed rotation");
  const rotated = await persistent(context);
  assert.ok(rotated && rotated.value !== grant.value, "restore must replace the validator");
  assert.ok(Math.abs(rotated.expires - grant.expires) <= 2, "rotation keeps the original deadline");
  if (process.env.REMEMBERED_DEVICES_SCREENSHOT) await first.screenshot({ path: process.env.REMEMBERED_DEVICES_SCREENSHOT, fullPage: true });
  assert.ok(await first.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "account security must fit the mobile viewport");
  console.log("PASS HTTPS opt-in, cookie attributes, one rotation across two tabs and 390px layout");

  await deviceCard(first).getByRole("button", { name: "撤销设备", exact: true }).click();
  await first.getByRole("dialog").getByRole("button", { name: "确认撤销", exact: true }).click();
  const loginButton = page => page.getByRole("button", { name: "登录控制台" });
  await Promise.all([loginButton(first).waitFor(), loginButton(second).waitFor()]);
  assert.equal(restorations, 1, "revocation must not attempt automatic restoration");
  const revoked = await context.request.post(`${base}/api/session/restore`, { data: {} });
  assert.equal(revoked.status(), 401, "revoked credentials must not restore a session");

  await login(first, true);
  const passwordContext = await browser.newContext({ ignoreHTTPSErrors: true });
  const passwordPage = await newPage(passwordContext);
  await login(passwordPage, false);
  assert.equal(await persistent(passwordContext), undefined, "opt-out must not mint persistent permission");
  await passwordPage.goto(`${base}/#security`);
  await deviceCard(passwordPage).getByRole("button", { name: "撤销全部", exact: true }).click();
  await passwordPage.getByRole("dialog").getByRole("button", { name: "确认撤销", exact: true }).click();
  await deviceCard(passwordPage).getByText("没有已记住的设备").waitFor();
  assert.equal((await passwordContext.request.get(`${base}/api/session`)).status(), 200);
  assert.equal((await context.request.get(`${base}/api/session`)).status(), 401);
  assert.deepEqual(errors, []);
  console.log("PASS current-device revocation synchronizes tabs and revoke-all preserves a password-only session");
} finally {
  if (browser) await browser.close();
  if (proxy) await close(proxy);
  if (child && child.exitCode === null && child.signalCode === null) {
    const exited = new Promise(resolve => child.once("exit", resolve));
    child.kill("SIGTERM");
    const timer = setTimeout(() => child.kill("SIGKILL"), 5000);
    await exited;
    clearTimeout(timer);
  }
  await rm(dir, { recursive: true, force: true });
}
