export type SchedulerObservation = {
  status: "not-started" | "starting" | "running" | "degraded" | "stalled" | "stopping" | "stopped";
  startedAt: string | null;
  lastCompletedAt: string | null;
  cycleStartedAt: string | null;
  stoppedAt: string | null;
  currentPhase: "jobs" | "mail" | "atlas" | "looking-glass" | "";
  failedPhases: string[];
  stallAfterSeconds: number;
};
export type MaintenanceJob = {
  kind: string;
  label: string;
  status: string;
  nextRunAt: string;
  lastRunAt?: string | null;
  lastError?: string;
  health?: "scheduled" | "due" | "overdue" | "running" | "lease-expired" | "unknown";
  leaseUntil?: string | null;
};
export type JobsObservation = {
  items: MaintenanceJob[];
  checkedAt?: string;
  scheduler?: SchedulerObservation;
};
export type SnapshotFreshness = "unavailable" | "refreshing" | "failed" | "stale" | "current";
export const notificationSnapshotTTL = 90000;

// Browser/server clock offsets must not change freshness. This receives only
// local monotonic timestamps; checkedAt remains server display metadata.
export function snapshotFreshness(
  receivedAt: number | undefined,
  now: number,
  failed: boolean,
  refreshing: boolean,
): SnapshotFreshness {
  if (receivedAt === undefined || !Number.isFinite(receivedAt) || !Number.isFinite(now)) return "unavailable";
  if (failed) return "failed";
  if (Math.max(0, now - receivedAt) >= notificationSnapshotTTL) return "stale";
  return refreshing ? "refreshing" : "current";
}

const statuses: Record<string, string> = {
  "not-started": "尚未启动",
  starting: "正在启动",
  running: "运行中",
  degraded: "部分阶段失败",
  stalled: "调度停滞",
  stopping: "正在停止",
  stopped: "已停止",
};
const phases: Record<string, string> = {
  jobs: "固定维护任务",
  mail: "邮件投递",
  atlas: "Atlas 状态同步",
  "looking-glass": "路由查询维护",
};
const health: Record<string, string> = {
  scheduled: "等待计划时间",
  due: "已到执行时间",
  overdue: "已超过计划时间",
  running: "正在执行",
  "lease-expired": "执行租约已过期",
  unknown: "任务状态未知",
};
export const schedulerStatusLabel = (status: string) => statuses[status] ?? "调度状态未知";
export const schedulerPhaseLabel = (phase: string) => phase === "" ? "等待下一轮" : phases[phase] ?? "未知阶段";
export const jobHealthLabel = (job: MaintenanceJob) =>
  job.health ? health[job.health] ?? "任务状态未知" : job.status === "running" ? "正在执行" : job.status === "idle" ? "等待执行" : "任务状态未知";
export const jobCanSubmit = (job: MaintenanceJob) =>
  ["token-expiry", "cleanup"].includes(job.kind) && (job.status !== "running" || job.health === "lease-expired");
export const freshnessLabel = (freshness: SnapshotFreshness) => ({
  unavailable: "暂无可用观测。",
  refreshing: "正在核对，以下为上次快照。",
  failed: "读取失败，以下仅为上次快照。",
  stale: "观测已过期，请刷新后判断当前状态。",
  current: "当前观测",
})[freshness];
