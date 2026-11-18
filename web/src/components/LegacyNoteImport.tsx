import { useState } from "react";
import { api } from "@/lib/api";
import type { LegacyNoteBatch } from "@/lib/legacy-notes";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, message } from "@/components/shared";

export default function LegacyNoteImport({ onChange }: { onChange: () => void }) {
  const [file, setFile] = useState<File | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [result, setResult] = useState<LegacyNoteBatch | null>(null);
  const submit = async () => {
    if (!file) return;
    setBusy(true); setError(""); setResult(null);
    try {
      if (file.size > 32 * 1024 * 1024) throw new Error("备注来源包不能超过 32 MiB。");
      // Send the file bytes unchanged. Parsing/stringifying here would round
      // legacy int64 identifiers before the server can verify the checksum.
      const response = await api<{ batch: LegacyNoteBatch }>("/import/note-archive", { method: "POST", body: file, headers: { "Content-Type": "application/json" } });
      setResult(response.batch); setFile(null); onChange();
    } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  return <Card className="mt-6 max-w-3xl"><CardHeader><CardTitle>旧备注与订阅来源归档</CardTitle><CardDescription>导入 ixp-migrate 生成的独立 JSON 来源包，最大 32 MiB。</CardDescription></CardHeader><CardContent className="space-y-4">
    <p className="text-sm leading-6 text-muted-foreground">导入仅保留旧备注和订阅来源，不改变当前备注、已读状态或订阅，也不会发送邮件。账号映射由管理员明确指定，订阅选择需目标员工本人审阅接受。</p>
    <ErrorNotice message={error} />
    <div className="field"><label htmlFor="legacy-note-archive-file">备注来源 JSON 包</label><Input id="legacy-note-archive-file" type="file" accept="application/json,.json" disabled={busy} onChange={e => { const next = e.target.files?.[0] ?? null; setError(""); setResult(null); setFile(null); if (next && next.size > 32 * 1024 * 1024) setError("备注来源包不能超过 32 MiB。"); else setFile(next); }} /></div>
    <div className="flex flex-wrap gap-2"><Button disabled={busy || !file} onClick={() => void submit()}>{busy ? "正在校验并导入…" : "导入备注来源包"}</Button><Button variant="outline" onClick={() => { location.hash = "security?section=legacy-notes"; }}>查看旧备注迁移</Button></div>
    {result && <p role="status" className="notice text-sm leading-6">已归档 {result.notes} 条备注、{result.users} 个旧账号和 {result.preferences} 条订阅来源。当前订阅与已读状态未改变，未产生邮件。</p>}
  </CardContent></Card>;
}
