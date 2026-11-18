export const storageCheckStages = [
  ["binding", "确认数据目录绑定"],
  ["directory", "检查目录"],
  ["create", "创建临时文件"],
  ["write", "写入测试内容"],
  ["sync-file", "同步文件数据"],
  ["rename", "重命名临时文件"],
  ["readback", "回读并核对内容"],
  ["cleanup", "清理临时文件"],
  ["sync-directory", "同步目录"],
  ["verify-binding", "复核数据目录绑定"],
] as const;

export type StorageCheckStage = typeof storageCheckStages[number][0];
export type StorageCheckReport = {
  status: "ok" | "failed" | "canceled";
  checkedAt: string;
  checks: { id: StorageCheckStage; status: "passed" | "failed"; code?: string }[];
};

const failureCodes: Record<string, string> = {
  "store-closed": "服务的数据目录已关闭，请重新连接服务后检查。",
  "binding-unavailable": "无法确认数据目录绑定，请检查服务状态。",
  "directory-unavailable": "无法打开数据目录，请检查路径和访问权限。",
  "directory-unsafe": "数据目录类型或安全属性不符合要求，请检查目录与挂载。",
  "directory-changed": "数据目录已变化，请核对挂载后重新运行。",
  "lock-unavailable": "无法确认目录锁，请检查服务端日志。",
  "lock-unsafe": "目录锁的安全属性不符合要求，请检查服务实例与目录。",
  "lock-changed": "目录锁已变化，请检查服务实例与目录。",
  "scratch-directory-failed": "无法创建检查用临时目录，请检查目录写权限。",
  "scratch-create-failed": "无法创建临时文件，请检查目录写权限。",
  "scratch-write-failed": "测试内容写入失败，请检查目录状态与服务端日志。",
  "scratch-sync-failed": "文件同步未完成，请检查存储状态与服务端日志。",
  "scratch-rename-failed": "临时文件重命名失败，请检查目录权限与服务端日志。",
  "scratch-readback-failed": "无法回读临时文件，请检查目录状态与服务端日志。",
  "scratch-readback-mismatch": "临时文件回读内容不一致，请检查服务端日志。",
  "scratch-cleanup-failed": "临时文件清理未完成，请查看服务端日志确认残留。",
  "directory-sync-failed": "目录同步未完成，请检查服务端日志。",
};
export const storageFailureMessage = (code?: string) =>
  code && Object.hasOwn(failureCodes, code)
    ? failureCodes[code]
    : "此步骤未通过，请检查数据目录与服务端日志。";
export const storageSafeCode = (code?: string) =>
  code && Object.hasOwn(failureCodes, code) ? code : null;
export const storageOutcomeLabel = (status: StorageCheckReport["status"]) => ({
  ok: "本次检查通过",
  failed: "本次检查未通过",
  canceled: "检查已取消",
})[status];

export function checkedStorageReport(value: StorageCheckReport): StorageCheckReport {
  const ids = new Set<string>(storageCheckStages.map(([id]) => id));
  if (!value || !["ok", "failed", "canceled"].includes(value.status) ||
    typeof value.checkedAt !== "string" || !Number.isFinite(Date.parse(value.checkedAt)) ||
    !Array.isArray(value.checks) || value.checks.length > ids.size)
    throw new Error("检查结果格式无法核对。");
  const seen = new Set<string>();
  for (const check of value.checks) {
    if (!check || !ids.has(check.id) || seen.has(check.id) ||
      !["passed", "failed"].includes(check.status) ||
      (check.code !== undefined && typeof check.code !== "string") ||
      (check.status === "passed" && !!check.code))
      throw new Error("检查步骤格式无法核对。");
    seen.add(check.id);
  }
  if (value.status === "ok" && (value.checks.length !== storageCheckStages.length ||
    value.checks.some((check, index) => check.id !== storageCheckStages[index][0] || check.status !== "passed")))
    throw new Error("检查成功结果缺少完整步骤。");
  if (value.status === "canceled" && value.checks.some(check => check.status === "failed"))
    throw new Error("取消结果包含失败步骤，请重新核对。");
  return value;
}
