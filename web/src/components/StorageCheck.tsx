import { useEffect, useRef, useState } from "react";
import { HardDrive, LoaderCircle, Play } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, time } from "@/components/shared";
import {
  checkedStorageReport,
  storageCheckStages,
  storageFailureMessage,
  storageOutcomeLabel,
  storageSafeCode,
  type StorageCheckReport,
} from "@/lib/storage-check";

type CheckState = {
  contextCurrent: () => boolean;
  phase: "running" | "complete" | "error";
  report: StorageCheckReport | null;
  error?: string;
};

function requestFailure(error: unknown) {
  if (error instanceof APIError) {
    if (error.status === 409) return "已有存储检查正在进行，请稍后手动重试。";
    if (error.status === 403) return "仅平台管理员可运行数据目录检查。";
    if (error.status === 401) return "登录状态已变化，请重新登录。";
    if ([400, 405, 415].includes(error.status)) return "检查请求未被接受，请刷新页面后手动重试。";
    if (error.status === 429) return "请求过于频繁，请稍后手动重试。";
  }
  // Do not echo arbitrary transport or filesystem messages and private paths.
  return "未能取得有效的本次检查结果，请查看服务端日志或稍后手动重试。";
}

export default function StorageCheck() {
  const [state, setState] = useState<CheckState | null>(null);
  const mounted = useRef(false), sequence = useRef(0), inFlight = useRef(false),
    pending = useRef<AbortController | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      sequence.current++;
      pending.current?.abort();
      inFlight.current = false;
    };
  }, []);

  const contextCurrent = state?.contextCurrent() ?? true;
  useEffect(() => {
    if (state && !contextCurrent) {
      sequence.current++;
      pending.current?.abort();
      inFlight.current = false;
      setState(null);
    }
  }, [state, contextCurrent]);

  const run = async () => {
    if (!mounted.current || inFlight.current) return;
    const contextCurrent = captureSessionContext();
    const request = ++sequence.current;
    const controller = new AbortController();
    pending.current = controller;
    inFlight.current = true;
    const current = () => mounted.current && !controller.signal.aborted &&
      request === sequence.current && contextCurrent();
    // New runs never retain a previous success or failure as the current result.
    setState({ contextCurrent, phase: "running", report: null });
    try {
      const report = checkedStorageReport(await api<StorageCheckReport>("/system/storage-check", {
        method: "POST",
        body: JSON.stringify({}),
        signal: controller.signal,
        cache: "no-store",
      }));
      if (current()) setState({ contextCurrent, phase: "complete", report });
    } catch (error) {
      if (current()) setState({ contextCurrent, phase: "error", report: null, error: requestFailure(error) });
    } finally {
      if (request === sequence.current) inFlight.current = false;
    }
  };

  const visible = contextCurrent ? state : null;
  const busy = visible?.phase === "running";
  const report = visible?.report;
  const checks = new Map(report?.checks.map(check => [check.id, check]) ?? []);
  return (
    <section aria-labelledby="data-directory-check-title" className="mt-6 max-w-3xl">
      <Card>
        <CardHeader>
          <CardTitle id="data-directory-check-title" className="flex items-center gap-2">
            <HardDrive className="size-5 text-primary" />
            数据目录检查
          </CardTitle>
          <CardDescription>
            在当前数据目录使用临时文件，检查写入、同步、重命名、回读与清理。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Button type="button" variant="outline" disabled={busy} onClick={() => void run()}>
            {busy ? <LoaderCircle className="animate-spin" /> : <Play />}
            运行存储检查
          </Button>
          <ErrorNotice message={visible?.error ?? ""} />
          {busy ? (
            <p role="status" className="text-sm text-muted-foreground">正在检查当前数据目录…</p>
          ) : !visible ? (
            <p className="text-sm text-muted-foreground">尚未运行检查。</p>
          ) : report ? (
            <>
              <div className="flex flex-wrap items-center justify-between gap-3" role="status">
                <Badge variant={report.status === "ok" ? "default" : report.status === "failed" ? "destructive" : "outline"}>
                  {storageOutcomeLabel(report.status)}
                </Badge>
                <span className="text-xs text-muted-foreground">检查时间：{time(report.checkedAt)}</span>
              </div>
              {report.status === "canceled" && (
                <p className="text-sm text-muted-foreground">已保留返回的步骤结果，其他步骤未报告；需要时请手动重新运行。</p>
              )}
              <ol className="divide-y rounded-lg border">
                {storageCheckStages.map(([id, label]) => {
                  const check = checks.get(id), safeCode = storageSafeCode(check?.code);
                  return (
                    <li key={id} data-check-id={id} className="space-y-2 p-3">
                      <div className="flex items-start justify-between gap-3">
                        <span className="min-w-0 break-words text-sm">{label}</span>
                        <Badge variant={check?.status === "failed" ? "destructive" : "outline"}>
                          {check?.status === "passed" ? "通过" : check?.status === "failed" ? "未通过" : "未报告"}
                        </Badge>
                      </div>
                      {check?.status === "failed" && (
                        <p className="text-sm leading-6 text-destructive">{storageFailureMessage(check.code)}</p>
                      )}
                      {safeCode && <p className="break-all text-xs text-muted-foreground">代码：{safeCode}</p>}
                    </li>
                  );
                })}
              </ol>
            </>
          ) : null}
          <p className="text-xs leading-6 text-muted-foreground">
            结果仅反映本次数据目录操作，不评估磁盘容量或长期可靠性。
          </p>
        </CardContent>
      </Card>
    </section>
  );
}
