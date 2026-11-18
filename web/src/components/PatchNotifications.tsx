import { useEffect, useRef, useState } from "react";
import { Mail, RefreshCw, Send } from "lucide-react";
import { api, APIError, save } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, Loading, message, time } from "@/components/shared";

type Kind = "connect" | "cease" | "info" | "loa";
type Header = "to" | "cc" | "bcc";
type Source = { header: Header; email: string; sourceId: string };
type Recipient = { id: string; label: string; email: string; kind: string };
type Draft = {
  type: Kind; revision: string; to: string[]; cc: string[]; bcc: string[];
  recipientSources?: Source[]; subject: string; text: string; loaId: string; documentIds: string[];
};
type Context = {
  portId: string; portIds: string[]; memberId: string; memberName: string; revision: string;
  type: Kind; deliveryEnabled: boolean; from: string; recipientOptions: Recipient[];
  loas: { id: string; label: string; expiresAt: string; size: number }[];
  documents: { id: string; name: string; mediaType: string; size: number }[];
  draft: Draft; blockingReasons: string[];
  limits: { maxRecipients: number; maxDocuments: number; maxAttachmentBytes: number; maxTextBytes: number };
};
type Review = {
  reviewToken: string; expiresAt: string; draft: Draft; to: string[]; cc: string[]; bcc: string[];
  recipients: { header: Header; email: string; label: string; sourceId?: string; manual: boolean }[];
  attachments: { kind: string; id: string; name: string; mediaType: string; size: number }[];
  subject: string; text: string; deliveryEnabled: boolean; blockingReasons: string[];
};
type Receipt = { notificationId: string; status: "queued"; replayed: boolean; recipientCount: number; bccCount: number; items: { id: string; to: string; status: string }[] };
const labels: Record<Kind, string> = { connect: "连接通知", cease: "终止通知", info: "配线信息", loa: "LOA 授权通知" };
const headers: Header[] = ["to", "cc", "bcc"];
const headerLabels: Record<Header, string> = { to: "收件人 To", cc: "抄送 CC", bcc: "密送 BCC" };
const blocks: Record<string, string> = {
  "delivery-disabled": "邮件投递尚未启用。可以预览，管理员配置 SMTP 后才能加入发送队列。",
  "allocation-unavailable": "当前分配已失效或无法确认，请先核对配线与成员。",
  "state-not-applicable": "当前配线状态不适用于此类通知，请选择适用类型或核对端口状态。",
  "loa-required": "此类通知需要有效 LOA，请先签发并明确选择授权文件。",
  "attachment-unavailable": "所选附件已失效或不再允许发送，请重新读取后核对。",
  "recipient-unavailable": "收件人来源已变化，请重新读取并核对邮箱。",
};
const addresses = (text: string) => text.split(/[,\n]/).map(value => value.trim()).filter(Boolean);
const size = (bytes: number) => `${(bytes / 1024).toFixed(1)} KiB`;

