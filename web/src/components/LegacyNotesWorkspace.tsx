import { useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { noteStaffRoles, sourceName, type CurrentStaff, type LegacyNoteArchive as Archive, type LegacyNoteIndex, type LegacyNoteMapping, type LegacyNotePending, type LegacyNotePendingList } from "@/lib/legacy-notes";
import type { NoteDraft, NotePreferences, NoteSummary } from "@/lib/note-notifications";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Empty, ErrorNotice, Loading, message, time } from "@/components/shared";
import LegacyNoteArchive from "@/components/LegacyNoteArchive";
import LegacyNoteMappings from "@/components/LegacyNoteMappings";
import LegacyNoteAccept from "@/components/LegacyNoteAccept";

const blankIndex: LegacyNoteIndex = { batches: [], mappings: [], canManage: false, readOnlySources: true };
export default function LegacyNotesWorkspace({ role }: { role: string }) {
  return noteStaffRoles.includes(role) ? <Workspace role={role} /> : null;
}
function Workspace({ role }: { role: string }) {
  const [index, setIndex] = useState(blankIndex), [pending, setPending] = useState<LegacyNotePending[]>([]), [preferences, setPreferences] = useState<NotePreferences | null>(null), [summary, setSummary] = useState<NoteSummary | null>(null), [staff, setStaff] = useState<CurrentStaff[]>([]), [loading, setLoading] = useState(true), [error, setError] = useState("");
  const [tab, setTab] = useState<"self" | "archive" | "mappings">("self"), [checksum, setChecksum] = useState(""), [archive, setArchive] = useState<Archive | null>(null), [archiveLoading, setArchiveLoading] = useState(false), [archiveError, setArchiveError] = useState(""), [query, setQuery] = useState(""), [page, setPage] = useState(1), [drafts, setDrafts] = useState<Record<string, NoteDraft>>({});
  const alive = useRef(true), loadSequence = useRef(0), archiveSequence = useRef(0);
  const refresh = async (): Promise<LegacyNoteMapping[]> => {
    const sequence = ++loadSequence.current;
    const [next, self, notes, accounts] = await Promise.all([
      api<LegacyNoteIndex>("/notes/legacy"), api<LegacyNotePendingList>("/account/note-migrations"), api<NoteSummary>("/account/note-summary"),
      role === "admin" ? api<{ items: CurrentStaff[] }>("/resources/users") : Promise.resolve({ items: [] as CurrentStaff[] }),
    ]);
    if (alive.current && sequence === loadSequence.current) {
      setIndex(next); setPreferences(self.preferences); setSummary(notes); setStaff(accounts.items.filter(item => noteStaffRoles.includes(item.role)));
      setPending(old => [...self.items.map(item => ({ ...item, available: true })), ...old.filter(item => !self.items.some(current => current.mapping.id === item.mapping.id)).map(item => ({ ...item, mapping: next.mappings.find(mapping => mapping.id === item.mapping.id) ?? item.mapping, available: false }))]);
      setChecksum(current => current || next.batches[0]?.checksum || ""); setError("");
    }
    return next.mappings;
  };
  const load = async () => { setLoading(true); setError(""); try { await refresh(); } catch (e) { if (alive.current) setError(message(e)); } finally { if (alive.current) setLoading(false); } };
  useEffect(() => { alive.current = true; void load(); return () => { alive.current = false; loadSequence.current++; archiveSequence.current++; }; }, [role]);
  const fetchArchive = async (selected = checksum) => {
    if (!selected) return;
    const sequence = ++archiveSequence.current; setArchiveLoading(true); setArchiveError(""); setArchive(null);
    try { const result = await api<{ archive: Archive }>(`/notes/legacy/archives/${selected}`); if (alive.current && sequence === archiveSequence.current) setArchive(result.archive); }
    catch (e) { if (alive.current && sequence === archiveSequence.current) setArchiveError(message(e)); }
    finally { if (alive.current && sequence === archiveSequence.current) setArchiveLoading(false); }
  };
  useEffect(() => { if (tab !== "self" && checksum && archive?.checksum !== checksum) void fetchArchive(checksum); }, [tab, checksum]);
  useEffect(() => { setPage(1); }, [query]);
  const rows = pending.filter(item => `${item.sourceUser.id} ${sourceName(item.sourceUser) ?? ""} ${item.mapping.targetName}`.toLowerCase().includes(query.toLowerCase()));
  const pages = Math.max(1, Math.ceil(rows.length / 20)), currentPage = Math.min(page, pages);
  return <div className="space-y-5"><div className="flex flex-wrap justify-between gap-3"><div className="flex flex-wrap gap-2"><Button variant={tab === "self" ? "default" : "outline"} onClick={() => setTab("self")}>我的迁移确认</Button><Button variant={tab === "archive" ? "default" : "outline"} onClick={() => setTab("archive")}>来源归档</Button>{role === "admin" && index.canManage && <Button variant={tab === "mappings" ? "default" : "outline"} onClick={() => setTab("mappings")}>账号映射</Button>}</div><Button variant="outline" disabled={loading} onClick={() => void load()}><RefreshCw />刷新迁移工作区</Button></div><ErrorNotice message={error} />
    {loading && !preferences ? <Loading /> : tab === "self" ? <section className="space-y-4" aria-label="本人待确认迁移"><p className="text-sm leading-6 text-muted-foreground">这里只列出明确映射到当前账号实体的来源。管理员建立映射后，仍需你本人选择当前订阅并预览确认。阅读来源不会标记当前备注为已读。</p><Input aria-label="搜索本人备注迁移" placeholder="搜索旧账号名称或编号" value={query} onChange={e => setQuery(e.target.value)} className="max-w-lg" />{rows.length === 0 ? <Empty title="没有待本人确认的来源" description="管理员可在账号映射中明确指定目标员工；未映射的旧账号不会自动关联。" /> : preferences && summary && <>{rows.slice((currentPage - 1) * 20, currentPage * 20).map(entry => <LegacyNoteAccept key={entry.mapping.id} entry={entry} preferences={preferences} summary={summary} refresh={async () => { await refresh(); }} initialSelection={drafts[entry.mapping.id]} onSelectionChange={value => setDrafts(old => ({ ...old, [entry.mapping.id]: value }))} />)}<div className="flex flex-wrap items-center justify-between gap-3 text-xs"><span>{rows.length} 项 · 第 {currentPage} / {pages} 页</span><div className="flex gap-2"><Button variant="outline" size="sm" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={currentPage >= pages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div></>}</section> : tab === "archive" ? <LegacyNoteArchive batches={index.batches} selected={checksum} onSelect={setChecksum} archive={archive?.checksum === checksum ? archive : null} loading={archiveLoading} error={archiveError} reload={() => fetchArchive()} /> : role === "admin" && <><div className="field"><label htmlFor="legacy-note-mapping-batch">映射来源批次</label><select id="legacy-note-mapping-batch" className="control max-w-full" value={checksum} onChange={e => setChecksum(e.target.value)}><option value="">选择来源批次</option>{index.batches.map(item => <option key={item.checksum} value={item.checksum}>{time(item.importedAt)} · {item.users} 个旧账号 · {item.checksum.slice(0, 8)}</option>)}</select></div><ErrorNotice message={archiveError} />{archiveLoading ? <Loading /> : <LegacyNoteMappings checksum={checksum} archive={archive?.checksum === checksum ? archive : null} staff={staff} mappings={index.mappings} refresh={refresh} />}</>}
  </div>;
}
