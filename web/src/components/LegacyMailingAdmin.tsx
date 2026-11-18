import { useEffect, useRef, useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { api, save } from "@/lib/api";
import { downloadMailingSource, entityKey, listKey, migrationStates, sourceLabel, type MailingArchive, type MailingBatch, type MailingMapping, type MailingTargetList, type MailingTargetUser } from "@/lib/legacy-mailing";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";
import LegacyMailingImport from "@/components/LegacyMailingImport";

type Data = { batches: MailingBatch[]; mappings: MailingMapping[]; canManage: boolean };
export default function LegacyMailingAdmin() {
  const [data, setData] = useState<Data | null>(null), [users, setUsers] = useState<MailingTargetUser[]>([]), [lists, setLists] = useState<MailingTargetList[]>([]), [checksum, setChecksum] = useState(""), [archive, setArchive] = useState<MailingArchive | null>(null), [source, setSource] = useState(""), [target, setTarget] = useState<MailingTargetUser | null>(null), [bindings, setBindings] = useState(new Map<string, MailingTargetList>()), [sourceQuery, setSourceQuery] = useState(""), [targetQuery, setTargetQuery] = useState(""), [busy, setBusy] = useState(false), [ready, setReady] = useState(false), [error, setError] = useState(""), [done, setDone] = useState(""), [cancel, setCancel] = useState<MailingMapping | null>(null), [page, setPage] = useState(1);
  const generation = useRef(0), archiveGeneration = useRef(0), mounted = useRef(false);
  const load = async () => {
    const request = ++generation.current; setBusy(true); setReady(false); setError("");
    try {
      const results = await Promise.allSettled([api<Data>("/mailing-lists/legacy"), api<{ items: MailingTargetUser[] }>("/resources/users"), api<{ items: MailingTargetList[] }>("/mailing-lists")]);
      if (!mounted.current || request !== generation.current) return;
      const failure = results.find(r => r.status === "rejected");
      if (failure?.status === "rejected") throw failure.reason;
      const summary = (results[0] as PromiseFulfilledResult<Data>).value, currentUsers = (results[1] as PromiseFulfilledResult<{ items: MailingTargetUser[] }>).value.items, currentLists = (results[2] as PromiseFulfilledResult<{ items: MailingTargetList[] }>).value.items;
      if (!summary.canManage || !Array.isArray(summary.batches) || !summary.batches.every(b => typeof b.checksum === "string" && /^[a-f0-9]{64}$/.test(b.checksum)) || !Array.isArray(summary.mappings) || !summary.mappings.every(m => typeof m.sourceUserId === "string" && Number.isSafeInteger(m.revision) && Object.hasOwn(migrationStates, m.status)) || !Array.isArray(currentUsers) || !currentUsers.every(u => typeof u.id === "string" && typeof u.createdAt === "string") || !Array.isArray(currentLists) || !currentLists.every(l => typeof l.id === "string" && typeof l.createdAt === "string" && Number.isSafeInteger(l.revision))) throw new Error("管理员迁移资料不完整，请重新读取。");
      setData(summary); setUsers(currentUsers); setLists(currentLists); setReady(true);
      setCancel(old => old ? summary.mappings.find(m => m.id === old.id) ?? null : null);
    } catch (e) { if (mounted.current && request === generation.current) setError(message(e)); }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  useEffect(() => { mounted.current = true; void load(); return () => { mounted.current = false; generation.current++; archiveGeneration.current++; }; }, []);
  const chooseBatch = async (next: string) => {
    const request = ++archiveGeneration.current;
    setChecksum(next); setArchive(null); setSource(""); setTarget(null); setBindings(new Map()); setPage(1); setError(""); setDone("");
    if (!next) return;
    try {
      const response = await api<{ archive: MailingArchive; readOnly: boolean }>(`/mailing-lists/legacy/archives/${next}`);
      if (!mounted.current || request !== archiveGeneration.current) return;
      const a = response.archive;
      if (!response.readOnly || !a || a.checksum !== next || a.kind !== "ixp-manager-mailing-sources" || !Array.isArray(a.users) || !a.users.every(u => typeof u.legacyId === "string" && u.facts && typeof u.facts === "object") || !Array.isArray(a.choices) || !a.choices.every(c => typeof c.sourceUserId === "string" && typeof c.sourceKey === "string")) throw new Error("来源批次内容不完整。");
      setArchive(a);
    } catch (e) { if (mounted.current && request === archiveGeneration.current) { setError(message(e)); setReady(false); } }
  };
  const selectedSource = archive?.users.find(u => u.legacyId === source);
  const keys = [...new Set(archive?.choices.filter(c => c.sourceUserId === source).map(c => c.sourceKey) ?? [])];
  const currentTarget = target && users.some(u => entityKey(u) === entityKey(target));
  const currentBindings = [...bindings].every(([key, list]) => keys.includes(key) && lists.some(l => listKey(l) === listKey(list)));
  const duplicateTargets = new Set([...bindings.values()].map(l => l.id)).size !== bindings.size;
  const canCreate = ready && !busy && !!selectedSource && !!currentTarget && bindings.size > 0 && currentBindings && !duplicateTargets;
  const create = async () => {
    if (!canCreate || !target) return;
    const request = ++generation.current;
    setBusy(true); setReady(false); setError(""); setDone("");
    try {
      await save("/mailing-lists/legacy/mappings", { archiveChecksum: checksum, sourceUserId: source, targetUserId: target.id, targetUserCreatedAt: target.createdAt, lists: [...bindings].map(([sourceKey, list]) => ({ sourceKey, listId: list.id, listCreatedAt: list.createdAt, listRevision: list.revision })) });
      if (!mounted.current || request !== generation.current) return;
      setSource(""); setTarget(null); setBindings(new Map()); setDone("映射已建立，等待目标账号本人选择并确认。"); await load();
    } catch (e) { if (mounted.current && request === generation.current) setError(`${message(e)} 已保留选择，请刷新并核对后再提交。`); }
    finally { if (mounted.current) setBusy(false); }
  };
  const cancelMapping = async () => {
    if (!cancel || !ready || busy || cancel.status !== "pending") return;
    const request = ++generation.current;
    setBusy(true); setReady(false); setError(""); setDone("");
    try {
      await save(`/mailing-lists/legacy/mappings/${encodeURIComponent(cancel.id)}/cancel`, { revision: cancel.revision });
      if (!mounted.current || request !== generation.current) return;
      setCancel(null); setDone("待确认映射已取消。可以重新核对账号和列表后建立新的映射。"); await load();
    } catch (e) { if (mounted.current && request === generation.current) setError(`${message(e)} 请重新读取当前映射。`); }
    finally { if (mounted.current) setBusy(false); }
  };
  const download = async () => {
    if (!ready || busy || !archive) return;
    const request = ++generation.current;
    setBusy(true); setError("");
    try { await downloadMailingSource(checksum, () => mounted.current && request === generation.current); }
    catch (e) { if (mounted.current && request === generation.current) { setError(message(e)); setReady(false); } }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  const matchingSources = archive?.users.filter(u => `${u.legacyId} ${sourceLabel(u)}`.toLowerCase().includes(sourceQuery.toLowerCase())) ?? [];
  const sourceOptions = matchingSources.slice(0, 100); if (selectedSource && !sourceOptions.includes(selectedSource)) sourceOptions.unshift(selectedSource);
  const matchingTargets = users.filter(u => `${u.name} ${u.email} ${u.role}`.toLowerCase().includes(targetQuery.toLowerCase()));
  const targetOptions = matchingTargets.slice(0, 100); if (target && currentTarget && !targetOptions.some(u => entityKey(u) === entityKey(target))) targetOptions.unshift(target);
  const mappings = data?.mappings.filter(m => !checksum || m.archiveChecksum === checksum) ?? [], pages = Math.max(1, Math.ceil(mappings.length / 20)), currentPage = Math.min(page, pages);
  return <section className="space-y-5 min-w-0" aria-label="管理员旧邮件订阅映射"><LegacyMailingImport onImported={() => { void load(); }} /><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-lg font-semibold">关联旧账号和邮件列表</h2><Button variant="outline" disabled={busy} onClick={() => void load()}><RefreshCw />刷新映射资料</Button></div><ErrorNotice message={error} />{done && <p className="notice text-sm" role="status">{done}</p>}{busy && !data && <Loading />}
    <Card className="min-w-0"><CardHeader><CardTitle>建立待确认映射</CardTitle><CardDescription>明确指定来源账号、当前账号及每个列表。目标账号本人审阅后，才保存所选偏好。</CardDescription></CardHeader><CardContent className="space-y-5 min-w-0">
      <div className="field"><label htmlFor="legacy-mailing-batch">来源批次</label><select id="legacy-mailing-batch" className="control w-full min-w-0" disabled={!ready || busy} value={checksum} onChange={e => void chooseBatch(e.target.value)}><option value="">请选择来源批次</option>{data?.batches.map(b => <option key={b.checksum} value={b.checksum}>{time(b.importedAt)} · {b.users} 个账号 · {b.checksum.slice(0, 12)}</option>)}</select></div>
      {checksum && !archive && !error && <Loading />}
      {archive && <><Button variant="outline" disabled={!ready || busy} onClick={() => void download()}><Download />下载原始来源 JSON</Button><div className="grid gap-5 md:grid-cols-2"><div className="space-y-3 min-w-0"><Input aria-label="搜索旧邮件账号" value={sourceQuery} onChange={e => setSourceQuery(e.target.value)} placeholder="搜索旧编号、姓名或邮箱" /><div className="field"><label htmlFor="legacy-mailing-source">旧账号</label><select id="legacy-mailing-source" className="control w-full min-w-0" disabled={!ready || busy} value={source} onChange={e => { setSource(e.target.value); setBindings(new Map()); }}><option value="">请选择旧账号</option>{sourceOptions.map(u => <option key={u.legacyId} value={u.legacyId}>#{u.legacyId} · {sourceLabel(u)}</option>)}</select></div>{matchingSources.length > 100 && <p className="text-xs text-muted-foreground">匹配 {matchingSources.length} 个，显示前 100 个；可继续缩小搜索。</p>}</div><div className="space-y-3 min-w-0"><Input aria-label="搜索当前邮件账号" value={targetQuery} onChange={e => setTargetQuery(e.target.value)} placeholder="搜索当前姓名或邮箱" /><div className="field"><label htmlFor="legacy-mailing-target">当前账号</label><select id="legacy-mailing-target" className="control w-full min-w-0" disabled={!ready || busy} value={target ? entityKey(target) : ""} onChange={e => setTarget(users.find(u => entityKey(u) === e.target.value) ?? null)}><option value="">请选择当前账号</option>{targetOptions.map(u => <option key={entityKey(u)} value={entityKey(u)}>{u.name} · {u.email} · {u.role}</option>)}</select></div>{matchingTargets.length > 100 && <p className="text-xs text-muted-foreground">匹配 {matchingTargets.length} 个，显示前 100 个。</p>}</div></div>
        {target && <p className="text-xs break-all text-muted-foreground">当前选择：{target.name} · {target.email} · 创建于 {time(target.createdAt)}{!currentTarget && "。账号身份已变化，请重新选择。"}</p>}
        {selectedSource && <div className="space-y-4">{keys.map(key => { const binding = bindings.get(key), current = binding && lists.some(l => listKey(l) === listKey(binding)); return <div className="rounded-lg border p-3 space-y-2 min-w-0" key={key}><label className="block text-sm font-medium break-all" htmlFor={`legacy-mailing-list-${key}`}>旧列表 {key}</label><p className="text-xs text-muted-foreground">保留 {archive.choices.filter(c => c.sourceUserId === source && c.sourceKey === key).length} 条来源记录，迁移方向由本人审阅。</p><select id={`legacy-mailing-list-${key}`} className="control w-full min-w-0" disabled={!ready || busy} value={binding ? listKey(binding) : ""} onChange={e => { const next = lists.find(l => listKey(l) === e.target.value); setBindings(old => { const updated = new Map(old); if (next) updated.set(key, next); else updated.delete(key); return updated; }); }}><option value="">本次不映射</option>{lists.map(l => <option key={listKey(l)} value={listKey(l)}>{l.name} · {l.email}{!l.enabled ? "（已停用）" : ""}</option>)}</select>{binding && !current && <p className="text-xs text-amber-700">列表已变化，请重新选择并核对。</p>}</div>; })}</div>}
        {duplicateTargets && <p role="alert" className="text-sm text-destructive">每个当前列表只能对应一个旧列表，请检查重复选择。</p>}
        <Button disabled={!canCreate} onClick={() => void create()}>创建待本人确认的映射</Button>
      </>}
    </CardContent></Card>
    {ready && !mappings.length && <Empty title="当前没有账号映射" description="选择来源批次、账号和列表后建立映射。" />}
    <div className="space-y-3">{mappings.slice((currentPage - 1) * 20, currentPage * 20).map(m => <Card key={m.id} className="min-w-0" data-mailing-mapping={m.id}><CardContent className="space-y-3 p-4"><div className="flex flex-wrap items-start justify-between gap-3"><p className="min-w-0 break-all text-sm font-medium">旧账号 #{m.sourceUserId} → {m.targetName || "账号已失效"}</p><Badge variant="outline">{migrationStates[m.status]}</Badge></div><ul className="space-y-1 text-xs text-muted-foreground">{m.lists.map(l => <li key={l.sourceKey} className="break-all">{l.sourceKey} → {l.listEmail}{m.status === "accepted" ? (m.acceptedChoices?.some(c => c.sourceKey === l.sourceKey) ? " · 已迁入" : " · 本次未选择") : ""}</li>)}</ul><p className="text-xs text-muted-foreground">{time(m.updatedAt)}</p>{m.status === "pending" && <Button variant="outline" disabled={busy || !ready} onClick={() => setCancel(m)}>取消待确认映射</Button>}</CardContent></Card>)}</div>
    {pages > 1 && <div className="flex flex-wrap items-center justify-between gap-3 text-sm"><span>第 {currentPage} / {pages} 页</span><div className="flex gap-2"><Button variant="outline" disabled={busy || currentPage === 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button variant="outline" disabled={busy || currentPage === pages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div>}
    <Dialog open={!!cancel} onOpenChange={open => { if (!open && !busy) setCancel(null); }}><DialogContent><DialogTitle>取消待确认的邮件订阅映射</DialogTitle><DialogDescription>取消后可重新建立账号和列表关联。已保存的当前偏好会保留。</DialogDescription><p className="break-all text-sm">旧账号 #{cancel?.sourceUserId} → {cancel?.targetName}</p><ErrorNotice message={error} /><div className="flex flex-wrap justify-end gap-3"><Button variant="outline" disabled={busy} onClick={() => setCancel(null)}>返回</Button>{!ready && <Button variant="outline" disabled={busy} onClick={() => void load()}>重新读取当前映射</Button>}<Button disabled={busy || !ready || cancel?.status !== "pending"} onClick={() => void cancelMapping()}>确认取消映射</Button></div></DialogContent></Dialog>
  </section>;
}
