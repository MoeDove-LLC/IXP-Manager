import { useEffect, useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { APIError, save } from "@/lib/api";
import { blankLegacySelection, downloadCanonicalSource, selectionBody, sourceName, type LegacyNoteAcceptance, type LegacyNotePending, type LegacyNoteReview } from "@/lib/legacy-notes";
import { noteModeLabels, type NoteDraft, type NoteMode, type NotePreferences, type NoteSummary } from "@/lib/note-notifications";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, message, time } from "@/components/shared";
import LegacyNoteWatchPicker from "@/components/LegacyNoteWatchPicker";

function PreferenceSummary({ title, value }: { title: string; value: NotePreferences }) {
  return <section className="min-w-0 space-y-2 rounded-lg border p-3"><h4 className="text-sm font-semibold">{title}</h4><p className="text-sm">{noteModeLabels[value.mode]}</p><p className="text-xs text-muted-foreground">成员订阅 {value.memberWatches.length} 项 · 备注订阅 {value.noteWatches.length} 项</p>
    {[...value.memberWatches, ...value.noteWatches].length > 0 && <ul className="max-h-40 space-y-1 overflow-auto text-xs">{[...value.memberWatches, ...value.noteWatches].map(item => <li className="break-words" key={`${item.id}:${item.createdAt}`}>{item.name || item.id}{item.stale ? "（已失效）" : ""}</li>)}</ul>}
  </section>;
}

