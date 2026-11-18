import { useEffect, useState } from "react";
import { ArrowLeft, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Heading, Loading, message } from "@/components/shared";

type Source = { id: string; name?: string; kind?: string; minPrivileges?: number; fileId?: string; downloadedBy?: number | null; createdAt?: string; facts: Record<string, string> };
type Entry = { source: Source; batchId: string; documentId?: string; review?: string };
type Archive = { directories: Entry[]; files: Entry[]; logs: Entry[]; batches: { id: string; sourceTimezone: string; importedAt: string }[] };
const blank: Archive = { directories: [], files: [], logs: [], batches: [] };
const labels = { files: "文件与权限", directories: "原目录记录", logs: "旧访问记录" };
export default function LegacyDocstore() {
  const [archive, setArchive] = useState<Archive>(blank), [tab, setTab] = useState<keyof typeof labels>("files");
  const [query, setQuery] = useState(""), [page, setPage] = useState(1), [loading, setLoading] = useState(true), [error, setError] = useState("");
  const [selected, setSelected] = useState<Entry | null>(null);
  const load = async () => {
    setLoading(true); setError("");
    try { const result = await api<{ archive: Archive }>("/documents/legacy"); setArchive({ ...blank, ...result.archive }); }
    catch (e) { setError(message(e)); } finally { setLoading(false); }
  };
  useEffect(() => { void load(); }, []);
  useEffect(() => { setPage(1); }, [query, tab]);
  const filtered = (archive[tab] ?? []).filter(item => !query || `${item.source.id} ${item.source.name ?? ""} ${item.source.fileId ?? ""}`.toLowerCase().includes(query.toLowerCase()));
  const pages = Math.max(1, Math.ceil(filtered.length / 20)), current = Math.min(page, pages);
  const privilege = (entry: Entry) => entry.review ? "仅工作人员 · 待核对" : ["共享文件公开 / 成员文件限所属成员", "普通成员及管理员", "成员管理员", "仅工作人员"][entry.source.minPrivileges ?? -1] ?? "未知权限 · 仅工作人员";
  return <>
    <Heading title="旧文档归档" description="保留原文件、目录关系和访问记录；文件按各自的原权限读取。">
      <Button variant="outline" onClick={() => { location.hash = "documents"; }}><ArrowLeft />文档库</Button>
      <Button variant="outline" disabled={loading} onClick={() => void load()}><RefreshCw />刷新</Button>
    </Heading>
    <p className="notice mb-4 text-sm leading-6">旧访问记录不区分查看和下载，也不证明读取成功。原账号编号仅作来源记录，不关联当前账号。</p>
    <ErrorNotice message={error} />
    <div className="mb-4 flex flex-wrap gap-2">{Object.entries(labels).map(([key, label]) => <Button key={key} variant={tab === key ? "default" : "outline"} onClick={() => setTab(key as keyof typeof labels)}>{label}</Button>)}</div>
    <Input aria-label="搜索旧文档归档" placeholder="搜索原名称或来源编号" value={query} onChange={e => setQuery(e.target.value)} className="mb-4 max-w-lg" />
    {loading ? <Loading /> : filtered.length === 0 ? <Empty title="没有匹配的来源记录" description="可以调整搜索条件，或在设置中导入离线文档包。" /> : <Card className="overflow-hidden">
      <div className="divide-y">{filtered.slice((current - 1) * 20, current * 20).map(item => <article key={item.source.id} className="min-w-0 space-y-3 p-4">
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><p className="break-words font-medium">{item.source.name || item.source.id}</p><p className="mt-1 break-all text-xs text-muted-foreground">{item.source.id}</p></div><Badge variant="outline">只读来源</Badge></div>
        {tab === "files" && <p className="text-sm">{privilege(item)}{item.review ? `：${item.review}` : ""}</p>}
        {tab === "logs" && <p className="text-sm text-muted-foreground">原文件：{item.source.fileId || "未解析"} · 原账号：{item.source.downloadedBy == null ? "未记录" : item.source.downloadedBy} · 结果未知</p>}
        <div className="flex flex-wrap gap-2"><Button variant="outline" size="sm" onClick={() => setSelected(item)}>查看来源详情</Button>{item.documentId && <Button variant="outline" size="sm" onClick={() => { location.hash = `documents?id=${encodeURIComponent(item.documentId!)}`; }}>查看只读文档</Button>}</div>
      </article>)}</div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t p-4 text-xs"><span>{filtered.length} 条 · 第 {current} / {pages} 页</span><div className="flex gap-2"><Button variant="outline" size="sm" disabled={current === 1} onClick={() => setPage(current - 1)}>上一页</Button><Button variant="outline" size="sm" disabled={current === pages} onClick={() => setPage(current + 1)}>下一页</Button></div></div>
    </Card>}
    <Dialog open={!!selected} onOpenChange={open => !open && setSelected(null)}><DialogContent className="max-w-3xl"><DialogTitle className="break-words">{selected?.source.name || selected?.source.id}</DialogTitle><DialogDescription>以下为旧数据库原值。原日期保留原时区语义，原账号不会获得本系统权限。</DialogDescription>
      <p className="text-xs text-muted-foreground">来源时区：{archive.batches.find(batch => batch.id === selected?.batchId)?.sourceTimezone || "unknown"}；unknown 表示原时区未经确认。</p>
      <dl className="grid min-w-0 gap-3 sm:grid-cols-2">{Object.entries(selected?.source.facts ?? {}).map(([key, value]) => <div className="min-w-0 rounded-lg border p-3" key={key}><dt className="text-xs text-muted-foreground">{key}</dt><dd className="mt-2 whitespace-pre-wrap break-all text-sm">{value || "（空值）"}</dd></div>)}</dl>
    </DialogContent></Dialog>
  </>;
}
