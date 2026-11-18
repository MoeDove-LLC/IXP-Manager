import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError } from "@/lib/api";
import { ErrorNotice, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import type { LegacyMetadata } from "@/lib/legacy-traffic";
export default function LegacyTrafficDelete({
  batch,
  onClose,
  onDeleted,
}: {
  batch: LegacyMetadata;
  onClose: () => void;
  onDeleted: () => Promise<void>;
}) {
  const [reason, setReason] = useState(""),
    [ack, setAck] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [stale, setStale] = useState(false);
  const request = useRef<AbortController | null>(null);
  const validChecksum =
    typeof batch.checksum === "string" && /^[a-f0-9]{64}$/.test(batch.checksum);
  useEffect(() => () => request.current?.abort(), []);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!ack || stale || busy || !reason.trim() || !validChecksum) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      await api(`/traffic/legacy/batches/${encodeURIComponent(batch.id)}`, {
        method: "DELETE",
        signal: controller.signal,
        body: JSON.stringify({
          expectedChecksum: batch.checksum,
          reason,
          acknowledge: true,
        }),
      });
      if (!controller.signal.aborted) {
        onClose();
        await onDeleted();
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && [409, 403, 404].includes(e.status))
        setStale(true);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent>
        <DialogTitle className="text-lg font-semibold">
          删除独立旧统计导入
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          只移除本系统中的这个导入批次，不修改原导出文件、旧数据库或实时统计。
        </DialogDescription>
        <ErrorNotice message={error} />
        {!validChecksum && (
          <ErrorNotice message="此目录项没有有效校验和，无法删除；请关闭并刷新后核对。" />
        )}
        <div className="rounded-lg border p-3 text-xs leading-6">
          <p>
            {batch.source.name} · {batch.rowCount} 条记录
          </p>
          <p className="break-all">批次 {batch.id}</p>
          <p className="break-all font-mono">SHA-256 {batch.checksum}</p>
        </div>
        <form onSubmit={submit} className="space-y-4">
          <div className="field">
            <label htmlFor="legacy-delete-reason">删除原因</label>
            <Input
              id="legacy-delete-reason"
              required
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={busy || stale}
              maxLength={1000}
            />
          </div>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-1"
              checked={ack}
              onChange={(e) => setAck(e.target.checked)}
              disabled={busy || stale}
            />
            <span>我已核对批次及校验和，确认删除此导入。</span>
          </label>
          {stale && (
            <p className="notice">
              批次或权限已变化，请关闭并刷新后重新核对，未自动重试删除。
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={onClose}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy || stale || !ack || !validChecksum}
            >
              {busy ? "正在删除…" : "确认删除此导入"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
