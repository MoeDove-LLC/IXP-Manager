import { useEffect, useRef, useState } from "react";
import { ImageIcon, RefreshCw, Trash2, Upload } from "lucide-react";
import { api, APIError, save } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ErrorNotice, Loading, message } from "@/components/shared";

type Logo = {
  memberId: string;
  memberName: string;
  configured: boolean;
  revision: string;
  canEdit: boolean;
  published: boolean;
  publiclyAvailable: boolean;
  width?: number;
  height?: number;
  size?: number;
  updatedAt?: string;
  previewUrl?: string;
  publicUrl?: string;
  imported?: boolean;
  legacySource?: Record<string, string>;
};

export default function MemberLogo({ memberId }: { memberId?: string }) {
  const path = memberId
    ? `/members/${encodeURIComponent(memberId)}/logo`
    : "/member/logo";
  const [current, setCurrent] = useState<Logo | null>(null),
    [newer, setNewer] = useState<Logo | null>(null),
    [file, setFile] = useState<File | null>(null),
    [published, setPublished] = useState(false),
    [busy, setBusy] = useState(false),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [deleteConfirm, setDeleteConfirm] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null),
    alive = useRef(true);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const next = await api<Logo>(path);
      if (alive.current) {
        setCurrent(next);
        setPublished(next.published);
        setNewer(null);
      }
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setLoading(false);
    }
  };
  useEffect(() => {
    alive.current = true;
    void load();
    return () => {
      alive.current = false;
    };
  }, [path]);
  const failure = async (e: unknown) => {
    setError(message(e));
    if (e instanceof APIError && e.status === 409) {
      try {
        setNewer(await api<Logo>(path));
      } catch (refreshError) {
        setError(`${message(e)}；当前资料读取失败：${message(refreshError)}`);
      }
    }
  };
  const commit = async (action: "upload" | "publish" | "delete") => {
    if (!current || newer) return;
    setBusy(true);
    setError("");
    setDone("");
    try {
      let next: Logo;
      if (action === "upload") {
        if (!file) throw new Error("请选择 PNG 或 JPEG 文件。");
        if (file.size > 5 * 1024 * 1024)
          throw new Error("图片不能超过 5 MiB。");
        const data = new FormData();
        data.set("file", file);
        data.set("expectedRevision", current.revision);
        data.set("published", String(published));
        next = await api<Logo>(path, { method: "POST", body: data });
      } else
        next = await save<Logo>(
          path,
          {
            expectedRevision: current.revision,
            ...(action === "publish" ? { published } : {}),
          },
          action === "delete" ? "DELETE" : "PUT",
        );
      if (!alive.current) return;
      setCurrent(next);
      setPublished(next.published);
      setFile(null);
      if (fileInput.current) fileInput.current.value = "";
      setDeleteConfirm(false);
      setDone(
        action === "delete"
          ? "成员 Logo 已删除，公开访问已撤销。"
          : "成员 Logo 已保存。",
      );
    } catch (e) {
      if (alive.current) await failure(e);
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <ImageIcon className="size-4" />
            成员 Logo
          </CardTitle>
          <p className="mt-2 text-sm text-muted-foreground">
            仅影响当前成员组织的标识。
          </p>
        </div>
        <Button
          size="icon"
          variant="ghost"
          aria-label="刷新成员 Logo"
          disabled={busy || !!file}
          onClick={() => void load()}
        >
          <RefreshCw className={loading ? "animate-spin" : ""} />
        </Button>
      </CardHeader>
      <CardContent className="space-y-5">
        <ErrorNotice message={error} />
        {done && (
          <p role="status" className="notice">
            {done}
          </p>
        )}
        {loading && !current ? (
          <Loading />
        ) : (
          current && (
            <>
              <div className="flex flex-wrap items-center gap-5 rounded-lg border bg-muted/20 p-4">
                <div className="flex h-24 w-40 shrink-0 items-center justify-center rounded border bg-white p-3">
                  {current.configured ? (
                    <img
                      src={current.previewUrl}
                      alt={`${current.memberName} 的 Logo`}
                      className="max-h-full max-w-full object-contain"
                    />
                  ) : (
                    <span className="text-xs text-slate-500">尚未上传</span>
                  )}
                </div>
                <div className="min-w-0 space-y-2">
                  <p className="font-medium break-words">
                    {current.memberName}
                  </p>
                  <Badge
                    variant={current.publiclyAvailable ? "default" : "outline"}
                  >
                    {current.publiclyAvailable
                      ? "已公开"
                      : current.published
                        ? "已选择发布 · 当前成员未激活"
                        : "私有"}
                  </Badge>
                  {current.configured && (
                    <p className="text-xs text-muted-foreground">
                      {current.width} × {current.height} px ·{" "}
                      {((current.size ?? 0) / 1024).toFixed(1)} KiB · PNG
                    </p>
                  )}
                  {current.imported && (
                    <p className="text-xs text-muted-foreground">
                      来自旧系统的图片；导入时保持私有。
                    </p>
                  )}
                </div>
              </div>
              {current.publicUrl && (
                <a
                  className="block break-all text-sm text-primary underline"
                  href={current.publicUrl}
                  target="_blank"
                  rel="noreferrer"
                >
                  查看当前公开图片
                </a>
              )}
              {newer && (
                <div className="space-y-3 rounded-lg border border-amber-400/40 bg-amber-50/30 p-4">
                  <p className="text-sm">
                    服务器已有更新，所选文件和发布选择已保留。请核对最新版本再重试。
                  </p>
                  <p className="text-xs text-muted-foreground">
                    当前服务器：
                    {newer.configured
                      ? `${newer.width} × ${newer.height} px`
                      : "未配置"}{" "}
                    · {newer.published ? "已选择发布" : "私有"} ·{" "}
                    {newer.updatedAt ?? "无更新时间"}
                  </p>
                  {newer.previewUrl && (
                    <img
                      src={newer.previewUrl}
                      alt="服务器最新成员 Logo"
                      className="max-h-20 max-w-full rounded bg-white p-2"
                    />
                  )}
                  <Button
                    variant="outline"
                    onClick={() => {
                      setCurrent(newer);
                      setNewer(null);
                      setError("");
                    }}
                  >
                    已核对，使用该版本重试
                  </Button>
                </div>
              )}
              {current.canEdit ? (
                <fieldset
                  disabled={busy || !!newer}
                  className="space-y-4 disabled:opacity-70"
                >
                  <div className="field">
                    <label htmlFor={`logo-file-${memberId ?? "self"}`}>
                      上传或替换图片
                    </label>
                    <Input
                      ref={fileInput}
                      id={`logo-file-${memberId ?? "self"}`}
                      type="file"
                      accept="image/png,image/jpeg"
                      onChange={(e) => {
                        setFile(e.target.files?.[0] ?? null);
                        setDone("");
                      }}
                    />
                    <p className="text-xs leading-5 text-muted-foreground">
                      PNG / JPEG，最多 5 MiB、单边 4096 像素及总计 800
                      万像素。服务端验证像素并重新编码为 PNG，输出也须小于 5
                      MiB；SVG 不受支持。
                    </p>
                  </div>
                  <label className="flex items-start gap-2 text-sm">
                    <input
                      className="mt-1"
                      type="checkbox"
                      checked={published}
                      onChange={(e) => setPublished(e.target.checked)}
                    />
                    <span>
                      允许通过公开链接展示此成员 Logo
                      <span className="mt-1 block text-xs leading-5 text-muted-foreground">
                        仅在成员处于激活状态时可公开读取。撤销发布可停止后续访问，无法收回已被他人保存的图片。
                      </span>
                    </span>
                  </label>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      disabled={!file}
                      onClick={() => void commit("upload")}
                    >
                      <Upload />
                      {busy ? "正在保存…" : "保存图片"}
                    </Button>
                    {current.configured && (
                      <>
                        <Button
                          variant="outline"
                          disabled={
                            file !== null || published === current.published
                          }
                          onClick={() => void commit("publish")}
                        >
                          保存发布设置
                        </Button>
                        <Button
                          variant="ghost"
                          onClick={() => setDeleteConfirm(true)}
                        >
                          <Trash2 />
                          删除图片
                        </Button>
                      </>
                    )}
                  </div>
                  {deleteConfirm && (
                    <div className="space-y-3 rounded-lg border p-4">
                      <p className="text-sm">
                        删除当前图片并撤销公开访问？此操作会释放文件占用。
                      </p>
                      <div className="flex gap-2">
                        <Button
                          variant="destructive"
                          onClick={() => void commit("delete")}
                        >
                          确认删除 Logo
                        </Button>
                        <Button
                          variant="outline"
                          onClick={() => setDeleteConfirm(false)}
                        >
                          取消
                        </Button>
                      </div>
                    </div>
                  )}
                </fieldset>
              ) : (
                <p className="text-sm text-muted-foreground">
                  只读查看；需要成员管理员或平台运营权限才能修改。
                </p>
              )}
              {current.legacySource && (
                <details className="text-xs">
                  <summary className="cursor-pointer text-muted-foreground">
                    旧系统来源（记录文字，不代表当前操作者或授权）
                  </summary>
                  <dl className="mt-3 grid gap-2 break-all">
                    {Object.entries(current.legacySource).map(([k, v]) => (
                      <div key={k}>
                        <dt className="text-muted-foreground">{k}</dt>
                        <dd>{v || "—"}</dd>
                      </div>
                    ))}
                  </dl>
                </details>
              )}
            </>
          )
        )}
      </CardContent>
    </Card>
  );
}
