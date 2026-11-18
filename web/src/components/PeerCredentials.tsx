import { useEffect, useState, type FormEvent } from "react";
import { Copy, KeyRound, RefreshCw, ShieldCheck } from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { ErrorNotice, Loading, message, time } from "@/components/shared";
type PeerIdentity = {
  id: string;
  name: string;
  routerId: string;
  memberId: string;
  address: string;
  asn: number;
};
type Metadata = {
  peerId: string;
  configured: boolean;
  revision: string;
  peerChecksum: string;
  updatedAt?: string;
  peer: PeerIdentity;
};
type Result = Metadata & { generatedSecret?: string };
export default function PeerCredentials({
  peer,
  routerName,
  memberName,
  onClose,
  onChanged,
}: {
  peer: Row;
  routerName?: string;
  memberName?: string;
  onClose: () => void;
  onChanged: () => void | Promise<void>;
}) {
  const [metadata, setMetadata] = useState<Metadata | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [mode, setMode] = useState<"generate" | "manual" | "remove">("generate"),
    [manualSecret, setManualSecret] = useState(""),
    [generated, setGenerated] = useState(""),
    [saved, setSaved] = useState(false),
    [copied, setCopied] = useState(false),
    [done, setDone] = useState(""),
    [conflict, setConflict] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    api<Metadata>(`/peer-credentials?peerId=${encodeURIComponent(peer.id)}`, {
      signal: controller.signal,
    })
      .then((data) => {
        setMetadata(data);
        if (
          data.peer.routerId !== peer.routerId ||
          data.peer.memberId !== peer.memberId ||
          data.peer.address !== peer.address ||
          data.peer.asn !== Number(peer.asn)
        ) {
          setConflict(true);
          setError("会话身份已变化，请关闭并刷新会话列表后重新核对。");
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [peer.id]);
  const close = () => {
    if (busy || (generated && !saved)) return;
    setGenerated("");
    setManualSecret("");
    onClose();
  };
  const refreshTarget = async () => {
    setGenerated("");
    setManualSecret("");
    onClose();
    await onChanged();
  };
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!metadata || conflict) return;
    const form = e.currentTarget,
      data = Object.fromEntries(new FormData(form));
    setBusy(true);
    setError("");
    setDone("");
    try {
      if (mode === "manual" && !/^[A-Za-z0-9+\/=_-]{1,80}$/.test(manualSecret))
        throw new Error("凭据仅允许 1–80 位英文字母、数字和 + / = _ -。");
      const shared = {
        expectedRevision: metadata.revision ?? "",
        expectedPeerChecksum: metadata.peerChecksum,
        currentPassword: data.currentPassword,
        code: data.code || undefined,
      };
      const result = await save<Result>(
        `/peer-credentials/${peer.id}`,
        {
          ...shared,
          ...(mode === "generate"
            ? { generate: true }
            : mode === "manual"
              ? { secret: manualSecret }
              : {}),
        },
        mode === "remove" ? "DELETE" : "PUT",
      );
      const { generatedSecret, ...safe } = result;
      setMetadata(safe);
      form.reset();
      setManualSecret("");
      setMode("generate");
      if (generatedSecret) {
        setGenerated(generatedSecret);
        setSaved(false);
        setCopied(false);
      } else
        setDone(
          mode === "remove"
            ? "已移除管理侧凭据，旧配置不会自动变化。请重新生成并审查配置。"
            : "凭据已保存，手动输入的值不会回显。请重新生成并审查配置。",
        );
      await onChanged();
    } catch (e) {
      if (e instanceof APIError && (e.status === 409 || e.status === 404)) {
        setConflict(true);
        setManualSecret("");
        form.reset();
        setError(
          "会话身份或凭据版本已经变化，本次操作未提交。请关闭并刷新会话列表后重新核对。",
        );
      } else setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(generated);
      setCopied(true);
    } catch {
      setError("浏览器未允许复制，请手动选择并保存此字段。");
    }
  };
  return (
    <Dialog open onOpenChange={(value) => !value && close()}>
      <DialogContent>
        <DialogTitle className="flex items-center gap-2 text-lg font-semibold">
          <KeyRound className="size-5 text-primary" />
          BGP 认证凭据 · {metadata?.peer.name ?? peer.name}
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          凭据变更需要两端协调同步。这里仅更新管理侧凭据，不自动下发或重载
          BGP；之后必须重新生成、校验并审批路由配置。
        </DialogDescription>
        <ErrorNotice message={error} />
        {loading ? (
          <Loading />
        ) : (
          metadata && (
            <>
              <div className="rounded-lg border bg-muted/30 p-4">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="mono break-all text-sm">
                    AS{metadata.peer.asn} · {metadata.peer.address}
                  </span>
                  <Badge variant={metadata.configured ? "default" : "outline"}>
                    {metadata.configured ? "已配置凭据" : "未配置凭据"}
                  </Badge>
                </div>
                <p className="mt-2 break-all text-xs text-muted-foreground">
                  路由器：{routerName ?? metadata.peer.routerId} ·{" "}
                  {metadata.peer.routerId.slice(0, 8)}
                  <br />
                  成员：{memberName ?? metadata.peer.memberId} ·{" "}
                  {metadata.peer.memberId.slice(0, 8)}
                </p>
                <p className="mt-2 break-all text-[11px] text-muted-foreground">
                  会话 ID：{metadata.peerId}
                </p>
                <p className="mt-1 text-xs text-muted-foreground">
                  凭据版本：
                  {metadata.revision
                    ? metadata.revision.slice(0, 12)
                    : "尚无版本"}{" "}
                  · 更新 {time(metadata.updatedAt)}
                </p>
              </div>
              {generated ? (
                <div className="space-y-4">
                  <p className="notice flex items-start gap-2">
                    <ShieldCheck className="mt-1 size-4 shrink-0" />
                    新凭据仅在当前窗口显示一次，凭据管理界面不提供再次查看。授权下载的生产配置会包含实际凭据。请保存在密码管理器中，并安排对端同步；不要仅修改一侧后立即应用配置。
                  </p>
                  <div className="field">
                    <label htmlFor="generated-peer-secret">
                      本次生成的共享凭据
                    </label>
                    <Input
                      id="generated-peer-secret"
                      className="mono"
                      value={generated}
                      readOnly
                      autoComplete="off"
                    />
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => void copy()}
                  >
                    <Copy />
                    {copied ? "已复制" : "复制本次凭据"}
                  </Button>
                  <label className="flex items-start gap-2 text-sm leading-6">
                    <input
                      className="mt-1"
                      type="checkbox"
                      checked={saved}
                      onChange={(e) => setSaved(e.target.checked)}
                    />
                    我已保存凭据，并理解需要协调两端后再发布配置。
                  </label>
                  <div className="flex justify-end">
                    <Button disabled={!saved} onClick={close}>
                      完成
                    </Button>
                  </div>
                </div>
              ) : conflict ? (
                <Button variant="outline" onClick={() => void refreshTarget()}>
                  <RefreshCw />
                  关闭并刷新会话列表
                </Button>
              ) : (
                <form onSubmit={submit} className="space-y-4">
                  {done && (
                    <p role="status" className="notice">
                      {done}
                      <a className="ml-2 underline" href="#deployments">
                        前往配置中心
                      </a>
                    </p>
                  )}
                  <fieldset
                    disabled={busy}
                    className="space-y-2 rounded-lg border p-3"
                  >
                    <legend className="px-1 text-xs text-muted-foreground">
                      更新方式
                    </legend>
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="radio"
                        name="mode"
                        checked={mode === "generate"}
                        onChange={() => {
                          setMode("generate");
                          setManualSecret("");
                        }}
                      />
                      自动生成高强度凭据（推荐）
                    </label>
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="radio"
                        name="mode"
                        checked={mode === "manual"}
                        onChange={() => setMode("manual")}
                      />
                      输入已有共享凭据
                    </label>
                    {metadata.configured && (
                      <label className="flex items-center gap-2 text-sm text-destructive">
                        <input
                          type="radio"
                          name="mode"
                          checked={mode === "remove"}
                          onChange={() => {
                            setMode("remove");
                            setManualSecret("");
                          }}
                        />
                        移除此会话的凭据
                      </label>
                    )}
                  </fieldset>
                  {mode === "manual" && (
                    <div className="field">
                      <label htmlFor="manual-peer-secret">新的共享凭据</label>
                      <Input
                        id="manual-peer-secret"
                        disabled={busy}
                        type="password"
                        autoComplete="new-password"
                        required
                        minLength={1}
                        maxLength={80}
                        value={manualSecret}
                        onChange={(e) => setManualSecret(e.target.value)}
                      />
                      <p className="text-xs leading-6 text-muted-foreground">
                        支持 1–80 位英文字母、数字和 + / = _ -；保存后不回显。
                      </p>
                    </div>
                  )}
                  {mode === "remove" && (
                    <p className="notice">
                      移除后，后续新配置将不再使用此凭据。请确认对端安排，避免两端认证不一致。
                    </p>
                  )}
                  <div className="field">
                    <label htmlFor="peer-credential-password">
                      当前登录密码
                    </label>
                    <Input
                      id="peer-credential-password"
                      disabled={busy}
                      name="currentPassword"
                      type="password"
                      autoComplete="current-password"
                      required
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="peer-credential-code">
                      验证器代码或恢复码（已开启时填写）
                    </label>
                    <Input
                      id="peer-credential-code"
                      disabled={busy}
                      name="code"
                      autoComplete="one-time-code"
                    />
                  </div>
                  <div className="flex justify-end gap-2">
                    <Button
                      type="button"
                      variant="outline"
                      disabled={busy}
                      onClick={close}
                    >
                      关闭
                    </Button>
                    <Button
                      variant={mode === "remove" ? "destructive" : "default"}
                      disabled={busy || !metadata.peerChecksum}
                    >
                      {busy
                        ? "正在验证并保存…"
                        : mode === "generate"
                          ? "生成并保存凭据"
                          : mode === "manual"
                            ? "保存共享凭据"
                            : "确认移除凭据"}
                    </Button>
                  </div>
                </form>
              )}
            </>
          )
        )}
        {!loading && !metadata && (
          <Button variant="outline" onClick={() => void refreshTarget()}>
            关闭并刷新会话列表
          </Button>
        )}
      </DialogContent>
    </Dialog>
  );
}
