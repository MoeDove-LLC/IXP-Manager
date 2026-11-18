import { useEffect, useRef, useState } from "react";
import {
  Download,
  Plus,
  ShieldCheck,
  Pencil,
  CircleStop,
  Trash2,
} from "lucide-react";
import { api, APIError } from "@/lib/api";
import type { SFlowCatalog, SFlowReceiver } from "@/lib/sflow";
import { Empty, ErrorNotice, message, time } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import SFlowReceiverEditor from "@/components/SFlowReceiverEditor";
import SFlowReceiverReview from "@/components/SFlowReceiverReview";

const statuses = {
  draft: "待审批",
  approved: "已审批",
  disabled: "已停用",
  stale: "审批已失效",
};
const deliveryStates: Record<string, string> = {
  unknown: "未知（无本机报告）",
  "authorization-only": "仅获得授权，尚无本机报告",
  "sent-to-socket": "采集器已提交本机 UDP socket（未证实送达）",
  "blocked-policy": "被采集器本地目的策略阻止",
  expired: "授权已过期，未发送",
  "encode-rejected": "重新编码未通过，未发送",
  "rate-limited": "受到采集器限速",
  "send-error": "本机发送失败",
  "partial-send-error": "部分提交后发生发送错误",
};
async function downloadPlan(
  item: SFlowReceiver,
  format: "plan" | "pretag",
  signal: AbortSignal,
) {
  const response = await fetch(`/api/sflow/receivers/${item.id}/${format}`, {
    credentials: "same-origin",
    signal,
  });
  if (!response.ok) {
    if (response.status === 401)
      window.dispatchEvent(new Event("session-expired"));
    let reason = `导出失败 (${response.status})`;
    try {
      reason = (await response.json()).error ?? reason;
    } catch {
      /* use status */
    }
    throw new APIError(reason, response.status);
  }
  const bytes = await response.arrayBuffer(),
    checksum = response.headers.get("X-Config-SHA256");
  if (
    response.headers.get("X-SFlow-Receiver-ID") !== item.id ||
    response.headers.get("X-SFlow-Receiver-Revision") !== item.revision ||
    !checksum ||
    !/^[a-f0-9]{64}$/i.test(checksum)
  )
    throw new Error(
      "导出结果与所选接收器修订不匹配，未保存文件。请刷新后重新核对。",
    );
  if (!globalThis.crypto?.subtle)
    throw new Error("请通过 HTTPS 或 localhost 打开控制台，以便校验导出文件。");
  const actual = [
    ...new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)),
  ]
    .map((value) => value.toString(16).padStart(2, "0"))
    .join("");
  if (actual !== checksum.toLowerCase())
    throw new Error("导出校验和不一致，未保存文件。");
  if (format === "plan") {
    const plan = JSON.parse(new TextDecoder().decode(bytes));
    if (
      plan.version !== 1 ||
      plan.receiver?.id !== item.id ||
      plan.receiver?.revision !== item.revision ||
      plan.forwardingEnabled !== false ||
      plan.nativeValidated !== false ||
      plan.requiresCurrentOwnershipCheck !== true
    )
      throw new Error("导出计划的修订或授权说明无效，未保存文件。");
  }
  if (signal.aborted) return;
  const url = URL.createObjectURL(
      new Blob([bytes], {
        type: format === "plan" ? "application/json" : "text/plain",
      }),
    ),
    link = document.createElement("a");
  link.href = url;
  link.download = `sflow-${item.id}-${format}.${format === "plan" ? "json" : "map"}`;
  link.click();
  URL.revokeObjectURL(url);
}

