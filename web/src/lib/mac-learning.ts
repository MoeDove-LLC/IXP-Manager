import type { Row } from "@/lib/resources";
export type RegisteredMAC = {
  id: string;
  name: string;
  address: string;
  connectionId: string;
  connectionLabel: string;
  memberId: string;
  memberLabel: string;
  vlanId: string;
  vlanLabel: string;
  observed: boolean;
};
export type LearnedMAC = {
  id: string;
  mac: string;
  ifIndex: number;
  bridgePort: number;
  fdbId: number;
  vlanTags: number[];
  portId?: string;
  portLabel?: string;
  vlanId?: string;
  vlanLabel?: string;
  connectionId?: string;
  connectionLabel?: string;
  memberId?: string;
  memberLabel?: string;
  registrationStatus:
    | "candidate"
    | "registered"
    | "conflict"
    | "ambiguous"
    | "unmapped"
    | "ineligible"
    | "stale";
  reasons: string[];
  canReview: boolean;
  registeredIds: string[];
};
export type MACLearning = {
  switch: { id: string; name: string };
  observation: {
    checksum: string;
    observedAt?: string;
    lastAttemptAt?: string;
    status: "fresh" | "stale" | "error" | "unknown";
    security?: string;
  };
  registered: RegisteredMAC[];
  registeredTotal?: number;
  registeredTruncated?: boolean;
  items: LearnedMAC[];
  offset: number;
  limit: number;
  totalRows: number;
  truncated: boolean;
  capabilities: { canReview: boolean; canApply: boolean };
  notices: string[];
};
export type MACReview = {
  candidate: LearnedMAC;
  reviewChecksum: string;
  expiresAt: string;
  consequences: string[];
};
export type MACApplied = { registered: Row; receiverReviewRequired: boolean };
export const macRegistrationLabels: Record<string, string> = {
  candidate: "可审阅登记",
  registered: "已有登记",
  conflict: "登记冲突",
  ambiguous: "归属不唯一",
  unmapped: "尚未映射",
  ineligible: "不符合登记条件",
  stale: "证据已过期",
};
export const macDraftKey = (switchId: string, row: LearnedMAC) =>
  `${switchId}/${row.mac}/${row.ifIndex}/${[...(row.vlanTags ?? [])].sort((a, b) => a - b).join(",")}`;
