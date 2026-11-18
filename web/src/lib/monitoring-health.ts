export type MonitoringConfigurationHealth = {
  profileId: string;
  profileName: string;
  kind: "nagios" | "smokeping";
  approvalStatus: "missing" | "stale" | "unavailable" | "current";
  revisionId?: string;
  generatedAt: string | null;
  lastPulledAt: string | null;
  lastDownloadedAt: string | null;
  pullStatus: "not-observed" | "recent" | "stale";
  targetCount?: number;
};

export type MonitoringHealthPage = {
  items: MonitoringConfigurationHealth[];
  nextAfter: string;
  checkedAt: string;
  pullStaleAfterSeconds: number;
};

export const monitoringHealthPageSize = 20;

export function monitoringHealthPath(after = "") {
  const query = new URLSearchParams({ limit: String(monitoringHealthPageSize) });
  if (after) query.set("after", after);
  return `/provisioning/monitoring-health?${query.toString()}`;
}

export const monitoringProfileLink = (id: string) =>
  `#provisioning?profileId=${encodeURIComponent(id)}`;

const approvalLabels = {
  missing: "无批准版本",
  stale: "批准版本待更新",
  unavailable: "批准文件不可用",
  current: "已批准且匹配当前配置",
};
const pullLabels = {
  "not-observed": "未观测到绑定拉取",
  recent: "近期有绑定拉取",
  stale: "绑定拉取记录已久",
};
export const monitoringApprovalLabel = (status: MonitoringConfigurationHealth["approvalStatus"]) =>
  approvalLabels[status];
export const monitoringPullLabel = (status: MonitoringConfigurationHealth["pullStatus"]) =>
  pullLabels[status];

const identifier = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(value);
const timestamp = (value: unknown): value is string =>
  typeof value === "string" && Number.isFinite(Date.parse(value));
const optionalTimestamp = (value: unknown) => value === null || timestamp(value);

export function checkedMonitoringHealth(
  value: MonitoringHealthPage,
  after: string,
): MonitoringHealthPage {
  if (
    !value || !Array.isArray(value.items) ||
    value.items.length > monitoringHealthPageSize ||
    typeof value.nextAfter !== "string" ||
    (value.nextAfter !== "" && value.nextAfter === after) ||
    !timestamp(value.checkedAt) ||
    !Number.isSafeInteger(value.pullStaleAfterSeconds) || value.pullStaleAfterSeconds <= 0
  ) throw new Error("监控配置检查响应不完整，请重新读取。");
  const seen = new Set<string>();
  for (const item of value.items) {
    if (
      !item || !identifier(item.profileId) || seen.has(item.profileId) ||
      typeof item.profileName !== "string" ||
      !["nagios", "smokeping"].includes(item.kind) ||
      !Object.hasOwn(approvalLabels, item.approvalStatus) ||
      !Object.hasOwn(pullLabels, item.pullStatus) ||
      (item.revisionId !== undefined && !identifier(item.revisionId)) ||
      !optionalTimestamp(item.generatedAt) ||
      !optionalTimestamp(item.lastPulledAt) ||
      !optionalTimestamp(item.lastDownloadedAt) ||
      (item.targetCount !== undefined && (!Number.isSafeInteger(item.targetCount) || item.targetCount < 0))
    ) throw new Error("监控配置条目信息无效，请重新读取。");
    seen.add(item.profileId);
  }
  return value;
}
