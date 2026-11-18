export type LookingGlassPeer = {
  peerId: string;
  name: string;
  routerId: string;
  routerName: string;
  platform: string;
  address: string;
  asn: number;
  views: string[];
};
export type LookingGlassRouter = {
  routerId: string;
  name: string;
  platform: string;
  families: number[];
  views: string[];
};
export type LookingGlassCapabilities = {
  items: {
    platform: string;
    views: string[];
    identity: boolean;
    notes: string;
  }[];
  peers: LookingGlassPeer[];
  routers?: LookingGlassRouter[];
  maxRows: number;
  staleAfterSeconds: number;
  pollAfterSeconds: number;
};
export type RouteQuery = {
  id: string;
  routerId: string;
  routerName: string;
  peerId?: string;
  peerName?: string;
  memberId?: string;
  platform: string;
  view: string;
  prefix?: string;
  family: number;
  maxRows: number;
  status: string;
  revision: string;
  createdAt: string;
  expiresAt: string;
  startedAt?: string;
  completedAt?: string;
  observedAt?: string;
  resultStatus?: string;
  routeCount: number;
  complete: boolean;
  truncated: boolean;
  errorCode?: string;
  pruned: boolean;
  canCancel: boolean;
  canPrune: boolean;
};
export type RoutePathSegment = {
  type: "sequence" | "set" | "confed-sequence" | "confed-set";
  asns: number[];
};
export type LookingGlassRoute = {
  prefix: string;
  nextHops: string[];
  asPath: RoutePathSegment[];
  communities?: number[][] | null;
  largeCommunities?: number[][] | null;
  filtered?: boolean | null;
  unavailableAttributes?: string[];
};
export type RouteQueryResult = {
  query: RouteQuery;
  result: {
    query: {
      routerId: string;
      peerId?: string;
      platform: string;
      neighborAddress?: string;
      neighborAsn?: number;
      view: string;
      prefix?: string;
      maxRows: number;
      ownershipRevision?: string;
      family: number;
    };
    observedAt: string;
    status: "ok" | "truncated" | "unsupported" | "error";
    complete: boolean;
    truncated: boolean;
    identity?: { address: string; asn: number; state: string };
    routes: LookingGlassRoute[];
    errorCode?: string;
  };
  freshness: {
    status: "fresh" | "stale" | "unavailable";
    observedAt?: string;
    ageSeconds?: number;
    staleAfterSeconds: number;
  };
};
export const queryViews: Record<string, string> = {
  accepted: "已接受路由",
  filtered: "被过滤路由",
  "pre-policy": "策略处理前的路由",
  exported: "发往此邻居的路由",
  table: "路由表视图",
};
export const queryStatuses: Record<string, string> = {
  queued: "排队中",
  running: "查询中",
  complete: "查询完成",
  failed: "查询失败",
  unsupported: "不支持此视图",
  expired: "查询已过期",
  cancelled: "已取消",
};
export const pathText = (segments: RoutePathSegment[] | null | undefined) =>
  (segments ?? [])
    .map((segment) =>
      segment.type === "set"
        ? `{${segment.asns.join(", ")}}`
        : segment.type === "confed-sequence"
          ? `联邦序列 (${segment.asns.join(" ")})`
          : segment.type === "confed-set"
            ? `联邦集合 {${segment.asns.join(", ")}}`
            : segment.asns.join(" "),
    )
    .join(" ");
