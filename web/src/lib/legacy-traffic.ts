export type LegacySource = {
  name: string;
  timezone: string;
  backend: string;
  table?: string;
  unit?: string;
  entityKind?: string;
  entityId?: string;
};
export type LegacyMetadata = {
  id: string;
  checksum?: string;
  inputChecksum?: string;
  format: string;
  source: LegacySource;
  rowCount: number;
  staffOnlyRows: number;
  originalDayFrom?: string;
  originalDayTo?: string;
  importedAt: string;
  size?: number;
  reviewed?: boolean;
};
export type LegacyCatalog = {
  sourceKind: "legacy-report";
  batches: LegacyMetadata[];
  bytes?: number;
  maxBytes?: number;
  maxBatches?: number;
  capabilities: { canPreview: boolean; canImport: boolean; canDelete: boolean };
  notices: string[];
};
export type LegacyInput = {
  format: string;
  source: LegacySource;
  content: string;
  columns?: unknown[];
  windows?: unknown[];
  owners?: LegacyOwnerEvidence[];
};
export type LegacyPreview = {
  batchId: string;
  checksum: string;
  source: LegacySource;
  format: string;
  rowCount: number;
  staffOnlyRows: number;
  rows: LegacyRow[];
  notices: string[];
};
export type LegacyMeasure = {
  average: string | null;
  maximum: string | null;
  minimum: string | null;
  last: string | null;
  total: string | null;
  maxAt: string | null;
  originalMaxAt?: string;
  unknown?: Record<string, string>;
};
export type LegacyOwnerEvidence = {
  entityKind: string;
  entityId: string;
  memberId: string;
  memberRevision: string;
  validFrom: string;
  validTo: string;
  referenceSha256: string;
  description: string;
};
export type LegacyRow = {
  id: string;
  recordId: string;
  sourceKind: string;
  entityKind: string;
  entityId: string;
  peerEntityId?: string;
  originalDay?: string;
  category: string;
  family?: number;
  totalUnit: string;
  rateUnit: string;
  window: {
    kind: string;
    period: string;
    nominalSeconds?: number;
    start: string | null;
    end: string | null;
    boundaryQuality: string;
    evidence?: string;
  };
  in: LegacyMeasure;
  out: LegacyMeasure;
  maxSemantics: string;
  staffOnly: boolean;
  memberId?: string;
  memberRevision?: string;
  ownerEvidence?: LegacyOwnerEvidence;
  notices: string[];
};
export type LegacyResult = {
  sourceKind: "legacy-report";
  batchId: string;
  rows: LegacyRow[];
  totalRows: number;
  offset: number;
  limit: number;
  truncated: boolean;
  noAggregation: true;
  notices: string[];
};
export const legacySourceLabels: Record<string, string> = {
  "legacy-mrtg-window": "旧 MRTG 滚动窗口",
  "legacy-rrd-window": "旧 RRD 滚动窗口",
  "legacy-p2p-daily": "旧 P2P 日报告（采样估算）",
  "legacy-rrd-rate": "旧 RRD 速率桶",
};
export const legacyPeriodLabels: Record<string, string> = {
  day: "滚动日",
  week: "滚动周",
  month: "滚动月",
  year: "滚动年",
  "calendar-day": "原日历日标签",
  step: "RRD 导出步长",
};
export const legacyEntityLabels: Record<string, string> = {
  customer: "原成员",
  "physical-interface": "原物理接口",
  dataset: "原数据集",
};
// Legacy values remain decimal strings. Never round via Number or combine
// overlapping report windows, independent maxima, batches, or live sources.
export const legacyDecimal = (value: string | null) => {
  if (value == null) return "未知";
  const [integer, fraction] = value.split(".");
  return (
    integer.replace(/\B(?=(\d{3})+(?!\d))/g, ",") +
    (fraction === undefined ? "" : `.${fraction}`)
  );
};
const notices: Record<string, string> = {
  "Rows are original independent reports/rate windows. No totals, rates or maxima are added across rows, periods or imports.":
    "逐条保留原报表和速率窗口；不跨记录、周期或批次相加总量、速率或最大值。",
  "Original day is the report-save date, not the end of this rolling window. Overlapping window totals must not be added.":
    "原报告日期是报表保存日期，不是滚动窗口的结束时间；重叠窗口的总量不能相加。",
  "Legacy graph processing could replace unavailable samples with zero; a stored zero cannot now establish complete observed coverage.":
    "旧图表处理可能曾将不可用样本变为零；原报表中的零不能证明完整有效覆盖。",
  "P2P totals are legacy sampled estimates; maxima were summed across independent interface graphs and are not simultaneous peaks.":
    "P2P 总量是旧采样估算；最大值来自独立接口图的最大值相加，并非同一时刻的整体峰值。",
  "The date identifies a requested calendar day; actual RRD sample coverage/NaN conversion cannot be reconstructed from this table.":
    "日期仅标识原请求的日历日，无法从此表还原真实 RRD 样本覆盖及 NaN 转换情况。",
};
export const legacyNotice = (notice: string) => notices[notice] ?? notice;
