import { useState } from "react";
import { Activity, ArrowDownLeft, ArrowUpRight } from "lucide-react";
import TrafficChart, { rate } from "@/components/TrafficChart";
import { Empty } from "@/components/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  exactTotal,
  trafficMetrics,
  trafficResolutions,
  utcTime,
  type TrafficDirection,
  type TrafficResult,
} from "@/lib/traffic";

const coverage = (d: TrafficDirection) =>
  d.expectedMillis > 0
    ? d.coverage > 0 && d.coverage < 0.001
      ? "<0.1%"
      : `${(d.coverage * 100).toFixed(1)}%`
    : "未建立覆盖区间";
const duration = (ms: number) =>
  `${(ms / 1000).toLocaleString("zh-CN", { maximumFractionDigits: 3 })} 秒`;
const intervalCounts = (d: TrafficDirection) =>
  `桶内区间片段数：有效 ${d.validIntervals} · 重置 ${d.resetIntervals} · 回绕 ${d.wrapIntervals} · 间断 ${d.gapIntervals}${d.ambiguousIntervals ? ` · 回绕歧义 ${d.ambiguousIntervals}` : ""}`;

export default function TrafficReport({ result }: { result: TrafficResult }) {
  const [page, setPage] = useState(0);
  const buckets = result.buckets ?? [];
  const formatRate = (value: number | null | undefined) => {
    if (value == null) return "—";
    if (result.rateUnit === "bits/s")
      return value > 0 && value < 1
        ? `${value.toPrecision(3)} bps`
        : rate(value);
    const unit = result.rateUnit === "packets/s" ? "包/秒" : "次/秒";
    for (const [scale, prefix] of [
      [1e9, "G"],
      [1e6, "M"],
      [1e3, "K"],
    ] as const)
      if (value >= scale)
        return `${(value / scale).toFixed(2)} ${prefix}${unit}`;
    const number =
      value > 0 && value < 1
        ? value.toPrecision(3)
        : value.toLocaleString("zh-CN", { maximumFractionDigits: 3 });
    return `${number} ${unit}`;
  };
  const points = buckets.flatMap((bucket, index) => {
    const previous = buckets[index - 1];
    const gap =
      previous && Date.parse(previous.end) < Date.parse(bucket.start)
        ? [
            {
              observedAt: previous.end,
              inBps: null,
              outBps: null,
              operStatus: "unknown",
              counterReset: false,
              counterWrapped: false,
            },
          ]
        : [];
    return [
      ...gap,
      {
        observedAt: bucket.start,
        inBps: bucket.in.averageRate,
        outBps: bucket.out.averageRate,
        operStatus: "unknown",
        counterReset: bucket.in.resetIntervals + bucket.out.resetIntervals > 0,
        counterWrapped: bucket.in.wrapIntervals + bucket.out.wrapIntervals > 0,
      },
    ];
  });
  const hasRates = points.some((p) => p.inBps != null || p.outBps != null);
  const resolution = trafficResolutions[result.resolution] ?? result.resolution;
  const grouped = result.summary.peakMethod === "aligned-bucket-average";
  const peakSeconds = result.summary.peakResolutionSeconds;
  const peakWindow =
    peakSeconds > 0
      ? peakSeconds % 86400 === 0
        ? `${peakSeconds / 86400} 日`
        : peakSeconds % 3600 === 0
          ? `${peakSeconds / 3600} 小时`
          : peakSeconds % 60 === 0
            ? `${peakSeconds / 60} 分钟`
            : `${peakSeconds} 秒`
      : resolution;
  const peakLabel =
    result.summary.peakMethod === "observed-interval"
      ? "已观测区间峰值"
      : `${peakWindow}聚合均值的峰值`;
  const rows = [...buckets].reverse().slice(page * 50, page * 50 + 50);
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Badge>物理计数器差值</Badge>
        <Badge variant="outline">
          {trafficMetrics[result.metric] ?? result.metric}
        </Badge>
        <span>
          {utcTime(result.from)} — {utcTime(result.to)}
        </span>
      </div>
      {result.alignmentNotice && (
        <p className="text-xs text-muted-foreground">
          {result.alignmentNotice}
        </p>
      )}
      <div className="grid gap-4 md:grid-cols-2">
        {(["in", "out"] as const).map((direction) => {
          const d = result.summary[direction],
            Icon = direction === "in" ? ArrowDownLeft : ArrowUpRight;
          return (
            <Card key={direction}>
              <CardContent className="pt-5">
                <div className="mb-3 flex items-center gap-2 text-sm text-muted-foreground">
                  <Icon className="size-4" />
                  {direction === "in" ? "入口" : "出口"}有效区间累计
                </div>
                <p
                  className="break-all font-mono text-2xl font-semibold"
                  data-testid={`traffic-${direction}-total`}
                >
                  {exactTotal(d.total, result.totalUnit)}
                </p>
                {!!d.ambiguousIntervals && d.lowerBoundTotal != null && (
                  <div
                    className="notice mt-3 text-xs"
                    data-testid={`traffic-${direction}-lower-bound`}
                  >
                    <p className="break-all font-mono">
                      至少 {exactTotal(d.lowerBoundTotal, result.totalUnit)}
                      （未排除多次回绕）
                    </p>
                    <p className="mt-1">
                      成立条件是相同计数器运行周期，且区间内没有未观测的重置。下界已包含已知精确部分，不能与上方累计相加；歧义部分不计入精确覆盖率或速率。
                    </p>
                  </div>
                )}
                <div className="mt-4 grid grid-cols-2 gap-4 text-xs">
                  <div>
                    <p className="text-muted-foreground">
                      {grouped ? "完整覆盖桶平均速率" : "有效区间平均速率"}
                    </p>
                    <p className="mt-1 font-medium">
                      {formatRate(d.averageRate)}
                    </p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{peakLabel}</p>
                    <p className="mt-1 font-medium">{formatRate(d.maxRate)}</p>
                    <p className="mt-1 text-muted-foreground">
                      {d.maxAt ? utcTime(d.maxAt) : "无峰值记录"}
                    </p>
                  </div>
                </div>
                <div className="mt-4 border-t pt-3 text-xs text-muted-foreground">
                  <p>覆盖率 {coverage(d)}</p>
                  <p className="mt-1">
                    {result.summary.contributingPorts} /{" "}
                    {result.summary.expectedPorts} 个端口贡献记录
                    {result.summary.partial ? " · 覆盖不完整" : ""}
                  </p>
                  <p className="mt-1">{intervalCounts(d)}</p>
                </div>
              </CardContent>
            </Card>
          );
        })}
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Activity className="size-4 text-primary" />
            {resolution}平均速率 · UTC
          </CardTitle>
        </CardHeader>
        <CardContent>
          {hasRates ? (
            <TrafficChart
              points={points}
              from={Date.parse(result.from)}
              to={Date.parse(result.to)}
              timeZone="UTC"
              formatRate={formatRate}
              description={`物理计数器${resolution}平均速率，${points.length} 个时间桶；紫色入口、绿色出口，无有效区间留空。`}
              hint="指向图表查看时间桶均值；缺失区间留空，所有时间为 UTC。"
            />
          ) : (
            <Empty
              title="所选范围没有有效速率区间"
              description="缺少相邻有效计数、回绕次数不确定、聚合范围未形成完整覆盖桶，或记录超出保留期时，速率保持为空；这不代表流量为零。"
            />
          )}
          <p className="mt-3 text-xs leading-6 text-muted-foreground">
            曲线显示所选分辨率的均值，峰值按报告中的区间口径展示，不代表瞬时峰值。物理端口计数无法拆分为地址族、VLAN
            或成员对之间的流量。
          </p>
          <p className="mt-2 text-xs leading-6 text-muted-foreground">
            覆盖率按端口时长计算；单端口均值使用有效区间，多端口仅使用所有预期端口均完整覆盖的对齐桶。累计量保留已观测差值，不为缺失部分补零或外推。跨
            UTC 桶的区间按时长均匀分摊。
          </p>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>时间桶明细 · 最新在前</CardTitle>
        </CardHeader>
        <CardContent>
          {rows.length ? (
            <div className="overflow-x-auto">
              <table>
                <thead>
                  <tr>
                    <th>UTC 时间桶</th>
                    <th>入口累计 / 均值</th>
                    <th>出口累计 / 均值</th>
                    <th>覆盖与质量</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((bucket) => (
                    <tr key={bucket.start}>
                      <td>
                        <p className="whitespace-nowrap">
                          {utcTime(bucket.start)}
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          至 {utcTime(bucket.end)}
                        </p>
                      </td>
                      {(["in", "out"] as const).map((d) => (
                        <td key={d}>
                          <p className="font-mono">
                            {exactTotal(bucket[d].total, result.totalUnit)}
                          </p>
                          {!!bucket[d].ambiguousIntervals &&
                            bucket[d].lowerBoundTotal != null && (
                              <p className="mt-1 text-xs text-muted-foreground">
                                至少{" "}
                                {exactTotal(
                                  bucket[d].lowerBoundTotal,
                                  result.totalUnit,
                                )}{" "}
                                · 未排除多次回绕，已含精确部分
                              </p>
                            )}
                          <p className="mt-1 text-xs text-muted-foreground">
                            均值 {formatRate(bucket[d].averageRate)} · 峰值{" "}
                            {formatRate(bucket[d].maxRate)}
                          </p>
                        </td>
                      ))}
                      <td>
                        <p className="text-xs">
                          入口 {coverage(bucket.in)} · 出口{" "}
                          {coverage(bucket.out)}
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {bucket.contributingPorts} / {bucket.expectedPorts}{" "}
                          个端口贡献记录
                          {bucket.partial ? " · 部分窗口" : ""}
                          {bucket.uniformAllocation ? " · 跨桶按时长分摊" : ""}
                        </p>
                        <details className="mt-2 text-xs">
                          <summary className="cursor-pointer text-primary">
                            区间证据
                          </summary>
                          <div className="mt-2 space-y-2 text-muted-foreground">
                            {(["in", "out"] as const).map((d) => (
                              <div key={d}>
                                <p>
                                  {d === "in" ? "入口" : "出口"}：
                                  {intervalCounts(bucket[d])}
                                </p>
                                <p>
                                  覆盖 {duration(bucket[d].coveredMillis)} /
                                  预期 {duration(bucket[d].expectedMillis)}
                                  （端口时长累计）
                                </p>
                                <p>
                                  用于速率的{grouped ? "完整桶" : "有效区间"}
                                  时长 {duration(bucket[d].rateCoveredMillis)}
                                </p>
                              </div>
                            ))}
                          </div>
                        </details>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              title="没有时间桶"
              description="当前范围没有可展示的归档记录。"
            />
          )}
          {buckets.length > 50 && (
            <div className="mt-4 flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
              <span>
                {page * 50 + 1}–{Math.min((page + 1) * 50, buckets.length)} /{" "}
                {buckets.length} 个时间桶
              </span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={page === 0}
                  onClick={() => setPage((p) => p - 1)}
                >
                  上一页
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={(page + 1) * 50 >= buckets.length}
                  onClick={() => setPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardContent className="grid gap-4 pt-5 text-xs sm:grid-cols-2">
          <div>
            <p className="text-muted-foreground">归档开始</p>
            <p className="mt-1">{utcTime(result.archiveStartedAt)}</p>
          </div>
          <div>
            <p className="text-muted-foreground">此分辨率可用记录起点</p>
            <p className="mt-1">{utcTime(result.retainedFrom)}</p>
          </div>
          <div>
            <p className="text-muted-foreground">最近归档样本</p>
            <p className="mt-1">{utcTime(result.latestAt)}</p>
          </div>
          <div>
            <p className="text-muted-foreground">查询完整性</p>
            <p className="mt-1">
              {result.retentionLimited
                ? "查询起点早于保留范围，早期记录不可用"
                : "依实际有效区间统计；缺失区间不补零"}
            </p>
          </div>
        </CardContent>
      </Card>
      {result.notices?.length > 0 && (
        <div className="notice space-y-1 text-xs">
          {result.notices.map((notice, i) => (
            <p key={i}>{notice}</p>
          ))}
        </div>
      )}
    </div>
  );
}
