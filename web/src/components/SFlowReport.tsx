import { useState } from "react";
import { ArrowRight } from "lucide-react";
import { Empty } from "@/components/shared";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { utcTime } from "@/lib/traffic";
import { sampledNumber, type SFlowResult } from "@/lib/sflow";

export default function SFlowReport({
  result,
  staff,
}: {
  result: SFlowResult;
  staff: boolean;
}) {
  const [page, setPage] = useState(0),
    [qualityPage, setQualityPage] = useState(0),
    items = result.items ?? [],
    quality = result.quality ?? [],
    size = 25;
  const labels = (label: string | undefined, id: string | undefined) =>
    label || (staff && id ? id : "未公开或未归属");
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
        <Badge>sFlow 采样估算</Badge>
        <span>
          {utcTime(result.from)} — {utcTime(result.to)}
        </span>
        <span>返回 {items.length} 条有向桶记录</span>
      </div>
      {result.alignmentNotice && (
        <p className="text-xs text-muted-foreground">
          {result.alignmentNotice}
        </p>
      )}
      {result.truncated && (
        <p className="notice">
          结果已截断，请缩小时间或对象范围。此页不能代表完整查询总量。
        </p>
      )}
      <Card>
        <CardHeader>
          <CardTitle>有向采样记录</CardTitle>
        </CardHeader>
        <CardContent>
          {!items.length ? (
            <Empty
              title="所选范围没有采样记录"
              description="没有采到样本不能解释为零流量，也不能说明双方没有连接。采样估算与物理计数器统计分别使用。"
            />
          ) : (
            <div className="overflow-x-auto">
              <table>
                <thead>
                  <tr>
                    <th>UTC 时间桶 / 地址族</th>
                    <th>来源 → 目的</th>
                    <th>估算字节 / 包</th>
                    <th>样本证据</th>
                  </tr>
                </thead>
                <tbody>
                  {items
                    .slice(page * size, (page + 1) * size)
                    .map((item, index) => (
                      <tr key={`${item.start}:${page}:${index}`}>
                        <td>
                          <p className="whitespace-nowrap">
                            {utcTime(item.start)}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {item.family === 4
                              ? "IPv4"
                              : item.family === 6
                                ? "IPv6"
                                : "地址族未判定"}
                            {item.vlanId ? ` · VLAN ${item.vlanId}` : ""}
                          </p>
                          {staff && item.exporterId && (
                            <p className="mt-1 break-all text-xs text-muted-foreground">
                              导出器 {item.exporterId}
                            </p>
                          )}
                        </td>
                        <td>
                          <div className="flex min-w-56 items-start gap-2">
                            <div className="min-w-0 flex-1">
                            <p className="whitespace-normal break-all">
                                {labels(
                                  item.sourceMemberLabel,
                                  item.sourceMemberId,
                                )}
                              </p>
                              {item.sourceConnectionLabel && (
                                <p className="mt-1 text-xs text-muted-foreground">
                                  {item.sourceConnectionLabel}
                                </p>
                              )}
                            </div>
                            <ArrowRight className="mt-1 size-4 shrink-0 text-muted-foreground" />
                            <div className="min-w-0 flex-1">
                            <p className="whitespace-normal break-all">
                                {labels(
                                  item.destinationMemberLabel,
                                  item.destinationMemberId,
                                )}
                              </p>
                              {item.destinationConnectionLabel && (
                                <p className="mt-1 text-xs text-muted-foreground">
                                  {item.destinationConnectionLabel}
                                </p>
                              )}
                            </div>
                          </div>
                          {item.unattributedReason && (
                            <p className="mt-2 text-xs text-muted-foreground">
                              归属限制：{item.unattributedReason}
                            </p>
                          )}
                        </td>
                        <td>
                          <p
                            className="whitespace-nowrap font-mono"
                            data-testid="sflow-estimated-bytes"
                          >
                            {sampledNumber(item.estimatedBytes, "B")}（估算）
                          </p>
                          <p className="mt-1 whitespace-nowrap font-mono text-xs text-muted-foreground">
                            {sampledNumber(item.estimatedPackets, "包")}（估算）
                          </p>
                        </td>
                        <td>
                          <p className="whitespace-nowrap">
                            {sampledNumber(item.sampleCount, "份样本")}
                          </p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            TCP/179 活动样本：
                            {sampledNumber(item.tcp179Samples)}
                          </p>
                          <details className="mt-2 text-xs">
                            <summary className="cursor-pointer text-primary">
                              接收时间
                            </summary>
                            <p className="mt-2 whitespace-nowrap">
                              首次 {utcTime(item.firstReceivedAt)}
                            </p>
                            <p className="mt-1 whitespace-nowrap">
                              最近 {utcTime(item.lastReceivedAt)}
                            </p>
                          </details>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          )}
          {items.length > size && (
            <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground">
              <span>
                {page * size + 1}–{Math.min((page + 1) * size, items.length)} /{" "}
                {items.length}
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
                  disabled={(page + 1) * size >= items.length}
                  onClick={() => setPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          )}
          <p className="mt-4 text-xs leading-6 text-muted-foreground">
            每行保留采样方向与时间桶，不把不同来源的估算相加，也不与物理计数器累计相加。TCP/179
            样本仅是采样到的活动证据，不证明 BGP
            Established；没有此样本不证明会话离线。
          </p>
        </CardContent>
      </Card>
      {staff && quality.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>整 UTC 日的导出器质量</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="overflow-x-auto">
              <table>
                <thead>
                  <tr>
                    <th>UTC 日 / 导出器</th>
                    <th>数据报</th>
                    <th>序号缺失 / 重复 / 乱序</th>
                    <th>其他计数</th>
                  </tr>
                </thead>
                <tbody>
                  {quality
                    .slice(qualityPage * size, (qualityPage + 1) * size)
                    .map((row, i) => (
                      <tr key={`${row.start}:${i}`}>
                        <td>
                          {utcTime(row.start)}
                          <p className="text-xs text-muted-foreground">
                            {row.exporterId}
                          </p>
                        </td>
                        <td>{sampledNumber(row.datagrams)}</td>
                        <td>
                          {sampledNumber(row.missingDatagrams)} /{" "}
                          {sampledNumber(row.duplicateDatagrams)} /{" "}
                          {sampledNumber(row.unorderedDatagrams)}
                        </td>
                        <td className="text-xs">
                          重启信号 {sampledNumber(row.restartSignals)}
                          <br />
                          样本序号间断 {sampledNumber(row.sampleSequenceGaps)}
                          <br />
                          独立计数样本 {sampledNumber(row.counterSamples)}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
            {quality.length > size && (
              <div className="mt-4 flex flex-wrap items-center justify-between gap-3 text-xs text-muted-foreground">
                <span>
                  {qualityPage * size + 1}–
                  {Math.min((qualityPage + 1) * size, quality.length)} /{" "}
                  {quality.length} 条日期×导出器记录
                </span>
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={qualityPage === 0}
                    onClick={() => setQualityPage((p) => p - 1)}
                  >
                    上一页质量记录
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={(qualityPage + 1) * size >= quality.length}
                    onClick={() => setQualityPage((p) => p + 1)}
                  >
                    下一页质量记录
                  </Button>
                </div>
              </div>
            )}
            <p className="mt-3 text-xs text-muted-foreground">
              这些计数覆盖整 UTC
              日，不按查询的秒级边界裁剪。序号间断可能来自设备、UDP
              传输、本地过滤或上传缺失，不能直接视作网络丢包数，也不是网络流量覆盖率；UDP
              来源白名单不等于加密身份认证。
            </p>
          </CardContent>
        </Card>
      )}
      <Card>
        <CardContent className="grid gap-4 pt-5 text-xs sm:grid-cols-3">
          <div>
            <p className="text-muted-foreground">保留上限</p>
            <p className="mt-1">{result.retentionSeconds / 86400} 天</p>
          </div>
          <div>
            <p className="text-muted-foreground">最早保留采样</p>
            <p className="mt-1">{utcTime(result.oldestRetainedAt)}</p>
          </div>
          <div>
            <p className="text-muted-foreground">最近保留采样</p>
            <p className="mt-1">{utcTime(result.newestRetainedAt)}</p>
          </div>
        </CardContent>
      </Card>
      {!!result.notices?.length && (
        <div className="notice space-y-1">
          {result.notices.map((notice, i) => (
            <p key={i}>{notice}</p>
          ))}
        </div>
      )}
    </div>
  );
}
