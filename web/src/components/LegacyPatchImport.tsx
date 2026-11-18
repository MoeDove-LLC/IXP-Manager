import { useRef, useState } from "react";
import { Archive, CheckCircle2, Upload } from "lucide-react";
import { api } from "@/lib/api";
import { fileBytes, sourceTimezone, type LegacyPatchBatch } from "@/lib/legacy-patching";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, message, time } from "@/components/shared";
type Result = { imported: number; patching: LegacyPatchBatch; warnings: string[]; legacySnapshots: boolean; loaAuthorizationImported: boolean };
export default function LegacyPatchImport({ onChange }: { onChange: () => void }) {
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null), [busy, setBusy] = useState(false), [ack, setAck] = useState(false), [error, setError] = useState(""), [result, setResult] = useState<Result | null>(null);
  const select = (selected?: File) => {
    if (!selected) return; setFile(null); setError(""); setResult(null); setAck(false);
    if (!/\.(tar\.gz|tgz)$/i.test(selected.name)) { setError("请选择 ixp-migrate 生成的 .tar.gz 配线迁移包。"); return; }
    if (selected.size > 1024 * 1024 * 1024 + 8 * 1024 * 1024) { setError("压缩迁移包超过 1 GiB + 8 MiB 的传输上限。"); return; }
    setFile(selected);
  };
  const upload = async () => {
    if (!file || !ack) return; setBusy(true); setError("");
    try { const body = new FormData(); body.set("bundle", file); const data = await api<Result>("/import/patching", { method: "POST", body }); setResult(data); setFile(null); onChange(); }
    catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return <>
    <Card className="mt-6 max-w-3xl"><CardHeader><CardTitle className="flex items-center gap-2"><Archive className="size-4 text-primary" />离线配线迁移包</CardTitle><CardDescription>迁移配线来源、旧分配快照与实际附件，保留原保存键和内容摘要。</CardDescription></CardHeader><CardContent className="space-y-4">
      <p className="text-sm leading-6 text-muted-foreground">选择离线工具生成的压缩包。服务器会核对来源关联、文件大小和摘要后整批提交；待核对的配线不会自动加入可分配库存。旧 LOA 凭据不会恢复，旧 PDF 仅作为只读附件。</p>
      <ErrorNotice message={!file ? error : ""} />
      <input id="patch-bundle-file" ref={input} type="file" accept=".tar.gz,.tgz,application/gzip" className="sr-only" aria-label="选择离线配线迁移包" disabled={busy} onChange={(e) => { select(e.target.files?.[0]); e.target.value = ""; }} />
      <div className="flex flex-wrap gap-3"><Button variant="outline" disabled={busy} onClick={() => input.current?.click()}><Upload />选择配线迁移包</Button><Button variant="ghost" onClick={() => { location.hash = "patching?view=legacy"; }}>查看遗留配线归档</Button></div>
      {result && <section aria-label="配线导入结果" role="status" className="space-y-4 rounded-lg border border-primary/25 bg-primary/5 p-4"><p className="flex items-center gap-2 text-sm font-medium"><CheckCircle2 className="size-4 text-primary" />配线迁移包已导入</p>
        <dl className="grid grid-cols-2 gap-4 text-sm">{[["新增资源", result.imported], ["已建立配线架", result.patching.panels], ["待核对配线架", result.patching.reviewPanels], ["待核对端口", result.patching.reviewPorts], ["旧分配快照", result.patching.histories], ["只读附件", result.patching.attachments]].map(([label, value]) => <div key={label}><dt className="text-xs text-muted-foreground">{label}</dt><dd className="mt-1 font-semibold">{value}</dd></div>)}</dl>
        <p className="text-xs leading-6 text-muted-foreground">导入时间：{time(result.patching.importedAt)} · {sourceTimezone(result.patching.sourceTimezone)}<br />旧历史保留为来源快照；未导入旧 LOA 授权。</p>
        {result.warnings?.length > 0 && <details><summary className="cursor-pointer text-sm">查看 {result.warnings.length} 条核对提示</summary><div className="mt-3 max-h-60 space-y-2 overflow-auto text-xs">{result.warnings.slice(0, 200).map((warning, i) => <p className="break-words" key={i}>{warning}</p>)}{result.warnings.length > 200 && <p>其余提示请查看离线转换日志。</p>}</div></details>}
      </section>}
    </CardContent></Card>
    <Dialog open={!!file} onOpenChange={(open) => !busy && !open && setFile(null)}><DialogContent>
      <DialogTitle>确认导入离线配线包</DialogTitle><DialogDescription>只导入经过核对的离线迁移结果；服务器将检查整包及每份附件，重复 ID 或无效内容会拒绝提交。</DialogDescription>
      <ErrorNotice message={error} /><div className="rounded-lg border p-4"><p className="break-all text-sm font-medium">{file?.name}</p><p className="mt-2 text-xs text-muted-foreground">{fileBytes(file?.size ?? 0)}</p></div>
      <label className="flex items-start gap-3 text-sm leading-6"><input id="patch-bundle-ack" className="mt-1" type="checkbox" checked={ack} disabled={busy} onChange={(e) => setAck(e.target.checked)} /><span>我已核对来源和迁移范围，理解旧历史与附件保留为只读来源，旧 LOA 不会获得新的授权效力。</span></label>
      <div className="flex flex-wrap justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setFile(null)}>取消</Button><Button disabled={busy || !ack} onClick={() => void upload()}>{busy ? "正在验证并导入…" : "确认导入配线包"}</Button></div>
    </DialogContent></Dialog>
  </>;
}
