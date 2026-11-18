import { Empty } from "@/components/shared";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { utcTime } from "@/lib/traffic";
import {
  legacyDecimal,
  legacyEntityLabels,
  legacyPeriodLabels,
  legacySourceLabels,
  legacyNotice,
  type LegacyMeasure,
  type LegacyResult,
  type LegacyRow,
} from "@/lib/legacy-traffic";

const boundaries: Record<string, string> = {
  unknown: "窗口边界未知",
  "calendar-label": "原日历标签（不证明完整采样覆盖）",
  export: "原导出窗口",
  declared: "由历史证据声明",
};
const unknownReasons: Record<string, string> = {
  missing: "原值缺失",
  null: "原值为空",
  nan: "原样本 NaN",
  "ambiguous-local-time": "原时区时间不唯一",
};
function Measures({
  value,
  row,
  label,
}: {
  value: LegacyMeasure;
  row: LegacyRow;
  label: string;
}) {
  return (
    <div className="min-w-0 rounded-lg border p-3">
      <h4 className="mb-3 text-sm font-semibold">{label}</h4>
      <dl className="space-y-2 text-xs">
        {(
          [
            ["average", "平均速率"],
            ["maximum", "原报告最大值"],
            ["minimum", "最小速率"],
            ["last", "末次速率"],
            ["total", "原报告总量"],
          ] as const
        ).map(([key, name]) => (
          <div
            key={key}
            className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)] gap-2"
          >
            <dt className="text-muted-foreground">{name}</dt>
            <dd
              className="break-all text-right font-mono"
              data-legacy-value={key}
            >
              {legacyDecimal(value[key])}
              {value[key] !== null
                ? ` ${key === "total" ? row.totalUnit : row.rateUnit}`
                : ""}
              {value[key] == null && value.unknown?.[key] && (
                <span className="mt-1 block font-sans text-[11px] text-muted-foreground">
                  {unknownReasons[value.unknown[key]] ?? value.unknown[key]}
                </span>
              )}
            </dd>
          </div>
        ))}
        <div className="border-t pt-2">
          <dt className="text-muted-foreground">最大值时间（UTC）</dt>
          <dd className="mt-1 break-all">
            {value.maxAt ? utcTime(value.maxAt) : "未知"}
          </dd>
          {value.originalMaxAt && (
            <dd className="mt-1 break-all text-muted-foreground">
              原始时间文本：{value.originalMaxAt}
            </dd>
          )}
        </div>
      </dl>
    </div>
  );
}
export default function LegacyTrafficReport({
  result,
  onPage,
  busy,
}: {
  result: LegacyResult;
  onPage: (offset: number) => void;
  busy: boolean;
}) {
  if (!result.rows.length)
    return (
      <Card>
        <Empty
          title="此批次没有符合条件的历史记录"
          description="这里仅展示独立旧报表，不以实时计数器或 sFlow 补齐缺失数据。"
        />
      </Card>
    );
  return (
    <div className="space-y-4">
      {!!result.notices?.length && (
        <div className="notice space-y-1">
          {result.notices.map((notice, i) => (
            <p key={i}>{legacyNotice(notice)}</p>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
        <p>
          第 {result.offset + 1}–{result.offset + result.rows.length} 条 /{" "}
          {result.totalRows} 条 · 原值独立展示，不求和
        </p>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={busy || result.offset <= 0}
            onClick={() => onPage(Math.max(0, result.offset - result.limit))}
          >
            上一页
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={
              busy ||
              !result.truncated ||
              result.offset + result.rows.length >= result.totalRows
            }
            onClick={() => onPage(result.offset + result.limit)}
          >
            下一页
          </Button>
        </div>
      </div>
      {result.rows.map((row) => (
        <Card key={row.id} data-testid="legacy-traffic-row">
          <CardHeader>
            <div className="flex flex-wrap items-start justify-between gap-3">
              <CardTitle className="min-w-0 break-all text-base">
                {legacyEntityLabels[row.entityKind] ?? row.entityKind}{" "}
                {row.entityId}
                {row.peerEntityId ? ` → ${row.peerEntityId}` : ""}
              </CardTitle>
              <Badge variant="outline">
                {legacySourceLabels[row.sourceKind] ?? row.sourceKind}
              </Badge>
            </div>
            <p className="text-xs text-muted-foreground">
              原记录 {row.recordId} · {row.category}
              {row.family ? ` · IPv${row.family}` : ""} ·{" "}
              {row.staffOnly ? "仅工作人员可见" : "存在历史归属证据"}
            </p>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="rounded-lg bg-muted/30 p-3 text-xs leading-6">
              <p>
                原报告日期：{row.originalDay || "未提供"} ·{" "}
                {legacyPeriodLabels[row.window.period] ?? row.window.period}
              </p>
              <p>
                边界依据：
                {boundaries[row.window.boundaryQuality] ??
                  row.window.boundaryQuality}
              </p>
              <p>
                开始（UTC）：
                {row.window.start ? utcTime(row.window.start) : "未知"}
                <br />
                结束（UTC）：{row.window.end ? utcTime(row.window.end) : "未知"}
              </p>
              {row.window.nominalSeconds != null && (
                <p>
                  名义窗口：{row.window.nominalSeconds} 秒（不等于有效采样覆盖）
                </p>
              )}
              {row.window.evidence && (
                <p className="break-all">边界证据：{row.window.evidence}</p>
              )}
            </div>
            {row.maxSemantics === "legacy-sum-of-independent-maxima" && (
              <p className="notice">
                此最大值由多个独立接口图的最大值相加得到，不是同一时刻的整体峰值。
              </p>
            )}
            <div className="grid gap-3 sm:grid-cols-2">
              <Measures value={row.in} row={row} label="原报告入方向" />
              <Measures value={row.out} row={row} label="原报告出方向" />
            </div>
            {row.ownerEvidence && (
              <details className="rounded-lg border p-3 text-xs leading-6">
                <summary className="cursor-pointer font-medium">
                  历史归属证据
                </summary>
                <p className="mt-2 break-all">
                  成员 {row.ownerEvidence.memberId} · 修订{" "}
                  {row.ownerEvidence.memberRevision}
                </p>
                <p>
                  {utcTime(row.ownerEvidence.validFrom)} –{" "}
                  {utcTime(row.ownerEvidence.validTo)}
                </p>
                <p className="break-all">{row.ownerEvidence.description}</p>
                <p className="break-all font-mono">
                  SHA-256 {row.ownerEvidence.referenceSha256}
                </p>
              </details>
            )}
            {!!row.notices?.length && (
              <div className="space-y-1 text-xs leading-6 text-muted-foreground">
                {row.notices.map((notice, i) => (
                  <p key={i}>{legacyNotice(notice)}</p>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  );
}
