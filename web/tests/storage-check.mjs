// Response safety only: no directory probes, files or application requests.
import assert from "node:assert/strict";
import {
  checkedStorageReport,
  storageCheckStages,
  storageFailureMessage,
  storageOutcomeLabel,
  storageSafeCode,
} from "../src/lib/storage-check.ts";

const complete = {
  status: "ok", checkedAt: "2026-11-18T10:00:00Z",
  checks: storageCheckStages.map(([id]) => ({ id, status: "passed" })),
};
assert.equal(checkedStorageReport(complete).checks.length, 10);
assert.equal(storageOutcomeLabel("ok"), "本次检查通过");
assert.equal(storageOutcomeLabel("failed"), "本次检查未通过");
assert.equal(storageOutcomeLabel("canceled"), "检查已取消");

const canceled = { ...complete, status: "canceled", checks: [
  ...complete.checks.slice(0, 4), { id: "cleanup", status: "passed" },
] };
assert.deepEqual(checkedStorageReport(canceled).checks, canceled.checks);
assert.equal(canceled.checks.some(check => check.id === "sync-directory"), false, "unreported phases must not become successful observations");
const cleanupFailure = { ...canceled, status: "failed", checks: [
  ...complete.checks.slice(0, 4), { id: "cleanup", status: "failed", code: "scratch-cleanup-failed" },
] };
assert.equal(checkedStorageReport(cleanupFailure).status, "failed");
assert.match(storageFailureMessage("scratch-cleanup-failed"), /清理未完成/);
assert.equal(storageSafeCode("directory-changed"), "directory-changed");

const unsafe = "/private/directory/error?credential=do-not-echo";
assert.equal(storageSafeCode(unsafe), null);
assert.equal(storageSafeCode("__proto__"), null);
assert.ok(!storageFailureMessage(unsafe).includes(unsafe));
assert.equal(storageFailureMessage("unknown-new-code"), "此步骤未通过，请检查数据目录与服务端日志。");
assert.equal(checkedStorageReport({ ...cleanupFailure, checks: [
  { id: "binding", status: "failed", code: unsafe },
] }).status, "failed", "unknown codes remain failed without unsafe echo");

for (const bad of [
  { ...complete, checks: complete.checks.slice(0, 9) },
  { ...complete, checks: [...complete.checks].reverse() },
  { ...complete, checks: [...complete.checks, complete.checks[0]] },
  { ...complete, checks: [{ id: "binding", status: "failed" }] },
  { ...complete, checks: complete.checks.map((check, index) => index ? check : { ...check, code: "store-closed" }) },
  { ...cleanupFailure, status: "canceled" },
  { ...cleanupFailure, checks: [{ id: "arbitrary-path", status: "failed" }] },
  { ...cleanupFailure, checks: [{ id: "binding", status: "failed", code: { privatePath: unsafe } }] },
  { ...complete, checkedAt: "invalid" },
]) assert.throws(() => checkedStorageReport(bad));
console.log("Storage check full-success, canceled-prefix, cleanup-failure and safe error-code contracts passed");
