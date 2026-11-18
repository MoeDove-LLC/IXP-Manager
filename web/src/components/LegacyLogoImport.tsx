import { useState } from "react";
import { api, APIError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, message } from "@/components/shared";
type Review = {
  reviewToken: string;
  checksum: string;
  bytes: number;
  items: {
    memberId: string;
    memberName: string;
    width: number;
    height: number;
    size: number;
    source: { originalName: string; uploadedBy: string; uploadedAt: string };
  }[];
};
export default function LegacyLogoImport({
  onChange,
}: {
  onChange: () => void;
}) {
  const [file, setFile] = useState<File | null>(null),
    [review, setReview] = useState<Review | null>(null),
    [ack, setAck] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState("");
  const submit = async (commit: boolean) => {
    if (!file || (commit && (!review || !ack))) return;
    setBusy(true);
    setError("");
    setDone("");
    try {
      if (file.size > 48 * 1024 * 1024)
        throw new Error("Logo 迁移包不能超过 48 MiB。");
      const body = new FormData();
      body.set("bundle", file);
      if (commit && review) body.set("reviewToken", review.reviewToken);
      if (commit) {
        const result = await api<{ imported: number }>("/import/member-logos", {
          method: "POST",
          body,
        });
        setDone(`已导入 ${result.imported} 个成员 Logo，全部保持私有。`);
        setReview(null);
        setFile(null);
        setAck(false);
        onChange();
      } else {
        setReview(
          await api<Review>("/import/member-logos/preview", {
            method: "POST",
            body,
          }),
        );
        setAck(false);
      }
    } catch (e) {
      setError(message(e));
      if (e instanceof APIError && e.status === 409) {
        setReview(null);
        setAck(false);
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <Card className="mt-6 max-w-3xl">
      <CardHeader>
        <CardTitle>离线成员 Logo 迁移</CardTitle>
        <p className="text-sm text-muted-foreground">
          先导入成员库存，再选择 ixp-migrate 生成的专用 Logo JSON 包。
        </p>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-sm leading-6 text-muted-foreground">
          只读取包内的实际 PNG，不访问旧站点或任意网址。导入前核对目标成员；已有
          Logo 历史的成员需使用成员 Logo 编辑器。旧系统公开状态不会恢复。
        </p>
        <ErrorNotice message={error} />
        {done && (
          <p className="notice" role="status">
            {done}
          </p>
        )}
        <div className="field">
          <label htmlFor="legacy-logo-bundle">Logo 迁移包</label>
          <Input
            id="legacy-logo-bundle"
            type="file"
            accept="application/json,.json"
            disabled={busy}
            onChange={(e) => {
              setFile(e.target.files?.[0] ?? null);
              setReview(null);
              setAck(false);
              setError("");
              setDone("");
            }}
          />
        </div>
        <Button
          variant="outline"
          disabled={busy || !file}
          onClick={() => void submit(false)}
        >
          {busy ? "正在验证…" : "预览 Logo 导入"}
        </Button>
        {review && (
          <section
            aria-label="Logo 导入审核"
            className="space-y-4 rounded-lg border p-4"
          >
            <p className="text-sm font-medium">
              {review.items.length} 个成员 · {(review.bytes / 1024).toFixed(1)}{" "}
              KiB · 全部私有
            </p>
            <div className="max-h-64 space-y-3 overflow-auto">
              {review.items.map((item) => (
                <div
                  key={item.memberId}
                  className="border-b pb-3 last:border-0"
                >
                  <p className="text-sm font-medium break-words">
                    {item.memberName}
                  </p>
                  <p className="break-all text-xs text-muted-foreground">
                    {item.memberId} · {item.width} × {item.height} px ·{" "}
                    {item.source.originalName}
                  </p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    旧上传者文字：{item.source.uploadedBy || "未知"} ·
                    原始时间：{item.source.uploadedAt || "未知"}
                  </p>
                </div>
              ))}
            </div>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                disabled={busy}
                checked={ack}
                onChange={(e) => setAck(e.target.checked)}
              />
              <span>已核对目标成员与图片来源；导入后逐成员审核公开发布。</span>
            </label>
            <Button disabled={busy || !ack} onClick={() => void submit(true)}>
              确认私有导入 Logo
            </Button>
          </section>
        )}
      </CardContent>
    </Card>
  );
}