export default function LegacyNoteAccept({ entry, preferences, summary, refresh, initialSelection, onSelectionChange }: { entry: LegacyNotePending; preferences: NotePreferences; summary: NoteSummary; refresh: () => Promise<void>; initialSelection?: NoteDraft; onSelectionChange: (value: NoteDraft) => void }) {
  const [mapping, setMapping] = useState(entry.mapping), [draft, setDraft] = useState<NoteDraft>(() => initialSelection ?? blankLegacySelection()), [review, setReview] = useState<LegacyNoteReview | null>(null), [ack, setAck] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [needsRefresh, setNeedsRefresh] = useState(false), [accepted, setAccepted] = useState<LegacyNoteAcceptance | null>(null);
  useEffect(() => { setMapping(entry.mapping); setReview(null); setAck(false); }, [entry.mapping]);
  useEffect(() => {
    if (!review) return;
    const remaining = new Date(review.expiresAt).getTime() - Date.now();
    const timer = window.setTimeout(() => { setReview(null); setAck(false); setError("审阅已过期，选择已保留。请重新读取并预览后确认。"); setNeedsRefresh(true); }, Math.max(0, Number.isFinite(remaining) ? remaining : 0));
    return () => window.clearTimeout(timer);
  }, [review]);
  const change = (next: NoteDraft) => { setDraft(next); onSelectionChange(next); setReview(null); setAck(false); setError(""); };
  const reload = async () => {
    setBusy(true); setReview(null); setAck(false); setError("");
    try { await refresh(); setNeedsRefresh(false); } catch (e) { setError(message(e)); } finally { setBusy(false); }
  };
  const preview = async () => {
    setBusy(true); setError(""); setReview(null); setAck(false);
    try {
      const response = await save<LegacyNoteReview>(`/account/note-migrations/${mapping.id}/review`, { revision: mapping.revision, preferenceRevision: preferences.revision, ...selectionBody(draft) });
      setMapping(response.mapping); setReview(response);
    } catch (e) {
      setError(`${message(e)} 选择已保留；重新读取后可以再次预览。`);
      if (e instanceof APIError && [401, 403, 404, 409].includes(e.status)) setNeedsRefresh(true);
    } finally { setBusy(false); }
  };
  const accept = async () => {
    if (!review || !ack) return;
    const reviewed = review;
    setBusy(true); setError("");
    try {
      const response = await save<LegacyNoteAcceptance>(`/account/note-migrations/${mapping.id}/accept`, { revision: reviewed.mapping.revision, reviewToken: reviewed.reviewToken, acknowledge: true });
      setAccepted(response); setMapping(response.mapping); setReview(null); setAck(false);
    } catch (e) {
      setReview(null); setAck(false); setNeedsRefresh(true);
      setError(`${message(e)} 选择已保留，请重新读取并预览；不会自动重试接受。`);
    } finally { setBusy(false); }
  };
  const available = entry.available !== false && mapping.status === "pending" && mapping.targetCurrent;
  return <Card aria-label={`本人迁移 ${entry.sourceUser.id}`}><CardHeader><div className="flex flex-wrap items-start justify-between gap-3"><CardTitle className="break-words">{sourceName(entry.sourceUser) || entry.sourceUser.id}</CardTitle><Badge variant="outline">{accepted || mapping.status === "accepted" ? "已接受" : available ? "待本人确认" : "当前不可接受"}</Badge></div><CardDescription className="break-all">旧账号 {entry.sourceUser.id} · 映射到当前账号 {mapping.targetName}</CardDescription></CardHeader><CardContent className="space-y-5">
    <p className="text-sm leading-6 text-muted-foreground">选择从“不发送备注邮件”开始。旧 missing、default、all 或历史已读时间不会自动成为当前设置；接受时只应用你在这里选择的当前对象。</p>
    <details className="rounded-lg border p-3"><summary className="cursor-pointer text-sm">查看本人规范化来源记录</summary><p className="mt-3 text-xs text-muted-foreground">来源时区：{entry.sourceTimezone || "unknown"}；这是规范化来源 JSON，不是原数据库文件字节。历史阅读记录不表示当前已读。</p><pre className="mt-3 max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{entry.sourceJSON.slice(0, 262144)}</pre>{entry.sourceJSON.length > 262144 && <p className="text-xs text-muted-foreground">预览仅显示前 256 KiB 字符，请下载完整规范化记录。</p>}<Button className="mt-3" variant="outline" size="sm" onClick={() => downloadCanonicalSource(entry.sourceJSON, `${entry.sourceUser.id}-source.json`)}><Download />下载本人来源记录</Button></details>
    <ErrorNotice message={error} />
    {accepted ? <p className="notice text-sm leading-6" role="status">已接受本次选择。未复制旧备注正文或已读游标，未因接受操作发送邮件。当前设置：{noteModeLabels[accepted.preferences.mode]}。</p> : mapping.status === "accepted" ? <p className="notice text-sm leading-6" role="status">该映射已于 {time(mapping.acceptedAt)} 被本人接受。当前订阅可在“备注通知与未读”中查看；此映射不能再次接受。</p> : <>
      {!available && <p className="notice text-sm">此映射已不再待确认，或目标账号身份发生变化。你的选择保留在当前页面，不能提交。</p>}
      <div className="field"><label htmlFor={`legacy-note-mode-${mapping.id}`}>本人选择的通知方式</label><select id={`legacy-note-mode-${mapping.id}`} className="control" value={draft.mode} disabled={busy || !available} onChange={e => change({ ...draft, mode: e.target.value as NoteMode })}>{Object.entries(noteModeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></div>
      {draft.mode === "watched" && <div className="grid gap-6 lg:grid-cols-2"><LegacyNoteWatchPicker label="成员" selected={draft.memberWatches} options={summary.members} disabled={busy || !available} onChange={memberWatches => change({ ...draft, memberWatches })} /><LegacyNoteWatchPicker label="备注" selected={draft.noteWatches} options={summary.items.map(item => ({ ...item, detail: item.memberName }))} disabled={busy || !available} onChange={noteWatches => change({ ...draft, noteWatches })} /></div>}
      <div className="flex flex-wrap gap-2"><Button disabled={busy || needsRefresh || !available} onClick={() => void preview()}>{busy ? "正在处理…" : "预览本人迁移选择"}</Button><Button variant="outline" disabled={busy} onClick={() => void reload()}><RefreshCw />重新读取，保留选择</Button></div>
    </>}
    <Dialog open={!!review} onOpenChange={open => { if (!open && !busy) { setReview(null); setAck(false); } }}><DialogContent className="max-w-3xl"><DialogTitle>本人确认订阅迁移</DialogTitle><DialogDescription>本次预览绑定当前账号、邮箱、映射和订阅版本，有效期五分钟。接受只应用本次明确选择。</DialogDescription>
      {review && <><div className="rounded-lg border p-3 text-sm leading-6"><p>当前邮箱：<strong className="break-all">{review.email}</strong></p><p className="text-xs text-muted-foreground">预览有效至 {time(review.expiresAt)}</p>{!review.deliveryEnabled && <p className="text-xs text-muted-foreground">当前邮件投递关闭。保存偏好不会补发停用期间的变更。</p>}</div>
        <div className="grid gap-3 sm:grid-cols-2"><PreferenceSummary title="当前订阅" value={review.before} /><PreferenceSummary title="本次选择" value={review.after} /></div>
        <label className="flex items-start gap-3 text-sm leading-6"><input type="checkbox" className="mt-1" checked={ack} disabled={busy} onChange={e => setAck(e.target.checked)} /><span>我已核对当前邮箱和本次选择，理解旧阅读记录与备注正文不会复制；{review.after.mode === "none" ? "保持不发送备注邮件。" : "启用投递后，未来符合这些订阅的备注变更会发送到此邮箱。"}</span></label>
        <div className="flex flex-wrap justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => { setReview(null); setAck(false); }}>返回调整</Button><Button disabled={busy || !ack} onClick={() => void accept()}>{busy ? "正在接受…" : "本人确认接受"}</Button></div></>}
    </DialogContent></Dialog>
  </CardContent></Card>;
}
