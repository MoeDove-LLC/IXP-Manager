import { useEffect, useRef, useState } from "react";
import { Mail, RefreshCw, Save } from "lucide-react";
import { api, APIError, save } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, ErrorNotice, Loading, message } from "@/components/shared";

export type MailingListDefinition = { id: string; key: string; name: string; description: string; email: string; archiveUrl: string; enabled: boolean; revision: number; createdAt?: string };
type PreferenceItem = MailingListDefinition & { listRevision: number; preferenceRevision: number; desired: "unset" | "subscribed" | "unsubscribed"; staleEmail: boolean };
type PreferenceView = { email: string; items: PreferenceItem[]; synchronization: "external-not-observed" };
export const safeRevision = (value: unknown): value is number => typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
export function validMailingList(item: MailingListDefinition) {
  return item && [item.id, item.key, item.name, item.description, item.email, item.archiveUrl].every(value => typeof value === "string") && typeof item.enabled === "boolean" && safeRevision(item.revision);
}
function checkedView(view: PreferenceView): PreferenceView {
  if (!view || typeof view.email !== "string" || view.synchronization !== "external-not-observed" || !Array.isArray(view.items) || !view.items.every(item => validMailingList(item) && safeRevision(item.listRevision) && safeRevision(item.preferenceRevision) && ["unset", "subscribed", "unsubscribed"].includes(item.desired) && typeof item.staleEmail === "boolean") || new Set(view.items.map(item => item.id)).size !== view.items.length) throw new Error("邮件列表响应不完整，请重新读取。");
  return view;
}
export function MailingArchiveLink({ url }: { url: string }) {
  let href = "";
  try { const parsed = new URL(url); if (["http:", "https:"].includes(parsed.protocol) && !parsed.username && !parsed.password) href = parsed.href; } catch { /* Invalid links remain plain text. */ }
  return url ? <p className="min-w-0 break-all text-xs leading-6 text-muted-foreground">归档：{href ? <a href={href} target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-4">{url}</a> : url}</p> : null;
}
const desiredLabel = { unset: "尚未选择", subscribed: "希望订阅", unsubscribed: "希望退订" };

export default function MailingLists() {
  const [view, setView] = useState<PreferenceView | null>(null), [drafts, setDrafts] = useState<Record<string, boolean>>({}), [error, setError] = useState(""), [done, setDone] = useState(""), [busy, setBusy] = useState(false), [ready, setReady] = useState(false), [needsRefresh, setNeedsRefresh] = useState(false);
  const generation = useRef(0), mounted = useRef(false);
  const load = async () => {
    const request = ++generation.current;
    setBusy(true); setReady(false); setError(""); setDone("");
    try {
      const next = checkedView(await api<PreferenceView>("/account/mailing-lists"));
      if (!mounted.current || request !== generation.current) return;
      setView(next); setReady(true); setNeedsRefresh(false);
    } catch (e) { if (mounted.current && request === generation.current) { setError(message(e)); setNeedsRefresh(true); } }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; generation.current++; }; }, []);
  const submit = async (item: PreferenceItem) => {
    if (!view || !ready || busy || needsRefresh) return;
    const selected = drafts[item.id] ?? (item.desired === "unset" ? undefined : item.desired === "subscribed");
    if (selected === undefined || selected && !item.enabled) return;
    const request = ++generation.current, email = view.email;
    setBusy(true); setError(""); setDone("");
    try {
      const next = checkedView(await save<PreferenceView>(`/account/mailing-lists/${encodeURIComponent(item.id)}`, { listRevision: item.listRevision, preferenceRevision: item.preferenceRevision, email, subscribed: selected }, "PUT"));
      if (!mounted.current || request !== generation.current) return;
      if (next.email !== email || !next.items.some(row => row.id === item.id)) throw new Error("保存结果需要重新核对，请重新读取。");
      // Other edited rows retain their reviewed CAS baselines. A successful PUT
      // must not silently rebase a different unsaved preference onto new data.
      setView(previous => previous ? { ...next, items: next.items.map(row => row.id !== item.id && Object.hasOwn(drafts, row.id) ? previous.items.find(old => old.id === row.id) ?? row : row) } : next);
      setDrafts(previous => { const copy = { ...previous }; delete copy[item.id]; return copy; });
      setDone(`${item.name}：偏好已保存；外部邮件列表尚未确认同步。`);
    } catch (e) {
      if (!mounted.current || request !== generation.current) return;
      setNeedsRefresh(true);
      setError(e instanceof APIError && e.status === 409 ? "列表、邮箱或偏好已变化。你的选择已保留；请重新读取并核对后再保存。" : `${message(e)} 请重新读取并核对保存结果；你的选择已保留。`);
    } finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  return <section className="space-y-5 min-w-0" aria-label="我的邮件列表偏好">
    <Card className="min-w-0"><CardHeader><CardTitle className="flex items-center gap-2"><Mail className="size-5" />邮件列表偏好</CardTitle><CardDescription>这里保存你希望订阅或退订的意图。外部邮件列表的实际成员状态尚未确认，保存不代表已经加入或退出。</CardDescription></CardHeader><CardContent className="space-y-4 min-w-0">
      {view && <p className="break-all text-sm">当前账号邮箱：<strong>{view.email || "尚未设置"}</strong></p>}
      <Button variant="outline" disabled={busy} onClick={() => void load()}><RefreshCw />{needsRefresh ? "重新读取并核对（保留选择）" : "刷新列表（保留选择）"}</Button>
      <ErrorNotice message={error} />{done && <p role="status" className="notice break-words">{done}</p>}
    </CardContent></Card>
    {!view && busy && <Loading />}
    {view && !view.items.length && <Empty title="暂无邮件列表" description="管理员配置邮件列表后，你可以在这里选择偏好。" />}
    {view?.items.map(item => {
      const chosen = Object.hasOwn(drafts, item.id) ? drafts[item.id] ? "subscribed" : "unsubscribed" : item.desired;
      const dirty = Object.hasOwn(drafts, item.id) || item.staleEmail && item.desired !== "unset";
      return <Card key={item.id} className="min-w-0" data-mailing-list={item.key}><CardHeader><div className="flex flex-wrap items-start justify-between gap-2"><CardTitle className="break-words min-w-0">{item.name}</CardTitle><Badge variant="outline">{item.enabled ? "可选择订阅" : "已禁用"}</Badge></div><CardDescription className="whitespace-pre-wrap break-words">{item.description}</CardDescription></CardHeader><CardContent className="min-w-0 space-y-4">
        <p className="break-all text-sm text-muted-foreground">列表地址：{item.email}</p><MailingArchiveLink url={item.archiveUrl} />
        <p className="text-sm">当前已保存偏好：{desiredLabel[item.desired]}</p>
        {item.staleEmail && <p className="notice text-sm">账号邮箱已改变，旧偏好需要按当前邮箱重新确认；不会把旧邮箱的选择自动当作新邮箱订阅。</p>}
        {!item.enabled && <p className="text-xs text-muted-foreground">此列表不接受新的订阅偏好，仍可明确选择退订。</p>}
        <div className="flex flex-col gap-3 sm:flex-row sm:items-end"><div className="field min-w-0 flex-1"><label htmlFor={`mailing-choice-${item.id}`}>希望如何处理 {item.name}</label><select id={`mailing-choice-${item.id}`} className="h-10 w-full min-w-0 rounded-md border bg-background px-3 text-sm" value={chosen} disabled={busy || !ready} onChange={e => { setDone(""); setDrafts(previous => ({ ...previous, [item.id]: e.target.value === "subscribed" })); }}><option value="unset" disabled>请选择订阅或退订</option><option value="subscribed" disabled={!item.enabled}>希望订阅</option><option value="unsubscribed">希望退订</option></select></div><Button className="h-auto min-h-10 max-w-full whitespace-normal break-words" disabled={busy || !ready || needsRefresh || !view.email || !dirty || chosen === "unset" || chosen === "subscribed" && !item.enabled} onClick={() => void submit(item)}><Save />保存 {item.name} 偏好</Button></div>
      </CardContent></Card>;
    })}
  </section>;
}
