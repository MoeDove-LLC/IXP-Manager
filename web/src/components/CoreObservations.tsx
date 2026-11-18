import { useEffect, useState } from "react";
import { Activity, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import {
  coreCapacity,
  coreLayerLabels,
  corePortLabel,
  type CoreBundleRecord,
  type CoreInventory,
  type CoreObservations as Observation,
  type CoreSeries,
} from "@/lib/core";
import TrafficChart, { rate } from "@/components/TrafficChart";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  ErrorNotice,
  Loading,
  message,
  time,
} from "@/components/shared";

export default function CoreObservations({
  bundle,
  inventory,
}: {
  bundle: CoreBundleRecord;
  inventory: CoreInventory;
}) {
  const [clock, setClock] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setClock(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const [observations, setObservations] = useState<Observation | null>(null),
    [series, setSeries] = useState<CoreSeries | null>(null),
    [error, setError] = useState(""),
    [seriesError, setSeriesError] = useState(""),
    [loading, setLoading] = useState(true),
    [seriesLoading, setSeriesLoading] = useState(false),
    [side, setSide] = useState("a"),
    [hours, setHours] = useState("1"),
    [version, setVersion] = useState(0);
  useEffect(() => {
    setObservations(null);
    setError("");
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const read = async () => {
      setLoading(true);
      try {
        const data = await api<Observation>(
          `/core/bundles/${bundle.id}/observations`,
          { signal: controller.signal },
        );
        if (!controller.signal.aborted) {
          setObservations(data);
          setError("");
        }
      } catch (error) {
        if (!controller.signal.aborted) setError(message(error));
      } finally {
        if (!controller.signal.aborted) {
          setLoading(false);
          timer = setTimeout(() => void read(), 30000);
        }
      }
    };
    void read();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [bundle.id, bundle.bindingRevision, version]);
  useEffect(() => {
    setSeries(null);
    setSeriesError("");
    setSeriesLoading(true);
    const controller = new AbortController(),
      to = new Date(),
      from = new Date(to.getTime() - Number(hours) * 3600000);
    api<CoreSeries>(
      `/core/bundles/${bundle.id}/series?${new URLSearchParams({ side, from: from.toISOString(), to: to.toISOString() })}`,
      { signal: controller.signal },
    )
      .then((data) => {
        if (!controller.signal.aborted) setSeries(data);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setSeriesError(message(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setSeriesLoading(false);
      });
    return () => controller.abort();
  }, [bundle.id, bundle.bindingRevision, side, hours, version]);
  const sameBinding = observations?.bindingRevision === bundle.bindingRevision,
    sameSeriesBinding = series?.bindingRevision === bundle.bindingRevision;
  const expired = (at: string | null) =>
    !at ||
    !Number.isFinite(Date.parse(at)) ||
    clock - Date.parse(at) >= (observations?.staleAfterSeconds ?? 120) * 1000 ||
    Date.parse(at) > clock + 30000;
  const layerStatus = (status: string, at: string | null) =>
    ["unsupported", "not-applicable"].includes(status)
      ? status
      : error
        ? "unknown"
        : at && expired(at)
          ? "stale"
          : status;
  const livePort = (point: Observation["links"][number]["sideA"]) =>
    !error &&
    sameBinding &&
    point.status === "fresh" &&
    point.identityVerified &&
    !expired(point.observedAt);
  const layers = observations
    ? ([
        ["physical", "物理端口"],
        ["lacp", "LACP"],
        ["bfd", "BFD"],
        ["bgp", "核心 BGP"],
      ] as const)
    : [];
  return (
    <Card className="min-w-0">
      <CardHeader>
        <div className="flex flex-wrap justify-between gap-3">
          <CardTitle className="flex items-center gap-2">
            <Activity className="size-4 text-primary" />
            实际观测
          </CardTitle>
          <Button
            variant="outline"
            size="sm"
            disabled={loading || seriesLoading}
            onClick={() => setVersion((value) => value + 1)}
          >
            <RefreshCw />
            刷新观测
          </Button>
        </div>
        <p className="text-xs leading-6 text-muted-foreground">
          双端 ifOperStatus 不能证明 LACP、BFD、BGP
          或端到端转发。每层只展示对应观测，不将期望启用映射为在线。
        </p>
      </CardHeader>
      <CardContent>
        <div className="space-y-5">
          <ErrorNotice message={error} />
          {error && observations && (
            <p className="notice">
              刷新失败，当前状态降为未知，当前速率暂不展示。下面仅保留上次采样信息和已取得的历史曲线。
            </p>
          )}
          {loading && !observations ? (
            <Loading />
          ) : observations && !sameBinding ? (
            <p className="notice">
              库存绑定已变化，请刷新整个
              Bundle。此处不会把旧拓扑与新观测混在一起。
            </p>
          ) : observations ? (
            <>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                {layers.map(([key, label]) => {
                  const layer = observations[key],
                    status = layerStatus(layer.status, layer.observedAt);
                  return (
                    <div
                      key={key}
                      className="rounded-lg border p-3"
                      data-core-layer={key}
                    >
                      <h3 className="text-sm font-semibold">{label}</h3>
                      <Badge className="mt-2" variant="outline">
                        {coreLayerLabels[status] ?? status}
                      </Badge>
                      <p className="mt-2 text-[11px] leading-5 text-muted-foreground">
                        采样 {time(layer.observedAt)}
                        <br />
                        {layer.identityVerified
                          ? "采样时身份已核对"
                          : "尚无该层身份观测证明"}
                      </p>
                      <p className="mt-2 text-xs leading-6 text-muted-foreground">
                        {layer.message}
                      </p>
                    </div>
                  );
                })}
              </div>
              <p className="notice">
                库存期望：{observations.desired.enabled ? "启用" : "停用"}，
                {observations.desired.enabledLinks}/
                {observations.desired.totalLinks} 条链路期望启用。观测超过{" "}
                {observations.staleAfterSeconds}{" "}
                秒会过期；没有启用链路不代表互联正常。
              </p>
              <div className="grid gap-3 md:grid-cols-2">
                {(["sideA", "sideB"] as const).map((key, index) => {
                  const capacity = observations.capacity[key],
                    traffic = observations.traffic[key];
                  return (
                    <div key={key} className="rounded-lg border p-4">
                      <h3 className="font-semibold">{index ? "B" : "A"} 侧</h3>
                      <p className="mt-2 text-xs leading-6">
                        库存名义容量：{coreCapacity(capacity.totalMbps)}
                        <br />
                        期望启用容量：{coreCapacity(capacity.enabledMbps)}
                        <br />
                        已知速率：{capacity.knownPorts}/{capacity.totalPorts}{" "}
                        个端口
                      </p>
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        容量来自物理端口库存，不是实测吞吐。
                      </p>
                      <div className="mt-3 border-t pt-3 text-xs leading-6">
                        <p>
                          当前入口{" "}
                          {rate(
                            !error && !expired(traffic.sourceFrom)
                              ? traffic.inBps
                              : null,
                          )}{" "}
                          · 出口{" "}
                          {rate(
                            !error && !expired(traffic.sourceFrom)
                              ? traffic.outBps
                              : null,
                          )}
                        </p>
                        <p>
                          采样覆盖 {traffic.coveredPorts}/
                          {traffic.expectedPorts} 个期望端口
                        </p>
                        <p className="text-muted-foreground">
                          来源窗口 {time(traffic.sourceFrom)} 至{" "}
                          {time(traffic.sourceTo)}
                        </p>
                      </div>
                    </div>
                  );
                })}
              </div>
              <div className="overflow-x-auto rounded-lg border">
                <table>
                  <thead>
                    <tr>
                      <th>链路 / 期望</th>
                      <th>物理观测</th>
                      <th>A 端</th>
                      <th>B 端</th>
                    </tr>
                  </thead>
                  <tbody>
                    {observations.links.map((link, index) => (
                      <tr key={link.id}>
                        <td className="text-xs">
                          #
                          {bundle.links.findIndex(
                            (item) => item.id === link.id,
                          ) + 1 || index + 1}{" "}
                          · {link.enabled ? "启用" : "停用"}
                        </td>
                        <td>
                          <Badge variant="outline">
                            {coreLayerLabels[
                              layerStatus(
                                link.physical.status,
                                link.physical.observedAt,
                              )
                            ] ?? link.physical.status}
                          </Badge>
                        </td>
                        {(["sideA", "sideB"] as const).map((key) => {
                          const point = link[key],
                            current = livePort(point),
                            status = error
                              ? "unknown"
                              : point.observedAt && expired(point.observedAt)
                                ? "stale"
                                : point.status;
                          return (
                            <td key={key} className="text-xs leading-6">
                              <p>{corePortLabel(point.portId, inventory)}</p>
                              <p>
                                {coreLayerLabels[status] ?? status} ·{" "}
                                {current ? "oper" : "历史 oper"}{" "}
                                {point.operStatus} · admin {point.adminStatus}
                              </p>
                              <p>
                                {current ? "观测速率" : "历史速率"}{" "}
                                {coreCapacity(point.speedMbps)} · 当前入{" "}
                                {rate(current ? point.inBps : null)} / 出{" "}
                                {rate(current ? point.outBps : null)}
                              </p>
                              <p className="text-[10px] text-muted-foreground">
                                {time(point.observedAt)} ·{" "}
                                {point.identityVerified
                                  ? "采样时身份已核对"
                                  : "身份未确认"}
                              </p>
                              {point.counterReset && (
                                <p>计数器重置，速率可能缺失</p>
                              )}
                              {point.counterWrapped && <p>已报告计数器回绕</p>}
                              {point.errorCode && (
                                <p className="mono text-destructive">
                                  {point.errorCode}
                                </p>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          ) : null}
          <div className="border-t pt-5">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
              <h3 className="text-sm font-semibold">当前绑定的聚合流量</h3>
              <div className="flex gap-2">
                <select
                  className="!w-auto !text-xs"
                  aria-label="核心流量统计侧"
                  value={side}
                  onChange={(event) => setSide(event.target.value)}
                >
                  <option value="a">以 A 侧统计</option>
                  <option value="b">以 B 侧统计</option>
                </select>
                <select
                  className="!w-auto !text-xs"
                  aria-label="核心流量时间范围"
                  value={hours}
                  onChange={(event) => setHours(event.target.value)}
                >
                  <option value="1">最近 1 小时</option>
                  <option value="6">最近 6 小时</option>
                  <option value="24">最近 24 小时</option>
                </select>
              </div>
            </div>
            <ErrorNotice message={seriesError} />
            {seriesLoading ? (
              <Loading />
            ) : series && !sameSeriesBinding ? (
              <p className="notice">
                序列属于另一个库存绑定版本，请刷新 Bundle 后查看。
              </p>
            ) : series ? (
              <>
                <p className="mb-3 text-xs leading-6 text-muted-foreground">
                  A/B 两侧分别统计，不将同一流量的两端重复相加。每{" "}
                  {series.bucketSeconds}{" "}
                  秒桶按端口取最后一个实际速率，采样可能不同步，缺口保持未知。
                </p>
                {series.points.some(
                  (point) => point.inBps !== null || point.outBps !== null,
                ) ? (
                  <TrafficChart
                    points={series.points.map((point) => ({
                      ...point,
                      operStatus: "unknown",
                    }))}
                    from={Date.parse(series.from)}
                    to={Date.parse(series.to)}
                  />
                ) : (
                  <Empty
                    title="尚无完整的当前绑定速率"
                    description="可能尚未采集、计数器需要第二个样本，或部分端口缺失。这里不会补成 0 bps。"
                  />
                )}
                {series.points.length > 0 && (
                  <details className="mt-4 rounded border p-3">
                    <summary className="cursor-pointer text-xs font-medium">
                      查看最近样本覆盖（总计 {series.points.length}{" "}
                      个返回时间桶）
                    </summary>
                    <div className="mt-3 max-h-64 overflow-auto">
                      <table>
                        <thead>
                          <tr>
                            <th>时间桶</th>
                            <th>覆盖</th>
                            <th>源采样范围</th>
                          </tr>
                        </thead>
                        <tbody>
                          {series.points.slice(-60).map((point) => (
                            <tr key={point.observedAt}>
                              <td className="text-xs">
                                {time(point.observedAt)}
                              </td>
                              <td className="text-xs">
                                {point.coveredPorts}/{point.expectedPorts}
                              </td>
                              <td className="text-xs">
                                {time(point.sourceFrom)} —{" "}
                                {time(point.sourceTo)}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  </details>
                )}
              </>
            ) : null}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
