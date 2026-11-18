import type { Row } from "@/lib/resources";
export type CoreEndpoint = {
  id: string;
  portId: string;
  speedMbps?: number;
  duplex: "full" | "half";
  autoneg: boolean;
  status?: string;
  rateLimitMbps?: number;
  notes?: string;
};
export type CoreSide = {
  switchId: string;
  ownerMemberId?: string;
  interfaceName: string;
  description?: string;
  channelGroup?: number;
  mtu: number;
  trunk: boolean;
  fastLacp: boolean;
  lagFraming: boolean;
};
export type CoreLink = {
  id: string;
  enabled: boolean;
  bfd: boolean;
  ipv4Subnet?: string;
  ipv6Subnet?: string;
  sideA: CoreEndpoint;
  sideB: CoreEndpoint;
};
export type CoreBundle = {
  id: string;
  name: string;
  graphTitle: string;
  type: string;
  enabled: boolean;
  bfd: boolean;
  ipv4Subnet?: string;
  ipv6Subnet?: string;
  stp: boolean;
  cost?: number;
  preference?: number;
  sideA: CoreSide;
  sideB: CoreSide;
  links: CoreLink[];
  bindingRevision?: string;
  createdAt?: string;
  updatedAt?: string;
};
export type CoreDerived = {
  addresses: {
    linkId?: string;
    ipv4?: { subnet: string; sideA: string; sideB: string };
    ipv6?: { subnet: string; sideA: string; sideB: string };
    bfd: boolean;
    enabled: boolean;
  }[];
  capacityKnown: boolean;
  capacityMbps?: number;
  bgpReady: boolean;
  localAsnA?: number;
  localAsnB?: number;
  routerIdA?: string;
  routerIdB?: string;
  inventoryOnly: boolean;
};
export type CoreWarning = { code: string; message: string };
export type CoreBundleRecord = CoreBundle & {
  revision: string;
  derived: CoreDerived;
  warnings: CoreWarning[];
};
export type CorePort = Row & {
  switchId: string;
  available: boolean;
  blockedReason: string;
  adoptable?: boolean;
  speed?: number;
  coreReserved?: boolean;
};
export type CoreInventory = {
  switches: Row[];
  ports: CorePort[];
  members: Row[];
};
export type CoreAdoption = { id: string; etag: string };
export type CoreLegacyLink = Row & {
  portAId: string;
  portBId: string;
  type: string;
  notes?: string;
};
export type CoreCapabilities = {
  types: string[];
  maxLinks: number;
  maxBundles: number;
  inventoryOnly: boolean;
  physicalObservation: boolean;
  lacpObservation: boolean;
  bgpObservation: boolean;
  bfdObservation: boolean;
  canWrite: boolean;
};
export type CoreList = {
  items: CoreBundleRecord[];
  unbundledLinks: CoreLegacyLink[];
  capabilities: CoreCapabilities;
};
export type CorePreview = {
  bundle: CoreBundle;
  derived: CoreDerived;
  warnings: CoreWarning[];
};
export type CoreLayer = {
  status: string;
  source: string;
  observedAt: string | null;
  identityVerified: boolean;
  message: string;
};
export type CorePortObservation = {
  portId: string;
  switchId: string;
  status: string;
  source: string;
  observedAt: string | null;
  identityVerified: boolean;
  operStatus: string;
  adminStatus: string;
  speedMbps: number | null;
  inBps: number | null;
  outBps: number | null;
  counterReset: boolean;
  counterWrapped: boolean;
  errorCode?: string;
};
export type CoreObservations = {
  bundleId: string;
  bindingRevision: string;
  desired: {
    enabled: boolean;
    type: string;
    enabledLinks: number;
    totalLinks: number;
  };
  physical: CoreLayer;
  lacp: CoreLayer;
  bgp: CoreLayer;
  bfd: CoreLayer;
  links: {
    id: string;
    enabled: boolean;
    physical: CoreLayer;
    sideA: CorePortObservation;
    sideB: CorePortObservation;
  }[];
  capacity: Record<
    "sideA" | "sideB",
    {
      totalMbps: number | null;
      enabledMbps: number | null;
      knownPorts: number;
      totalPorts: number;
      source: string;
    }
  >;
  traffic: Record<
    "sideA" | "sideB",
    {
      inBps: number | null;
      outBps: number | null;
      coveredPorts: number;
      expectedPorts: number;
      sourceFrom: string | null;
      sourceTo: string | null;
    }
  >;
  staleAfterSeconds: number;
};
export type CoreSeries = {
  bundleId: string;
  bindingRevision: string;
  side: "a" | "b";
  portIds: string[];
  from: string;
  to: string;
  bucketSeconds: number;
  alignment: string;
  notice: string;
  points: {
    observedAt: string;
    inBps: number | null;
    outBps: number | null;
    coveredPorts: number;
    expectedPorts: number;
    sourceFrom: string | null;
    sourceTo: string | null;
    counterReset: boolean;
    counterWrapped: boolean;
  }[];
};
export const coreTypes: Record<string, string> = {
  ecmp: "ECMP（逐链路三层）",
  "l2-lag": "L2-LAG（二层聚合）",
  "l3-lag": "L3-LAG（三层聚合）",
};
export const coreEndpointStates: Record<string, string> = {
  "": "未指定",
  connected: "已登记连接",
  disabled: "期望停用",
  "not-connected": "尚未连接",
  "awaiting-cross-connect": "等待交叉连接",
  quarantine: "期望隔离",
};
export const coreLayerLabels: Record<string, string> = {
  up: "端口报告 Up",
  down: "端口报告 Down",
  degraded: "部分链路异常",
  unknown: "尚无可判定观测",
  stale: "观测已过期",
  error: "观测失败",
  unsupported: "尚无独立观测适配",
  "not-applicable": "当前类型不适用",
  fresh: "采样新鲜",
};
export const coreCapacity = (value: number | null | undefined) =>
  value == null
    ? "未知"
    : value >= 1000000
      ? `${value / 1000000} Tbps`
      : value >= 1000
        ? `${value / 1000} Gbps`
        : `${value} Mbps`;
