// Counter totals never pass through Number: large decimal values stay exact.
export const exactTotal = (value: string | null | undefined, unit: string) =>
  value == null
    ? "无有效区间"
    : /^\d+$/.test(value)
      ? `${value.replace(/\B(?=(\d{3})+(?!\d))/g, ",")} ${{ bytes: "B", packets: "包", events: "次" }[unit] ?? unit}`
      : "未提供";
export const utcTime = (value: string | null | undefined) => {
  if (!value) return "未提供";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "未提供"
    : `${date.toISOString().slice(0, 19).replace("T", " ")} UTC`;
};
export const utcInput = (value: Date) => value.toISOString().slice(0, 19);
export const fromUTCInput = (value: string) => {
  const date = new Date(`${value}Z`);
  if (!value || Number.isNaN(date.getTime()))
    throw new Error("请输入有效的 UTC 日期与时间。");
  return date.toISOString();
};
export const trafficResolutions: Record<string, string> = {
  "5m": "5 分钟",
  "1h": "小时",
  "1d": "日",
};

export const trafficMetrics: Record<string, string> = {
  octets: "流量字节",
  unicast: "单播包",
  multicast: "组播包",
  broadcast: "广播包",
  errors: "错误计数",
  discards: "丢弃计数",
};
export type TrafficDirection = {
  total: string | null;
  lowerBoundTotal?: string | null;
  ambiguousIntervals?: number;
  averageRate: number | null;
  maxRate: number | null;
  maxAt?: string | null;
  coveredMillis: number;
  rateCoveredMillis: number;
  expectedMillis: number;
  coverage: number;
  validIntervals: number;
  resetIntervals: number;
  wrapIntervals: number;
  gapIntervals: number;
};
export type TrafficBucket = {
  start: string;
  end: string;
  in: TrafficDirection;
  out: TrafficDirection;
  contributingPorts: number;
  expectedPorts: number;
  uniformAllocation: boolean;
  peakMethod: string;
  peakResolutionSeconds: number;
  partial: boolean;
};
export type TrafficResult = {
  sourceKind: string;
  scope: string;
  id: string;
  metric: string;
  resolution: string;
  totalUnit: string;
  rateUnit: string;
  from: string;
  to: string;
  buckets: TrafficBucket[];
  summary: TrafficBucket;
  retainedFrom: string | null;
  latestAt: string | null;
  archiveStartedAt: string | null;
  retentionLimited: boolean;
  notices: string[];
  targetLabel?: string;
  scopeNotice?: string;
  side?: string;
  coreSide?: string;
  requestedFrom?: string;
  requestedTo?: string;
  alignmentNotice?: string;
};

export type TrafficTarget = {
  key?: string;
  scope: string;
  id: string;
  label: string;
  notice?: string;
  requiresSide?: boolean;
  memberRevision?: string;
  sourceControl?: TrafficSourceControl;
};
export type TrafficSourceControl = {
  configured: string;
  status: string;
  revision: string;
  choices: { value: string; label: string }[];
  editable: boolean;
};
export type TrafficCatalog = {
  sourceKind: "counter";
  timezone: "UTC";
  targets: TrafficTarget[];
  metrics: { id: string; label: string; totalUnit: string; rateUnit: string }[];
  resolutions: { id: string; label: string; retentionDays: number }[];
  defaults: { metric: string; resolution: string; hours: number };
  notice?: string;
};
export const scopeLabels: Record<string, string> = {
  port: "物理端口",
  member: "成员历史",
  switch: "交换机",
  rack: "机柜",
  location: "站点",
  infrastructure: "基础网络",
  "core-bundle": "核心互联",
  connection: "接入连接",
};
