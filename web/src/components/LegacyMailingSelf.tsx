import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api, save } from "@/lib/api";
import { checkedMigrationView, choiceLabels, migrationStates, type MailingMigrationItem, type MailingMigrationReview, type MailingMigrationView } from "@/lib/legacy-mailing";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";

function Acceptance({ item, email, ready, refresh, onAccepted }: { item: MailingMigrationItem; email: string; ready: boolean; refresh: () => Promise<void>; onAccepted: () => void }) {
  const [mapping, setMapping] = useState(item.mapping), [selected, setSelected] = useState(new Set<string>()), [review, setReview] = useState<MailingMigrationReview | null>(null), [ack, setAck] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [needsRefresh, setNeedsRefresh] = useState(false);
  const generation = useRef(0);
  useEffect(() => { generation.current++; setMapping(item.mapping); setReview(null); setAck(false); setNeedsRefresh(false); }, [item, email]);
  useEffect(() => () => { generation.current++; }, []);
  useEffect(() => {
    if (!review) return;
    const timer = window.setTimeout(() => { setReview(null); setAck(false); setNeedsRefresh(true); setError("审阅已过期。选择已保留，请重新读取并审阅。"); }, Math.max(0, Date.parse(review.expiresAt) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [review]);
  const chosen = item.choices.filter(c => selected.has(c.sourceKey));
  const canReview = ready && !busy && !needsRefresh && mapping.status === "pending" && mapping.targetCurrent && chosen.length > 0 && chosen.length === selected.size && chosen.every(c => c.available && c.desired !== "unknown");
  const preview = async () => {
    if (!canReview) return;
    const request = ++generation.current;
    setBusy(true); setError(""); setReview(null); setAck(false);
    try {
      const response = await save<MailingMigrationReview>(`/account/mailing-migrations/${encodeURIComponent(mapping.id)}/review`, { revision: mapping.revision, email, choices: chosen.map(c => ({ sourceKey: c.sourceKey, listRevision: c.listRevision, preferenceRevision: c.preferenceRevision })) });
      if (request !== generation.current) return;
      if (response.email !== email || response.mapping?.id !== mapping.id || !Number.isSafeInteger(response.mapping.revision) || typeof response.reviewToken !== "string" || !response.reviewToken || !Array.isArray(response.choices) || response.choices.length !== chosen.length || new Set(response.choices.map(c => c?.sourceKey)).size !== chosen.length || response.externalApplied !== false || !Number.isFinite(Date.parse(response.expiresAt)) || Date.parse(response.expiresAt) <= Date.now() || response.choices.some(c => !chosen.some(old => old.sourceKey === c.sourceKey && old.listId === c.listId && old.desired === c.desired && old.listRevision === c.listRevision && old.preferenceRevision === c.preferenceRevision))) throw new Error("审阅结果与当前选择不一致，请重新读取。");
      setMapping(response.mapping); setReview(response);
    } catch (e) { if (request === generation.current) { setError(`${message(e)} 选择已保留；请重新读取后再次审阅。`); setNeedsRefresh(true); } }
    finally { if (request === generation.current) setBusy(false); }
  };
  const accept = async () => {
    if (!review || !ack || busy || !ready || Date.parse(review.expiresAt) <= Date.now()) return;
    const request = ++generation.current;
    setBusy(true); setError("");
    try {
      await save(`/account/mailing-migrations/${encodeURIComponent(mapping.id)}/accept`, { revision: review.mapping.revision, reviewToken: review.reviewToken, acknowledge: true });
      if (request !== generation.current) return;
      setReview(null); setAck(false); setSelected(new Set()); onAccepted(); await refresh();
    } catch (e) { if (request === generation.current) { setError(`${message(e)} 请重新读取并核对结果。`); setNeedsRefresh(true); setReview(null); setAck(false); } }
    finally { setBusy(false); }
  };
  return <Card className="min-w-0" data-mailing-migration={mapping.sourceUserId}><CardHeader><div className="flex flex-wrap items-center justify-between gap-2"><CardTitle className="break-all">旧账号 #{mapping.sourceUserId}</CardTitle><Badge variant="outline">{migrationStates[mapping.status]}</Badge></div><CardDescription>将所选的旧订阅偏好迁入当前账号。</CardDescription></CardHeader><CardContent className="space-y-4 min-w-0">
    <ErrorNotice message={error} />
    {mapping.status === "accepted" && <p className="notice text-sm">已完成本次本人确认。可以在邮件列表偏好中查看当前选择；外部同步状态另行确认。</p>}
    {!mapping.targetCurrent && <p className="text-sm text-muted-foreground">账号关联已失效，请联系管理员重新核对。</p>}
    <div className="space-y-3">{item.choices.map(c => <label key={c.sourceKey} className="flex items-start gap-3 rounded-lg border p-3"><input type="checkbox" className="mt-1" aria-label={`选择迁移 ${c.sourceKey}`} checked={selected.has(c.sourceKey)} disabled={!ready || busy || mapping.status !== "pending" || !mapping.targetCurrent || !c.available || c.desired === "unknown"} onChange={e => { const checked = e.target.checked; setSelected(old => { const next = new Set(old); if (checked) next.add(c.sourceKey); else next.delete(c.sourceKey); return next; }); setReview(null); setAck(false); }} /><span className="min-w-0 space-y-1 text-sm"><span className="block break-words font-medium">{c.listName || c.sourceKey} · {choiceLabels[c.desired]}</span><span className="block break-all text-xs text-muted-foreground">旧列表 {c.sourceKey} → {c.listEmail}</span>{mapping.status === "accepted" && <span className="block text-xs text-muted-foreground">{mapping.acceptedChoices?.some(v => v.sourceKey === c.sourceKey) ? "本次已迁入" : "本次未选择，来源仍保留"}</span>}{!c.available && <span className="block break-words text-xs text-muted-foreground">{c.desired === "unknown" ? "来源值无法确定，请在当前邮件列表页面重新选择。" : c.reason}</span>}</span></label>)}</div>
    {mapping.status === "pending" && <div className="flex flex-wrap gap-3"><Button disabled={!canReview} onClick={() => void preview()}>审阅所选迁移</Button><Button variant="outline" disabled={busy} onClick={() => { setReview(null); setAck(false); setError(""); void refresh().catch(e => setError(message(e))); }}>重新读取并核对</Button></div>}
    <Dialog open={!!review} onOpenChange={open => { if (!open && !busy) { setReview(null); setAck(false); } }}><DialogContent><DialogTitle>确认迁入所选邮件偏好</DialogTitle><DialogDescription>核对当前邮箱和每一项选择后，再确认保存。</DialogDescription>
      <p className="break-all text-sm">当前邮箱：{review?.email}</p><div className="max-h-64 space-y-2 overflow-y-auto">{review?.choices.map(c => <p key={c.sourceKey} className="rounded border p-3 text-sm break-words"><strong>{choiceLabels[c.desired]}</strong> {c.listName} <span className="block break-all text-xs text-muted-foreground">{c.listEmail}</span></p>)}</div>
      <p className="text-xs text-muted-foreground">审阅有效至 {time(review?.expiresAt)}。保存后为当前偏好，外部邮件列表是否应用需另行确认。</p>
      <p className="text-xs text-muted-foreground">本次仅迁入所选项目。其余来源项保留在归档，日后可由管理员重新映射再审阅。</p>
      <label className="flex items-start gap-2 text-sm"><input type="checkbox" className="mt-1" checked={ack} disabled={busy} onChange={e => setAck(e.target.checked)} />我已核对当前邮箱及所选订阅、退订方向。</label>
      <div className="flex justify-end gap-3"><Button variant="outline" disabled={busy} onClick={() => { setReview(null); setAck(false); }}>返回选择</Button><Button disabled={busy || !ack || !ready || !review || Date.parse(review.expiresAt) <= Date.now()} onClick={() => void accept()}>确认接受所选偏好</Button></div>
    </DialogContent></Dialog>
  </CardContent></Card>;
}

export default function LegacyMailingSelf() {
  const [view, setView] = useState<MailingMigrationView | null>(null), [busy, setBusy] = useState(false), [ready, setReady] = useState(false), [error, setError] = useState(""), [done, setDone] = useState(""), [page, setPage] = useState(1);
  const generation = useRef(0), mounted = useRef(false);
  const load = async () => {
    const request = ++generation.current; setBusy(true); setReady(false); setError("");
    try { const next = checkedMigrationView(await api<MailingMigrationView>("/account/mailing-migrations")); if (mounted.current && request === generation.current) { setView(next); setReady(true); } }
    catch (e) { if (mounted.current && request === generation.current) setError(message(e)); throw e; }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  useEffect(() => { mounted.current = true; void load().catch(() => {}); return () => { mounted.current = false; generation.current++; }; }, []);
  const pages = Math.max(1, Math.ceil((view?.items.length ?? 0) / 10)), currentPage = Math.min(page, pages);
  return <section className="space-y-5 min-w-0" aria-label="本人旧邮件订阅迁移"><div className="flex flex-wrap items-start justify-between gap-3"><div><h2 className="text-lg font-semibold">本人审阅</h2><p className="text-sm leading-6 text-muted-foreground">逐项选择需要迁入的旧偏好。已有的当前邮箱选择会保留。</p>{view && <p className="break-all text-sm mt-2">当前账号邮箱：{view.email}</p>}</div><Button variant="outline" disabled={busy} onClick={() => { setDone(""); void load().catch(() => {}); }}><RefreshCw />重新读取本人迁移</Button></div><ErrorNotice message={error} />{done && <p role="status" className="notice text-sm">{done}</p>}{busy && !view && <Loading />}
    {ready && !view?.items.length && <Empty title="没有待核对的旧邮件订阅" description="管理员完成账号和列表映射后，会在此显示你的来源记录。" />}
    {view?.items.slice((currentPage - 1) * 10, currentPage * 10).map(item => <Acceptance key={item.mapping.id} item={item} email={view.email} ready={ready && !busy} refresh={load} onAccepted={() => setDone("所选旧偏好已保存。可在当前邮件列表偏好中核对；外部同步需另行确认。")} />)}
    {pages > 1 && <div className="flex flex-wrap items-center justify-between gap-3 text-sm"><span>第 {currentPage} / {pages} 页</span><div className="flex gap-2"><Button variant="outline" disabled={busy || currentPage === 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button variant="outline" disabled={busy || currentPage === pages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div>}
  </section>;
}
