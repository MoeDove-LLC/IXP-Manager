import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, ArrowRight, RefreshCw } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import {
  checkedMonitoringHealth,
  monitoringApprovalLabel,
  monitoringHealthPath,
  monitoringProfileLink,
  monitoringPullLabel,
  type MonitoringHealthPage,
} from "@/lib/monitoring-health";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";

type PageState = {
  after: string;
  request: number;
  contextCurrent: () => boolean;
  status: "loading" | "ready" | "error";
  data: MonitoringHealthPage | null;
  error?: string;
};

export default function MonitoringConfigurationHealth({
  onProfile,
}: {
  onProfile: (profileId: string) => void;
}) {
  const [cursors, setCursors] = useState<string[]>([""]),
    [state, setState] = useState<PageState | null>(null);
  const after = cursors[cursors.length - 1];
  const sequence = useRef(0), pending = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    const request = ++sequence.current;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const contextCurrent = captureSessionContext();
    const current = () =>
      !controller.signal.aborted && request === sequence.current && contextCurrent();
    // No previous page or previous account data is shown while reloading.
    setState({ after, request, contextCurrent, status: "loading", data: null });
    try {
      const data = checkedMonitoringHealth(
        await api<MonitoringHealthPage>(monitoringHealthPath(after), {
          signal: controller.signal,
          cache: "no-store",
        }),
        after,
      );
      if (current()) setState({ after, request, contextCurrent, status: "ready", data });
    } catch (e) {
      if (!current()) return;
      setState({
        after,
        request,
        contextCurrent,
        status: "error",
        data: null,
        error: e instanceof APIError && e.status === 403
          ? "仅平台管理员可查看监控配置检查。"
          : message(e),
      });
    }
  }, [after]);

  useEffect(() => {
    void load();
    return () => { sequence.current++; pending.current?.abort(); };
  }, [load]);
  const contextCurrent = state?.contextCurrent() ?? true;
  useEffect(() => {
    if (state && state.after === after && state.request === sequence.current && !contextCurrent)
      void load();
  }, [state, after, contextCurrent, load]);

  const visible = state?.after === after && contextCurrent ? state : null;
  const loading = !visible || visible.status === "loading";
  const data = visible?.status === "ready" ? visible.data : null;
  const firstPage = () => {
    if (cursors.length === 1) void load();
    else setCursors([""]);
  };

  return (
    <section aria-labelledby="monitoring-configuration-health-title">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0 space-y-2">
              <CardTitle id="monitoring-configuration-health-title" className="flex items-center gap-2">
                <Activity className="size-5 text-primary" />
                监控配置检查
              </CardTitle>
              <CardDescription>Nagios 与 Smokeping 的审批状态和配置提供记录。</CardDescription>
            </div>
            <Button variant="outline" disabled={loading} onClick={() => void load()}>
              <RefreshCw className={loading ? "animate-spin" : ""} />
              刷新监控检查
            </Button>
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          <p className="notice text-sm leading-6">
            目标加载状态：未观测。下列拉取和人工下载时间仅记录服务端提供配置响应。
          </p>
          <ErrorNotice message={visible?.error ?? ""} />
          {loading ? <Loading /> : data && (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
                <span>核对时间：{time(data.checkedAt)}</span>
                <span>
                  近期拉取范围：{data.pullStaleAfterSeconds % 3600 === 0
                    ? `${data.pullStaleAfterSeconds / 3600} 小时`
                    : `${data.pullStaleAfterSeconds} 秒`}
                </span>
              </div>
              {!data.items.length ? (
                <Empty
                  title={after ? "本页没有可用规则" : "暂无监控配置规则"}
                  description={after
                    ? "规则列表可能已变化，可返回首页重新读取。"
                    : "在生成规则中配置 Nagios 或 Smokeping 后，可在这里检查审批和提供记录。"}
                  action={after ? "返回检查首页" : undefined}
                  onAction={firstPage}
                />
              ) : (
                <div className="space-y-4">
                  {data.items.map(item => (
                    <article key={item.profileId} data-profile-id={item.profileId} className="rounded-lg border p-4">
                      <div className="flex flex-wrap items-start justify-between gap-3">
                        <div className="min-w-0 space-y-2">
                          <h3 className="break-words text-sm font-semibold">{item.profileName}</h3>
                          <Badge variant="outline">{item.kind === "nagios" ? "Nagios" : "Smokeping"}</Badge>
                        </div>
                        <Button size="sm" variant="outline" asChild>
                          <a
                            href={monitoringProfileLink(item.profileId)}
                            aria-label={`查看规则 ${item.profileName}`}
                            onClick={() => onProfile(item.profileId)}
                          >
                            查看规则
                            <ArrowRight />
                          </a>
                        </Button>
                      </div>
                      <dl className="mt-4 grid gap-4 text-sm md:grid-cols-3">
                        <div className="min-w-0 space-y-2">
                          <dt className="font-medium">配置审批</dt>
                          <dd><Badge variant={item.approvalStatus === "current" ? "default" : item.approvalStatus === "unavailable" ? "destructive" : "outline"}>{monitoringApprovalLabel(item.approvalStatus)}</Badge></dd>
                          <dd className="break-words text-xs text-muted-foreground">版本生成：{item.generatedAt ? time(item.generatedAt) : "未提供"}</dd>
                          <dd className="text-xs text-muted-foreground">该版本目标条目：{item.targetCount ?? "未提供"}</dd>
                        </div>
                        <div className="min-w-0 space-y-2">
                          <dt className="font-medium">绑定规则的拉取</dt>
                          <dd><Badge variant="outline">{monitoringPullLabel(item.pullStatus)}</Badge></dd>
                          <dd className="break-words text-xs text-muted-foreground">最近记录：{item.lastPulledAt ? time(item.lastPulledAt) : "未记录"}</dd>
                        </div>
                        <div className="min-w-0 space-y-2">
                          <dt className="font-medium">工作人员人工下载</dt>
                          <dd className="break-words">{item.lastDownloadedAt ? time(item.lastDownloadedAt) : "未记录人工下载"}</dd>
                        </div>
                      </dl>
                    </article>
                  ))}
                </div>
              )}
            </>
          )}
          <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
            <p className="text-xs text-muted-foreground">
              第 {cursors.length} 页{data ? ` · 本页 ${data.items.length} 条` : ""}
            </p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" size="sm" disabled={loading || cursors.length === 1} onClick={() => setCursors(value => value.slice(0, -1))}>上一页</Button>
              <Button variant="outline" size="sm" disabled={loading || !data?.nextAfter || cursors.includes(data.nextAfter)} onClick={() => {
                if (data?.nextAfter) setCursors(value => [...value, data.nextAfter]);
              }}>下一页</Button>
              {after && <Button variant="ghost" size="sm" disabled={loading} onClick={firstPage}>返回检查首页</Button>}
            </div>
          </div>
        </CardContent>
      </Card>
    </section>
  );
}
