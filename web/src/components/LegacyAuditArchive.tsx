import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ErrorNotice, message, time } from "@/components/shared";

type Batch = { checksum: string; source: string; sourceTimezone: string; count: number; importedAt: string };
type Entry = { id: string; userId?: string; model: string; modelId?: string; action: string; createdAt?: string; updatedAt?: string };
type Page = { batch: Batch; entries: Entry[]; nextOffset: number | null };

const labels: Record<string, string> = { CREATED: "创建", UPDATED: "更新", DELETED: "删除", UNKNOWN: "未知操作" };

export default function LegacyAuditArchive({ onImported }: { onImported: () => void }) {
  const [batches, setBatches] = useState<Batch[]>([]);
  const [selected, setSelected] = useState("");
  const [offset, setOffset] = useState(0);
  const [page, setPage] = useState<Page | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [uploading, setUploading] = useState(false);
  const [loadingBatches, setLoadingBatches] = useState(true);
  const [loadingPage, setLoadingPage] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    api<{ batches: Batch[] }>("/audit/legacy", { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setBatches(result.batches); })
      .catch(error => { if (!controller.signal.aborted) setError(message(error)); })
      .finally(() => { if (!controller.signal.aborted) setLoadingBatches(false); });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (!selected) return;
    const controller = new AbortController();
    setPage(null);
    setLoadingPage(true);
    api<Page>(`/audit/legacy/${selected}?offset=${offset}`, { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setPage(result); })
      .catch(error => { if (!controller.signal.aborted) setError(message(error)); })
      .finally(() => { if (!controller.signal.aborted) setLoadingPage(false); });
    return () => controller.abort();
  }, [selected, offset]);

  const upload = async () => {
    if (!file || uploading || loadingBatches) return;
    setUploading(true);
    setError("");
    setNotice("");
    try {
      const { batch } = await api<{ batch: Batch }>("/import/audit-archive", {
        method: "POST", body: file, headers: { "Content-Type": "application/json" },
      });
      setBatches(current => [batch, ...current]);
      setOffset(0);
      setSelected(batch.checksum);
      setFile(null);
      setNotice(`已加入 ${batch.count} 条历史审计摘要。`);
      onImported();
    } catch (error) {
      setError(message(error));
    } finally {
      setUploading(false);
    }
  };

  return <Card className="mt-6">
    <CardHeader>
      <CardTitle>旧操作审计归档</CardTitle>
      <CardDescription>查看旧系统的行号、来源用户 ID、操作类型和时间。当前系统的审计记录显示在上方。</CardDescription>
    </CardHeader>
    <CardContent className="space-y-4">
      <ErrorNotice message={error} />
      {notice && <p role="status" className="text-sm text-primary">{notice}</p>}
      <div className="space-y-2">
        <label htmlFor="legacy-audit-file" className="text-sm font-medium">选择旧审计摘要文件</label>
        <Input id="legacy-audit-file" type="file" accept="application/json,.json" disabled={uploading || loadingBatches} onChange={event => {
          const chosen = event.target.files?.[0] ?? null;
          event.target.value = "";
          setError("");
          setNotice("");
          if (chosen && chosen.size > 8 * 1024 * 1024) {
            setFile(null);
            setError("摘要文件不能超过 8 MiB。");
            return;
          }
          setFile(chosen);
        }} />
        {file && <p className="break-all text-xs text-muted-foreground">{file.name}</p>}
        <Button disabled={!file || uploading || loadingBatches} onClick={() => void upload()}>导入历史摘要</Button>
      </div>
      {batches.length > 0 && (
        <div className="space-y-2">
          <label htmlFor="legacy-audit-batch" className="text-sm font-medium">历史批次</label>
          <select id="legacy-audit-batch" value={selected} onChange={event => {
            setOffset(0);
            setSelected(event.target.value);
            setPage(null);
            setError("");
          }}>
            <option value="">请选择批次</option>
            {batches.map(batch => <option key={batch.checksum} value={batch.checksum}>{batch.source} · {batch.count} 条 · {time(batch.importedAt)}</option>)}
          </select>
        </div>
      )}
      {selected && loadingPage && <p role="status" className="text-sm text-muted-foreground">正在读取历史摘要…</p>}
      {selected && page && <>
        <p className="text-xs text-muted-foreground">来源时区：{page.batch.sourceTimezone === "unknown" ? "未确认" : page.batch.sourceTimezone}。下方时间按来源显示。</p>
        <ul className="divide-y rounded-lg border">
          {page.entries.map(entry => <li key={entry.id} className="space-y-1 p-3">
            <p className="break-words text-sm font-medium">{labels[entry.action]} · {entry.model}{entry.modelId ? ` #${entry.modelId}` : ""}</p>
            <p className="break-words text-xs text-muted-foreground">旧行 {entry.id} · {entry.userId ? `来源用户 ${entry.userId}` : "来源用户未记录"}</p>
            <p className="text-xs text-muted-foreground">{entry.createdAt || "来源未记录创建时间"}</p>
          </li>)}
        </ul>
        <div className="flex justify-end gap-2">
          <Button size="sm" variant="outline" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 50))}>上一页</Button>
          <Button size="sm" variant="outline" disabled={page.nextOffset === null} onClick={() => { if (page.nextOffset !== null) setOffset(page.nextOffset); }}>下一页</Button>
        </div>
      </>}
    </CardContent>
  </Card>;
}