export default function SFlowReceivers({
  catalog,
  role,
  onChanged,
  targetId = "",
}: {
  catalog: SFlowCatalog;
  role: string;
  onChanged: () => Promise<void>;
  targetId?: string;
}) {
  const [editor, setEditor] = useState<SFlowReceiver | "new" | null>(null),
    [review, setReview] = useState<SFlowReceiver | null>(null),
    [action, setAction] = useState<{
      item: SFlowReceiver;
      kind: "disable" | "delete";
    } | null>(null),
    [reason, setReason] = useState(""),
    [ack, setAck] = useState(false),
    [busy, setBusy] = useState(""),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [conflict, setConflict] = useState(false);
  const request = useRef<AbortController | null>(null);
  const items = (catalog.receivers ?? []).filter(
    (item) => !targetId || item.id === targetId,
  );
  useEffect(() => () => request.current?.abort(), []);
  const mutate = async () => {
    if (!action || busy || conflict || !ack) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(action.item.id);
    setError("");
    try {
      await api(
        `/sflow/receivers/${action.item.id}${action.kind === "disable" ? "/disable" : ""}`,
        {
          method: action.kind === "disable" ? "POST" : "DELETE",
          signal: controller.signal,
          body: JSON.stringify({
            expectedRevision: action.item.revision,
            ...(action.kind === "delete" ? { reason } : {}),
          }),
        },
      );
      if (controller.signal.aborted) return;
      setAction(null);
      setNotice(
        action.kind === "disable"
          ? "已立即停止签发新授权；已发的不超过 5 秒授权和在途 UDP 不可撤回。"
          : "接收器已删除。",
      );
      await onChanged();
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && [409, 403, 404].includes(e.status)) {
        setConflict(true);
        setError(
          "接收器修订或权限已变化，本次操作未执行。请关闭并刷新后重新核对。",
        );
      }
    } finally {
      if (!controller.signal.aborted) setBusy("");
    }
  };
  const download = async (item: SFlowReceiver, format: "plan" | "pretag") => {
    const controller = new AbortController();
    request.current = controller;
    setBusy(item.id);
    setError("");
    setNotice("");
    try {
      await downloadPlan(item, format, controller.signal);
      if (controller.signal.aborted) return;
      setNotice(
        "已核对修订及校验和并导出静态计划。它不授予实时转发，也不证明目的端收到数据。",
      );
    } catch (e) {
      if (!controller.signal.aborted) setError(message(e));
    } finally {
      if (!controller.signal.aborted) setBusy("");
    }
  };
  return (
    <div className="space-y-5">
      <div className="notice leading-6">
        接收器申请描述期望目的和范围；管理员审批后，还需采集器本机明确启用转发、本地目的策略及服务端逐包短期授权同时允许。授权计数仅表示许可，不是外部
        UDP 送达回执。
      </div>
      {!action && <ErrorNotice message={error} />}
      {targetId && (
        <div className="notice flex flex-wrap items-center justify-between gap-3">
          <span>
            {items.length
              ? "已定位搜索结果"
              : "此接收器已删除，或当前无权查看。"}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              location.hash = "sflow?tab=receivers";
            }}
          >
            查看全部接收器
          </Button>
        </div>
      )}
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <CardTitle>成员接收器</CardTitle>
            {catalog.capabilities.canManageReceivers && (
              <Button onClick={() => setEditor("new")}>
                <Plus />
                新增接收器
              </Button>
            )}
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {!items.length ? (
            <Empty
              title={targetId ? "未找到指定接收器" : "暂无接收器申请"}
              description="成员可提出自己的接收目的和连接范围，由管理员审阅后批准。保存库存不会自动启动实时转发。"
            />
          ) : (
            items.map((item) => (
              <article
                key={item.id}
                className={`min-w-0 rounded-xl border p-4 ${targetId === item.id ? "border-primary bg-primary/5" : ""}`}
                data-testid="sflow-receiver"
                data-receiver-id={item.id}
                data-located={targetId === item.id ? "true" : undefined}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="break-all font-semibold">{item.name}</h3>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {item.memberLabel}
                    </p>
                  </div>
                  <Badge
                    variant={item.status === "approved" ? "default" : "outline"}
                  >
                    {statuses[item.status] ?? item.status}
                  </Badge>
                </div>
                <div className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
                  <div>
                    <p className="text-xs text-muted-foreground">期望目的</p>
                    <p className="mt-1 break-all font-mono">
                      {item.spec.address} · UDP {item.spec.port}
                    </p>
                    <p className="mt-1 text-xs">
                      {item.spec.enabled
                        ? "期望启用（需有效审批）"
                        : "期望停用"}
                    </p>
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">
                      授权与本机报告 · 未证实送达
                    </p>
                    <p className="mt-1">
                      {item.authorizationCount != null
                        ? `${item.authorizationCount} 次授权`
                        : "暂无授权计数"}
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      最近授权：{time(item.lastAuthorizedAt)}
                    </p>
                    <p className="mt-2 text-xs leading-6">
                      {deliveryStates[item.deliveryState ?? "unknown"] ??
                        item.deliveryState}
                    </p>
                    {item.lastReceiptAt && (
                      <p className="mt-1 text-xs text-muted-foreground">
                        最近本机报告：{time(item.lastReceiptAt)}
                      </p>
                    )}
                    {item.receiptRevision && (
                      <p className="mt-1 text-xs text-muted-foreground">
                        {item.receiptForCurrentRevision
                          ? "报告对应当前接收器修订"
                          : "报告属于此前修订，不代表当前状态"}{" "}
                        · {item.receiptDatagrams ?? "未知"} 个数据报 /{" "}
                        {item.receiptBytes ?? "未知"} 字节
                      </p>
                    )}
                  </div>
                </div>
                <p className="mt-3 text-xs text-muted-foreground">
                  范围：{item.spec.connectionIds.length} 条连接 ·{" "}
                  {item.spec.vlanIds.length} 个 VLAN · 更新时间{" "}
                  {time(item.updatedAt)}
                </p>
                {item.approvedAt && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    审批记录：{time(item.approvedAt)}
                    {item.status !== "approved" ? "（不代表当前有效）" : ""}
                  </p>
                )}
                {item.deliveryNotice && (
                  <p className="mt-2 text-xs leading-6 text-muted-foreground">
                    {item.deliveryNotice}
                  </p>
                )}
                {item.evidencePersistence === "current-process" && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    运行证据仅在服务当前进程内保留，重启后为未知；未知不等于零。
                  </p>
                )}
                {item.notice && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    {item.notice}
                  </p>
                )}
                {item.staleReason && (
                  <p className="notice mt-2 break-all text-xs">
                    当前无法按此审批转发：{item.staleReason}
                  </p>
                )}
                <div className="mt-4 flex flex-wrap gap-2">
                  {item.canEdit && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={!!busy}
                      onClick={() => setEditor(item)}
                    >
                      <Pencil />
                      编辑申请
                    </Button>
                  )}
                  {(item.canEdit || item.canApprove) && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={!!busy}
                      onClick={() => setReview(item)}
                    >
                      <ShieldCheck />
                      审阅范围
                    </Button>
                  )}
                  {["approved", "disabled"].includes(item.status) && (
                    <>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={!!busy}
                        onClick={() => void download(item, "plan")}
                      >
                        <Download />
                        导出静态计划
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={!!busy}
                        onClick={() => void download(item, "pretag")}
                      >
                        导出 pretag
                      </Button>
                    </>
                  )}
                  {item.canEdit && item.status !== "disabled" && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={!!busy}
                      onClick={() => {
                        setAction({ item, kind: "disable" });
                        setAck(false);
                        setConflict(false);
                        setError("");
                      }}
                    >
                      <CircleStop />
                      停发并撤销授权
                    </Button>
                  )}
                  {item.canDelete && item.status === "disabled" && (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={!!busy}
                      onClick={() => {
                        setAction({ item, kind: "delete" });
                        setReason("");
                        setAck(false);
                        setConflict(false);
                        setError("");
                      }}
                    >
                      <Trash2 />
                      删除
                    </Button>
                  )}
                </div>
              </article>
            ))
          )}
        </CardContent>
      </Card>
      {editor && (
        <SFlowReceiverEditor
          item={editor === "new" ? undefined : editor}
          catalog={catalog}
          role={role}
          onClose={() => setEditor(null)}
          onSaved={onChanged}
        />
      )}
      {review && (
        <SFlowReceiverReview
          item={review}
          catalog={catalog}
          role={role}
          onClose={() => setReview(null)}
          onSaved={onChanged}
        />
      )}
      {action && (
        <Dialog open onOpenChange={(open) => !open && !busy && setAction(null)}>
          <DialogContent>
            <DialogTitle className="text-lg font-semibold">
              {action.kind === "disable"
                ? "停发并撤销后续授权"
                : "删除已停用接收器"}
            </DialogTitle>
            <DialogDescription className="break-all text-sm leading-6 text-muted-foreground">
              {action.item.name} · {action.item.memberLabel} ·{" "}
              {action.item.spec.address} · UDP {action.item.spec.port}
            </DialogDescription>
            <ErrorNotice message={error} />
            <p className="text-sm leading-6">
              {action.kind === "disable"
                ? "立即停止签发新授权；已发的不超过 5 秒授权和在途 UDP 不可撤回。此操作不证明远端已停止处理旧数据。"
                : "仅删除已停用的接收器，不影响已保留的审计记录。"}
            </p>
            {action.kind === "delete" && (
              <div className="field">
                <label htmlFor="receiver-delete-reason">删除原因</label>
                <Input
                  id="receiver-delete-reason"
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  maxLength={500}
                  disabled={!!busy || conflict}
                />
              </div>
            )}
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                className="mt-1"
                checked={ack}
                onChange={(e) => setAck(e.target.checked)}
                disabled={!!busy || conflict}
              />
              <span>我已核对接收器、目的地址和本次操作。</span>
            </label>
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                variant="outline"
                disabled={!!busy}
                onClick={() => setAction(null)}
              >
                取消
              </Button>
              <Button
                variant="destructive"
                disabled={
                  !!busy ||
                  conflict ||
                  !ack ||
                  (action.kind === "delete" && !reason.trim())
                }
                onClick={() => void mutate()}
              >
                {busy
                  ? "正在处理…"
                  : action.kind === "disable"
                    ? "确认停发"
                    : "确认删除"}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      )}
    </div>
  );
}
