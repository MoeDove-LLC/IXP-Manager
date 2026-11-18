import { useEffect, useState } from "react";
import { Check, Clock3, Mail, Play, RefreshCw } from "lucide-react";
import { api, save } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
type Notice = {
  id: string;
  to: string;
  subject: string;
  status: string;
  attempts: number;
  createdAt: string;
  nextAttemptAt?: string;
  expiresAt?: string;
  lastError?: string;
};
type Outbox = { deliveryEnabled: boolean; from?: string; items: Notice[] };
type Job = {
  kind: string;
  label: string;
  status: string;
  nextRunAt: string;
  lastRunAt?: string;
  lastError?: string;
};
const labels: Record<string, string> = {
  pending: "等待发送",
  sending: "正在发送",
  sent: "已发送",
  failed: "发送失败",
  expired: "已过期",
  idle: "等待执行",
  running: "正在执行",
};
export default function Notifications() {
  const [outbox, setOutbox] = useState<Outbox | null>(null),
    [jobs, setJobs] = useState<Job[]>([]),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(""),
    [error, setError] = useState(""),
    [done, setDone] = useState("");
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const [mail, j] = await Promise.all([
        api<Outbox>("/notifications"),
        api<{ items: Job[] }>("/jobs"),
      ]);
      setOutbox(mail);
      setJobs(j.items);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 30000);
    return () => clearInterval(timer);
  }, []);
  const run = async (kind: string) => {
    setBusy(kind);
    setError("");
    setDone("");
    try {
      await save(`/jobs/${kind}/run`, {});
      setDone("任务已提交，将按后台执行状态更新结果。");
      await load();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  const retry = async (id: string) => {
    setBusy(id);
    setError("");
    setDone("");
    try {
      await save(`/notifications/${id}/retry`, {});
      setDone("失败邮件已重新排队。");
      await load();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  return (
    <>
      <Heading
        title="通知与后台任务"
        description="检查邮件投递准备状态与固定维护任务；敏感链接和邮件正文不在控制台展示。"
      >
        <Button
          variant="outline"
          disabled={loading}
          onClick={() => void load()}
        >
          <RefreshCw />
          刷新
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5 flex items-center gap-2">
          <Check className="size-4" />
          {done}
        </p>
      )}
      {!outbox ? (
        loading && <Loading />
      ) : (
        <>
          <div className="grid gap-5 lg:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Mail className="size-5 text-primary" />
                  邮件发送准备状态
                </CardTitle>
              </CardHeader>
              <CardContent>
                <Badge variant={outbox.deliveryEnabled ? "default" : "outline"}>
                  {outbox.deliveryEnabled ? "已启用邮件投递" : "未启用邮件投递"}
                </Badge>
                <p className="mt-4 text-sm leading-7 text-muted-foreground">
                  {outbox.deliveryEnabled
                    ? `发件地址：${outbox.from ?? "由服务端配置"}`
                    : "恢复邮件和成员邀请需要先由部署环境启用邮件传输。"}
                </p>
                <p className="mt-3 text-xs leading-6 text-muted-foreground">
                  SMTP
                  主机、凭据与传输安全参数由服务器的私有配置文件管理。本页面只显示准备状态，不读取或修改密码。
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Clock3 className="size-5 text-primary" />
                  固定维护任务
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                {!jobs.length ? (
                  <p className="text-sm text-muted-foreground">
                    暂无任务状态。
                  </p>
                ) : (
                  jobs.map((job) => (
                    <div key={job.kind} className="rounded-lg border p-3">
                      <div className="flex items-center justify-between gap-2">
                        <span className="text-sm font-medium">{job.label}</span>
                        <Badge variant="outline">
                          {labels[job.status] ?? job.status}
                        </Badge>
                      </div>
                      <div className="mt-2 text-xs leading-6 text-muted-foreground">
                        <p>上次执行：{time(job.lastRunAt)}</p>
                        <p>下次执行：{time(job.nextRunAt)}</p>
                        {job.lastError && (
                          <p className="text-destructive">
                            任务未完成，请检查服务器任务日志。
                          </p>
                        )}
                      </div>
                      {["token-expiry", "cleanup"].includes(job.kind) && (
                        <Button
                          className="mt-2"
                          size="sm"
                          variant="outline"
                          disabled={!!busy || job.status === "running"}
                          onClick={() => void run(job.kind)}
                        >
                          <Play />
                          提交执行
                        </Button>
                      )}
                    </div>
                  ))
                )}
              </CardContent>
            </Card>
          </div>
          <Card className="mt-6 overflow-hidden">
            <CardHeader>
              <CardTitle>邮件投递记录</CardTitle>
              <p className="text-xs leading-6 text-muted-foreground">
                仅展示收件地址、主题与投递状态；恢复令牌、邀请令牌、邮件正文均不展示。
              </p>
            </CardHeader>
            {!outbox.items.length ? (
              <Empty
                title="还没有投递记录"
                description="密码恢复和成员邀请产生的邮件任务会在这里显示安全的状态摘要。"
              />
            ) : (
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>收件地址 / 主题</th>
                      <th>状态</th>
                      <th>尝试次数</th>
                      <th>创建时间</th>
                      <th>下次尝试</th>
                      <th className="text-right">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {outbox.items.map((item) => (
                      <tr key={item.id}>
                        <td>
                          <p className="font-medium">{item.to}</p>
                          <p className="mt-1 max-w-xs truncate text-xs text-muted-foreground">
                            {item.subject}
                          </p>
                        </td>
                        <td>
                          <Badge
                            variant={
                              item.status === "failed"
                                ? "destructive"
                                : item.status === "sent"
                                  ? "default"
                                  : "outline"
                            }
                          >
                            {labels[item.status] ?? item.status}
                          </Badge>
                          {item.lastError && (
                            <p className="mt-2 text-xs text-muted-foreground">
                              投递未完成，请检查传输状态。
                            </p>
                          )}
                        </td>
                        <td>{item.attempts}</td>
                        <td className="text-xs text-muted-foreground">
                          {time(item.createdAt)}
                        </td>
                        <td className="text-xs text-muted-foreground">
                          {time(
                            item.status === "pending"
                              ? item.nextAttemptAt
                              : undefined,
                          )}
                        </td>
                        <td className="text-right">
                          {item.status === "failed" && (
                            <Button
                              variant="outline"
                              size="sm"
                              disabled={
                                !!busy ||
                                !outbox.deliveryEnabled ||
                                (!!item.expiresAt &&
                                  Date.parse(item.expiresAt) <= Date.now())
                              }
                              onClick={() => void retry(item.id)}
                            >
                              重新排队
                            </Button>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <p className="mt-4 text-xs text-muted-foreground">
            每 30 秒刷新一次。已发送、正在发送或已过期的邮件不能手动重试。
          </p>
        </>
      )}
    </>
  );
}
