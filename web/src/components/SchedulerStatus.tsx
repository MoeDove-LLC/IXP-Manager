import { Activity } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Loading, time } from "@/components/shared";
import {
  freshnessLabel,
  schedulerPhaseLabel,
  schedulerStatusLabel,
  type JobsObservation,
  type SnapshotFreshness,
} from "@/lib/scheduler";

export default function SchedulerStatus({
  observation,
  freshness,
  loading,
  error,
}: {
  observation: JobsObservation | null;
  freshness: SnapshotFreshness;
  loading: boolean;
  error: string;
}) {
  const scheduler = observation?.scheduler;
  const current = freshness === "current";
  const failed = scheduler?.status === "stalled" || scheduler?.status === "degraded";
  const statusLabel = scheduler ? schedulerStatusLabel(scheduler.status) : "未提供调度状态";
  const details = scheduler ? [
    ["启动时间", time(scheduler.startedAt)],
    ["最近完成一轮", scheduler.lastCompletedAt ? time(scheduler.lastCompletedAt) : "尚无完成记录"],
    ["本轮开始", scheduler.cycleStartedAt ? time(scheduler.cycleStartedAt) : "当前无运行中的轮次"],
    ...(scheduler.stoppedAt || ["stopping", "stopped"].includes(scheduler.status) ? [["停止时间", time(scheduler.stoppedAt)]] : []),
  ] : [];

  return (
    <section aria-labelledby="background-scheduler-title" className="mb-5">
      <Card className={current && failed ? "border-destructive/40" : undefined}>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CardTitle id="background-scheduler-title" className="flex items-center gap-2">
              <Activity className="size-5 text-primary" />
              后台调度
            </CardTitle>
            {scheduler && (
              <Badge variant={current ? failed ? "destructive" : scheduler.status === "running" ? "default" : "outline" : "outline"}>
                {current ? statusLabel : `上次：${statusLabel}`}
              </Badge>
            )}
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {!observation && loading ? <Loading /> : !observation ? (
            <p className="text-sm text-muted-foreground">{error ? "暂时无法读取后台调度状态，请使用页面刷新重试。" : "暂无后台调度观测。"}</p>
          ) : (
            <>
              {!current && (
                <p role="status" className="rounded-lg border bg-muted/30 p-3 text-sm">
                  {freshnessLabel(freshness)}
                </p>
              )}
              <p className="text-xs text-muted-foreground">核对时间：{time(observation.checkedAt)}</p>
              {!scheduler ? (
                <p className="text-sm text-muted-foreground">服务端尚未提供调度观测。</p>
              ) : (
                <>
                  <p className="text-sm">
                    {current ? "当前阶段" : "上次阶段"}：{schedulerPhaseLabel(scheduler.currentPhase)}
                  </p>
                  {current && scheduler.status === "stalled" && (
                    <p className="text-sm leading-6 text-destructive">
                      最近一轮未完成。请按当前阶段检查服务日志。
                    </p>
                  )}
                  {current && scheduler.status === "not-started" && (
                    <p className="text-sm leading-6 text-muted-foreground">后台调度尚未启动，请检查服务启动配置。</p>
                  )}
                  {current && scheduler.status === "stopped" && (
                    <p className="text-sm leading-6 text-muted-foreground">后台调度已停止，提交的任务需要等待调度恢复。</p>
                  )}
                  {Array.isArray(scheduler.failedPhases) && scheduler.failedPhases.length > 0 && (
                    <p className={current ? "text-sm text-destructive" : "text-sm text-muted-foreground"}>
                      {current ? "失败阶段" : "上次失败阶段"}：{scheduler.failedPhases.map(schedulerPhaseLabel).join("、")}
                    </p>
                  )}
                  <dl className="grid gap-4 text-sm sm:grid-cols-2 xl:grid-cols-4">
                    {details.map(([label, value]) => (
                      <div className="min-w-0" key={label}>
                        <dt className="text-xs text-muted-foreground">{label}</dt>
                        <dd className="mt-1 break-words">{value}</dd>
                      </div>
                    ))}
                  </dl>
                  {scheduler.stallAfterSeconds > 0 && (
                    <p className="text-xs text-muted-foreground">服务端停滞判定阈值：{scheduler.stallAfterSeconds} 秒。</p>
                  )}
                </>
              )}
            </>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
