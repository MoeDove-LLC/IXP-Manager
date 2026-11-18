import { useEffect, useRef, useState, type FormEvent } from "react";
import { Download, Pencil, Plus, RefreshCw, Save, X } from "lucide-react";
import { api, APIError, save } from "@/lib/api";
import { MailingArchiveLink, safeRevision, validMailingList, type MailingListDefinition } from "@/components/MailingLists";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";

type Draft = { key: string; name: string; description: string; email: string; archiveUrl: string; enabled: boolean };
type Editor = { id: string | null; revision: number | null; draft: Draft };
type ExportView = { version: number; list: { id: string; key: string; email: string; revision: number; createdAt: string }; checksum: string; subscribed: string[]; unsubscribed: string[]; generatedAt: string; externalApplied: boolean };
const emptyDraft = (): Draft => ({ key: "", name: "", description: "", email: "", archiveUrl: "", enabled: true });
function fields(row: MailingListDefinition): Draft { return { key: row.key, name: row.name, description: row.description, email: row.email, archiveUrl: row.archiveUrl, enabled: row.enabled }; }

export default function MailingListAdmin() {
  const [items, setItems] = useState<MailingListDefinition[]>([]), [editor, setEditor] = useState<Editor | null>(null), [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [done, setDone] = useState(""), [needsRefresh, setNeedsRefresh] = useState(false), [reviewRequired, setReviewRequired] = useState(false);
  const generation = useRef(0), mounted = useRef(false);
  const load = async (preserve: Editor | null = null) => {
    const request = ++generation.current;
    setBusy(true); setReady(false); setError("");
    try {
      const data = await api<{ items: MailingListDefinition[] }>("/mailing-lists");
      if (!mounted.current || request !== generation.current) return;
      if (!data || !Array.isArray(data.items) || !data.items.every(item => validMailingList(item) && typeof item.createdAt === "string") || new Set(data.items.map(item => item.id)).size !== data.items.length) throw new Error("邮件列表定义响应不完整。");
      setItems(data.items); setReady(true); setNeedsRefresh(false);
      if (preserve?.id) {
        const current = data.items.find(item => item.id === preserve.id);
        if (!current) { setReady(false); throw new Error("编辑中的列表已不可用，请取消编辑后重新读取。"); }
        setEditor({ ...preserve, revision: current.revision, draft: { ...preserve.draft, key: current.key, email: current.email } });
        setReviewRequired(true);
      } else setReviewRequired(false);
    } catch (e) { if (mounted.current && request === generation.current) { setError(message(e)); setNeedsRefresh(true); } }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; generation.current++; }; }, []);
  const edit = (row?: MailingListDefinition) => { setError(""); setDone(""); setNeedsRefresh(false); setReviewRequired(false); setEditor(row ? { id: row.id, revision: row.revision, draft: fields(row) } : { id: null, revision: null, draft: emptyDraft() }); };
  const update = (key: keyof Draft, value: string | boolean) => setEditor(previous => previous && { ...previous, draft: { ...previous.draft, [key]: value } });
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!editor || busy || !ready || needsRefresh || reviewRequired) return;
    const baseline = editor, request = ++generation.current;
    setBusy(true); setError(""); setDone("");
    try {
      const { name, description, archiveUrl, enabled } = baseline.draft;
      if (baseline.id) await save(`/mailing-lists/${encodeURIComponent(baseline.id)}`, { revision: baseline.revision, name, description, archiveUrl, enabled }, "PUT");
      else await save("/mailing-lists", baseline.draft);
      if (!mounted.current || request !== generation.current) return;
      setEditor(null); setDone("列表定义已保存；这不会向外部邮件列表发送同步请求。");
      await load();
    } catch (e) {
      if (!mounted.current || request !== generation.current) return;
      setNeedsRefresh(true); setReviewRequired(true);
      setError(e instanceof APIError && e.status === 409 ? "定义已变化或标识已存在。编辑内容已保留，请重新读取并核对。" : `${message(e)} 操作结果需要核对；请重新读取，避免重复创建。`);
    } finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  const exportList = async (row: MailingListDefinition) => {
    if (!ready || busy || !row.enabled || needsRefresh) return;
    const request = ++generation.current;
    setBusy(true); setError(""); setDone("");
    try {
      const result = await api<ExportView>(`/mailing-lists/${encodeURIComponent(row.id)}/export`);
      if (!mounted.current || request !== generation.current) return;
      if (!result || result.version !== 1 || result.externalApplied !== false || result.list?.id !== row.id || result.list.key !== row.key || result.list.email !== row.email || result.list.createdAt !== row.createdAt || !safeRevision(result.list.revision) || result.list.revision !== row.revision || !Array.isArray(result.subscribed) || !result.subscribed.every(value => typeof value === "string") || !Array.isArray(result.unsubscribed) || !result.unsubscribed.every(value => typeof value === "string") || !/^[a-f0-9]{64}$/.test(result.checksum) || typeof result.generatedAt !== "string") throw new Error("导出与当前列表定义不一致，或外部应用状态不明确，请重新读取。");
      const url = URL.createObjectURL(new Blob([JSON.stringify(result, null, 2) + "\n"], { type: "application/json" }));
      const link = document.createElement("a"); link.href = url; link.download = `mailing-list-${row.key}.json`; document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
      setDone(`${row.name}：已导出 ${result.subscribed.length} 个订阅意图、${result.unsubscribed.length} 个退订意图；外部系统尚未确认应用。`);
    } catch (e) { if (mounted.current && request === generation.current) { setError(message(e)); setNeedsRefresh(true); } }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  const current = editor?.id ? items.find(item => item.id === editor.id) : null;
  const duplicate = editor && !editor.id && items.some(item => item.key === editor.draft.key);
  return <section className="space-y-5 min-w-0" aria-label="邮件列表管理">
    <div className="space-y-3"><p className="text-sm leading-7 text-muted-foreground">配置可供用户选择的邮件列表。导出仅包含明确的订阅与退订意图，不包含尚未选择的账号；尚未连接外部 Mailman 同步。</p><div className="flex flex-wrap gap-3"><Button disabled={busy || !ready || needsRefresh} onClick={() => edit()}><Plus />新增邮件列表</Button><Button variant="outline" disabled={busy} onClick={() => void load(editor)}><RefreshCw />{needsRefresh || editor ? "重新读取并核对（保留编辑）" : "刷新邮件列表"}</Button></div></div>
    <ErrorNotice message={error} />{done && <p className="notice break-words" role="status">{done}</p>}{busy && !ready && !items.length && <Loading />}
    {editor && <Card className="min-w-0"><CardHeader><div className="flex items-center justify-between gap-3"><CardTitle>{editor.id ? "编辑邮件列表" : "新建邮件列表"}</CardTitle><Button variant="ghost" size="icon" aria-label="取消邮件列表编辑" disabled={busy} onClick={() => { setEditor(null); setReviewRequired(false); }}><X /></Button></div><CardDescription>标识和列表邮箱创建后固定。停用保留已有偏好，用户仍可选择退订。</CardDescription></CardHeader><CardContent><form className="space-y-5" onSubmit={submit}>
      {current && reviewRequired && <div className="notice space-y-2 text-sm break-words"><p>服务器当前定义：{current.name} · {current.enabled ? "启用" : "禁用"}</p><p className="whitespace-pre-wrap">{current.description}</p><MailingArchiveLink url={current.archiveUrl} /><Button type="button" variant="outline" className="h-auto min-h-10 max-w-full whitespace-normal" disabled={!ready || needsRefresh} onClick={() => setReviewRequired(false)}>已核对当前定义，保留我的编辑</Button></div>}
      <div className="form-grid">{([{ key: "key", label: "列表标识", required: true }, { key: "name", label: "列表名称", required: true }, { key: "email", label: "列表邮箱", type: "email", required: true }, { key: "archiveUrl", label: "邮件归档 URL", type: "url" }] as const).map(field => <div className="field min-w-0" key={field.key}><label htmlFor={`mailing-admin-${field.key}`}>{field.label}</label><Input id={`mailing-admin-${field.key}`} type={"type" in field ? field.type : "text"} required={"required" in field && field.required} value={editor.draft[field.key]} disabled={busy || !!editor.id && ["key", "email"].includes(field.key)} onChange={event => update(field.key, event.target.value)} /></div>)}</div>
      <div className="field min-w-0"><label htmlFor="mailing-admin-description">列表说明</label><textarea id="mailing-admin-description" value={editor.draft.description} disabled={busy} onChange={event => update("description", event.target.value)} rows={3} className="w-full min-w-0 rounded-md border bg-background px-3 py-2 text-sm" /></div>
      <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={editor.draft.enabled} disabled={busy} onChange={event => update("enabled", event.target.checked)} />启用此邮件列表</label>
      {duplicate && <p role="alert" className="text-sm text-destructive">该标识已存在。请取消新建，选择已有列表进行编辑。</p>}
      <Button disabled={busy || !ready || needsRefresh || reviewRequired || !!duplicate}><Save />{editor.id ? "保存列表定义" : "创建邮件列表"}</Button>
    </form></CardContent></Card>}
    {ready && !items.length && <Empty title="尚未配置邮件列表" description="创建列表定义后，用户可在账号安全中保存自己的偏好。" />}
    <div className="space-y-4">{items.map(row => <Card key={row.id} className="min-w-0" data-mailing-definition={row.key}><CardHeader><div className="flex flex-wrap items-start justify-between gap-2"><CardTitle className="break-words min-w-0">{row.name}</CardTitle><Badge variant="outline">{row.enabled ? "启用" : "禁用"}</Badge></div><CardDescription className="whitespace-pre-wrap break-words">{row.description}</CardDescription></CardHeader><CardContent className="min-w-0 space-y-3"><p className="break-all text-sm">{row.key} · {row.email}</p><MailingArchiveLink url={row.archiveUrl} /><p className="text-xs text-muted-foreground">创建于 {time(row.createdAt)}</p><div className="flex flex-wrap gap-3"><Button className="h-auto min-h-10 max-w-full whitespace-normal break-words" variant="outline" disabled={busy || !ready || needsRefresh} onClick={() => edit(row)}><Pencil />编辑 {row.name}</Button><Button className="h-auto min-h-10 max-w-full whitespace-normal break-words" variant="outline" disabled={busy || !ready || needsRefresh || !row.enabled} onClick={() => void exportList(row)}><Download />导出 {row.name} 意图</Button></div>{!row.enabled && <p className="text-xs text-muted-foreground">列表禁用时不能导出新的同步意图。</p>}</CardContent></Card>)}</div>
  </section>;
}
