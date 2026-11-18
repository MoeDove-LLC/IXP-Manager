import { useEffect, useState } from "react";
import { APIError, save } from "@/lib/api";
import { sourceName, sourceUserID, staffEntityKey, type CurrentStaff, type LegacyNoteArchive, type LegacyNoteMapping } from "@/lib/legacy-notes";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, message, time } from "@/components/shared";

const states = { pending: "待本人确认", accepted: "已接受", cancelled: "已取消" };
export default function LegacyNoteMappings({ checksum, archive, staff, mappings, refresh }: { checksum: string; archive: LegacyNoteArchive | null; staff: CurrentStaff[]; mappings: LegacyNoteMapping[]; refresh: () => Promise<LegacyNoteMapping[]> }) {
  const [source, setSource] = useState(""), [target, setTarget] = useState<CurrentStaff | null>(null), [busy, setBusy] = useState(false), [error, setError] = useState(""), [done, setDone] = useState(""), [cancel, setCancel] = useState<LegacyNoteMapping | null>(null), [cancelStale, setCancelStale] = useState(false), [cancelError, setCancelError] = useState("");
  const [query, setQuery] = useState(""), [page, setPage] = useState(1);
  useEffect(() => { setSource(""); setPage(1); }, [checksum]);
  useEffect(() => { setPage(1); }, [query]);
  const rows = mappings.filter(item => (!checksum || item.archiveChecksum === checksum) && `${item.sourceUserId} ${item.targetName} ${item.status}`.toLowerCase().includes(query.toLowerCase()));
  const pages = Math.max(1, Math.ceil(rows.length / 20)), currentPage = Math.min(page, pages);
  const sourceRecord = archive?.users.find(item => item.id === source);
  const targetCurrent = target && staff.some(item => staffEntityKey(item) === staffEntityKey(target));
  const create = async () => {
    if (!sourceRecord || !target || !targetCurrent) return;
    setBusy(true); setError(""); setDone("");
    try {
      await save("/notes/legacy/mappings", { archiveChecksum: checksum, sourceUserId: sourceUserID(sourceRecord), targetUserId: target.id, targetUserCreatedAt: target.createdAt });
      setDone("账号映射已创建，目标员工需本人审阅并明确接受。当前订阅未改变。");
      await refresh();
    } catch (e) { setError(`${message(e)} 已保留源账号与目标选择，请刷新后重新核对。`); } finally { setBusy(false); }
  };
  const cancelMapping = async () => {
    if (!cancel || cancelStale) return;
    setBusy(true); setCancelError("");
    try { await save(`/notes/legacy/mappings/${cancel.id}/cancel`, { revision: cancel.revision }); setCancel(null); setDone("待确认映射已取消，当前订阅未改变。"); await refresh(); }
    catch (e) { setCancelError(message(e)); if (e instanceof APIError && [401, 403, 404, 409].includes(e.status)) setCancelStale(true); }
    finally { setBusy(false); }
  };
  const rereadCancel = async () => {
    if (!cancel) return; setBusy(true); setCancelError("");
    try { const rows = await refresh(); const current = rows.find(item => item.id === cancel.id); if (current) { setCancel(current); setCancelStale(current.status !== "pending"); if (current.status !== "pending") setCancelError("映射已不再待确认，不能取消。"); } else { setCancelStale(true); setCancelError("该映射已不可读取，不能取消。"); } }
    catch (e) { setCancelError(message(e)); } finally { setBusy(false); }
  };
  return <section className="space-y-5" aria-label="旧备注账号映射"><Card><CardHeader><CardTitle>明确关联旧账号与当前员工</CardTitle><CardDescription>不会按邮箱或名称自动配对；创建映射不改变当前权限或订阅。</CardDescription></CardHeader><CardContent className="space-y-4"><ErrorNotice message={error} />{done && <p className="notice text-sm" role="status">{done}</p>}<div className="grid gap-4 sm:grid-cols-2"><div className="field"><label htmlFor="legacy-note-source-user">当前批次的旧账号</label><select id="legacy-note-source-user" className="control max-w-full" disabled={busy || !archive} value={source} onChange={e => setSource(e.target.value)}><option value="">请选择旧账号</option>{archive?.users.map(item => <option key={item.id} value={item.id}>{sourceName(item) || "未命名"} · #{sourceUserID(item)}</option>)}</select></div><div className="field"><label htmlFor="legacy-note-target-user">当前员工账号</label><select id="legacy-note-target-user" className="control max-w-full" disabled={busy} value={target ? staffEntityKey(target) : ""} onChange={e => setTarget(staff.find(item => staffEntityKey(item) === e.target.value) ?? null)}><option value="">请选择当前员工</option>{staff.map(item => <option key={staffEntityKey(item)} value={staffEntityKey(item)}>{item.name} · {item.email} · {item.role}</option>)}</select></div></div>
    {target && <p className="break-words text-xs text-muted-foreground">已选择 {target.name} · {target.email} · 账号创建于 {time(target.createdAt)}{!targetCurrent && <strong className="text-amber-700 dark:text-amber-300">。账号身份已变化，请重新选择。</strong>}</p>}
    <div className="flex flex-wrap gap-2"><Button disabled={busy || !sourceRecord || !sourceUserID(sourceRecord) || !targetCurrent || !checksum} onClick={() => void create()}>{busy ? "正在处理…" : "创建待确认账号映射"}</Button><Button variant="outline" disabled={busy} onClick={() => { setError(""); void refresh().catch(e => setError(message(e))); }}>刷新账号与映射，保留选择</Button></div></CardContent></Card>
    <Input aria-label="搜索旧备注账号映射" placeholder="搜索旧账号编号或当前员工" value={query} onChange={e => setQuery(e.target.value)} className="max-w-lg" />
    {rows.length === 0 ? <Empty title="没有匹配的账号映射" description="可以调整搜索条件，或选择来源批次与两个明确账号创建映射。" /> : <><div className="space-y-3">{rows.slice((currentPage - 1) * 20, currentPage * 20).map(item => <Card key={item.id} aria-label={`账号映射 ${item.sourceUserId}`}><CardContent className="min-w-0 space-y-3 p-4"><div className="flex flex-wrap items-start justify-between gap-3"><p className="min-w-0 break-words text-sm font-medium">旧账号 #{item.sourceUserId} → {item.targetName}</p><Badge variant="outline">{states[item.status]}</Badge></div><p className="break-all text-xs text-muted-foreground">来源批次 {item.archiveChecksum.slice(0, 12)} · 版本 {item.revision} · {time(item.updatedAt)}</p>{!item.targetCurrent && <p className="text-xs text-amber-700 dark:text-amber-300">目标员工实体已失效，不会自动关联同编号的新账号。</p>}{item.status === "pending" && <Button size="sm" variant="outline" disabled={busy} onClick={() => { setCancel(item); setCancelStale(false); setCancelError(""); }}>取消待确认映射</Button>}</CardContent></Card>)}</div><div className="flex flex-wrap items-center justify-between gap-3 text-xs"><span>{rows.length} 条 · 第 {currentPage} / {pages} 页</span><div className="flex gap-2"><Button size="sm" variant="outline" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button size="sm" variant="outline" disabled={currentPage >= pages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div></>}
    <Dialog open={!!cancel} onOpenChange={open => !open && !busy && setCancel(null)}><DialogContent><DialogTitle>取消待确认账号映射</DialogTitle><DialogDescription>取消后，目标员工不能再接受此映射；已存在的当前订阅不受影响。</DialogDescription><p className="break-words text-sm">旧账号 #{cancel?.sourceUserId} → {cancel?.targetName} · 版本 {cancel?.revision}</p><ErrorNotice message={cancelError} /><div className="flex flex-wrap justify-end gap-2"><Button variant="outline" disabled={busy} onClick={() => setCancel(null)}>返回</Button>{cancelStale && <Button variant="outline" disabled={busy} onClick={() => void rereadCancel()}>重新读取此映射</Button>}<Button disabled={busy || cancelStale || cancel?.status !== "pending"} onClick={() => void cancelMapping()}>确认取消映射</Button></div></DialogContent></Dialog>
  </section>;
}
