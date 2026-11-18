import { useEffect, useRef, useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { api, APIError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, Loading, message } from "@/components/shared";

type Option = { value: string; groups: number };
type Selection = { date: string; names: Option[]; types: Option[]; members: { id: string; name: string; eligible: boolean }[]; maxRows: number; maxBytes: number };
const maximum = 16 * 1024 * 1024;
function checked(value: Selection): Selection {
  const options = (v: Option[]) => Array.isArray(v) && v.every(o => typeof o.value === "string" && o.value.length > 0 && Number.isSafeInteger(o.groups) && o.groups > 0) && new Set(v.map(o => o.value)).size === v.length;
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value.date) || !options(value.names) || !options(value.types) || !Array.isArray(value.members) || !value.members.every(m => typeof m.id === "string" && typeof m.name === "string" && typeof m.eligible === "boolean") || value.maxBytes !== maximum || value.maxRows !== 10000) throw new Error("联系人导出筛选资料不完整，请重新读取。");
  return value;
}

export default function ContactGroupExport() {
  const [open, setOpen] = useState(false), [data, setData] = useState<Selection | null>(null), [ready, setReady] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [done, setDone] = useState("");
  const [mode, setMode] = useState<"name" | "type">("name"), [selected, setSelected] = useState(""), [member, setMember] = useState(""), [format, setFormat] = useState<"json" | "csv">("json");
  const generation = useRef(0), mounted = useRef(false), abort = useRef<AbortController | null>(null);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; generation.current++; abort.current?.abort(); }; }, []);
  const load = async () => {
    const request = ++generation.current;
    abort.current?.abort(); setBusy(true); setReady(false); setError(""); setDone("");
    try {
      const next = checked(await api<Selection>("/contact-export?options=1"));
      if (mounted.current && request === generation.current) { setData(next); setReady(true); }
    } catch (e) { if (mounted.current && request === generation.current) setError(message(e)); }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  const close = () => { generation.current++; abort.current?.abort(); setOpen(false); setBusy(false); setReady(false); setData(null); setSelected(""); setMember(""); setError(""); setDone(""); };
  const options = mode === "name" ? data?.names : data?.types;
  const choice = options?.find(o => o.value === selected);
  const canDownload = ready && !busy && !!choice && (!member || data?.members.some(m => m.id === member && m.eligible));
  const download = async () => {
    if (!canDownload) return;
    const request = ++generation.current, controller = new AbortController(); abort.current = controller;
    setBusy(true); setError(""); setDone("");
    try {
      const params = new URLSearchParams({ [mode]: selected, format }); if (member) params.set("memberId", member);
      const response = await fetch(`/api/contact-export?${params}`, { credentials: "same-origin", signal: controller.signal, headers: { Accept: format === "json" ? "application/json" : "text/csv" } });
      if (!response.ok) {
        if (response.status === 401) window.dispatchEvent(new Event("session-expired"));
        throw new APIError(response.status === 413 ? "导出超过上限，请缩小分组或成员范围。" : "联系人导出未完成，请刷新资料与当前权限后重试。", response.status);
      }
      const mime = format === "json" ? "application/json" : "text/csv";
      if (!response.body || !response.headers.get("Content-Type")?.startsWith(mime)) throw new Error("联系人导出格式不符。");
      const count = response.headers.get("X-Contact-Export-Count"), date = response.headers.get("X-Contact-Export-Date");
      if (!count || !/^\d+$/.test(count) || Number(count) > 10000 || !date || !/^\d{4}-\d{2}-\d{2}$/.test(date)) throw new Error("联系人导出结果不完整。");
      const reader = response.body.getReader(), chunks: Uint8Array<ArrayBuffer>[] = []; let size = 0;
      try { for (;;) { const { value, done } = await reader.read(); if (done) break; size += value.byteLength; if (size > maximum) throw new Error("联系人导出超过 16 MiB。"); chunks.push(new Uint8Array(value)); } } finally { await reader.cancel(); }
      if (!mounted.current || request !== generation.current) return;
      const url = URL.createObjectURL(new Blob(chunks, { type: mime })), a = document.createElement("a");
      a.href = url; a.download = `contacts-by-group.${format}`; a.click(); URL.revokeObjectURL(url);
      setDone(`已下载 ${count} 条联系人（${date} UTC）。`);
    } catch (e) { if (mounted.current && request === generation.current) { setError(message(e)); setReady(false); } }
    finally { if (mounted.current && request === generation.current) setBusy(false); }
  };
  return <><Button size="sm" variant="outline" onClick={() => { setOpen(true); void load(); }}><Download />按联系组导出</Button>
    <Dialog open={open} onOpenChange={next => { if (!next) close(); }}><DialogContent><DialogTitle>按联系组导出联系人</DialogTitle><DialogDescription>先明确选择分组名称或类型，可进一步限制到一个成员。</DialogDescription>
      <ErrorNotice message={error} />{done && <p role="status" className="notice text-sm">{done}</p>}{busy && !data && <Loading />}
      <div className="space-y-4 min-w-0">
        <div className="field"><label htmlFor="contact-export-mode">分组匹配方式</label><select id="contact-export-mode" className="control w-full min-w-0" value={mode} disabled={!ready || busy} onChange={e => { setMode(e.target.value as "name" | "type"); setSelected(""); setDone(""); }}><option value="name">按分组名称</option><option value="type">按分组类型</option></select></div>
        <div className="field"><label htmlFor="contact-export-group">{mode === "name" ? "联系组名称" : "联系组类型"}</label><select id="contact-export-group" className="control w-full min-w-0" value={selected} disabled={!ready || busy} onChange={e => { setSelected(e.target.value); setDone(""); }}><option value="">请选择{mode === "name" ? "分组名称" : "分组类型"}</option>{options?.map(o => <option key={o.value} value={o.value}>{o.value}（{o.groups} 个组）</option>)}</select>{choice && <p className="text-xs leading-5 text-muted-foreground break-words">将合并匹配的 {choice.groups} 个组。同一联系人加入多个匹配组时只导出一次。</p>}</div>
        <div className="field"><label htmlFor="contact-export-member">成员范围</label><select id="contact-export-member" className="control w-full min-w-0" value={member} disabled={!ready || busy} onChange={e => { setMember(e.target.value); setDone(""); }}><option value="">全部在期成员</option>{data?.members.map(m => <option key={m.id} value={m.id} disabled={!m.eligible}>{m.name}{!m.eligible ? "（日期不在范围或缺加入日期）" : ""}</option>)}</select></div>
        <div className="field"><label htmlFor="contact-export-format">下载格式</label><select id="contact-export-format" className="control w-full min-w-0" value={format} disabled={!ready || busy} onChange={e => { setFormat(e.target.value as "json" | "csv"); setDone(""); }}><option value="json">JSON · 保留字段原值</option><option value="csv">CSV · 电子表格文本保护</option></select></div>
        <p className="text-xs leading-5 text-muted-foreground">按服务器当前 UTC 日期筛选：已加入且未离开，离开当天仍包括。缺少加入日期的成员不导出。正常、待接入和暂停状态均保留。</p>
        <p className="text-xs leading-5 text-muted-foreground">导出姓名、职位、邮箱、电话、手机、备注和成员状态，最多 10,000 条 / 16 MiB。{format === "csv" && "CSV 为电话、手机和可能被识别为公式的值添加可见 text: 前缀，保留加号与前导零；需要精确原值请选择 JSON。"}</p>
      </div>
      <div className="flex flex-wrap justify-end gap-3"><Button variant="outline" disabled={busy} onClick={() => void load()}><RefreshCw />刷新筛选资料</Button><Button disabled={!canDownload} onClick={() => void download()}>{busy ? "正在读取…" : "下载联系人文件"}</Button></div>
    </DialogContent></Dialog></>;
}
