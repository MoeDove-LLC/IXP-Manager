import { useEffect, useMemo, useState } from "react";
import { Archive, ArrowLeft, Download, ExternalLink, RefreshCw, Search } from "lucide-react";
import { api, downloadFile } from "@/lib/api";
import { fileBytes, legacyFacts, legacyPatchStates, sourceTimezone, type LegacyPatchDocument, type LegacyPatchListing, type LegacyPatchRecord } from "@/lib/legacy-patching";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Heading, Loading, message, time } from "@/components/shared";

type Kind = "panel" | "port" | "history" | "file";
type Entry = LegacyPatchRecord & { kind: Kind; key: string; title: string; reasons: string[] };
const kinds = { panel: "配线架来源", port: "端口来源", history: "旧分配快照", file: "附件归档" };
const blank: LegacyPatchListing = { batches: [], panels: [], ports: [], histories: [], attachments: [] };
const pageSize = 20;
export default function LegacyPatching({ role }: { role: string }) {
  return ["admin", "operator", "viewer"].includes(role) ? <LegacyArchive role={role} /> : null;
}
function LegacyArchive({ role }: { role: string }) {
  const [data, setData] = useState<LegacyPatchListing>(blank), [docs, setDocs] = useState<LegacyPatchDocument[]>([]);
  const [loading, setLoading] = useState(true), [error, setError] = useState("");
  const [tab, setTab] = useState("review"), [batch, setBatch] = useState(""), [query, setQuery] = useState(""), [page, setPage] = useState(1);
  const [selected, setSelected] = useState<Entry | null>(null);
  const load = async () => {
    setLoading(true); setError("");
    try {
      const [archive, documents] = await Promise.all([api<LegacyPatchListing>("/patching/legacy"), api<{ items: LegacyPatchDocument[] }>("/documents")]);
      setData({ ...blank, ...archive }); setDocs(documents.items ?? []);
    } catch (e) { setError(message(e)); } finally { setLoading(false); }
  };
  useEffect(() => { void load(); }, []);
  useEffect(() => { setPage(1); }, [tab, batch, query]);
  const documents = useMemo(() => new Map(docs.map((doc) => [doc.id, doc])), [docs]);
  const batches = useMemo(() => new Map(data.batches.map((item) => [item.id, item])), [data.batches]);
  const entries = useMemo(() => {
    const panelNames = new Map(data.panels.map((item) => [item.source.id, item.source.name ?? item.source.id]));
    const groups: [Kind, LegacyPatchRecord[]][] = [["panel", data.panels], ["port", data.ports], ["history", data.histories], ["file", data.attachments]];
    return groups.flatMap(([kind, items]) => items.map((item): Entry => ({ ...item, kind, key: `${kind}:${item.source.id}`,
      title: kind === "panel" || kind === "file" ? item.source.name || item.source.id : kind === "port"
        ? `${panelNames.get(item.source.panelId ?? "") ?? "未解析配线架"} · 原端口 ${item.source.number ?? "—"}`
        : `旧分配快照 #${item.source.legacyId} · 原端口 ${item.source.number ?? "—"}`,
      reasons: Array.isArray(item.review) ? item.review : item.review ? [item.review] : [],
    })));
  }, [data]);
  const filtered = useMemo(() => entries.filter((item) => (!batch || item.batchId === batch)
    && (tab === "review" ? item.reasons.length > 0 : tab === "sources" ? ["panel", "port"].includes(item.kind) : tab === "history" ? item.kind === "history" : item.kind === "file")
    && (!query || `${item.title} ${item.source.id} ${item.source.portId ?? ""} ${item.source.parentId ?? ""} ${item.source.facts.customer ?? ""} ${item.source.facts.switchport ?? ""}`.toLowerCase().includes(query.toLowerCase()))), [entries, batch, tab, query]);
  const totalPages = Math.max(1, Math.ceil(filtered.length / pageSize)), currentPage = Math.min(page, totalPages);
  const current = filtered.slice((currentPage - 1) * pageSize, currentPage * pageSize);
  const permission = (entry: Entry) => {
    const doc = documents.get(entry.documentId ?? "");
    return !doc ? "当前不可访问" : doc.visibility === "member" && doc.allocationAccessActive ? "当前分配成员可读" : "仅工作人员";
  };
  const download = async (entry: Entry) => {
    const doc = documents.get(entry.documentId ?? ""); if (!doc) return;
    try { await downloadFile(`/documents/${doc.id}/download`, doc.name); } catch (e) { setError(message(e)); }
  };
  return <>
    <Heading className="flex-wrap" title="遗留配线归档" description="核对迁移来源、旧分配快照和附件；此区域仅向工作人员开放。">
      <div className="flex flex-wrap gap-2"><Button variant="outline" onClick={() => { location.hash = "patching"; }}><ArrowLeft />当前配线</Button><Button variant="outline" disabled={loading} onClick={() => void load()}><RefreshCw />刷新归档</Button></div>
    </Heading>
    <div className="notice mb-5 text-sm leading-6">旧分配记录是来源快照，不是本系统审计事件。原日期 / 时间按原文保留，上传人字段不代表当前系统操作人。旧附件（包括 PDF）不构成新签发的 LOA。</div>
    <ErrorNotice message={error} />
    <div className="mb-5 grid grid-cols-2 gap-3 lg:grid-cols-4">
      {[["导入批次", data.batches.length], ["待核对记录", entries.filter((item) => item.reasons.length).length], ["旧分配快照", data.histories.length], ["只读附件", data.attachments.length]].map(([label, count]) => <Card key={label}><CardContent className="p-4"><p className="text-xs text-muted-foreground">{label}</p><p className="mt-2 text-2xl font-semibold">{count}</p></CardContent></Card>)}
    </div>
    <Card className="min-w-0 overflow-hidden">
      <div className="flex flex-wrap gap-2 border-b p-4" aria-label="遗留归档分类">{[["review", "待核对"], ["sources", "配线来源"], ["history", "分配快照"], ["files", "附件归档"]].map(([value, label]) => <Button key={value} size="sm" variant={tab === value ? "default" : "outline"} aria-pressed={tab === value} onClick={() => setTab(value)}>{label}</Button>)}</div>
      <div className="grid gap-3 border-b p-4 sm:grid-cols-2">
        <select aria-label="选择配线导入批次" value={batch} onChange={(e) => setBatch(e.target.value)}><option value="">全部导入批次</option>{data.batches.map((item) => <option value={item.id} key={item.id}>{time(item.importedAt)} · {sourceTimezone(item.sourceTimezone)}</option>)}</select>
        <div className="relative min-w-0"><Search className="absolute left-3 top-3 size-4 text-muted-foreground" /><Input className="pl-9" aria-label="搜索遗留配线记录" placeholder="来源名称、ID 或历史成员文字…" value={query} onChange={(e) => setQuery(e.target.value)} /></div>
      </div>
      {loading ? <Loading /> : !current.length ? <Empty title={data.batches.length ? "没有匹配的遗留记录" : "尚未导入配线归档"} description={data.batches.length ? "切换分类、批次或搜索词查看其他来源记录。" : "管理员可在交换中心设置中导入离线迁移包。旧历史不会被补写为操作事件。"} /> : <div className="divide-y">{current.map((item) => <article key={item.key} className="min-w-0 space-y-3 p-4 sm:p-5">
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0 flex-1"><p className="break-words text-sm font-semibold">{item.title}</p><p className="mt-1 break-all text-xs text-muted-foreground">{item.source.id}</p></div><Badge variant={item.reasons.length ? "destructive" : "outline"}>{item.reasons.length ? "待核对" : kinds[item.kind]}</Badge></div>
        {item.kind === "history" && <p className="break-words text-xs text-muted-foreground">原成员文字：{item.source.facts.customer || "未记录"} · 原记录日期：{item.source.facts.created_at || "未记录"}</p>}
        {item.kind === "file" && <div className="flex flex-wrap gap-2 text-xs text-muted-foreground"><span>{item.source.kind === "history" ? "历史快照附件" : "原当前端口附件"}</span><span>· {fileBytes(item.source.size ?? 0)}</span><span>· {permission(item)}</span><Badge variant="outline">遗留只读</Badge></div>}
        {item.reasons.length > 0 && <div className="rounded-lg bg-muted/60 p-3 text-xs leading-6"><p className="font-medium">需工作人员核对，不自动作为可分配库存。</p>{item.reasons.slice(0, 3).map((reason, index) => <p className="break-words" key={index}>{reason}</p>)}</div>}
        <div className="flex flex-wrap items-center justify-between gap-3"><p className="text-xs text-muted-foreground">{sourceTimezone(batches.get(item.batchId)?.sourceTimezone)}</p><div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => setSelected(item)}>查看来源详情</Button>
          {item.kind === "file" && <Button size="sm" variant="outline" disabled={!documents.has(item.documentId ?? "")} onClick={() => void download(item)}><Download />下载附件</Button>}
          {item.kind === "port" && item.nativeId && <Button size="sm" variant="outline" onClick={() => { location.hash = `patching?id=${encodeURIComponent(item.nativeId!)}`; }}><ExternalLink />查看当前端口</Button>}
        </div></div>
      </article>)}</div>}
      <div className="flex flex-wrap items-center justify-between gap-3 border-t p-4 text-xs text-muted-foreground"><span>{filtered.length} 条 · 第 {currentPage} / {totalPages} 页</span><div className="flex gap-2"><Button size="sm" variant="outline" disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button size="sm" variant="outline" disabled={currentPage >= totalPages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div>
    </Card>
    {role === "admin" && <p className="mt-4 text-sm"><a className="text-primary underline" href="#settings">在设置中导入离线配线迁移包</a></p>}
    <Dialog open={!!selected} onOpenChange={(open) => !open && setSelected(null)}><DialogContent className="max-w-4xl">
      <DialogTitle className="pr-6 text-lg font-semibold break-words">{selected?.title}</DialogTitle><DialogDescription>来源记录只读；原值不转换为本系统事件，也不恢复旧的访问授权。</DialogDescription>
      {selected && <>
        <div className="flex flex-wrap gap-2"><Badge variant="outline"><Archive className="mr-1 size-3" />{kinds[selected.kind]}</Badge><Badge variant="outline">{sourceTimezone(batches.get(selected.batchId)?.sourceTimezone)}</Badge></div>
        <div className="rounded-lg border p-3 text-xs leading-6"><p>导入时间：{time(batches.get(selected.batchId)?.importedAt)}</p><p className="break-all">来源 ID：{selected.source.id}</p>{selected.kind === "file" && <><p>当前访问范围：{permission(selected)}；原文件标记：{selected.source.private ? "私有" : "非私有"}</p><p className="break-all">实际内容 SHA-256：{selected.source.checksum || "未提供"}</p></>}</div>
        {selected.reasons.length > 0 && <div className="notice space-y-1 text-xs">{selected.reasons.map((reason, i) => <p className="break-words" key={i}>{reason}</p>)}</div>}
        {selected.source.state != null && <p className="text-sm">原状态：{legacyPatchStates[selected.source.state] ?? "未识别"}（{selected.source.state}）</p>}
        <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{Object.entries(selected.source.facts).sort(([a], [b]) => a.localeCompare(b)).map(([key, value]) => <div className="min-w-0 rounded-lg border p-3" key={key}><dt className="text-xs text-muted-foreground">{legacyFacts[key] ?? key}{/^\d{4}-\d{2}-\d{2}$/.test(value) ? " · 仅日期" : ""}</dt><dd className="mt-2 whitespace-pre-wrap break-all text-sm">{value || "（空值）"}</dd></div>)}</dl>
        {selected.documentId && documents.has(selected.documentId) && <Button variant="outline" onClick={() => { location.hash = `documents?id=${encodeURIComponent(selected.documentId!)}`; }}><ExternalLink />在文档库查看只读附件</Button>}
      </>}
    </DialogContent></Dialog>
  </>;
}
