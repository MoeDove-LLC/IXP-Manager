import { useEffect, useState } from "react";
import { Activity, Download, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
import TrafficChart, {
  rate,
  type TrafficPoint,
} from "@/components/TrafficChart";
type Port = {
  portId: string;
  name: string;
  memberIds: string[];
  observedAt?: string;
  inOctets?: string;
  outOctets?: string;
  inBps?: number | null;
  outBps?: number | null;
  operStatus: string;
  status: "fresh" | "stale" | "unknown";
  counterReset?: boolean;
  counterWrapped?: boolean;
};
type Peer = {
  peerId: string;
  routerId: string;
  name: string;
  address: string;
  asn: number;
  memberId: string;
  state: string;
  receivedPrefixes?: number;
  observedAt?: string;
  status: "fresh" | "stale" | "unknown";
};
type Summary = {
  observedAt: string;
  staleAfterSeconds: number;
  ports: Port[];
  sessions: Peer[];
};
const freshness = { fresh: "采样正常", stale: "采样过期", unknown: "尚未采样" };
const operational: Record<string, string> = {
  up: "链路在线",
  down: "链路离线",
  unknown: "状态未知",
};
export default function Monitoring({ role }: { role: string }) {
  const showCounters = role !== "member";
  const [summary, setSummary] = useState<Summary | null>(null),
    [error, setError] = useState(""),
    [refreshing, setRefreshing] = useState(false),
    [selected, setSelected] = useState(""),
    [hours, setHours] = useState("1"),
    [points, setPoints] = useState<TrafficPoint[]>([]),
    [seriesError, setSeriesError] = useState(""),
    [seriesLoading, setSeriesLoading] = useState(false),
    [showAll, setShowAll] = useState(false);
  const load = async () => {
    setRefreshing(true);
    setError("");
    try {
      const data = await api<Summary>("/monitoring/summary");
      setSummary(data);
      setSelected((id) =>
        data.ports.some((p) => p.portId === id)
          ? id
          : (data.ports[0]?.portId ?? ""),
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setRefreshing(false);
    }
  };
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 30000);
    return () => clearInterval(timer);
  }, []);
  const to = summary ? Date.parse(summary.observedAt) : Date.now(),
    from = to - Number(hours) * 60 * 60 * 1000;
  useEffect(() => {
    if (!selected || !summary) return;
    const controller = new AbortController();
    setSeriesLoading(true);
    setSeriesError("");
    setShowAll(false);
    const q = new URLSearchParams({
      portId: selected,
      from: new Date(from).toISOString(),
      to: new Date(to).toISOString(),
    });
    api<{ portId: string; points: TrafficPoint[] }>(`/monitoring/series?${q}`, {
      signal: controller.signal,
    })
      .then((data) => setPoints(data.points))
      .catch((e) => {
        if (!controller.signal.aborted) setSeriesError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setSeriesLoading(false);
      });
    return () => controller.abort();
  }, [selected, hours, summary?.observedAt]);
  const current = summary?.ports.find((p) => p.portId === selected),
    fresh = summary?.ports.filter((p) => p.status === "fresh") ?? [],
    unknown = summary?.ports.filter((p) => p.status === "unknown").length ?? 0,
    stale = summary?.ports.filter((p) => p.status === "stale").length ?? 0;
  const exportSeries = () => {
    const rows = [
      [
        "observedAt",
        ...(showCounters ? ["inOctets", "outOctets"] : []),
        "inBps",
        "outBps",
        "operStatus",
        "counterReset",
        "counterWrapped",
      ],
      ...points.map((p) => [
        p.observedAt,
        ...(showCounters ? [p.inOctets ?? "", p.outOctets ?? ""] : []),
        p.inBps ?? "",
        p.outBps ?? "",
        p.operStatus,
        !!p.counterReset,
        !!p.counterWrapped,
      ]),
    ];
    const content = rows
        .map((row) =>
          row.map((v) => `"${String(v).replace(/"/g, '""')}"`).join(","),
        )
        .join("\r\n"),
      url = URL.createObjectURL(
        new Blob(["\ufeff", content], { type: "text/csv;charset=utf-8" }),
      ),
      link = document.createElement("a");
    link.href = url;
    link.download = `traffic-${selected}.csv`;
    link.click();
    URL.revokeObjectURL(url);
  };
  return (
    <>
      <Heading
        title="网络监测"
        description="查看实际接口采样与 BGP 状态，保留未知、过期和计数器重置的信息。"
      >
        <Button
          variant="outline"
          disabled={refreshing}
          onClick={() => void load()}
        >
          <RefreshCw className={refreshing ? "animate-spin" : ""} />
          刷新
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {error && summary && (
        <p className="notice my-4">
          更新失败，以下为上次成功读取的数据：{time(summary.observedAt)}。
        </p>
      )}
      {!summary ? (
        !error && <Loading />
      ) : (
        <>
          <div className="stat-grid">
            {[
              {
                label: "已配置监测端口",
                value: summary.ports.length,
                note: "当前账号可访问的物理端口",
              },
              {
                label: "新鲜采样",
                value: fresh.length,
                note: `最近 ${summary.staleAfterSeconds} 秒内采样`,
              },
              { label: "采样过期", value: stale, note: "检查采集器与管理网络" },
              {
                label: "尚未采样",
                value: unknown,
                note: "等待第一份计数与状态数据",
              },
            ].map((stat) => (
              <Card key={stat.label} className="stat-card">
                <p className="text-xs text-muted-foreground">{stat.label}</p>
                <p className="stat-value mt-3">{stat.value}</p>
                <p className="mt-2 text-xs text-muted-foreground">
                  {stat.note}
                </p>
              </Card>
            ))}
          </div>
          <Card className="mt-6">
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <CardTitle className="flex items-center gap-2">
                  <Activity className="size-4 text-primary" />
                  接口流量
                </CardTitle>
                <div className="flex flex-wrap gap-2">
                  <select
                    aria-label="监测端口"
                    className="!w-auto max-w-xs !text-xs"
                    value={selected}
                    onChange={(e) => setSelected(e.target.value)}
                  >
                    {!summary.ports.length && (
                      <option value="">没有可访问端口</option>
                    )}
                    {summary.ports.map((p) => (
                      <option key={p.portId} value={p.portId}>
                        {p.name} · {p.portId.slice(0, 8)}
                      </option>
                    ))}
                  </select>
                  <select
                    aria-label="监测时间范围"
                    className="!w-auto !text-xs"
                    value={hours}
                    onChange={(e) => setHours(e.target.value)}
                  >
                    <option value="1">最近 1 小时</option>
                    <option value="6">最近 6 小时</option>
                    <option value="24">最近 24 小时</option>
                  </select>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              {current && (
                <div className="mb-4 flex flex-wrap items-center gap-3 text-xs">
                  <Badge
                    variant={
                      current.status === "fresh"
                        ? "default"
                        : current.status === "stale"
                          ? "destructive"
                          : "outline"
                    }
                  >
                    {freshness[current.status]}
                  </Badge>
                  <span className="text-muted-foreground">
                    {current.status === "stale" ? "上次状态：" : ""}
                    {operational[current.operStatus] ?? current.operStatus}
                  </span>
                  <span className="text-muted-foreground">
                    最后采样：{time(current.observedAt)}
                  </span>
                  <span>
                    入口 {rate(current.inBps)} · 出口 {rate(current.outBps)}
                  </span>
                </div>
              )}
              <ErrorNotice message={seriesError} />
              {!selected ? (
                <Empty
                  title="还没有监测端口"
                  description="添加物理端口及成员接入后，由采集器上报接口数据。"
                />
              ) : seriesLoading ? (
                <Loading />
              ) : seriesError ? null : !points.length ? (
                <Empty
                  title="所选时段没有采样"
                  description="采集器上报后，这里会显示真实流量曲线。未采样不会显示为零流量。"
                />
              ) : (
                <>
                  <TrafficChart points={points} from={from} to={to} />
                  {points.every((p) => p.inBps == null && p.outBps == null) && (
                    <p className="notice mt-4">
                      已收到计数器值，尚无可计算速率的有效连续采样。重置后的首个采样保留为空。
                    </p>
                  )}
                  <div className="mt-5 flex items-center justify-between border-t pt-4">
                    <span className="text-xs text-muted-foreground">
                      {points.length} 个真实采样点 ·{" "}
                      {showCounters
                        ? "计数器单位为字节"
                        : "成员视图仅展示速率，不展示累计计数"}
                    </span>
                    <Button variant="outline" size="sm" onClick={exportSeries}>
                      <Download />
                      导出采样
                    </Button>
                  </div>
                  <details className="mt-4 rounded-lg border">
                    <summary className="cursor-pointer px-4 py-3 text-xs font-medium">
                      查看采样明细
                    </summary>
                    <div className="overflow-x-auto">
                      <table>
                        <thead>
                          <tr>
                            <th>采样时间</th>
                            <th>入口速率</th>
                            <th>出口速率</th>
                            {showCounters && (
                              <>
                                <th>入口计数</th>
                                <th>出口计数</th>
                              </>
                            )}
                            <th>计数器事件</th>
                          </tr>
                        </thead>
                        <tbody>
                          {[...points]
                            .reverse()
                            .slice(0, showAll ? points.length : 20)
                            .map((p, i) => (
                              <tr key={i}>
                                <td className="text-xs">
                                  {time(p.observedAt)}
                                </td>
                                <td>{rate(p.inBps)}</td>
                                <td>{rate(p.outBps)}</td>
                                {showCounters && (
                                  <>
                                    <td className="mono text-xs">
                                      {p.inOctets ?? "—"}
                                    </td>
                                    <td className="mono text-xs">
                                      {p.outOctets ?? "—"}
                                    </td>
                                  </>
                                )}
                                <td>
                                  {p.counterReset
                                    ? "重置"
                                    : p.counterWrapped
                                      ? "回绕"
                                      : "—"}
                                </td>
                              </tr>
                            ))}
                        </tbody>
                      </table>
                    </div>
                    {points.length > 20 && !showAll && (
                      <Button
                        className="m-3"
                        size="sm"
                        variant="ghost"
                        onClick={() => setShowAll(true)}
                      >
                        显示全部 {points.length} 个采样
                      </Button>
                    )}
                  </details>
                </>
              )}
            </CardContent>
          </Card>
          <Card className="mt-6 overflow-hidden">
            <CardHeader>
              <CardTitle>BGP 会话观测</CardTitle>
              <p className="text-xs text-muted-foreground">
                采样时间与会话状态分开展示；过期的 Established
                状态不代表当前在线。
              </p>
            </CardHeader>
            {!summary.sessions.length ? (
              <Empty
                title="还没有可观测会话"
                description="登记 BGP 会话并配置状态采集后，可在这里检查邻居状态与前缀数量。"
              />
            ) : (
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>会话</th>
                      <th>邻居</th>
                      <th>状态</th>
                      <th>收到前缀</th>
                      <th>采样状态</th>
                      <th>最后观测</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.sessions.map((s) => (
                      <tr key={s.peerId}>
                        <td className="font-medium">{s.name}</td>
                        <td className="mono text-xs">
                          {s.address} · AS{s.asn}
                        </td>
                        <td>
                          <Badge
                            variant={
                              s.state === "Established" && s.status === "fresh"
                                ? "default"
                                : "outline"
                            }
                          >
                            {s.state === "unknown" ? "未知" : s.state}
                          </Badge>
                        </td>
                        <td>{s.receivedPrefixes ?? "—"}</td>
                        <td>
                          <Badge
                            variant={
                              s.status === "stale" ? "destructive" : "outline"
                            }
                          >
                            {freshness[s.status]}
                          </Badge>
                        </td>
                        <td className="text-xs text-muted-foreground">
                          {time(s.observedAt)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <p className="mt-4 text-xs text-muted-foreground">
            每 30 秒自动刷新 · 读取时间 {time(summary.observedAt)} ·
            历史保留最近 24 小时
          </p>
        </>
      )}
    </>
  );
}
