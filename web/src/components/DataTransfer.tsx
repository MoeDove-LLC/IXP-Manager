import { useState, type ChangeEvent } from "react";
import { Download, Upload, Check } from "lucide-react";
import { api } from "@/lib/api";
import { resources } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardHeader,
  CardTitle,
  CardDescription,
  CardContent,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { ErrorNotice, message } from "@/components/shared";

type ImportPreview = {
  checked: number;
  resourceCounts: Record<string, number>;
  coreBundles: number;
  sflowReceivers: number;
  patching: boolean;
  warnings: string[];
  warningCount: number;
};

export default function DataTransfer({ onChange }: { onChange: () => void }) {
  const [pending, setPending] = useState<File | null>(null);
  const [preview, setPreview] = useState<ImportPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [done, setDone] = useState("");
  const exportData = async () => {
    setBusy(true);
    setError("");
    setDone("");
    try {
      const data = await api<unknown>("/export");
      const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
      const link = document.createElement("a");
      link.href = url;
      link.download = `moedove-resources-${new Date().toISOString().slice(0, 10)}.json`;
      link.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      setError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const inspect = async (file: File) => {
    setBusy(true);
    setPreview(null);
    setError("");
    try {
      const result = await api<ImportPreview>("/import/preview", {
        method: "POST", body: file, headers: { "Content-Type": "application/json" },
      });
      setPreview(result);
    } catch (error) {
      setError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const select = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || busy) return;
    setError("");
    setDone("");
    if (file.size > 8 * 1024 * 1024) {
      setError("导入文件不能超过 8 MiB。");
      return;
    }
    setPending(file);
    void inspect(file);
  };
  const apply = async () => {
    if (!pending || !preview || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await api<{ imported: number }>("/import", {
        method: "POST", body: pending, headers: { "Content-Type": "application/json" },
      });
      setPending(null);
      setPreview(null);
      setDone(`成功导入 ${result.imported} 条记录。`);
      onChange();
    } catch (error) {
      setError(message(error));
      setPreview(null);
    } finally {
      setBusy(false);
    }
  };
  const close = () => {
    setPending(null);
    setPreview(null);
    setError("");
  };

  return (
    <>
      <Card className="mt-6 max-w-3xl">
        <CardHeader>
          <CardTitle>数据导入与导出</CardTitle>
          <CardDescription>迁移资源信息，或导出 JSON 留存。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm leading-6 text-muted-foreground">
            导出包含网络资源。账号、凭据、配置版本和审计记录请使用完整备份。导入会新增记录，发现重复 ID 或无效关联时整批取消。
          </p>
          <ErrorNotice message={!pending ? error : ""} />
          {done && <p role="status" className="flex items-center gap-2 text-sm text-primary"><Check className="size-4" />{done}</p>}
          <div className="flex flex-wrap gap-3">
            <Button type="button" variant="outline" disabled={busy} onClick={() => void exportData()}>
              <Download />导出资源 JSON
            </Button>
            <label className={`inline-flex h-10 items-center gap-2 rounded-md border px-4 text-sm font-medium ${busy ? "pointer-events-none opacity-50" : "cursor-pointer hover:bg-accent"}`}>
              <Upload className="size-4" />选择导入文件
              <input type="file" className="sr-only" accept="application/json,.json" disabled={busy} onChange={select} />
            </label>
          </div>
        </CardContent>
      </Card>
      <Dialog open={!!pending} onOpenChange={open => { if (!busy && !open) close(); }}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">确认导入资源</DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            服务器先按当前库存检查文件。确认后会重新核对最新库存，再一起新增记录。
          </DialogDescription>
          {pending && <p className="break-all text-sm text-muted-foreground">{pending.name}</p>}
          <ErrorNotice message={error} />
          {busy && <p role="status" className="text-sm text-muted-foreground">正在核对资源…</p>}
          {preview && <>
            <p className="text-sm font-medium text-primary">{preview.checked} 条资源通过预检。</p>
            <div className="max-h-44 space-y-2 overflow-auto rounded-lg border p-4">
              {Object.entries(preview.resourceCounts).map(([kind, count]) => (
                <div key={kind} className="flex justify-between gap-3 text-sm">
                  <span className="break-all">{resources[kind]?.label ?? kind}</span>
                  <span className="shrink-0 text-muted-foreground">{count} 条</span>
                </div>
              ))}
              {preview.coreBundles > 0 && <p className="text-sm">核心互联组 {preview.coreBundles} 条</p>}
              {preview.sflowReceivers > 0 && <p className="text-sm">sFlow 接收器 {preview.sflowReceivers} 条</p>}
              {preview.patching && <p className="text-sm">包含旧配线架归档</p>}
            </div>
            {preview.warningCount > 0 && (
              <div className="notice max-h-36 overflow-auto break-words">
                <p className="font-medium">源文件的迁移提示 {preview.warningCount} 条</p>
                <ul className="mt-2 list-disc space-y-1 pl-5">
                  {preview.warnings.map((warning, index) => <li key={index}>{warning}</li>)}
                </ul>
                {preview.warningCount > preview.warnings.length && <p className="mt-2">其余提示请查看源文件的 warnings 字段。</p>}
              </div>
            )}
          </>}
          <div className="flex flex-wrap justify-end gap-2">
            <Button variant="outline" disabled={busy} onClick={close}>取消</Button>
            {!preview && pending && <Button variant="outline" disabled={busy} onClick={() => void inspect(pending)}>重新核对</Button>}
            <Button disabled={busy || !preview} onClick={() => void apply()}>确认导入</Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
