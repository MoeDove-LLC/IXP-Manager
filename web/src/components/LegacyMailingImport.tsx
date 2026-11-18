import { useRef, useState } from "react";
import { api } from "@/lib/api";
import { mailingArchiveLimit, type MailingBatch } from "@/lib/legacy-mailing";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, message } from "@/components/shared";

export default function LegacyMailingImport({ onImported }: { onImported?: () => void }) {
  const [file, setFile] = useState<File | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [result, setResult] = useState<MailingBatch | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const submit = async () => {
    if (!file || busy) return;
    setBusy(true); setError(""); setResult(null);
    try {
      if (file.size > mailingArchiveLimit) throw new Error("邮件订阅来源包不能超过 16 MiB。");
      const response = await api<{ batch: MailingBatch }>("/import/mailing-archive", { method: "POST", body: file, headers: { "Content-Type": "application/json" } });
      if (!response.batch || !/^[a-f0-9]{64}$/.test(response.batch.checksum)) throw new Error("导入响应不完整，请刷新批次核对。");
      setResult(response.batch); setFile(null); if (input.current) input.current.value = ""; onImported?.();
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return <Card className="mt-6 max-w-3xl min-w-0"><CardHeader><CardTitle>旧邮件订阅来源</CardTitle><CardDescription>导入 ixp-migrate --mailing-archive 生成的独立 JSON 包。</CardDescription></CardHeader><CardContent className="space-y-4">
    <p className="text-sm leading-6 text-muted-foreground">先归档来源，再由管理员明确关联账号和列表，最后由目标账号本人选择接受。当前偏好仅在本人确认后更新。</p>
    <ErrorNotice message={error} />
    <div className="field"><label htmlFor="legacy-mailing-file">邮件订阅来源 JSON（最大 16 MiB）</label><Input ref={input} id="legacy-mailing-file" type="file" accept="application/json,.json" disabled={busy} onChange={e => { const next = e.target.files?.[0] ?? null; setResult(null); setError(""); setFile(null); if (next && next.size > mailingArchiveLimit) setError("邮件订阅来源包不能超过 16 MiB。"); else setFile(next); }} /></div>
    <div className="flex flex-wrap gap-3"><Button disabled={busy || !file} onClick={() => void submit()}>{busy ? "正在校验来源…" : "导入邮件订阅来源"}</Button><Button variant="outline" disabled={busy} onClick={() => { location.hash = "security?section=legacy-mailing"; }}>查看邮件订阅迁移</Button></div>
    {result && <p role="status" className="notice text-sm leading-6">已归档 {result.users} 个旧账号、{result.choices} 条选择记录。请继续建立明确映射，等待本人审阅。</p>}
  </CardContent></Card>;
}
