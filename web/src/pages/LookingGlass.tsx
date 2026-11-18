import { useEffect, useRef, useState, type FormEvent } from "react";
import { Clock3, Eye, RefreshCw, Search, Trash2, X } from "lucide-react";
import { api, save } from "@/lib/api";
import {
  queryStatuses,
  queryViews,
  pathText,
  type LookingGlassCapabilities,
  type LookingGlassRoute,
  type RouteQuery,
  type RouteQueryResult,
} from "@/lib/looking-glass";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";

function Freshness({
  value,
  received,
}: {
  value: RouteQueryResult["freshness"];
  received: { wall: number; monotonic: number };
}) {
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = setInterval(() => tick((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, []);
  const elapsed =
    Math.max(
      0,
      Date.now() - received.wall,
      performance.now() - received.monotonic,
    ) / 1000;
  const age =
    typeof value.ageSeconds === "number" && value.ageSeconds >= 0
      ? value.ageSeconds + elapsed
      : undefined;
  const status =
    value.status === "unavailable" || age === undefined
      ? "unavailable"
      : value.status === "stale" || age >= value.staleAfterSeconds
        ? "stale"
        : "fresh";
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={status === "fresh" ? "default" : "outline"}>
          {status === "fresh"
            ? "近期采样"
            : status === "stale"
              ? "采样已过期"
              : "无有效采样时间"}
        </Badge>
        <span className="text-xs text-muted-foreground">
          采样：{time(value.observedAt)}
        </span>
        {age !== undefined && (
          <span className="text-xs text-muted-foreground">
            距采样 {Math.floor(age)} 秒
          </span>
        )}
      </div>
      {status === "stale" && (
        <p className="notice">
          这份结果超过 {value.staleAfterSeconds}{" "}
          秒有效期。它保留了当时观察，不能代表当前路由状态；需要新采样时请再次发起查询。
        </p>
      )}
    </div>
  );
}
const filterLabel = (value: unknown) =>
  value === true ? "已过滤" : value === false ? "未过滤" : "过滤状态未知";
const communityText = (value: number[][] | null | undefined) =>
  Array.isArray(value)
    ? value.length
      ? value.map((parts) => parts.join(":")).join("\n")
      : "返回 0 项"
    : "未提供";
export default function LookingGlass({ role }: { role: string }) {
  const [cap, setCap] = useState<LookingGlassCapabilities | null>(null),
    [items, setItems] = useState<RouteQuery[]>([]),
    [cursor, setCursor] = useState(""),
    [hasMore, setHasMore] = useState(false),
    [loading, setLoading] = useState(true),
    [listError, setListError] = useState(""),
    [busy, setBusy] = useState(false);
  const [targetType, setTargetType] = useState("peer"),
    [targetId, setTargetId] = useState(""),
    [view, setView] = useState(""),
    [family, setFamily] = useState("4"),
    [prefix, setPrefix] = useState(""),
    [maxRows, setMaxRows] = useState("100"),
    [formError, setFormError] = useState(""),
    [selected, setSelected] = useState(""),
    [query, setQuery] = useState<RouteQuery | null>(null),
    [result, setResult] = useState<RouteQueryResult | null>(null),
    [received, setReceived] = useState({
      wall: Date.now(),
      monotonic: performance.now(),
    }),
    [queryError, setQueryError] = useState(""),
    [detailLoading, setDetailLoading] = useState(false),
    [refresh, setRefresh] = useState(0),
    [cachedId, setCachedId] = useState(""),
    [search, setSearch] = useState(""),
    [route, setRoute] = useState<LookingGlassRoute | null>(null),
    [cleanup, setCleanup] = useState<{
      action: "prune" | "delete";
      query: RouteQuery;
    } | null>(null),
    [cleanupError, setCleanupError] = useState("");
  const alive = useRef(true),
    listSequence = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      listSequence.current++;
    };
  }, []);
  const load = async (more = false) => {
    const sequence = ++listSequence.current;
    setLoading(true);
    setListError("");
    try {
      const [caps, list] = await Promise.all([
        api<LookingGlassCapabilities>("/looking-glass/capabilities"),
        api<{
          items: RouteQuery[];
          nextCursor?: string;
          hasMore: boolean;
          pollAfterSeconds: number;
        }>(
          `/looking-glass/queries?${new URLSearchParams({ limit: "50", ...(more && cursor ? { before: cursor } : {}) })}`,
        ),
      ]);
      if (!alive.current || sequence !== listSequence.current) return;
      setCap(caps);
      setMaxRows((value) =>
        String(Math.min(Number(value) || 100, caps.maxRows)),
      );
      setItems((previous) =>
        more
          ? [
              ...previous,
              ...list.items.filter(
                (item) => !previous.some((existing) => existing.id === item.id),
              ),
            ]
          : list.items,
      );
      setCursor(list.nextCursor ?? "");
      setHasMore(list.hasMore);
      if (!more) setSelected((id) => id || list.items[0]?.id || "");
    } catch (error) {
      if (alive.current && sequence === listSequence.current)
        setListError(message(error));
    } finally {
      if (alive.current && sequence === listSequence.current) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    setQuery(null);
    setResult(null);
    setQueryError("");
    setRoute(null);
    setSearch("");
    if (!selected) {
      setDetailLoading(false);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    setDetailLoading(true);
    const read = async () => {
      try {
        const current = await api<RouteQuery>(
          `/looking-glass/queries/${selected}`,
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        setQuery(current);
        setItems((rows) =>
          rows.map((row) => (row.id === current.id ? current : row)),
        );
        setDetailLoading(false);
        if (["queued", "running"].includes(current.status)) {
          timer = setTimeout(
            () => void read(),
            Math.max(2000, (cap?.pollAfterSeconds ?? 2) * 1000),
          );
          return;
        }
        if (!current.pruned && current.status === "complete") {
          setDetailLoading(true);
          const fetchStarted = performance.now();
          const envelope = await api<RouteQueryResult>(
            `/looking-glass/queries/${selected}/result`,
            { signal: controller.signal },
          );
          if (controller.signal.aborted) return;
          if (
            envelope.query.id !== selected ||
            envelope.result.query.routerId !== current.routerId ||
            (current.peerId && envelope.result.query.peerId !== current.peerId)
          )
            throw new Error("返回结果不属于当前查询目标，已拒绝显示。");
          if (typeof envelope.freshness.ageSeconds === "number")
            envelope.freshness.ageSeconds +=
              Math.max(0, performance.now() - fetchStarted) / 1000;
          setResult(envelope);
          setQuery(envelope.query);
          setReceived({ wall: Date.now(), monotonic: performance.now() });
          setDetailLoading(false);
        }
      } catch (error) {
        if (!controller.signal.aborted) {
          setQueryError(message(error));
          setDetailLoading(false);
        }
      }
    };
    void read();
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [selected, refresh]);
  const peer = cap?.peers.find((item) => item.peerId === targetId),
    router = cap?.routers?.find((item) => item.routerId === targetId),
    views = (targetType === "peer" ? peer?.views : router?.views) ?? [],
    platform = targetType === "peer" ? peer?.platform : router?.platform,
    platformCap = cap?.items.find((item) => item.platform === platform);
  const platformGuidance = (() => {
    if (!platformCap) return "";
    const supported = platformCap.views,
      notes: string[] = [];
    if (platform === "bird2" || platform === "bird3") {
      if (supported.includes("filtered"))
        notes.push("过滤视图需要保留已过滤路由。");
      if (supported.includes("pre-policy"))
        notes.push("策略前视图需要启用导入表。");
    } else if (platform === "frr") {
      if (supported.includes("filtered") || supported.includes("pre-policy"))
        notes.push("过滤与策略前视图需要启用入站软重配置。");
      notes.push("部分视图不提供社区属性；已接受路由可按精确前缀进一步查看。");
    } else if (platform === "openbgpd") {
      if (!supported.includes("filtered"))
        notes.push("当前平台不提供已过滤路由视图。");
      if (!supported.includes("table"))
        notes.push("当前平台不提供独立表级查询。");
    }
    if (targetType === "table" && supported.includes("table"))
      notes.push("表级查询需要管理员在采集器启用，并成功核对目标路由器身份。");
    if (!platformCap.identity)
      notes.push("此平台无法核对目标身份，当前不能提供可靠查询。");
    return (
      notes.join(" ") ||
      "查询范围限于列出的可用视图，结果以实际采样与目标身份核对为准。"
    );
  })();
  const chooseTarget = (id: string, type = targetType) => {
    setTargetId(id);
    const target =
      type === "peer"
        ? cap?.peers.find((item) => item.peerId === id)
        : cap?.routers?.find((item) => item.routerId === id);
    const modes = target?.views ?? [];
    setView(modes.includes("accepted") ? "accepted" : (modes[0] ?? ""));
    if (type === "table") {
      const table = cap?.routers?.find((item) => item.routerId === id);
      setFamily(String(table?.families[0] ?? 4));
    }
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!targetId || !views.includes(view)) return;
    setBusy(true);
    setFormError("");
    try {
      const response = await save<{ item: RouteQuery; cached: boolean }>(
        "/looking-glass/queries",
        {
          ...(targetType === "peer"
            ? { peerId: targetId }
            : { routerId: targetId, family: Number(family) }),
          view,
          ...(prefix.trim() ? { prefix: prefix.trim() } : {}),
          maxRows: Number(maxRows),
        },
      );
      if (!alive.current) return;
      setItems((rows) => [
        response.item,
        ...rows.filter((item) => item.id !== response.item.id),
      ]);
      setCachedId(response.cached ? response.item.id : "");
      setSelected(response.item.id);
      setRefresh((value) => value + 1);
    } catch (error) {
      if (alive.current) setFormError(message(error));
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const cancel = async () => {
    if (!query?.canCancel) return;
    setBusy(true);
    setQueryError("");
    try {
      await save(`/looking-glass/queries/${query.id}/cancel`, {
        expectedRevision: query.revision,
      });
      setRefresh((value) => value + 1);
      void load();
    } catch (error) {
      setQueryError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const clean = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!cleanup) return;
    const data = new FormData(event.currentTarget);
    setBusy(true);
    setCleanupError("");
    try {
      await save(
        `/looking-glass/queries/${cleanup.query.id}${cleanup.action === "prune" ? "/prune" : ""}`,
        {
          expectedRevision: cleanup.query.revision,
          acknowledge: true,
          reason: String(data.get("reason") ?? ""),
        },
        cleanup.action === "delete" ? "DELETE" : "POST",
      );
      const deleted = cleanup.action === "delete";
      setCleanup(null);
      if (deleted) {
        setSelected("");
        setQuery(null);
        setResult(null);
      } else setRefresh((value) => value + 1);
      await load();
    } catch (error) {
      setCleanupError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const rows = (result?.result.routes ?? []).filter((item) =>
    `${item.prefix} ${item.nextHops?.join(" ")} ${pathText(item.asPath)}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  const readable = result && ["ok", "truncated"].includes(result.result.status);
  return (
    <>
      <Heading
        title="Looking Glass"
        description="按会话和平台支持的固定视图查询路由，查看采样时的路径与社区属性。"
      >
        <Button
          variant="outline"
          disabled={loading}
          onClick={() => {
            void load();
            setRefresh((value) => value + 1);
          }}
        >
          <RefreshCw />
          刷新记录
        </Button>
      </Heading>
      <div className="notice mb-5">
        查询完成表示已收到一次观察结果，不表示网络健康或路由已经生效。策略前视图保留原始观察，过滤状态以明确返回的字段为准。
      </div>
      <ErrorNotice message={listError} />
      {!cap ? (
        loading ? (
          <Loading />
        ) : null
      ) : (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Search className="size-4 text-primary" />
              发起路由查询
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="space-y-4">
              <ErrorNotice message={formError} />
              {role !== "member" && !!cap.routers?.length && (
                <div className="flex flex-wrap gap-4 text-sm">
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      checked={targetType === "peer"}
                      onChange={() => {
                        setTargetType("peer");
                        chooseTarget("", "peer");
                      }}
                    />
                    按 BGP 会话
                  </label>
                  <label className="flex items-center gap-2">
                    <input
                      type="radio"
                      checked={targetType === "table"}
                      onChange={() => {
                        setTargetType("table");
                        chooseTarget("", "table");
                      }}
                    />
                    按路由器表
                  </label>
                </div>
              )}
              <div className="form-grid">
                <div className="field">
                  <label htmlFor="lg-target">
                    {targetType === "peer" ? "可访问的 BGP 会话" : "路由器"}
                  </label>
                  <select
                    id="lg-target"
                    required
                    value={targetId}
                    onChange={(event) => chooseTarget(event.target.value)}
                  >
                    <option value="">请选择查询目标</option>
                    {targetType === "peer"
                      ? cap.peers.map((item) => (
                          <option key={item.peerId} value={item.peerId}>
                            {item.name} · AS{item.asn} · {item.address}
                          </option>
                        ))
                      : cap.routers?.map((item) => (
                          <option key={item.routerId} value={item.routerId}>
                            {item.name} · {item.platform}
                          </option>
                        ))}
                  </select>
                </div>
                <div className="field">
                  <label htmlFor="lg-view">查询视图</label>
                  <select
                    id="lg-view"
                    required
                    value={view}
                    onChange={(event) => setView(event.target.value)}
                    disabled={!views.length}
                  >
                    <option value="">请选择可用视图</option>
                    {views.map((item) => (
                      <option key={item} value={item}>
                        {queryViews[item] ?? item}
                      </option>
                    ))}
                  </select>
                </div>
                {targetType === "table" && (
                  <div className="field">
                    <label htmlFor="lg-family">地址族</label>
                    <select
                      id="lg-family"
                      value={family}
                      onChange={(event) => setFamily(event.target.value)}
                    >
                      {router?.families.map((value) => (
                        <option key={value} value={value}>
                          IPv{value}
                        </option>
                      ))}
                    </select>
                  </div>
                )}
                <div className="field">
                  <label htmlFor="lg-prefix">前缀（可选）</label>
                  <Input
                    id="lg-prefix"
                    placeholder="198.51.100.0/24 或 2001:db8::/48"
                    value={prefix}
                    onChange={(event) => setPrefix(event.target.value)}
                  />
                </div>
                <div className="field">
                  <label htmlFor="lg-max-rows">最多返回行数</label>
                  <Input
                    id="lg-max-rows"
                    required
                    type="number"
                    min={1}
                    max={cap.maxRows}
                    value={maxRows}
                    onChange={(event) => setMaxRows(event.target.value)}
                  />
                </div>
              </div>
              {platformCap && (
                <p className="text-xs leading-6 text-muted-foreground">
                  {platformGuidance}
                </p>
              )}
              {targetId && !views.length && (
                <p className="notice">当前平台或目标没有可用查询视图。</p>
              )}
              {view === "pre-policy" && (
                <p className="notice">
                  策略处理前的路由包含尚未判断或最终被接受的条目，不能将整份结果标为被拒绝。
                </p>
              )}
              {!cap.peers.length && targetType === "peer" && (
                <p className="text-xs text-muted-foreground">
                  当前账号没有可查询的活动会话。
                </p>
              )}
              <div className="flex justify-end">
                <Button
                  disabled={busy || !targetId || !view || !views.includes(view)}
                >
                  <Search />
                  {busy ? "正在提交…" : "发起查询"}
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      )}
      <div className="deployment-grid">
        <Card className="self-start overflow-hidden">
          <CardHeader>
            <CardTitle>查询记录</CardTitle>
          </CardHeader>
          {!items.length ? (
            loading ? (
              <Loading />
            ) : (
              <Empty description="尚无可访问的查询记录。选择目标后发起第一次查询。" />
            )
          ) : (
            <div className="max-h-[650px] overflow-y-auto">
              {items.map((item) => (
                <button
                  key={item.id}
                  data-query-id={item.id}
                  className={`w-full border-t px-5 py-4 text-left ${selected === item.id ? "bg-primary/5" : "hover:bg-muted/30"}`}
                  onClick={() => {
                    setSelected(item.id);
                    setCachedId("");
                  }}
                >
                  <p className="truncate text-sm font-medium">
                    {item.peerName || item.routerName}
                  </p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {queryViews[item.view] ?? item.view} · IPv{item.family}
                  </p>
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    {time(item.createdAt)}
                  </p>
                  <div className="mt-2 flex flex-wrap gap-2">
                    <Badge variant="outline">
                      {queryStatuses[item.status] ?? item.status}
                    </Badge>
                    {item.pruned && <Badge variant="outline">结果已清理</Badge>}
                  </div>
                </button>
              ))}
            </div>
          )}
          {hasMore && (
            <div className="border-t p-3">
              <Button
                className="w-full"
                variant="ghost"
                disabled={loading}
                onClick={() => void load(true)}
              >
                加载更早记录
              </Button>
            </div>
          )}
        </Card>
        <div className="min-w-0 space-y-4">
          <ErrorNotice message={queryError} />
          {queryError && query && (
            <p className="notice">
              更新失败，以下状态来自上一次成功读取。可刷新记录重新核对。
            </p>
          )}
          {detailLoading && !query ? (
            <Loading />
          ) : !query ? (
            <Card>
              <Empty description="选择一条记录查看排队状态和路由结果。" />
            </Card>
          ) : (
            <Card className="min-w-0">
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <CardTitle>{query.peerName || query.routerName}</CardTitle>
                  <Badge variant="outline">
                    {queryStatuses[query.status] ?? query.status}
                  </Badge>
                </div>
                <p className="text-xs leading-6 text-muted-foreground">
                  {query.routerName} · {query.platform} ·{" "}
                  {queryViews[query.view] ?? query.view} · IPv{query.family}
                  {query.prefix ? ` · ${query.prefix}` : ""}
                </p>
                <p className="text-xs text-muted-foreground">
                  请求创建：{time(query.createdAt)} · 完成：
                  {time(query.completedAt)}
                </p>
              </CardHeader>
              <CardContent>
                <div className="space-y-4">
                  {cachedId === query.id && (
                    <p className="notice">
                      服务端返回已有查询记录，请以结果中的采样时间为准。
                    </p>
                  )}
                  {["queued", "running"].includes(query.status) && (
                    <div className="notice flex items-start gap-3">
                      <Clock3 className="mt-1 size-4 shrink-0" />
                      <div>
                        <p>
                          {query.status === "queued"
                            ? "等待对应采集器处理。"
                            : "采集器正在执行固定查询。"}
                        </p>
                        <p className="mt-1 text-xs">
                          查询到期：{time(query.expiresAt)}
                          。页面仅轮询此任务，不会重复提交。
                        </p>
                      </div>
                    </div>
                  )}
                  {query.pruned ? (
                    <p className="notice">
                      此查询的结果文件已清理，仅保留任务元数据。
                    </p>
                  ) : result ? (
                    <>
                      <Freshness
                        key={`${query.id}-${received.monotonic}`}
                        value={result.freshness}
                        received={received}
                      />
                      {result.result.identity?.address && (
                        <div className="rounded-lg border p-3 text-xs leading-6">
                          采样确认的邻居：
                          <span className="mono">
                            {result.result.identity.address} · AS
                            {result.result.identity.asn}
                          </span>
                          <br />
                          查询时邻居状态：
                          {result.result.identity.state || "未提供"}
                        </div>
                      )}
                      {(result.result.truncated || query.truncated) && (
                        <p className="notice">
                          结果已截断，当前只显示返回的{" "}
                          {result.result.routes.length}{" "}
                          条路由。未显示的条目不能据此判断为不存在。
                        </p>
                      )}
                      {readable ? (
                        <>
                          <div className="flex flex-wrap items-center justify-between gap-3">
                            <p className="text-xs text-muted-foreground">
                              返回 {result.result.routes.length} 条 · 当前显示{" "}
                              {rows.length} 条
                            </p>
                            <Input
                              className="max-w-xs"
                              aria-label="筛选本次路由结果"
                              placeholder="筛选本次结果：前缀、下一跳、ASN…"
                              value={search}
                              onChange={(event) =>
                                setSearch(event.target.value)
                              }
                            />
                          </div>
                          {rows.length ? (
                            <div className="overflow-x-auto rounded-lg border">
                              <table>
                                <thead>
                                  <tr>
                                    <th>前缀</th>
                                    <th>下一跳</th>
                                    <th>AS 路径</th>
                                    <th>过滤状态</th>
                                    <th>详情</th>
                                  </tr>
                                </thead>
                                <tbody>
                                  {rows.map((item, index) => (
                                    <tr key={`${item.prefix}-${index}`}>
                                      <td className="mono text-xs">
                                        {item.prefix}
                                      </td>
                                      <td className="mono text-xs">
                                        {item.nextHops?.join(", ") || "未提供"}
                                      </td>
                                      <td className="mono max-w-xs break-words text-xs">
                                        {pathText(item.asPath) || "未提供"}
                                      </td>
                                      <td>
                                        <Badge variant="outline">
                                          {filterLabel(item.filtered)}
                                        </Badge>
                                      </td>
                                      <td>
                                        <Button
                                          variant="ghost"
                                          size="icon"
                                          aria-label={`查看路由 ${item.prefix}`}
                                          onClick={() => setRoute(item)}
                                        >
                                          <Eye />
                                        </Button>
                                      </td>
                                    </tr>
                                  ))}
                                </tbody>
                              </table>
                            </div>
                          ) : (
                            <Empty
                              title={
                                result.result.routes.length
                                  ? "没有匹配的返回行"
                                  : "本次返回没有路由"
                              }
                              description="仅表示此目标、视图和前缀条件下的采样结果。"
                            />
                          )}
                        </>
                      ) : (
                        <p className="notice">
                          {result.result.status === "unsupported"
                            ? "目标运行环境不支持本次视图。"
                            : "本次没有取得可用的路由结果。"}
                          {result.result.errorCode && (
                            <span className="mono ml-2">
                              {result.result.errorCode}
                            </span>
                          )}
                        </p>
                      )}
                    </>
                  ) : detailLoading ? (
                    <Loading />
                  ) : [
                      "failed",
                      "unsupported",
                      "expired",
                      "cancelled",
                    ].includes(query.status) ? (
                    <p className="notice">
                      {queryStatuses[query.status]}。
                      {query.errorCode && (
                        <span className="mono">{query.errorCode}</span>
                      )}
                      没有可展示的路由结果。
                    </p>
                  ) : null}
                  <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
                    <Button
                      variant="outline"
                      disabled={busy || detailLoading}
                      onClick={() => setRefresh((value) => value + 1)}
                    >
                      <RefreshCw />
                      读取当前任务状态
                    </Button>
                    {query.canCancel && (
                      <Button
                        variant="outline"
                        disabled={busy}
                        onClick={() => void cancel()}
                      >
                        <X />
                        取消查询
                      </Button>
                    )}
                    {query.canPrune && (
                      <Button
                        variant="ghost"
                        disabled={busy}
                        onClick={() => {
                          setCleanup({
                            action: "prune",
                            query: structuredClone(query),
                          });
                          setCleanupError("");
                        }}
                      >
                        <Trash2 />
                        清理结果文件
                      </Button>
                    )}
                    {role === "admin" && query.pruned && (
                      <Button
                        variant="ghost"
                        disabled={busy}
                        onClick={() => {
                          setCleanup({
                            action: "delete",
                            query: structuredClone(query),
                          });
                          setCleanupError("");
                        }}
                      >
                        删除已清理记录
                      </Button>
                    )}
                  </div>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      </div>
      <Dialog open={!!route} onOpenChange={(value) => !value && setRoute(null)}>
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            路由详情 · {route?.prefix}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            这是同一份采样中的路由属性。社区仅作属性展示；过滤状态取自明确返回的字段。
          </DialogDescription>
          {route && (
            <div className="space-y-4 text-sm">
              <div>
                <span className="text-xs text-muted-foreground">过滤状态</span>
                <p className="mt-1">{filterLabel(route.filtered)}</p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">下一跳</span>
                <p className="mono mt-1 break-all">
                  {route.nextHops?.join(", ") || "未提供"}
                </p>
              </div>
              <div>
                <span className="text-xs text-muted-foreground">
                  AS 路径（花括号代表无序 AS_SET）
                </span>
                <p className="mono mt-1 break-all">
                  {pathText(route.asPath) || "未提供"}
                </p>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <span className="text-xs text-muted-foreground">
                    返回的标准社区
                  </span>
                  <pre className="mono mt-2 max-h-60 overflow-auto whitespace-pre-wrap break-all rounded border p-3 text-xs">
                    {route.unavailableAttributes?.includes("communities")
                      ? "此原生视图未提供"
                      : communityText(route.communities)}
                  </pre>
                </div>
                <div>
                  <span className="text-xs text-muted-foreground">
                    返回的大型社区
                  </span>
                  <pre className="mono mt-2 max-h-60 overflow-auto whitespace-pre-wrap break-all rounded border p-3 text-xs">
                    {route.unavailableAttributes?.includes("largeCommunities")
                      ? "此原生视图未提供"
                      : communityText(route.largeCommunities)}
                  </pre>
                </div>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!cleanup}
        onOpenChange={(value) => !value && !busy && setCleanup(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {cleanup?.action === "delete"
              ? "删除已清理查询记录？"
              : "清理查询结果文件？"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            清理不可撤销，请先保留需要的观察记录。服务端会再次核对任务修订与操作权限。
          </DialogDescription>
          <ErrorNotice message={cleanupError} />
          <form onSubmit={clean} className="space-y-4">
            <div className="field">
              <label htmlFor="lg-cleanup-reason">
                操作原因{cleanup?.action === "delete" ? "（必填）" : "（可选）"}
              </label>
              <textarea
                id="lg-cleanup-reason"
                name="reason"
                required={cleanup?.action === "delete"}
                maxLength={1000}
              />
            </div>
            <label className="flex items-start gap-2 text-sm leading-6">
              <input type="checkbox" required className="mt-1" />
              我已保留需要的记录，理解此次清理无法撤销。
            </label>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setCleanup(null)}
              >
                取消
              </Button>
              <Button variant="destructive" disabled={busy}>
                {busy ? "正在处理…" : "确认清理"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
