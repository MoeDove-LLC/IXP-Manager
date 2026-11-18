// Pure presentation policy tests: no services, external checks or state writes.
import assert from "node:assert/strict";
import {
  freshnessLabel,
  jobCanSubmit,
  jobHealthLabel,
  notificationSnapshotTTL,
  schedulerPhaseLabel,
  schedulerStatusLabel,
  snapshotFreshness,
} from "../src/lib/scheduler.ts";

assert.equal(schedulerStatusLabel("stalled"), "调度停滞");
assert.equal(schedulerStatusLabel("running"), "运行中");
assert.notEqual(schedulerStatusLabel("degraded"), schedulerStatusLabel("running"));
assert.equal(schedulerPhaseLabel("looking-glass"), "路由查询维护");
assert.equal(schedulerPhaseLabel("unexpected-private-transport-error"), "未知阶段");
assert.equal(snapshotFreshness(undefined, 1000, false, true), "unavailable");
assert.equal(snapshotFreshness(1000, 1001, false, false), "current");
assert.equal(snapshotFreshness(1000, 1001, true, false), "failed");
assert.equal(snapshotFreshness(1000, 1001, false, true), "refreshing");
assert.equal(snapshotFreshness(1000, 1000 + notificationSnapshotTTL, false, false), "stale");
assert.equal(freshnessLabel("failed"), "读取失败，以下仅为上次快照。");

// Server dates are display metadata: browser/server clock skew is irrelevant.
const realNow = Date.now;
try {
  Date.now = () => Date.parse("2099-01-01T00:00:00Z");
  assert.equal(snapshotFreshness(1000, 1500, false, false), "current");
  Date.now = () => Date.parse("1999-01-01T00:00:00Z");
  assert.equal(snapshotFreshness(1000, 1500, false, false), "current");
} finally {
  Date.now = realNow;
}

const job = { kind: "cleanup", label: "清理", status: "running", health: "running", nextRunAt: "2030-01-01T00:00:00Z" };
assert.equal(jobCanSubmit(job), false);
assert.equal(jobCanSubmit({ ...job, health: "lease-expired" }), true);
assert.equal(jobHealthLabel({ ...job, health: "lease-expired" }), "执行租约已过期");
assert.equal(jobCanSubmit({ ...job, status: "unexpected", health: "unknown" }), true);
assert.equal(jobHealthLabel({ ...job, status: "unexpected", health: "unknown" }), "任务状态未知");
assert.equal(jobCanSubmit({ ...job, kind: "arbitrary-command", health: "lease-expired" }), false);
assert.equal(jobHealthLabel({ ...job, status: "idle", health: "due" }), "已到执行时间");
assert.equal(jobHealthLabel({ ...job, status: "idle", health: "overdue" }), "已超过计划时间");
assert.equal(jobHealthLabel({ ...job, status: "idle", health: undefined }), "等待执行");
console.log("Scheduler labels / monotonic snapshot expiry / failed poll / recoverable lease and fixed-job policy passed");