export const corePorts = (bundle: CoreBundle | undefined) =>
  bundle
    ? [
        ...new Set(
          bundle.links
            .flatMap((link) => [link.sideA.portId, link.sideB.portId])
            .filter(Boolean),
        ),
      ]
    : [];
export const corePortLabel = (id: string, inventory: CoreInventory) => {
  const port = inventory.ports.find((port) => port.id === id),
    sw = inventory.switches.find((sw) => sw.id === port?.switchId);
  return port
    ? `${sw?.name ?? port.switchId} / ${port.name ?? port.id}`
    : id || "未选择端口";
};
export const coreDesired = (record: CoreBundle): CoreBundle => ({
  id: record.id,
  name: record.name,
  graphTitle: record.graphTitle,
  type: record.type,
  enabled: record.enabled,
  bfd: record.bfd,
  ipv4Subnet: record.ipv4Subnet ?? "",
  ipv6Subnet: record.ipv6Subnet ?? "",
  stp: record.stp,
  ...(record.cost !== undefined ? { cost: record.cost } : {}),
  ...(record.preference !== undefined ? { preference: record.preference } : {}),
  sideA: structuredClone(record.sideA),
  sideB: structuredClone(record.sideB),
  links: structuredClone(record.links),
});
export const newCoreEndpoint = (portId = ""): CoreEndpoint => ({
  id: "",
  portId,
  duplex: "full",
  autoneg: true,
  status: "",
});
export const newCoreLink = (): CoreLink => ({
  id: "",
  enabled: true,
  bfd: false,
  ipv4Subnet: "",
  ipv6Subnet: "",
  sideA: newCoreEndpoint(),
  sideB: newCoreEndpoint(),
});
export const newCoreBundle = (): CoreBundle => ({
  id: "",
  name: "",
  graphTitle: "",
  type: "",
  enabled: true,
  bfd: false,
  ipv4Subnet: "",
  ipv6Subnet: "",
  stp: false,
  sideA: {
    switchId: "",
    interfaceName: "",
    mtu: 0,
    trunk: false,
    fastLacp: false,
    lagFraming: false,
  },
  sideB: {
    switchId: "",
    interfaceName: "",
    mtu: 0,
    trunk: false,
    fastLacp: false,
    lagFraming: false,
  },
  links: [newCoreLink()],
});

export const coreWarningLabels: Record<string, string> = {
  "configured-speed-mismatch":
    "声明速率与物理端口库存不同，双方数值均保留，应用设备配置前需核对。",
  "capacity-unknown": "物理速率缺失或双端不同，不能断言聚合容量。",
  "no-enabled-links": "当前没有期望启用的链路，不能据此判定互联正常。",
  "bgp-incomplete":
    "生成BGP草案需要两端交换机ASN、IPv4 Router ID和loopback接口名。",
  "ipv6-mtu-incomplete": "IPv6需核对两侧MTU至少1280；0代表未知或未设。",
  "inventory-only":
    "这是库存意图，尚未应用设备配置，也不证明LACP、BFD或BGP健康。",
};
