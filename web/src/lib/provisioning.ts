import { readAS112Bundle } from "@/lib/as112";
import { RFC2317_MERGE_KIND, verifyRFC2317Merge, type RFC2317Metadata } from "@/lib/rfc2317";

export type ProvisioningRevision = {
  id: string;
  kind: string;
  profileName: string;
  profileId: string;
  status: "draft" | "validated" | "approved";
  checksum: string;
  checksumKind: string;
  validationLevel: string;
  validationNotice: string;
  validation?: string;
  complete?: boolean;
  filename?: string;
  zone?: string;
  switchId?: string;
  routerId?: string;
  dnsSerial?: number;
  rfc2317?: RFC2317Metadata;
  targetCount?: number;
  notices?: {
    severity: string;
    code: string;
    message: string;
    targetId?: string;
  }[];
  createdAt: string;
  pruned?: boolean;
  artifactUnavailable?: boolean;
};

export type ProvisioningDiff = {
  redacted: boolean;
  baseId: string;
  baseUnavailable?: boolean;
  changes: { type: "context" | "add" | "remove"; text: string }[];
  added: number;
  removed: number;
  truncated: boolean;
};

export type ProvisioningCapability = {
  kind: string;
  label: string;
  validationLevel: "native" | "structural";
  nativeCheckerAvailable: boolean;
  complete: boolean;
  validationNotice: string;
};

export type ProvisioningProfile = {
  id: string;
  name: string;
  revision: string;
  options: ProvisioningOptions;
  dnsNewZone?: boolean;
  dnsSerialFloor?: number;
  createdAt: string;
  updatedAt: string;
};

export const provisioningLabels: Record<string, string> = {
  "dns-arpa": "DNS 反向解析",
  nagios: "Nagios",
  smokeping: "Smokeping",
  "switch-core": "交换机核心网络计划",
  "as112-dns": "AS112 权威 DNS 服务包",
};

export type ProvisioningOptions = {
  kind: "dns-arpa" | "nagios" | "smokeping" | "switch-core" | "as112-dns";
  switchId?: string;
  routerId?: string;
  vlanId?: string;
  infrastructureId?: string;
  families: number[];
  dns?: {
    prefix: string;
    primaryNS: string;
    responsibleMailbox: string;
    nameServers: string[];
    ttl?: number;
    refresh?: number;
    retry?: number;
    expire?: number;
    minimum?: number;
    nameSuffix?: string;
    activeOnly?: boolean;
  };
  nagios?: Record<string, string>;
  smokeping?: { level: number; probe4: string; probe6: string };
  as112?: { serverName: string; responsibleMailbox: string; facility: string; location: string; uniqueIP: string; loc: string };
};

// Review checksums are HMAC tokens. Downloads must independently bind the file
// SHA and target metadata before any Blob URL is made available to the browser.
export async function verifyProvisioningDownload(
  revision: ProvisioningRevision,
  headers: Headers,
  bytes: ArrayBuffer,
): Promise<string> {
  const expected = headers.get("X-Config-SHA256") ?? "";
  if (
    !/^[a-f0-9]{64}$/i.test(expected) ||
    headers.get("X-Provisioning-Revision") !== revision.id ||
    headers.get("X-Provisioning-Profile-ID") !== revision.profileId ||
    headers.get("X-Provisioning-Kind") !== revision.kind
  ) throw new Error("下载目标、版本或真实文件 SHA-256 不匹配，未保存文件。");
  const hash = await crypto.subtle.digest("SHA-256", bytes);
  const actual = Array.from(new Uint8Array(hash), value => value.toString(16).padStart(2, "0")).join("");
  if (actual !== expected.toLowerCase()) throw new Error("下载文件校验失败，未保存文件。");
  if (revision.kind === RFC2317_MERGE_KIND) {
    await verifyRFC2317Merge(revision.rfc2317, bytes);
  }
  if (revision.kind === "as112-dns") {
    if (!revision.routerId || headers.get("X-Provisioning-Router-ID") !== revision.routerId)
      throw new Error("下载 AS112 路由节点标识不匹配，未保存文件。");
    try { readAS112Bundle(new TextDecoder("utf-8", { fatal: true }).decode(bytes), revision.routerId); }
    catch { throw new Error("AS112 下载包的路由目标或服务检查边界不匹配，未保存文件。"); }
  }
  if (revision.kind === "switch-core") {
    if (!revision.switchId || headers.get("X-Provisioning-Switch-ID") !== revision.switchId)
      throw new Error("下载交换机标识不匹配，未保存文件。");
    let plan: unknown;
    try { plan = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(bytes)); }
    catch { throw new Error("下载内容不是有效的交换机 JSON 计划，未保存文件。"); }
    if (!plan || typeof plan !== "object" || Array.isArray(plan))
      throw new Error("下载内容不是交换机计划对象，未保存文件。");
    const value = plan as Record<string, unknown>;
    if (value.switchId !== revision.switchId || value.kind !== "switch-core" || value.version !== 1 || value.inventoryOnly !== true || value.nativeValidated !== false || value.requiresTargetAdapter !== true)
      throw new Error("JSON 内的交换机目标或计划校验范围不匹配，未保存文件。");
  }
  return actual;
}
