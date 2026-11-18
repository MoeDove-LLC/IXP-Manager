import { useState } from "react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, message } from "@/components/shared";

type Result = { docstore: { files: number; directories: number; logs: number; reviewFiles: number }; warnings: string[] };
export default function LegacyDocstoreImport({ onChange }: { onChange: () => void }) {
  const [file, setFile] = useState<File | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [result, setResult] = useState<Result | null>(null);
  const upload = async () => {
    if (!file) return; setBusy(true); setError(""); setResult(null);
    try { const body = new FormData(); body.set("bundle", file); setResult(await api<Result>("/import/docstore", { method: "POST", body })); setFile(null); onChange(); }
    catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return <Card className="mt-6 max-w-3xl"><CardHeader><CardTitle>离线文档迁移包</CardTitle><CardDescription>导入旧 Docstore 文件、原目录记录与访问历史。</CardDescription></CardHeader><CardContent className="space-y-4">
    <p className="text-sm leading-6 text-muted-foreground">先导入对应成员，再选择离线工具生成的文档包。原文件逐份保留访问权限；无法确认的权限或成员归属仅工作人员可见。旧访问记录不会转换为成功下载记录。</p>
    <ErrorNotice message={error} />
    <input type="file" aria-label="选择离线文档迁移包" accept=".tar.gz,.tgz,application/gzip" disabled={busy} className="block max-w-full text-sm" onChange={e => {
      const chosen = e.target.files?.[0]; setError(""); setResult(null); setFile(null);
      if (chosen && (!/\.(tar\.gz|tgz)$/i.test(chosen.name) || chosen.size > 1024 * 1024 * 1024 + 8 * 1024 * 1024)) setError("请选择不超过 1 GiB + 8 MiB 的 .tar.gz 文档包。");
      else setFile(chosen ?? null);
    }} />
    <div className="flex flex-wrap gap-2"><Button disabled={!file || busy} onClick={() => void upload()}>{busy ? "正在验证并导入…" : "导入文档迁移包"}</Button><Button variant="outline" onClick={() => { location.hash = "documents?view=legacy"; }}>查看旧文档归档</Button></div>
    {result && <div role="status" className="rounded-lg border p-4 text-sm leading-7">已导入 {result.docstore.files} 份文件、{result.docstore.directories} 条原目录和 {result.docstore.logs} 条访问记录；{result.docstore.reviewFiles} 份文件仅工作人员可见，待核对归属。
      {result.warnings?.length > 0 && <details><summary>核对提示（{result.warnings.length}）</summary>{result.warnings.slice(0, 200).map((warning, i) => <p className="break-words" key={i}>{warning}</p>)}</details>}
    </div>}
  </CardContent></Card>;
}
