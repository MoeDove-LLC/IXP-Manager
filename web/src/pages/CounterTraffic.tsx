import { useEffect, useRef, useState, type FormEvent } from "react";
import { Clock3, RefreshCw, Search } from "lucide-react";
import { api } from "@/lib/api";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
} from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import TrafficReport from "@/components/TrafficReport";
import TrafficSourceControl from "@/components/TrafficSourceControl";
import {
  fromUTCInput,
  scopeLabels,
  utcInput,
  type TrafficCatalog,
  type TrafficResult,
  type TrafficTarget,
} from "@/lib/traffic";

type Filter = {
  target: string;
  metric: string;
  resolution: string;
  from: string;
  to: string;
  side: string;
};
const targetKey = (target: TrafficTarget) =>
  target.key ??
  `${target.scope}:${target.id}${target.memberRevision ? `:${target.memberRevision}` : ""}`;
const emptyFilter: Filter = {
  target: "",
  metric: "",
  resolution: "",
  from: "",
  to: "",
  side: "a",
};

export default function CounterTraffic({ role }: { role: string }) {
  const [catalog, setCatalog] = useState<TrafficCatalog | null>(null);
  const [catalogLoading, setCatalogLoading] = useState(true),
    [catalogError, setCatalogError] = useState("");
  const [filter, setFilter] = useState<Filter>(emptyFilter),
    [search, setSearch] = useState("");
  const [result, setResult] = useState<TrafficResult | null>(null),
    [resultLabel, setResultLabel] = useState("");
  const [queryLoading, setQueryLoading] = useState(false),
    [queryError, setQueryError] = useState("");
  const [applied, setApplied] = useState(""),
    [reportVersion, setReportVersion] = useState(0);
  const request = useRef<AbortController | null>(null),
    catalogRequest = useRef<AbortController | null>(null);
  const run = async (next: Filter, available: TrafficCatalog) => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setQueryError("");
    setResult(null);
    setQueryLoading(true);
    try {
      const target = available.targets.find(
        (t) => targetKey(t) === next.target,
      );
      if (!target) throw new Error("请选择当前账号可访问的统计对象。");
      const from = fromUTCInput(next.from),
        to = fromUTCInput(next.to);
      if (Date.parse(from) >= Date.parse(to))
        throw new Error("结束时间必须晚于开始时间。");
      const params = new URLSearchParams({
        scope: target.scope,
        id: target.id,
        metric: next.metric,
        resolution: next.resolution,
        from,
        to,
      });
      if (target.requiresSide) params.set("side", next.side);
      if (target.memberRevision)
        params.set("memberRevision", target.memberRevision);
      const data = await api<TrafficResult>(`/traffic/query?${params}`, {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (data.sourceKind !== "counter")
        throw new Error("此页面仅显示计数器归档，返回的数据来源不匹配。");
      setResult(data);
      setResultLabel(data.targetLabel || target.label);
      setApplied(JSON.stringify(next));
      setReportVersion((v) => v + 1);
    } catch (e) {
      if (!controller.signal.aborted) setQueryError(message(e));
    } finally {
      if (!controller.signal.aborted) setQueryLoading(false);
    }
  };
  const load = async (initial: boolean) => {
    catalogRequest.current?.abort();
    request.current?.abort();
    const controller = new AbortController();
    catalogRequest.current = controller;
    setCatalogLoading(true);
    setCatalogError("");
    setResult(null);
    setQueryLoading(false);
    setQueryError("");
    try {
      const data = await api<TrafficCatalog>("/traffic/catalog", {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (data.sourceKind !== "counter" || data.timezone !== "UTC")
        throw new Error("统计目录的数据来源或时区不受此页面支持。");
      setCatalog(data);
      const end = new Date(),
        start = new Date(end.getTime() - data.defaults.hours * 3600000);
      const next = initial
        ? {
            target: data.targets[0] ? targetKey(data.targets[0]) : "",
            metric: data.defaults.metric,
            resolution: data.defaults.resolution,
            from: utcInput(start),
            to: utcInput(end),
            side: "a",
          }
        : {
            ...filter,
            target: data.targets.some((t) => targetKey(t) === filter.target)
              ? filter.target
              : data.targets[0]
                ? targetKey(data.targets[0])
                : "",
          };
      setFilter(next);
      if (next.target) void run(next, data);
    } catch (e) {
      if (!controller.signal.aborted) {
        setCatalog(null);
        setCatalogError(message(e));
      }
    } finally {
      if (!controller.signal.aborted) setCatalogLoading(false);
    }
  };
  useEffect(() => {
    void load(true);
    return () => {
      request.current?.abort();
      catalogRequest.current?.abort();
    };
  }, []);
  const selected = catalog?.targets.find((t) => targetKey(t) === filter.target);
  const targets =
    catalog?.targets.filter(
      (t) =>
        targetKey(t) === filter.target ||
        `${t.label} ${t.id} ${scopeLabels[t.scope] ?? t.scope}`
          .toLowerCase()
          .includes(search.toLowerCase()),
    ) ?? [];
  const scopes = [...new Set(targets.map((t) => t.scope))];
  const update = (key: keyof Filter, value: string) =>
    setFilter((current) => ({ ...current, [key]: value }));
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (catalog) void run(filter, catalog);
  };
  return (
    <>
      <Heading
        title="长期流量"
        description="按采样时归属归档物理计数器差值，查看长期累计、速率与有效覆盖区间。"
      >
        <Button
          variant="outline"
          disabled={catalogLoading || queryLoading}
          onClick={() => void load(false)}
        >
          <RefreshCw className={catalogLoading ? "animate-spin" : ""} />
          刷新可选对象
        </Button>
      </Heading>
      <ErrorNotice message={catalogError} />
      {catalogLoading ? (
        <Loading />
      ) : (
        catalog && (
          <>
            {!catalog.targets.length ? (
              <Card>
                <Empty
                  title="暂无可访问的统计对象"
                  description="对象列表由服务端按账号权限提供；配置接入与计数采集后再刷新查看。"
                />
              </Card>
            ) : (
              <Card className="mb-5">
                <CardHeader>
                  <CardTitle className="flex items-center gap-2">
                    <Clock3 className="size-4 text-primary" />
                    查询归档
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  <form onSubmit={submit} className="space-y-4">
                    <div className="grid gap-4 md:grid-cols-2">
                      <div className="field">
                        <label htmlFor="traffic-search">查找统计对象</label>
                        <Input
                          id="traffic-search"
                          placeholder="按名称、ID 或对象类型搜索"
                          value={search}
                          onChange={(e) => setSearch(e.target.value)}
                        />
                      </div>
                      <div className="field">
                        <label htmlFor="traffic-target">统计对象</label>
                        <select
                          id="traffic-target"
                          required
                          value={filter.target}
                          onChange={(e) => update("target", e.target.value)}
                        >
                          {scopes.map((scope) => (
                            <optgroup
                              key={scope}
                              label={scopeLabels[scope] ?? scope}
                            >
                              {targets
                                .filter((t) => t.scope === scope)
                                .map((t) => (
                                  <option
                                    key={targetKey(t)}
                                    value={targetKey(t)}
                                  >
                                    {t.label}
                                  </option>
                                ))}
                            </optgroup>
                          ))}
                        </select>
                      </div>
                    </div>
                    {selected?.notice && (
                      <p className="notice text-xs">{selected.notice}</p>
                    )}
                    {role !== "member" &&
                      selected?.scope === "port" &&
                      selected.sourceControl && (
                        <TrafficSourceControl
                          key={targetKey(selected)}
                          portId={selected.id}
                          control={selected.sourceControl}
                          canWrite={role === "admin" || role === "operator"}
                          onSaved={() => load(false)}
                        />
                      )}
                    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                      <div className="field">
                        <label htmlFor="traffic-metric">计数指标</label>
                        <select
                          id="traffic-metric"
                          value={filter.metric}
                          onChange={(e) => update("metric", e.target.value)}
                        >
                          {catalog.metrics.map((m) => (
                            <option value={m.id} key={m.id}>
                              {m.label}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="field">
                        <label htmlFor="traffic-resolution">统计分辨率</label>
                        <select
                          id="traffic-resolution"
                          value={filter.resolution}
                          onChange={(e) => update("resolution", e.target.value)}
                        >
                          {catalog.resolutions.map((r) => (
                            <option key={r.id} value={r.id}>
                              {r.label} · 保留上限 {r.retentionDays} 天
                            </option>
                          ))}
                        </select>
                      </div>
                      {selected?.requiresSide && (
                        <div className="field">
                          <label htmlFor="traffic-side">核心互联方向</label>
                          <select
                            id="traffic-side"
                            value={filter.side}
                            onChange={(e) => update("side", e.target.value)}
                          >
                            <option value="a">A 端物理端口</option>
                            <option value="b">B 端物理端口</option>
                          </select>
                        </div>
                      )}
                      <div className="field">
                        <label htmlFor="traffic-from">开始时间（UTC）</label>
                        <Input
                          id="traffic-from"
                          type="datetime-local"
                          step="1"
                          required
                          value={filter.from}
                          onChange={(e) => update("from", e.target.value)}
                        />
                      </div>
                      <div className="field">
                        <label htmlFor="traffic-to">结束时间（UTC）</label>
                        <Input
                          id="traffic-to"
                          type="datetime-local"
                          step="1"
                          required
                          value={filter.to}
                          onChange={(e) => update("to", e.target.value)}
                        />
                      </div>
                      <div className="flex items-end">
                        <Button type="submit" disabled={queryLoading}>
                          <Search />
                          {queryLoading ? "正在查询…" : "查询归档"}
                        </Button>
                      </div>
                    </div>
                  </form>
                  <p className="mt-4 text-xs leading-6 text-muted-foreground">
                    {role === "member"
                      ? "成员历史按采样时的所属成员保留；物理端口仅查询当前账号获准的归属区间。"
                      : "历史统计使用采样时冻结的成员、交换机和站点归属，不按当前端口持有人重新标记。"}
                    核心互联两端分别统计，不能相加作为交换量。所有查询时间均为
                    UTC。
                  </p>
                  {catalog.notice && (
                    <p className="mt-2 text-xs leading-6 text-muted-foreground">
                      {catalog.notice}
                    </p>
                  )}
                </CardContent>
              </Card>
            )}
            <ErrorNotice message={queryError} />
            {queryLoading ? (
              <Loading />
            ) : (
              result && (
                <div className="space-y-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <h2 className="text-lg font-semibold break-all">
                      {resultLabel}
                      {result.side || result.coreSide
                        ? ` · ${(result.side || result.coreSide)!.toUpperCase()} 端`
                        : ""}
                    </h2>
                    <span className="text-xs text-muted-foreground">
                      只读归档报告
                    </span>
                  </div>
                  {applied !== JSON.stringify(filter) && (
                    <p className="notice text-xs">
                      筛选条件已更改；下方仍为上次查询结果，请点击“查询归档”应用新条件。
                    </p>
                  )}
                  {result.scopeNotice && (
                    <p className="notice text-xs">{result.scopeNotice}</p>
                  )}
                  <TrafficReport key={reportVersion} result={result} />
                </div>
              )
            )}
          </>
        )
      )}
    </>
  );
}
