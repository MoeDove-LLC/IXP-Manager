// Read-only rendering contracts. No network, target checks or state changes.
import assert from "node:assert/strict";
import {
  checkedMonitoringHealth,
  monitoringApprovalLabel,
  monitoringHealthPath,
  monitoringProfileLink,
  monitoringPullLabel,
} from "../src/lib/monitoring-health.ts";

assert.equal(monitoringHealthPath(), "/provisioning/monitoring-health?limit=20");
assert.equal(new URLSearchParams(monitoringHealthPath().split("?")[1]).has("after"), false);
const cursor = "next+opaque=/value?";
assert.equal(new URLSearchParams(monitoringHealthPath(cursor).split("?")[1]).get("after"), cursor);
assert.equal(monitoringProfileLink("legacy-monitoring_9"), "#provisioning?profileId=legacy-monitoring_9");
assert.equal(monitoringProfileLink("a/b?c"), "#provisioning?profileId=a%2Fb%3Fc");

const item = {
  profileId: "profile-a", profileName: "Nagios fixture", kind: "nagios",
  approvalStatus: "current", revisionId: "revision-a",
  generatedAt: "2026-11-18T02:00:00Z", lastPulledAt: null,
  lastDownloadedAt: "2026-11-18T03:00:00Z", pullStatus: "not-observed", targetCount: 0,
};
const response = { items: [item], nextAfter: "opaque-next", checkedAt: "2026-11-18T04:00:00Z", pullStaleAfterSeconds: 86400 };
const checked = checkedMonitoringHealth(response, "");
assert.equal(checked.items[0].pullStatus, "not-observed", "manual download must not become profile-bound pull evidence");
assert.equal(checked.items[0].lastPulledAt, null, "missing pull timestamps must remain missing");
assert.equal(checked.items[0].targetCount, 0, "an actual zero target count must remain distinct from unavailable");
assert.equal(monitoringApprovalLabel("current"), "已批准且匹配当前配置");
assert.equal(monitoringApprovalLabel("stale"), "批准版本待更新");
assert.equal(monitoringPullLabel("recent"), "近期有绑定拉取");
assert.equal(monitoringPullLabel("not-observed"), "未观测到绑定拉取");
assert.equal(checkedMonitoringHealth({ ...response, items: [], nextAfter: "" }, "").items.length, 0);
for (const mutated of [
  { ...response, nextAfter: "cursor" },
  { ...response, items: [item, item] },
  { ...response, items: [{ ...item, profileId: "../other" }] },
  { ...response, items: [{ ...item, kind: "dns-arpa" }] },
  { ...response, items: [{ ...item, approvalStatus: "deployed" }] },
  { ...response, items: [{ ...item, lastPulledAt: "not-a-date" }] },
  { ...response, items: [{ ...item, targetCount: -1 }] },
  { ...response, checkedAt: "not-a-date" },
  { ...response, pullStaleAfterSeconds: 0 },
]) assert.throws(() => checkedMonitoringHealth(mutated, "cursor"));
assert.throws(() => checkedMonitoringHealth({ ...response, items: Array.from({ length: 21 }, (_, index) => ({ ...item, profileId: `profile-${index}` })) }, ""));
console.log("Monitoring approval/pull separation, null/zero preservation, exact links and bounded cursor contracts passed");
