import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw, UserMinus } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { ErrorNotice, Loading, message } from "@/components/shared";

type Target = { id: string; name: string; email: string };
export type MemberRemovalPreview = {
  memberId: string;
  memberName: string;
  target: Target & { memberRole: "member" | "member-admin" };
  canRemove: boolean;
  blockedReason: string;
  signsOutCurrentSession: boolean;
  detachesAccount: boolean;
  _etag: string;
};
export type MemberRemovalResult = {
  ok: true;
  memberId: string;
  userId: string;
  signedOut: boolean;
};

export default function MemberUserRemoval({
  memberId,
  target,
  onClose,
  onRemoved,
  onRefresh,
}: {
  memberId: string;
  target: Target;
  onClose: () => void;
  onRemoved: (result: MemberRemovalResult, preview: MemberRemovalPreview) => void;
  onRefresh: () => void;
}) {
  const [preview, setPreview] = useState<MemberRemovalPreview | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [missing, setMissing] = useState(false);
  const sequence = useRef(0),
    pending = useRef<AbortController | null>(null),
    submitting = useRef(false),
    contextCurrent = useRef(captureSessionContext()).current;
  const path = `/member/users/${encodeURIComponent(target.id)}`;

  const readPreview = useCallback(async () => {
    if (submitting.current || !contextCurrent()) return;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const request = ++sequence.current;
    const current = () =>
      !controller.signal.aborted && request === sequence.current && contextCurrent();
    setPreview(null);
    setLoading(true);
    setMissing(false);
    setError("");
    try {
      const result = await api<MemberRemovalPreview>(`${path}/removal-preview`, {
        signal: controller.signal,
        cache: "no-store",
      });
      if (!current()) return;
      if (
        !result ||
        result.memberId !== memberId ||
        result.target?.id !== target.id ||
        typeof result.memberName !== "string" ||
        typeof result.target.name !== "string" ||
        typeof result.target.email !== "string" ||
        !["member", "member-admin"].includes(result.target.memberRole) ||
        typeof result.canRemove !== "boolean" ||
        typeof result.blockedReason !== "string" ||
        typeof result.detachesAccount !== "boolean" ||
        typeof result.signsOutCurrentSession !== "boolean" ||
        typeof result._etag !== "string" ||
        !/^[a-f0-9]{64}$/.test(result._etag) ||
        (result.canRemove && result.blockedReason !== "")
      )
        throw new Error("移除预览与当前组织或目标不一致，请重新读取。");
      setPreview(result);
    } catch (e) {
      if (!current()) return;
      if (e instanceof APIError && e.status === 404) {
        setMissing(true);
        setError("该账号已不在当前组织，请关闭并刷新名册。");
      } else if (e instanceof APIError && e.status === 403) {
        setError("当前账号没有移除组织账号的权限，请重新确认成员管理员权限。");
      } else {
        setError(message(e));
      }
    } finally {
      if (current()) setLoading(false);
    }
  }, [contextCurrent, memberId, path, target.id]);

  useEffect(() => {
    void readPreview();
    return () => {
      sequence.current++;
      pending.current?.abort();
    };
  }, [readPreview]);

  const remove = async () => {
    if (!preview?.canRemove || loading || submitting.current || !contextCurrent())
      return;
    const reviewed = preview;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const request = ++sequence.current;
    submitting.current = true;
    setBusy(true);
    setError("");
    const current = () =>
      !controller.signal.aborted &&
      request === sequence.current &&
      contextCurrent();
    try {
      // The confirmation uses exactly the version displayed in this dialog.
      // There is no pre-submit refetch and no automatic retry of this mutation.
      const result = await api<MemberRemovalResult>(path, {
        method: "DELETE",
        signal: controller.signal,
        headers: { "If-Match": `"${reviewed._etag}"` },
      });
      if (!current()) return;
      if (
        result?.ok !== true ||
        result.memberId !== memberId ||
        result.userId !== target.id ||
        typeof result.signedOut !== "boolean" ||
        result.signedOut !== reviewed.signsOutCurrentSession
      )
        throw new Error("移除响应与当前预览不一致，请重新核对名册。");
      onRemoved(result, reviewed);
    } catch (e) {
      if (!current()) return;
      // A failed or ambiguous response must be reviewed again before any retry.
      setPreview(null);
      if (e instanceof APIError && [409, 412, 428].includes(e.status)) {
        setError("组织关系或权限已变化，本次未移除。请重新读取移除预览后再确认。");
      } else if (e instanceof APIError && e.status === 404) {
        setMissing(true);
        setError("该账号已不在当前组织，请关闭并刷新名册。");
      } else if (e instanceof APIError && e.status === 403) {
        setError("移除权限已变化，请重新确认成员管理员权限。");
      } else {
        setError(`尚未确认移除结果，请重新读取预览核对。${message(e)}`);
      }
    } finally {
      submitting.current = false;
      if (current()) setBusy(false);
    }
  };

  if (!contextCurrent()) return null;
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent>
        <DialogTitle className="flex items-center gap-2 text-lg font-semibold">
          <UserMinus className="size-5 text-primary" />
          移出当前组织
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          移除当前组织的账号关系，保留账号、双重验证设置与其他组织关系。
        </DialogDescription>
        <ErrorNotice message={error} />
        {loading ? (
          <Loading />
        ) : preview ? (
          <>
            <dl className="space-y-4 rounded-lg border bg-muted/20 p-4">
              <div>
                <dt className="text-xs text-muted-foreground">当前组织</dt>
                <dd className="mt-1 break-words text-sm font-medium">
                  {preview.memberName}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">目标账号</dt>
                <dd className="mt-1 break-words text-sm font-medium">
                  {preview.target.name}
                </dd>
                <dd className="mt-1 break-all text-sm text-muted-foreground">
                  {preview.target.email}
                </dd>
              </div>
              <div>
                <dt className="text-xs text-muted-foreground">当前组织权限</dt>
                <dd className="mt-1 text-sm">
                  {preview.target.memberRole === "member-admin"
                    ? "成员管理员"
                    : "普通成员"}
                </dd>
              </div>
            </dl>
            {!preview.canRemove ? (
              <p role="status" className="notice text-sm leading-6">
                {preview.blockedReason === "last-member-admin"
                  ? "这是当前组织最后一位有效成员管理员，请先安排其他成员管理员。"
                  : "当前关系暂不能移除，请重新读取预览或联系平台管理员。"}
              </p>
            ) : (
              <div className="space-y-3 text-sm leading-6">
                <p>该账号的旧会话、API 令牌和记住设备将失效，需要重新登录。</p>
                {preview.detachesAccount && (
                  <p className="notice">
                    这是该账号最后一条组织关系。移出后账号将受限，需重新受邀加入组织后才能访问组织资料。
                  </p>
                )}
                {preview.signsOutCurrentSession && (
                  <p className="font-medium text-destructive">
                    你正在移除自己的当前组织关系，确认后将退出登录。
                  </p>
                )}
              </div>
            )}
          </>
        ) : (
          <p className="break-words text-sm text-muted-foreground">
            所选账号：{target.name} · {target.email}
          </p>
        )}
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="outline" disabled={busy} onClick={onClose}>
            取消
          </Button>
          {missing ? (
            <Button variant="outline" disabled={busy} onClick={onRefresh}>
              关闭并刷新名册
            </Button>
          ) : (
            <Button
              variant="outline"
              disabled={loading || busy}
              onClick={() => void readPreview()}
            >
              <RefreshCw />
              重新读取移除预览
            </Button>
          )}
          <Button
            variant="destructive"
            disabled={loading || busy || !preview?.canRemove}
            onClick={() => void remove()}
          >
            {busy ? "正在移除…" : "确认移出组织"}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
