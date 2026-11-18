import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError } from "@/lib/api";
import { ErrorNotice, Loading, message, time } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import type { LearnedMAC, MACApplied, MACReview } from "@/lib/mac-learning";

export default function MACLearningReview({
  switchId,
  switchName,
  observationChecksum,
  observedAt,
  candidate,
  initialName,
  canApply,
  onClose,
  onApplied,
  onRefresh,
}: {
  switchId: string;
  switchName: string;
  observationChecksum: string;
  observedAt?: string;
  candidate: LearnedMAC;
  initialName?: string;
  canApply: boolean;
  onClose: () => void;
  onApplied: (value: MACApplied) => Promise<void>;
  onRefresh: (name: string) => Promise<void>;
}) {
  const [name, setName] = useState(initialName ?? `MAC ${candidate.mac}`),
    [review, setReview] = useState<MACReview | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [ack, setAck] = useState(false),
    [stale, setStale] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    request.current = controller;
    api<MACReview>("/devices/mac-learning/review", {
      method: "POST",
      signal: controller.signal,
      body: JSON.stringify({
        switchId,
        observationChecksum,
        candidateId: candidate.id,
      }),
    })
      .then((data) => {
        if (controller.signal.aborted) return;
        if (
          data.candidate.id !== candidate.id ||
          data.candidate.mac !== candidate.mac ||
          data.candidate.connectionId !== candidate.connectionId ||
          data.candidate.memberId !== candidate.memberId ||
          data.candidate.vlanId !== candidate.vlanId ||
          data.candidate.ifIndex !== candidate.ifIndex ||
          !data.reviewChecksum ||
          !data.expiresAt
        )
          throw new Error(
            "审阅结果与所选观测或归属不一致，请刷新证据后重新选择。",
          );
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
  }, [switchId, observationChecksum, candidate.id]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!review || stale || busy || !ack || !canApply) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const result = await api<MACApplied>("/devices/mac-learning/apply", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({
          switchId,
          observationChecksum,
          candidateId: review.candidate.id,
          reviewChecksum: review.reviewChecksum,
          expiresAt: review.expiresAt,
          name,
          acknowledge: true,
        }),
      });
      if (!controller.signal.aborted) {
        onClose();
        await onApplied(result);
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && [409, 403, 404, 422].includes(e.status)) {
        setStale(true);
        setAck(false);
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const reviewed = review?.candidate ?? candidate;
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="max-w-2xl"
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
        onPointerDownOutside={(event) => busy && event.preventDefault()}
      >
        <DialogTitle className="text-lg font-semibold">
          审阅学习到的 MAC
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          核对这次观测与当前唯一连接归属，再明确确认登记。已有登记不会被移动或覆盖。
        </DialogDescription>
        <ErrorNotice message={error} />
        {loading ? (
          <Loading />
        ) : (
          <form onSubmit={submit} className="space-y-4">
            <div className="rounded-lg border bg-muted/20 p-4 text-sm leading-7">
              <p className="break-all font-mono text-base font-semibold">
                {reviewed.mac}
              </p>
              <p>交换机：{switchName}</p>
              <p>
                物理接口：{reviewed.portLabel || "未确定"} · ifIndex{" "}
                {reviewed.ifIndex}
              </p>
              <p>
                VLAN：{reviewed.vlanLabel || "未确定"} · 报告 tag{" "}
                {reviewed.vlanTags.join("、") || "未提供"}
              </p>
              <p>
                成员：<strong>{reviewed.memberLabel || "未确定"}</strong>
              </p>
              <p>
                连接：<strong>{reviewed.connectionLabel || "未确定"}</strong>
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                观测时间：{time(observedAt)}
                {review && (
                  <>
                    <br />
                    审阅有效至：{time(review.expiresAt)}
                  </>
                )}
              </p>
            </div>
            {review?.consequences?.length ? (
              <div className="notice space-y-1">
                {review.consequences.map((text, i) => (
                  <p key={i}>{text}</p>
                ))}
              </div>
            ) : null}
            <div className="field">
              <label htmlFor="mac-registration-name">登记名称</label>
              <Input
                id="mac-registration-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                maxLength={200}
                disabled={busy}
              />
            </div>
            {stale ? (
              <div className="notice space-y-3" role="status">
                <p>
                  证据、登记或归属已变化，尚未应用。登记名称已保留；请查看最新证据后重新选择并审阅。
                </p>
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => void onRefresh(name)}
                >
                  保留名称并刷新证据
                </Button>
              </div>
            ) : (
              <label className="flex items-start gap-2 rounded-lg border p-3">
                <input
                  id="mac-registration-ack"
                  type="checkbox"
                  className="mt-1"
                  checked={ack}
                  onChange={(e) => setAck(e.target.checked)}
                  disabled={busy || !canApply}
                />
                <span>
                  我已核对 MAC、物理接口、VLAN、成员与连接，确认新增此登记。
                </span>
              </label>
            )}
            <div className="flex flex-wrap justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={onClose}
              >
                取消
              </Button>
              <Button disabled={busy || stale || !review || !ack || !canApply}>
                {busy ? "正在登记…" : "确认登记 MAC"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
