import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError } from "@/lib/api";
import type {
  SFlowCatalog,
  SFlowReceiver,
  SFlowReceiverReview as Review,
} from "@/lib/sflow";
import { ErrorNotice, Loading, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

export default function SFlowReceiverReview({
  item,
  catalog,
  role,
  onClose,
  onSaved,
}: {
  item: SFlowReceiver;
  catalog: SFlowCatalog;
  role: string;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [review, setReview] = useState<Review | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [stale, setStale] = useState(false),
    [ack, setAck] = useState(false),
    [mfa, setMfa] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    api<Review>(`/sflow/receivers/${item.id}/review`, {
      method: "POST",
      signal: controller.signal,
      body: JSON.stringify({ expectedRevision: item.revision }),
    })
      .then((data) => {
        if (controller.signal.aborted) return;
        if (
          data.receiver.id !== item.id ||
          data.receiver.revision !== item.revision ||
          !data.reviewChecksum
        )
          throw new Error("审阅结果与打开的接收器修订不匹配，请关闭后刷新。");
        setReview(data);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setError(message(e));
          setStale(true);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [item.id, item.revision]);
  const approve = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!review || stale || !ack || busy) return;
    const form = event.currentTarget,
      fields = new FormData(form),
      controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      await api(`/sflow/receivers/${item.id}/approve`, {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({
          expectedRevision: review.receiver.revision,
          reviewChecksum: review.reviewChecksum,
          acknowledgeHeaderData: true,
          currentPassword: fields.get("currentPassword"),
          code: fields.get("code") || undefined,
        }),
      });
      if (!controller.signal.aborted) {
        onClose();
        await onSaved();
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && e.data?.mfaRequired) setMfa(true);
      if (
        e instanceof APIError &&
        [409, 404].includes(e.status) &&
        !e.data?.mfaRequired
      ) {
        setStale(true);
        setAck(false);
        setError(
          "接收器、绑定范围或审批权限已变化；未批准。请关闭并刷新后重新审阅。",
        );
      }
    } finally {
      form.reset();
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const label = (kind: "connections" | "vlans", id: string) => {
    const found = catalog.selectors[kind].find((r) => r.id === id);
    return found ? `${found.label || found.name} · ${id}` : id;
  };
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="max-w-3xl"
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
        onPointerDownOutside={(event) => busy && event.preventDefault()}
      >
        <DialogTitle className="text-lg font-semibold">
          审阅接收器范围 · {item.name}
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          审批绑定本次目的地址、连接、VLAN
          与当前映射。修改或归属变化后必须重新审批；批准不证明采集器启动或目的端收到数据。
        </DialogDescription>
        <ErrorNotice message={error} />
        {loading ? (
          <Loading />
        ) : (
          review && (
            <>
              <div className="rounded-lg border bg-muted/20 p-4 text-sm leading-7">
                <p>
                  所属成员：<strong>{review.receiver.memberLabel}</strong>
                </p>
                <p>
                  目的：
                  <strong className="break-all font-mono">
                    {review.receiver.spec.address} · UDP{" "}
                    {review.receiver.spec.port}
                  </strong>
                </p>
                <p>
                  期望状态：{review.receiver.spec.enabled ? "启用" : "停用"}
                </p>
                <div className="mt-2">
                  <p className="font-medium">连接范围</p>
                  {review.receiver.spec.connectionIds.map((id) => (
                    <p className="break-all text-xs" key={id}>
                      {label("connections", id)}
                    </p>
                  ))}
                </div>
                <div className="mt-2">
                  <p className="font-medium">VLAN 范围</p>
                  {review.receiver.spec.vlanIds.map((id) => (
                    <p className="break-all text-xs" key={id}>
                      {label("vlans", id)}
                    </p>
                  ))}
                </div>
                <p className="mt-2 break-all text-[11px] text-muted-foreground">
                  修订：{review.receiver.revision}
                  <br />
                  审阅摘要：{review.reviewChecksum}
                </p>
              </div>
              <section className="space-y-2" aria-label="当前转发匹配范围">
                <h3 className="text-sm font-semibold">
                  当前转发匹配范围 · {review.bindings.length} 条
                </h3>
                {review.bindings.map((b, i) => (
                  <div
                    key={i}
                    className="rounded-lg border p-3 text-xs leading-6"
                  >
                    <p className="break-all">
                      {b.direction === "source"
                        ? "成员作为来源"
                        : b.direction === "destination"
                          ? "成员作为目的"
                          : b.direction || "方向未提供"}{" "}
                      · 连接{" "}
                      {b.connectionId
                        ? label("connections", b.connectionId)
                        : "未提供"}
                    </p>
                    <p className="break-all">
                      VLAN {b.vlanId ? label("vlans", b.vlanId) : "未提供"}
                      {b.vlanTag != null ? `（tag ${b.vlanTag}）` : ""} ·
                      ifIndex {b.ifIndex ?? "未提供"} · MAC {b.mac || "未提供"}
                    </p>
                    {(b.exporterId || b.switchId) && (
                      <p className="break-all text-muted-foreground">
                        导出器 {b.exporterId || "未提供"} · 交换机{" "}
                        {b.switchId || "未提供"}
                      </p>
                    )}
                    {b.mappingRevision && (
                      <p className="break-all text-muted-foreground">
                        映射修订 {b.mappingRevision}
                      </p>
                    )}
                  </div>
                ))}
              </section>
              <section
                className="notice space-y-2"
                aria-label="采样包头数据披露"
              >
                <h3 className="font-semibold">采样包头数据披露</h3>
                {(review.disclosure ?? []).map((text, i) => (
                  <p key={i}>{text}</p>
                ))}
                <p>
                  接收数据可能包含通信对端的
                  MAC、IP、协议与端口等包头信息。请核对接收端和上述范围，确认此次数据共享。
                </p>
              </section>
              {!!review.notices?.length && (
                <div className="space-y-1 text-xs leading-6 text-muted-foreground">
                  {review.notices.map((text, i) => (
                    <p key={i}>{text}</p>
                  ))}
                </div>
              )}
              {role === "admin" && review.receiver.canApprove && (
                <form onSubmit={approve} className="space-y-4">
                  <label className="flex items-start gap-3 rounded-lg border p-3">
                    <input
                      className="mt-1"
                      type="checkbox"
                      id="receiver-disclosure-ack"
                      checked={ack}
                      onChange={(e) => setAck(e.target.checked)}
                      disabled={busy || stale}
                    />
                    <span>
                      我已核对目的地址、成员连接/VLAN
                      范围及包头数据披露，批准此修订。
                    </span>
                  </label>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <div className="field">
                      <label htmlFor="receiver-approval-password">
                        当前管理员密码
                      </label>
                      <Input
                        id="receiver-approval-password"
                        name="currentPassword"
                        type="password"
                        autoComplete="current-password"
                        required
                        disabled={busy || stale}
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="receiver-approval-code">
                        验证器代码或恢复码
                        {mfa ? "（必填）" : "（已启用时填写）"}
                      </label>
                      <Input
                        id="receiver-approval-code"
                        name="code"
                        autoComplete="one-time-code"
                        required={mfa}
                        disabled={busy || stale}
                      />
                    </div>
                  </div>
                  <div className="flex flex-wrap justify-end gap-2">
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy}
                      onClick={onClose}
                    >
                      关闭
                    </Button>
                    <Button disabled={busy || stale || !ack}>
                      {busy ? "正在审批…" : "批准此修订"}
                    </Button>
                  </div>
                </form>
              )}
              {(role !== "admin" || !review.receiver.canApprove) && (
                <Button variant="outline" onClick={onClose}>
                  关闭审阅
                </Button>
              )}
            </>
          )
        )}
      </DialogContent>
    </Dialog>
  );
}