export default function PatchNotifications({ portId, portName, revision, allocated }: { portId: string; portName: string; revision: string; allocated: boolean }) {
  const [open, setOpen] = useState(false), [kind, setKind] = useState<Kind>("info");
  const [context, setContext] = useState<Context | null>(null), [draft, setDraft] = useState<Draft | null>(null);
  const [addressText, setAddressText] = useState<Record<Header, string>>({ to: "", cc: "", bcc: "" });
  const [suggestionHeader, setSuggestionHeader] = useState<Header>("to");
  const [review, setReview] = useState<Review | null>(null), [receipt, setReceipt] = useState<Receipt | null>(null);
  const [loading, setLoading] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [reloadRequired, setReloadRequired] = useState(false), [ack, setAck] = useState(false), [uncertain, setUncertain] = useState(false);
  const epoch = useRef(0), previousRevision = useRef(revision), openRef = useRef(false);
  const endpoint = `/patching/ports/${encodeURIComponent(portId)}/notifications`;
  useEffect(() => () => { epoch.current++; }, []);
  useEffect(() => {
    if (previousRevision.current === revision) return;
    previousRevision.current = revision;
    if (openRef.current && !uncertain) {
      epoch.current++; setReview(null); setAck(false); setReloadRequired(true); setBusy(false); setLoading(false);
      setError("端口状态已变化，草稿已保留。请重新读取并再次预览。");
    }
  }, [revision, uncertain]);
  useEffect(() => {
    if (!review || uncertain) return;
    const expire = () => {
      setReview(null); setAck(false); setReloadRequired(true);
      setError("审阅已过期，草稿已保留。请重新读取并再次预览。");
    };
    const remaining = new Date(review.expiresAt).getTime() - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) { expire(); return; }
    const timer = setTimeout(expire, remaining);
    return () => clearTimeout(timer);
  }, [review, uncertain]);
  const resetReview = () => { setReview(null); setAck(false); setError(""); };
  const load = async (preserve: boolean, nextKind = kind) => {
    const request = ++epoch.current;
    setLoading(true); resetReview(); setContext(null); setReloadRequired(true); setReceipt(null); setUncertain(false);
    try {
      const value = await api<Context>(`${endpoint}?type=${nextKind}`);
      if (request !== epoch.current || !openRef.current) return;
      if (value.portId !== portId || value.type !== nextKind || value.draft.type !== nextKind || value.draft.revision !== value.revision)
        throw new Error("通知草稿与当前配线不一致，请重新读取。");
      setContext(value); setKind(nextKind);
      if (preserve) setDraft(current => current ? { ...current, revision: value.revision } : value.draft);
      else {
        setDraft(value.draft);
        setAddressText({ to: value.draft.to.join(", "), cc: value.draft.cc.join(", "), bcc: value.draft.bcc.join(", ") });
      }
      setReloadRequired(false);
    } catch (e) { if (request === epoch.current) setError(message(e)); }
    finally { if (request === epoch.current) setLoading(false); }
  };
  const change = (patch: Partial<Draft>) => { resetReview(); setDraft(current => current ? { ...current, ...patch } : current); };
  const editAddress = (header: Header, value: string) => {
    setAddressText(current => ({ ...current, [header]: value }));
    change({ recipientSources: draft?.recipientSources?.filter(source => source.header !== header) ?? [] });
  };
  const suggest = (option: Recipient) => {
    if (headers.some(header => addresses(addressText[header]).some(email => email.toLowerCase() === option.email.toLowerCase()))) {
      setError("该邮箱已在收件列表中。每个地址只需选择一次。"); return;
    }
    setAddressText(current => ({ ...current, [suggestionHeader]: [...addresses(current[suggestionHeader]), option.email].join(", ") }));
    change({ recipientSources: [...(draft?.recipientSources ?? []), { header: suggestionHeader, email: option.email, sourceId: option.id }] });
  };
  const preview = async () => {
    if (!draft || !context || reloadRequired || busy) return;
    const request = ++epoch.current;
    setBusy(true); resetReview();
    try {
      const value = await save<Review>(`${endpoint}/review`, { ...draft, ...Object.fromEntries(headers.map(header => [header, addresses(addressText[header])])) });
      if (request !== epoch.current || !openRef.current) return;
      if (!value.reviewToken || value.draft.type !== kind || value.draft.revision !== draft.revision || !Number.isFinite(new Date(value.expiresAt).getTime()))
        throw new Error("审阅结果未绑定当前草稿，请重新读取。");
      setReview(value);
    } catch (e) {
      if (request !== epoch.current) return;
      setError(message(e));
      if (e instanceof APIError && [401, 403, 404, 409].includes(e.status)) setReloadRequired(true);
    } finally { if (request === epoch.current) setBusy(false); }
  };
  const queue = async () => {
    if (!review || !ack || busy || !review.deliveryEnabled || review.blockingReasons.length) return;
    setBusy(true); setError("");
    try {
      const value = await save<Receipt>(`${endpoint}/queue`, { ...review.draft, reviewToken: review.reviewToken, expiresAt: review.expiresAt, acknowledge: true });
      if (!openRef.current) return;
      setReceipt(value); setReview(null); setUncertain(false); setAck(false);
    } catch (e) {
      if (!openRef.current) return;
      setAck(false);
      if (e instanceof APIError && (e.status < 500 || e.status === 503)) {
        setReview(null); setReloadRequired(true);
        setError(`${message(e)} 草稿已保留，请重新读取并再次预览。`);
      } else {
        setUncertain(true);
        setError("未取得本次提交结果。请先查看发件箱；如需重试，下方仅重试同一次确认，不会创建第二份队列。不要重新撰写相同通知。");
      }
    } finally { if (openRef.current) setBusy(false); }
  };
  const close = (value: boolean) => {
    if (busy) return;
    openRef.current = value; setOpen(value);
    if (!value) { epoch.current++; setReview(null); setAck(false); setContext(null); setDraft(null); setReceipt(null); setUncertain(false); setLoading(false); }
  };
  const fieldDisabled = loading || busy || !!review;
  return <>
    <Button variant="outline" disabled={!allocated} title={!allocated ? "先将端口分配给成员，再撰写通知" : undefined} onClick={() => { openRef.current = true; setOpen(true); void load(false, "info"); }}><Mail />配线通知邮件</Button>
    <Dialog open={open} onOpenChange={close}>
      <DialogContent className="max-w-3xl [&>*]:min-w-0">
        <DialogTitle className="pr-6 text-lg font-semibold">{portName} · 配线通知邮件</DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">核对收件人、正文与真实附件，预览后明确确认加入邮件队列。配线状态不会因撰写或排队而改变。</DialogDescription>
        <ErrorNotice message={error} />
        {loading && <Loading />}
        {receipt ? <section aria-label="配线通知排队结果" className="space-y-4 rounded-lg border p-4">
          <p role="status" className="font-medium text-primary">{receipt.replayed ? "已找到本次排队结果，没有重复创建邮件。" : "通知已加入发送队列，尚不能据此认定已经投递。"}</p>
          <p className="break-all text-xs text-muted-foreground">通知编号：{receipt.notificationId}</p>
          <p className="text-sm">收件人 {receipt.recipientCount} 个，其中密送 {receipt.bccCount} 个。</p>
          <a className="text-sm text-primary underline" href="#notifications" onClick={() => close(false)}>查看发件箱投递状态</a>
          <div className="flex flex-wrap justify-end gap-2"><Button variant="outline" onClick={() => close(false)}>完成</Button><Button onClick={() => void load(false)}>撰写另一封通知</Button></div>
        </section> : <>
          {context && draft && <>
            <div className="flex flex-wrap items-center gap-2 text-xs"><Badge variant="outline">当前成员：{context.memberName}</Badge><Badge variant={context.deliveryEnabled ? "secondary" : "outline"}>{context.deliveryEnabled ? "邮件投递已启用" : "邮件投递未启用"}</Badge><span className="break-all text-muted-foreground">发件人：{context.from || "尚未配置"}</span></div>
            <div className="field"><label htmlFor="patch-mail-kind">通知类型</label><select id="patch-mail-kind" value={kind} disabled={fieldDisabled} onChange={e => void load(false, e.target.value as Kind)}>{Object.entries(labels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select><p className="text-xs text-muted-foreground">切换类型会载入该类型的新模板。连接通知默认选择有效 LOA，可取消；LOA 通知必须附授权文件。</p></div>
            <fieldset disabled={fieldDisabled} className="min-w-0 space-y-4">
              {headers.map(header => <div className="field" key={header}><label htmlFor={`patch-mail-${header}`}>{headerLabels[header]}{header === "to" ? " *" : ""}</label><textarea id={`patch-mail-${header}`} rows={2} maxLength={4096} value={addressText[header]} onChange={e => editAddress(header, e.target.value)} placeholder="name@example.net，多个地址用英文逗号或换行分隔" /><p className="text-xs text-muted-foreground">{header === "bcc" ? "密送地址不会展示给其他收件人。" : "可填写机房工程师等外部联系人的邮箱。"} {addresses(addressText[header]).length > 0 && (!(draft.recipientSources ?? []).some(source => source.header === header) ? "手工指定，请确认发送授权。" : "包含来源邮箱；手动修改本栏后按手工指定处理。")}</p></div>)}
              {context.recipientOptions.length > 0 && <div className="space-y-2 rounded-lg border p-3"><div className="flex flex-wrap items-center gap-2"><span className="text-xs font-medium">从当前联系资料添加</span><select aria-label="建议邮箱添加位置" className="!w-auto" value={suggestionHeader} onChange={e => setSuggestionHeader(e.target.value as Header)}>{headers.map(header => <option key={header} value={header}>{headerLabels[header]}</option>)}</select></div><div className="flex flex-wrap gap-2">{context.recipientOptions.map(option => <Button type="button" key={option.id} size="sm" variant="outline" className="h-auto max-w-full whitespace-normal break-all py-2" onClick={() => suggest(option)}>{option.label} · {option.email}</Button>)}</div></div>}
              <p className="text-xs text-muted-foreground">To、CC、BCC 合计最多 {context.limits.maxRecipients} 个邮箱，同一地址不要重复填写。</p>
              <div className="field"><label htmlFor="patch-mail-subject">邮件主题 *</label><Input id="patch-mail-subject" maxLength={300} value={draft.subject} onChange={e => change({ subject: e.target.value })} /></div>
              <div className="field"><label htmlFor="patch-mail-body">纯文本正文 *</label><textarea id="patch-mail-body" rows={9} value={draft.text} onChange={e => change({ text: e.target.value })} /><p className="text-xs text-muted-foreground">正文按纯文本发送，最多 {size(context.limits.maxTextBytes)}。HTML 和 Markdown 标记不会作为网页执行。</p></div>
              <div className="field"><label htmlFor="patch-mail-loa">LOA PDF 附件</label><select id="patch-mail-loa" value={draft.loaId || ""} onChange={e => change({ loaId: e.target.value })}><option value="">不附 LOA</option>{context.loas.map(loa => <option key={loa.id} value={loa.id}>{loa.label} · 截止 {time(loa.expiresAt)} · {size(loa.size)}</option>)}{draft.loaId && !context.loas.some(loa => loa.id === draft.loaId) && <option value={draft.loaId}>原选 LOA 已不可用，请重新选择</option>}</select></div>
              <section className="space-y-2" aria-label="通知附件选择"><h3 className="text-sm font-medium">当前分配的授权资料</h3><p className="text-xs leading-6 text-muted-foreground">仅信息与终止通知可明确附资料。最多 {context.limits.maxDocuments} 份，连同 LOA 总计不超过 {size(context.limits.maxAttachmentBytes)}。内部和私有附件不在此列表。</p>{context.documents.map(document => <label key={document.id} className="flex items-start gap-2 rounded-lg border p-3 text-sm"><input type="checkbox" className="mt-1" checked={draft.documentIds.includes(document.id)} disabled={!["info", "cease"].includes(kind)} onChange={e => change({ documentIds: e.target.checked ? [...draft.documentIds, document.id] : draft.documentIds.filter(id => id !== document.id) })} /><span className="min-w-0 break-all">{document.name}<span className="mt-1 block text-xs text-muted-foreground">{document.mediaType} · {size(document.size)}</span></span></label>)}{draft.documentIds.filter(id => !context.documents.some(document => document.id === id)).map(id => <label key={id} className="flex items-start gap-2 text-sm text-destructive"><input type="checkbox" checked onChange={() => change({ documentIds: draft.documentIds.filter(value => value !== id) })} />原选资料已不可用，请取消后重新核对</label>)}</section>
            </fieldset>
            {!review && context.blockingReasons.length > 0 && <ul className="notice space-y-1 text-xs">{context.blockingReasons.map(code => <li key={code}>{blocks[code] ?? "当前条件未满足，请核对配线后重新读取。"}</li>)}</ul>}
          </>}
          {review && <section aria-label="配线通知发送预览" className="min-w-0 space-y-4 rounded-lg border bg-muted/20 p-4">
            <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">请核对本次发送内容</h3><span className="min-w-0 break-all text-xs text-muted-foreground">审阅有效至 {time(review.expiresAt)}</span></div>
            <dl className="space-y-3 text-sm">{headers.map(header => <div key={header}><dt className="font-medium">{headerLabels[header]}</dt><dd className="mt-1 space-y-1 break-all">{review.recipients.filter(recipient => recipient.header === header).map(recipient => <p key={recipient.email}>{recipient.email} <span className="text-xs text-muted-foreground">{recipient.manual ? "手工指定" : recipient.label}</span></p>)}{!review.recipients.some(recipient => recipient.header === header) && "无"}</dd></div>)}</dl>
            <p className="break-all font-medium">{review.subject}</p><pre className="min-w-0 max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-lg border bg-background p-3 text-sm leading-6">{review.text}</pre>
            <div><h4 className="mb-2 text-sm font-medium">实际附件（{review.attachments.length}）</h4>{review.attachments.length ? <ul className="space-y-2 text-xs">{review.attachments.map(attachment => <li key={`${attachment.kind}:${attachment.id}`} className="break-all">{attachment.name} · {attachment.mediaType} · {size(attachment.size)}</li>)}</ul> : <p className="text-xs text-muted-foreground">没有附件</p>}</div>
            {review.blockingReasons.length > 0 && <ul className="notice space-y-1 text-xs">{review.blockingReasons.map(code => <li key={code}>{blocks[code] ?? "当前条件未满足，不能加入发送队列。"}</li>)}</ul>}
            <label className="flex items-start gap-2 text-sm leading-6"><input type="checkbox" className="mt-1.5" checked={ack} disabled={busy || !review.deliveryEnabled || review.blockingReasons.length > 0} onChange={e => setAck(e.target.checked)} />我已核对全部 To、CC、BCC、正文和附件，并获准向这些地址发送。</label>
            {uncertain && <a href="#notifications" className="text-sm text-primary underline" onClick={() => close(false)}>查看发件箱投递状态</a>}
            <div className="flex flex-wrap justify-end gap-2">{!uncertain && <Button variant="outline" disabled={busy} onClick={resetReview}>返回编辑</Button>}<Button disabled={busy || !ack || !review.deliveryEnabled || review.blockingReasons.length > 0} onClick={() => void queue()}><Send />{busy ? "正在排队…" : uncertain ? "重试本次确认" : "确认加入发送队列"}</Button></div>
          </section>}
          {!review && <div className="flex flex-wrap justify-end gap-2"><Button variant="outline" disabled={loading || busy} onClick={() => void load(!!draft)}><RefreshCw />重新读取，保留编辑</Button><Button disabled={!context || !draft || loading || busy || reloadRequired} onClick={() => void preview()}>{busy ? "正在生成预览…" : "预览通知邮件"}</Button></div>}
        </>}
      </DialogContent>
    </Dialog>
  </>;
}
