import { useEffect, useRef, useState, type FormEvent } from "react";
import { RefreshCw, Search, Settings2 } from "lucide-react";
import { api } from "@/lib/api";
import { useHashParameters } from "@/lib/hash-route";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
} from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import SFlowReport from "@/components/SFlowReport";
import SFlowReceivers from "@/components/SFlowReceivers";
import { fromUTCInput, utcInput, utcTime } from "@/lib/traffic";
import type { SFlowCatalog, SFlowResult, SFlowExporter } from "@/lib/sflow";

type Filter = {
  from: string;
  to: string;
  resolution: string;
  family: string;
  exporterId: string;
  vlanId: string;
  memberId: string;
};
export default function SFlow({ role }: { role: string }) {
  const route = useHashParameters("sflow");
  const section = route.get("tab") === "receivers" ? "receivers" : "query";
  const receiverId = route.get("receiverId") ?? "";
  const currentSection = useRef(section);
  currentSection.current = section;
  const [clock, setClock] = useState(() => performance.now());
  const received = useRef({ monotonic: performance.now(), server: Date.now() });
  const [catalog, setCatalog] = useState<SFlowCatalog | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>({
    from: "",
    to: "",
    resolution: "5m",
    family: "0",
    exporterId: "",
    vlanId: "",
    memberId: "",
  });
  const [result, setResult] = useState<SFlowResult | null>(null),
    [queryLoading, setQueryLoading] = useState(false),
    [queryError, setQueryError] = useState("");
  const [applied, setApplied] = useState(""),
    [version, setVersion] = useState(0);
  const catalogRequest = useRef<AbortController | null>(null),
    queryRequest = useRef<AbortController | null>(null);
  const run = async (next: Filter, data: SFlowCatalog) => {
    queryRequest.current?.abort();
    const controller = new AbortController();
    queryRequest.current = controller;
    setQueryLoading(true);
    setQueryError("");
    setResult(null);
    try {
      const from = fromUTCInput(next.from),
        to = fromUTCInput(next.to);
      if (Date.parse(from) >= Date.parse(to))
        throw new Error("结束时间必须晚于开始时间。");
      const params = new URLSearchParams({
        from,
        to,
        resolution: next.resolution,
        family: next.family,
      });
      if (next.vlanId) params.set("vlanId", next.vlanId);
      if (data.capabilities.staff && role !== "member") {
        if (next.exporterId) params.set("exporterId", next.exporterId);
        if (next.memberId) params.set("memberId", next.memberId);
      }
      const report = await api<SFlowResult>(`/sflow/query?${params}`, {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (report.sourceKind !== "sampled")
        throw new Error("返回的数据不是 sFlow 采样估算，无法显示为采样报告。");
      setResult(report);
      setApplied(JSON.stringify(next));
      setVersion((value) => value + 1);
    } catch (e) {
      if (!controller.signal.aborted) setQueryError(message(e));
    } finally {
      if (!controller.signal.aborted) setQueryLoading(false);
    }
  };
  const load = async (initial = false) => {
    catalogRequest.current?.abort();
    queryRequest.current?.abort();
    const controller = new AbortController();
    catalogRequest.current = controller;
    setLoading(true);
    setError("");
    setResult(null);
    setQueryLoading(false);
    setQueryError("");
    try {
      const data = await api<SFlowCatalog>("/sflow", {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (data.sourceKind !== "sampled")
        throw new Error("采样目录的数据来源不匹配。");
      data.selectors = {
        switches: data.selectors?.switches ?? [],
        vlans: data.selectors?.vlans ?? [],
        members: data.selectors?.members ?? [],
        connections: data.selectors?.connections ?? [],
      };
      data.exporters = data.exporters ?? [];
      const serverTime = data.observedAt ? Date.parse(data.observedAt) : NaN;
      received.current = {
        monotonic: performance.now(),
        server: Number.isFinite(serverTime) ? serverTime : Date.now(),
      };
      setCatalog(data);
      const end = new Date();
      const next = initial
        ? {
            ...filter,
            from: utcInput(new Date(end.getTime() - 3600000)),
            to: utcInput(end),
          }
        : {
            ...filter,
            vlanId: data.selectors.vlans.some(
              (item) => item.id === filter.vlanId,
            )
              ? filter.vlanId
              : "",
            exporterId: data.exporters?.some(
              (item) => item.id === filter.exporterId,
            )
              ? filter.exporterId
              : "",
            memberId: data.selectors.members.some(
              (item) => item.id === filter.memberId,
            )
              ? filter.memberId
              : "",
          };
      setFilter(next);
      if (currentSection.current === "query") void run(next, data);
    } catch (e) {
      if (!controller.signal.aborted) {
        setCatalog(null);
        setError(message(e));
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };
  useEffect(() => {
    void load(true);
    const tick = () => setClock(performance.now());
    const timer = setInterval(tick, 1000);
    window.addEventListener("focus", tick);
    document.addEventListener("visibilitychange", tick);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", tick);
      document.removeEventListener("visibilitychange", tick);
      catalogRequest.current?.abort();
      queryRequest.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (section === "receivers") {
      queryRequest.current?.abort();
      setQueryLoading(false);
    } else if (catalog && !loading && !result && !queryLoading)
      void run(filter, catalog);
  }, [section]);
  const update = (key: keyof Filter, value: string) =>
    setFilter((previous) => ({ ...previous, [key]: value }));
  const staff = role !== "member" && !!catalog?.capabilities.staff;
  const freshness = (exporter: SFlowExporter) => {
    if (exporter.receiveStatus !== "fresh")
      return exporter.receiveStatus ?? "unknown";
    const at = exporter.lastReceivedAt
      ? Date.parse(exporter.lastReceivedAt)
      : NaN;
    if (!Number.isFinite(at) || !catalog?.staleAfterSeconds) return "unknown";
    const age =
      Math.max(0, received.current.server - at) +
      Math.max(0, clock - received.current.monotonic);
    return age >= catalog.staleAfterSeconds * 1000 ? "stale" : "fresh";
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (catalog) void run(filter, catalog);
  };
  return (
    <>
      <Heading
        title="sFlow 采样"
        description="查看采样元数据形成的有向流量估算、样本与传输质量。"
      >
        <Button
          variant="outline"
          disabled={loading || queryLoading}
          onClick={() => void load()}
        >
          <RefreshCw className={loading ? "animate-spin" : ""} />
          刷新
        </Button>
      </Heading>
      <div className="mb-5 flex flex-wrap gap-2" aria-label="sFlow 功能">
        <Button
          variant={section === "query" ? "secondary" : "ghost"}
          aria-pressed={section === "query"}
          onClick={() => {
            location.hash = "sflow";
          }}
        >
          采样查询
        </Button>
        <Button
          variant={section === "receivers" ? "secondary" : "ghost"}
          aria-pressed={section === "receivers"}
          onClick={() => {
            location.hash = "sflow?tab=receivers";
          }}
        >
          成员接收器
        </Button>
      </div>
      <ErrorNotice message={error} />
      {loading ? (
        <Loading />
      ) : (
        catalog &&
        (section === "receivers" ? (
          <SFlowReceivers
            key={receiverId || "all"}
            targetId={receiverId}
            catalog={catalog}
            role={role}
            onChanged={() => load()}
          />
        ) : (
          <div className="space-y-5">
            <div className="notice">
              采样估算具有不确定性，不能与物理计数器总量相加，也不作为精确计费依据。
              {!catalog.capabilities.liveForwarding &&
                "当前未启用向成员接收器的实时转发。"}
            </div>
            <Card>
              <CardHeader>
                <CardTitle>查询采样记录 · UTC</CardTitle>
              </CardHeader>
              <CardContent>
                <form
                  onSubmit={submit}
                  className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3"
                >
                  {staff && (
                    <div className="field">
                      <label htmlFor="sflow-exporter">采样导出器</label>
                      <select
                        id="sflow-exporter"
                        value={filter.exporterId}
                        onChange={(e) => update("exporterId", e.target.value)}
                      >
                        <option value="">全部可访问来源</option>
                        {catalog.exporters?.map((item) => (
                          <option key={item.id} value={item.id}>
                            {item.name}
                          </option>
                        ))}
                      </select>
                    </div>
                  )}
                  <div className="field">
                    <label htmlFor="sflow-vlan">VLAN</label>
                    <select
                      id="sflow-vlan"
                      value={filter.vlanId}
                      onChange={(e) => update("vlanId", e.target.value)}
                    >
                      <option value="">全部可访问 VLAN</option>
                      {catalog.selectors.vlans.map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.label || item.name}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="field">
                    <label htmlFor="sflow-family">地址族</label>
                    <select
                      id="sflow-family"
                      value={filter.family}
                      onChange={(e) => update("family", e.target.value)}
                    >
                      <option value="0">全部（含未判定）</option>
                      <option value="4">IPv4</option>
                      <option value="6">IPv6</option>
                    </select>
                  </div>
                  {staff && (
                    <div className="field">
                      <label htmlFor="sflow-member">涉及成员</label>
                      <select
                        id="sflow-member"
                        value={filter.memberId}
                        onChange={(e) => update("memberId", e.target.value)}
                      >
                        <option value="">全部可访问成员</option>
                        {catalog.selectors.members.map((item) => (
                          <option key={item.id} value={item.id}>
                            {item.label || item.name}
                          </option>
                        ))}
                      </select>
                    </div>
                  )}
                  <div className="field">
                    <label htmlFor="sflow-resolution">时间桶</label>
                    <select
                      id="sflow-resolution"
                      value={filter.resolution}
                      onChange={(e) => update("resolution", e.target.value)}
                    >
                      <option value="5m">5 分钟</option>
                      <option value="1d">UTC 日</option>
                    </select>
                  </div>
                  <div className="field">
                    <label htmlFor="sflow-from">开始时间（UTC）</label>
                    <Input
                      id="sflow-from"
                      type="datetime-local"
                      step="1"
                      required
                      value={filter.from}
                      onChange={(e) => update("from", e.target.value)}
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="sflow-to">结束时间（UTC）</label>
                    <Input
                      id="sflow-to"
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
                      {queryLoading ? "正在查询…" : "查询采样"}
                    </Button>
                  </div>
                </form>
                <p className="mt-4 text-xs leading-6 text-muted-foreground">
                  时间以采集器接收窗口为准；结果按服务端返回的完整 UTC
                  时间桶展示。归属与对端标签由服务端授权投影提供，不按当前库存重新标记历史。
                </p>
              </CardContent>
            </Card>
            <ErrorNotice message={queryError} />
            {queryLoading ? (
              <Loading />
            ) : (
              result && (
                <>
                  {applied !== JSON.stringify(filter) && (
                    <p className="notice">
                      筛选已更改；下方仍为上次查询，请点击“查询采样”应用新条件。
                    </p>
                  )}
                  <SFlowReport key={version} result={result} staff={staff} />
                </>
              )
            )}
            {staff && (
              <Card>
                <CardHeader>
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <CardTitle>采样来源配置</CardTitle>
                    <Button asChild variant="outline" size="sm">
                      <a href="#sflow-exporters">
                        <Settings2 />
                        {catalog.capabilities.canManageExporters
                          ? "管理导出器"
                          : "查看导出器"}
                      </a>
                    </Button>
                  </div>
                </CardHeader>
                <CardContent>
                  {!catalog.exporters?.length ? (
                    <Empty
                      title="尚未登记导出器"
                      description="登记来源、交换机和 VLAN 范围后，使用独立采集器提交采样元数据。保存库存不会配置交换机或启动采集器。"
                    />
                  ) : (
                    <div className="overflow-x-auto">
                      <table>
                        <thead>
                          <tr>
                            <th>导出器</th>
                            <th>传输来源 / Agent</th>
                            <th>配置状态</th>
                            <th>最近收包证据</th>
                          </tr>
                        </thead>
                        <tbody>
                          {catalog.exporters.map((item) => (
                            <tr key={item.id}>
                              <td>
                                {item.name}
                                <p className="mt-1 text-xs text-muted-foreground">
                                  {catalog.selectors.switches.find(
                                    (sw) => sw.id === item.switchId,
                                  )?.name || item.switchId}
                                </p>
                              </td>
                              <td className="font-mono text-xs">
                                {item.sourceAddress} / {item.agentAddress}
                                <p className="mt-1 text-muted-foreground">
                                  子 Agent {item.subAgentId}
                                </p>
                              </td>
                              <td>
                                <Badge variant="outline">
                                  {item.enabled ? "配置已启用" : "配置已停用"}
                                </Badge>
                              </td>
                              <td>
                                <p className="text-xs">
                                  {item.lastReceivedAt
                                    ? utcTime(item.lastReceivedAt)
                                    : "尚无接收记录"}
                                </p>
                                <p className="mt-1 text-xs text-muted-foreground">
                                  {freshness(item) === "stale"
                                    ? "收包证据已过期"
                                    : freshness(item) === "fresh"
                                      ? "近期收到数据报"
                                      : "接收情况未知"}
                                </p>
                                {item.pendingRestart && (
                                  <p className="mt-1 text-xs text-muted-foreground">
                                    存在待确认的重启信号，不等于设备已重启
                                  </p>
                                )}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  )}
                  <p className="mt-3 text-xs text-muted-foreground">
                    收包时间与序号只描述接收证据，不证明设备、链路或 BGP
                    会话健康。新数据需刷新读取，旧证据会按服务端阈值过期。
                  </p>
                </CardContent>
              </Card>
            )}
            {staff && catalog.usage && (
              <p className="text-xs text-muted-foreground">
                采样归档：{catalog.usage.rows.toLocaleString()} /{" "}
                {catalog.usage.maxRows.toLocaleString()} 行 ·{" "}
                {(catalog.usage.bytes / 1048576).toFixed(2)} /{" "}
                {(catalog.usage.maxBytes / 1048576).toFixed(0)}{" "}
                MiB。保留期受实际采集与配额约束。
              </p>
            )}
            {!!catalog.notices?.length && (
              <div className="notice space-y-1">
                {catalog.notices.map((notice, index) => (
                  <p key={index}>{notice}</p>
                ))}
              </div>
            )}
          </div>
        ))
      )}
    </>
  );
}
