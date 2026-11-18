import { useEffect, useState } from "react";
import { Eye } from "lucide-react";
import { api } from "@/lib/api";
import type { Row } from "@/lib/resources";
import {
  atlasStatuses,
  rtt,
  type AtlasRun,
  type AtlasResults as Results,
  type AtlasResult,
} from "@/lib/atlas";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Loading,
  message,
  time,
} from "@/components/shared";

export default function AtlasResults({
  run,
  members,
  reload,
}: {
  run: AtlasRun;
  members: Row[];
  reload: number;
}) {
  const [direction, setDirection] = useState(""),
    [probe, setProbe] = useState(""),
    [data, setData] = useState<Results | null>(null),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(""),
    [selected, setSelected] = useState<AtlasResult | null>(null),
    [visible, setVisible] = useState(50);
  const memberName = (id: string) =>
      String(members.find((member) => member.id === id)?.name ?? id),
    measurements = run.items ?? [];
  const measurementRevision = measurements
    .map(
      (item) =>
        `${item.id}:${item.status}:${item.resultCount}:${item.lastSyncedAt ?? ""}`,
    )
    .join("|");
  useEffect(() => {
    setSelected(null);
    setData(null);
    setError("");
    setVisible(50);
    if (run.pruned) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    api<Results>(
      `/atlas/runs/${run.id}/results${direction ? `?${new URLSearchParams({ measurementId: direction })}` : ""}`,
      { signal: controller.signal },
    )
      .then((result) => {
        if (!controller.signal.aborted) setData(result);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(message(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [run.id, run.pruned, run.hasResults, run.lastSyncedAt, direction, reload]);
  const matrixMembers = [
      ...new Set(
        measurements.flatMap((item) => [
          item.sourceMemberId,
          item.targetMemberId,
        ]),
      ),
    ],
    rows = (data?.items ?? []).filter(
      (item) => !probe || String(item.probeId) === probe,
    ),
    probeIDs = [
      ...new Set((data?.items ?? []).map((item) => item.probeId)),
    ].sort((a, b) => a - b);
  return (
    <Card className="min-w-0">
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle>测量结果</CardTitle>
          <Badge variant="outline">
            {atlasStatuses[run.status] ?? run.status}
          </Badge>
        </div>
        <p className="text-xs leading-6 text-muted-foreground">
          完成、运行中等为任务状态；缺少结果、超时或路径地址命中都不代表 BGP
          会话健康。实际扣费未由结果接口报告。
        </p>
      </CardHeader>
      <CardContent>
        <div className="space-y-4">
          {run.mode === "matrix" && measurements.length > 0 && (
            <>
              <div className="overflow-x-auto rounded-lg border">
                <table aria-label="Atlas 有向测量矩阵">
                  <thead>
                    <tr>
                      <th>来源 ↓ / 目标 →</th>
                      {matrixMembers.map((id) => (
                        <th key={id} className="min-w-32">
                          {memberName(id)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {matrixMembers.map((source) => (
                      <tr key={source}>
                        <th>{memberName(source)}</th>
                        {matrixMembers.map((target) => {
                          const item = measurements.find(
                            (row) =>
                              row.sourceMemberId === source &&
                              row.targetMemberId === target,
                          );
                          return (
                            <td key={target}>
                              {source === target ? (
                                <span className="text-muted-foreground">—</span>
                              ) : item ? (
                                <Button
                                  variant={
                                    direction === item.id
                                      ? "secondary"
                                      : "ghost"
                                  }
                                  size="sm"
                                  className="h-auto min-h-10 flex-col items-start whitespace-normal text-left"
                                  aria-label={`查看 ${memberName(source)} 到 ${memberName(target)} 的结果`}
                                  onClick={() => setDirection(item.id)}
                                >
                                  <span>
                                    {atlasStatuses[item.status] ?? item.status}
                                  </span>
                                  <span className="mt-1 text-[10px] text-muted-foreground">
                                    {run.pruned
                                      ? "结果已清理"
                                      : item.hasResults
                                        ? `${item.resultCount} 条样本`
                                        : "尚无结果"}
                                  </span>
                                </Button>
                              ) : (
                                <span className="text-xs text-muted-foreground">
                                  未提供方向记录
                                </span>
                              )}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="text-xs text-muted-foreground">
                两个方向分别保留各自状态与样本，不从单向结果推断对称性或连通正常。
              </p>
            </>
          )}
          <div className="flex flex-wrap gap-3">
            <div className="field min-w-0 flex-1">
              <label htmlFor={`atlas-result-direction-${run.id}`}>
                结果方向
              </label>
              <select
                id={`atlas-result-direction-${run.id}`}
                value={direction}
                onChange={(event) => {
                  setDirection(event.target.value);
                  setProbe("");
                }}
              >
                <option value="">本次返回的所有方向</option>
                {measurements.map((item) => (
                  <option key={item.id} value={item.id}>
                    {memberName(item.sourceMemberId)} →{" "}
                    {memberName(item.targetMemberId)} · {item.targetAddress}
                  </option>
                ))}
              </select>
            </div>
            <div className="field min-w-0 sm:w-48">
              <label htmlFor={`atlas-result-probe-${run.id}`}>
                筛选返回样本
              </label>
              <select
                id={`atlas-result-probe-${run.id}`}
                value={probe}
                onChange={(event) => {
                  setProbe(event.target.value);
                  setVisible(50);
                }}
              >
                <option value="">所有返回探针</option>
                {probe && !probeIDs.includes(Number(probe)) && (
                  <option value={probe}>#{probe}（本次无样本）</option>
                )}
                {probeIDs.map((id) => (
                  <option key={id} value={id}>
                    探针 #{id}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <ErrorNotice message={error} />
          {run.pruned ? (
            <p className="notice">结果快照已清理，仅保留测量与审计元数据。</p>
          ) : loading ? (
            <Loading />
          ) : data ? (
            <>
              {data.truncated && (
                <p className="notice">
                  返回结果已达到上限，当前不是完整样本集。可以选择单个方向读取，不能把未返回样本当作零延迟或无丢包。
                </p>
              )}
              {!!data.snapshots?.length && (
                <p className="text-xs text-muted-foreground">
                  来自 {data.snapshots.length} 份缓存快照
                  {data.snapshot
                    ? ` · 获取于 ${time(data.snapshot.fetchedAt)}`
                    : ""}
                  ，当前返回 {data.items.length} 条样本。
                </p>
              )}
              {data.directions?.map((item) => (
                <div
                  key={item.id}
                  className="rounded-lg border p-3 text-xs leading-6"
                >
                  <p className="font-medium">
                    {memberName(item.sourceMemberId)} →{" "}
                    {memberName(item.targetMemberId)} ·{" "}
                    {atlasStatuses[item.status] ?? item.status}
                  </p>
                  <p className="text-muted-foreground">
                    {item.resultCount} 条实际样本 · 最近采样{" "}
                    {time(item.lastObservedAt)}
                  </p>
                  {item.currentVLANAddressesSeen?.length > 0 ? (
                    <p className="mono break-all">
                      样本命中当前 VLAN 地址：
                      {item.currentVLANAddressesSeen.join(", ")}
                    </p>
                  ) : (
                    <p className="text-muted-foreground">
                      本次结果未报告当前 VLAN 的精确地址命中。
                    </p>
                  )}
                </div>
              ))}
              {!data.items.length ? (
                <Empty
                  title={
                    data.hasResults ? "本次没有可展示样本" : "尚未取得结果"
                  }
                  description="结果缺失会保留为未知，不填充 0 ms、成功或无丢包。可由有权限的运维人员同步 provider 状态与结果。"
                />
              ) : !rows.length ? (
                <Empty
                  title="当前筛选没有样本"
                  description="调整当前探针筛选，不会发起新的外部测量。"
                />
              ) : (
                <>
                  <div className="overflow-x-auto rounded-lg border">
                    <table>
                      <thead>
                        <tr>
                          <th>采样时间</th>
                          <th>探针 / 观测目标</th>
                          <th>响应</th>
                          <th>平均 RTT</th>
                          <th>结果说明</th>
                          <th>详情</th>
                        </tr>
                      </thead>
                      <tbody>
                        {rows.slice(0, visible).map((item, index) => (
                          <tr
                            key={`${item.measurementId}-${item.probeId}-${item.timestamp}-${index}`}
                          >
                            <td className="whitespace-nowrap text-xs">
                              {time(item.timestamp)}
                            </td>
                            <td className="text-xs">
                              <span className="mono">#{item.probeId}</span>
                              <p className="mono mt-1 break-all text-muted-foreground">
                                {item.targetAddress || "目标未观测"}
                              </p>
                            </td>
                            <td className="text-xs">
                              {typeof item.received === "number" &&
                              typeof item.sent === "number"
                                ? `${item.received} / ${item.sent}`
                                : "未提供"}
                            </td>
                            <td className="text-xs">
                              {item.type === "ping"
                                ? rtt(item.avgRtt)
                                : "逐跳查看"}
                            </td>
                            <td className="max-w-xs break-words text-xs">
                              {item.error ||
                                (item.received === 0
                                  ? "没有有效响应"
                                  : item.type === "traceroute"
                                    ? Array.isArray(item.hops)
                                      ? `${item.hops.length} 个返回跳记录`
                                      : "未提供跳记录"
                                    : "返回了测量样本")}
                            </td>
                            <td>
                              <Button
                                variant="ghost"
                                size="icon"
                                aria-label={`查看探针 ${item.probeId} 的样本 ${index + 1}`}
                                onClick={() => setSelected(item)}
                              >
                                <Eye />
                              </Button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {visible < rows.length && (
                    <Button
                      variant="outline"
                      onClick={() => setVisible((value) => value + 50)}
                    >
                      显示更多已返回样本（{visible}/{rows.length}）
                    </Button>
                  )}
                </>
              )}
            </>
          ) : null}
        </div>
      </CardContent>
      <Dialog
        open={!!selected}
        onOpenChange={(value) => !value && setSelected(null)}
      >
        <DialogContent className="max-w-3xl">
          <DialogTitle className="text-lg font-semibold">
            探针 #{selected?.probeId} ·{" "}
            {selected?.type === "traceroute" ? "Traceroute" : "Ping"} 样本
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            采样 {time(selected?.timestamp)}
            。每条回复、超时和错误按返回数据保留，不合并为虚构的单一路径。
          </DialogDescription>
          {selected && (
            <div className="space-y-4">
              <div className="rounded-lg border p-3 text-xs leading-6">
                <p className="mono break-all">
                  来源地址：{selected.sourceAddress || "未提供"}
                </p>
                <p className="mono break-all">
                  观测目标地址：{selected.targetAddress || "目标未观测"}
                </p>
                <p>provider measurement ID：{selected.measurementId}</p>
              </div>
              {selected.error && <ErrorNotice message={selected.error} />}
              <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                {[
                  ["发送", selected.sent ?? "未提供"],
                  ["响应", selected.received ?? "未提供"],
                  ["最低 RTT", rtt(selected.minRtt)],
                  ["平均 RTT", rtt(selected.avgRtt)],
                  ["最高 RTT", rtt(selected.maxRtt)],
                ].map(([label, value]) => (
                  <div key={String(label)} className="rounded border p-3">
                    <p className="text-[11px] text-muted-foreground">{label}</p>
                    <p className="mt-1 text-sm">{value}</p>
                  </div>
                ))}
              </div>
              {selected.type === "traceroute" && (
                <div className="space-y-3">
                  {selected.hops?.length ? (
                    selected.hops.map((hop) => (
                      <div
                        key={hop.hop}
                        className="flex gap-3 rounded-lg border p-3"
                      >
                        <Badge variant="outline" className="h-fit shrink-0">
                          第 {hop.hop} 跳
                        </Badge>
                        <div className="min-w-0 space-y-2 text-xs leading-6">
                          {hop.error && (
                            <p className="text-destructive">
                              此跳报告探测错误：{hop.error}
                            </p>
                          )}
                          {hop.replies.length ? (
                            hop.replies.map((reply, index) => (
                              <p key={index} className="mono break-all">
                                {reply.timeout
                                  ? "超时"
                                  : reply.address || "未返回地址"}{" "}
                                ·{" "}
                                {reply.error || reply.timeout
                                  ? "未报告成功 RTT"
                                  : rtt(reply.rtt)}
                                {reply.error ? ` · 错误：${reply.error}` : ""}
                              </p>
                            ))
                          ) : (
                            <p className="text-muted-foreground">
                              此跳未提供回复。
                            </p>
                          )}
                        </div>
                      </div>
                    ))
                  ) : (
                    <p className="notice">
                      没有返回跳记录；不能据此推断目标可达。
                    </p>
                  )}
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </Card>
  );
}
