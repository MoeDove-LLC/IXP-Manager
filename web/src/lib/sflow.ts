export type SFlowSelector = {
  id: string;
  name: string;
  label?: string;
  memberId?: string;
  vlanId?: string;
};
export type SFlowReceiverSpec = {
  id?: string;
  memberId: string;
  connectionIds: string[];
  vlanIds: string[];
  address: string;
  port: number;
  enabled: boolean;
  revision?: string;
};
export type SFlowReceiver = {
  id: string;
  name: string;
  spec: SFlowReceiverSpec;
  memberLabel: string;
  revision: string;
  status: "draft" | "approved" | "disabled" | "stale";
  createdAt: string;
  updatedAt: string;
  approvedAt?: string;
  approvedBy?: string;
  canEdit: boolean;
  canApprove: boolean;
  canDelete: boolean;
  liveForwarding: boolean;
  deliveryNotice?: string;
  staleReason?: string;
  authorizationCount?: number | string;
  lastAuthorizedAt?: string;
  lastReceiptAt?: string;
  deliveryState?: string;
  receiptDatagrams?: number;
  receiptBytes?: number;
  receiptRevision?: string;
  receiptForCurrentRevision?: boolean;
  evidencePersistence?: "current-process";
  notice?: string;
};
export type SFlowReceiverBinding = {
  exporterId?: string;
  switchId?: string;
  vlanId?: string;
  vlanTag?: number;
  ifIndex?: number;
  mac?: string;
  direction?: string;
  connectionId?: string;
  memberId?: string;
  mappingRevision?: string;
};
export type SFlowReceiverReview = {
  receiver: SFlowReceiver;
  reviewChecksum: string;
  bindings: SFlowReceiverBinding[];
  disclosure: string[];
  notices: string[];
};
export type SFlowExporter = {
  id: string;
  name: string;
  switchId: string;
  sourceAddress: string;
  agentAddress: string;
  subAgentId: number;
  vlanIds: string[];
  enabled: boolean;
  receiveStatus?: "unknown" | "fresh" | "stale";
  lastReceivedAt?: string;
  sequenceState?: string;
  pendingRestart?: boolean;
};
export type SFlowCatalog = {
  sourceKind: "sampled";
  observedAt?: string;
  staleAfterSeconds?: number;
  exporters?: SFlowExporter[];
  receivers?: SFlowReceiver[];
  selectors: {
    switches: SFlowSelector[];
    vlans: SFlowSelector[];
    members: SFlowSelector[];
    connections: SFlowSelector[];
  };
  capabilities: {
    staff: boolean;
    canManageExporters: boolean;
    canManageReceivers: boolean;
    canApproveReceivers: boolean;
    liveForwarding: boolean;
  };
  usage?: {
    rows: number;
    bytes: number;
    maxRows: number;
    maxBytes: number;
    fiveMinuteRetentionSeconds: number;
    dailyRetentionSeconds: number;
  };
  notices: string[];
};
export type SFlowBucket = {
  start: string;
  resolution: string;
  exporterId?: string;
  switchId?: string;
  infrastructureId?: string;
  vlanId?: string;
  mappingRevision?: string;
  family: number;
  sourceMemberId?: string;
  destinationMemberId?: string;
  sourceConnectionId?: string;
  destinationConnectionId?: string;
  sourceMemberLabel?: string;
  destinationMemberLabel?: string;
  sourceConnectionLabel?: string;
  destinationConnectionLabel?: string;
  estimatedBytes: string;
  estimatedPackets: string;
  sampleCount: string;
  tcp179Samples: string;
  unattributedReason?: string;
  firstReceivedAt: string;
  lastReceivedAt: string;
};
export type SFlowQuality = {
  start: string;
  exporterId?: string;
  switchId?: string;
  datagrams: string;
  missingDatagrams: string;
  duplicateDatagrams: string;
  unorderedDatagrams: string;
  restartSignals: string;
  sampleSequenceGaps: string;
  counterSamples: string;
};
export type SFlowResult = {
  sourceKind: "sampled";
  from: string;
  to: string;
  items: SFlowBucket[];
  truncated: boolean;
  resolution: string;
  retentionSeconds: number;
  oldestRetainedAt: string | null;
  newestRetainedAt: string | null;
  quality?: SFlowQuality[];
  notices?: string[];
  alignmentNotice?: string;
};
export const sampledNumber = (value: string | null | undefined, unit = "") =>
  value == null || !/^\d+$/.test(value)
    ? "未提供"
    : `${value.replace(/\B(?=(\d{3})+(?!\d))/g, ",")}${unit ? ` ${unit}` : ""}`;
