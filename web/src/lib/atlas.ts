export type AtlasLimits = {
  maxProbes: number;
  minIntervalSeconds: number;
  maxIntervalSeconds?: number;
  maxMatrixMembers?: number;
  maxDurationSeconds: number;
  maxSamples: number;
  maxEstimatedCredits: number;
};
export type AtlasProbe = {
  id: number;
  asnV4?: number | null;
  asnV6?: number | null;
  addressV4?: string;
  addressV6?: string;
  countryCode?: string;
  isAnchor: boolean;
  isPublic: boolean;
  status: string | number;
  lastConnected?: string | null;
};
export type AtlasProbeSnapshot = {
  id: string;
  memberId: string;
  af: number;
  asn: number;
  fetchedAt: string;
  count: number;
  checksum: string;
  stale: boolean;
  truncated?: boolean;
};
export type AtlasProbeCache = {
  snapshot: AtlasProbeSnapshot | null;
  items: AtlasProbe[];
  stale: boolean;
};
export type AtlasMeasurement = {
  id: string;
  sourceMemberId: string;
  targetMemberId: string;
  targetAddress: string;
  probeIds: number[];
  status: string;
  providerId?: number;
  providerStatus?: string;
  ownershipConfirmed: boolean;
  estimatedCredits: number;
  estimatedSamples: number;
  lastSyncedAt?: string | null;
  lastError?: string;
  hasResults: boolean;
  resultCount: number;
  scheduledStart?: string | null;
  scheduledStop?: string | null;
};
export type AtlasRun = {
  id: string;
  name: string;
  mode?: "single" | "matrix";
  sourceMemberId?: string;
  targetMemberId?: string;
  type: "ping" | "traceroute";
  af: number;
  targetAddress?: string;
  probeIds?: number[];
  isOneoff: boolean;
  intervalSeconds?: number;
  durationSeconds?: number;
  startAt?: string | null;
  isPublic: boolean;
  status: string;
  checksum: string;
  estimatedCredits: number;
  estimatedSamples: number;
  createdAt: string;
  approvedAt?: string | null;
  providerId?: number;
  providerStatus?: string;
  lastSyncedAt?: string | null;
  lastError?: string;
  hasResults: boolean;
  pruned: boolean;
  measurementCount?: number;
  remainingCount?: number;
  uncertainCount?: number;
  canResume?: boolean;
  canReapprove?: boolean;
  approvalExpired?: boolean;
  resumeRequired?: boolean;
  remainingEstimatedCredits?: number;
  remainingEstimatedSamples?: number;
  stopRequested?: boolean;
  items?: AtlasMeasurement[];
  spec?: Record<string, unknown>;
  notices?: unknown[];
};
export type AtlasSummary = {
  enabled: boolean;
  provider: string;
  limits: AtlasLimits;
  probeSnapshots: AtlasProbeSnapshot[];
  runs: AtlasRun[];
};
export type AtlasReply = {
  address?: string;
  rtt?: number | null;
  timeout?: boolean;
  error?: string;
};
export type AtlasResult = {
  probeId: number;
  measurementId: number;
  type: "ping" | "traceroute";
  af: number;
  timestamp: string;
  targetAddress: string;
  sourceAddress?: string;
  sent?: number;
  received?: number;
  minRtt?: number | null;
  avgRtt?: number | null;
  maxRtt?: number | null;
  hops?: { hop: number; replies: AtlasReply[]; error?: string }[];
  error?: string;
};
export type AtlasDirection = {
  id: string;
  providerId: number;
  sourceMemberId: string;
  targetMemberId: string;
  status: string;
  resultCount: number;
  hasResults: boolean;
  lastObservedAt?: string | null;
  currentVLANAddressesSeen: string[];
};
export type AtlasResults = {
  items: AtlasResult[];
  directions: AtlasDirection[];
  snapshots: {
    id: string;
    measurementId: string;
    checksum: string;
    fetchedAt: string;
    count: number;
  }[];
  snapshot?: {
    id: string;
    measurementId: string;
    checksum: string;
    fetchedAt: string;
    count: number;
  } | null;
  hasResults: boolean;
  truncated: boolean;
  status: string;
};
export const atlasStatuses: Record<string, string> = {
  draft: "待审核提案",
  approved: "已批准待提交",
  creating: "正在提交",
  uncertain: "提交结果不确定",
  scheduled: "已计划",
  running: "外部运行中",
  stopping: "正在停止",
  stopped: "已停止",
  completed: "已完成",
  failed: "失败",
  archived: "已归档",
  partial: "部分执行",
  cancelled: "已取消",
};
export const probeAddress = (probe: AtlasProbe, af: number) =>
  af === 6 ? probe.addressV6 : probe.addressV4;
export const rtt = (value: unknown) =>
  typeof value === "number" && Number.isFinite(value) && value >= 0
    ? `${value.toLocaleString("zh-CN", { maximumFractionDigits: 3 })} ms`
    : "未提供";
