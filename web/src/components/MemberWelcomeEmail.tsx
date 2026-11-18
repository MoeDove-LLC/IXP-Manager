import { useCallback, useEffect, useRef, useState } from "react";
import { Mail, RefreshCw, Send, X } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { ErrorNotice, Loading, message, time } from "@/components/shared";
import {
  checkedWelcomeContext,
  checkedWelcomeReview,
  copyWelcomeDraft,
  normalizedWelcomeEmail,
  validateWelcomeDraft,
  welcomeBlockLabel,
  welcomeBytes,
  welcomeHeaderLabel,
  welcomeHeaders,
  welcomeKindLabel,
  welcomeMaxRecipients,
  welcomeQueueBody,
  welcomeReceiptStatus,
  type WelcomeContext,
  type WelcomeDraft,
  type WelcomeHeader,
  type WelcomeReceipt,
  type WelcomeReview,
} from "@/lib/member-welcome-email";

export default function MemberWelcomeEmail({
  memberId,
  name,
  intentCurrent,
  onClose,
}: {
  memberId: string;
  name: string;
  intentCurrent: () => boolean;
  onClose: () => void;
}) {
  const [context, setContext] = useState<WelcomeContext | null>(null),
    [draft, setDraft] = useState<WelcomeDraft | null>(null),
    [review, setReview] = useState<WelcomeReview | null>(null),
    [receipt, setReceipt] = useState<WelcomeReceipt | null>(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState<"" | "preview" | "queue">(""),
    [error, setError] = useState(""),
    [reloadRequired, setReloadRequired] = useState(false),
    [uncertain, setUncertain] = useState(false);
  const sequence = useRef(0), pending = useRef<AbortController | null>(null),
    inFlight = useRef(false), frozenQueue = useRef<string | null>(null),
    draftRef = useRef<WelcomeDraft | null>(null),
    sessionCurrent = useRef(captureSessionContext()).current;
  const scopeCurrent = useCallback(() => intentCurrent() && sessionCurrent(), [intentCurrent, sessionCurrent]);
  const endpoint = `/members/${encodeURIComponent(memberId)}/welcome-email`;
  const keepDraft = (value: WelcomeDraft | null) => {
    draftRef.current = value;
    setDraft(value);
  };
  const discardReview = () => {
    setReview(null);
    frozenQueue.current = null;
  };
  const load = useCallback(async (preserve: boolean) => {
    if (!scopeCurrent() || inFlight.current) return;
    const request = ++sequence.current;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const current = () => !controller.signal.aborted && request === sequence.current && scopeCurrent();
    const previous = preserve ? draftRef.current : null;
    discardReview();
    setContext(null);
    setLoading(true);
    setReloadRequired(true);
    setError("");
    setReceipt(null);
    setUncertain(false);
    try {
      const value = checkedWelcomeContext(await api<WelcomeContext>(endpoint, {
        signal: controller.signal, cache: "no-store",
      }), memberId);
      if (!current()) return;
      setContext(value);
      keepDraft(previous ? { ...copyWelcomeDraft(previous), revision: value.draft.revision } : copyWelcomeDraft(value.draft));
      setReloadRequired(false);
    } catch (e) {
      if (current()) setError(message(e));
    } finally {
      if (current()) setLoading(false);
    }
  }, [endpoint, memberId, scopeCurrent]);

  useEffect(() => {
    void load(false);
    return () => { sequence.current++; pending.current?.abort(); };
  }, [load]);
  useEffect(() => {
    if (!review || uncertain) return;
    const remaining = Date.parse(review.expiresAt) - Date.now();
    const expire = () => {
      if (inFlight.current || !scopeCurrent()) return;
      discardReview();
      setReloadRequired(true);
      setError("预览已过期，编辑已保留。请重新读取资料并再次预览。");
    };
    if (remaining <= 0) { expire(); return; }
    const timer = window.setTimeout(expire, Math.min(remaining, 2147483647));
    return () => clearTimeout(timer);
  }, [review, uncertain, scopeCurrent]);

  const change = (patch: Partial<WelcomeDraft>) => {
    if (!draft || loading || inFlight.current || review || uncertain || !scopeCurrent()) return;
    discardReview();
    setError("");
    keepDraft({ ...draft, ...patch });
  };
  const assign = (header: WelcomeHeader, address: string) => {
    if (!context || !draft || !address) return;
    const option = context.recipientOptions.find(item => item.email === address);
    if (!option) { setError("请选择当前成员范围内的收件地址。"); return; }
    if (draft[header].includes(address)) return;
    const exists = welcomeHeaders.some(key => draft[key].includes(address));
    if (!exists && welcomeHeaders.flatMap(key => draft[key]).length >= welcomeMaxRecipients) {
      setError("To、CC、BCC 合计最多 10 个地址。");
      return;
    }
    change(Object.fromEntries(welcomeHeaders.map(key => [key,
      key === header ? [...draft[key].filter(email => email !== address), address] : draft[key].filter(email => email !== address),
    ])));
  };
  const preview = async () => {
    if (!draft || !context || reloadRequired || inFlight.current || !scopeCurrent()) return;
    discardReview();
    setError("");
    try { validateWelcomeDraft(draft, context.recipientOptions); }
    catch (e) { setError(message(e)); return; }
    const submitted = copyWelcomeDraft(draft), request = ++sequence.current;
    const controller = new AbortController();
    pending.current = controller;
    const current = () => !controller.signal.aborted && request === sequence.current && scopeCurrent();
    inFlight.current = true;
    setBusy("preview");
    try {
      const value = checkedWelcomeReview(await api<WelcomeReview>(`${endpoint}/review`, {
        method: "POST", signal: controller.signal, body: JSON.stringify(submitted),
      }), submitted, context);
      if (!current()) return;
      if (Date.parse(value.expiresAt) <= Date.now()) throw new Error("预览已过期，请重新读取资料后再试。");
      frozenQueue.current = JSON.stringify(welcomeQueueBody(value));
      setReview(value);
      keepDraft(copyWelcomeDraft(value.draft));
    } catch (e) {
      if (!current()) return;
      setError(message(e));
      if (e instanceof APIError && [401, 403, 404, 409, 412].includes(e.status)) setReloadRequired(true);
    } finally {
      if (request === sequence.current) inFlight.current = false;
      if (current()) setBusy("");
    }
  };
  const queue = async () => {
    if (!review || !frozenQueue.current || !review.deliveryEnabled || inFlight.current || !scopeCurrent()) return;
    if (!uncertain && Date.parse(review.expiresAt) <= Date.now()) {
      discardReview();
      setReloadRequired(true);
      setError("预览已过期，请重新读取资料并再次预览。");
      return;
    }
    const body = frozenQueue.current, request = ++sequence.current;
    const controller = new AbortController();
    pending.current = controller;
    const current = () => !controller.signal.aborted && request === sequence.current && scopeCurrent();
    inFlight.current = true;
    setBusy("queue");
    setError("");
    try {
      const value = await api<WelcomeReceipt>(`${endpoint}/queue`, {
        method: "POST", signal: controller.signal, body,
      });
      if (!current()) return;
      if (!value || typeof value.id !== "string" || !value.id || typeof value.outboxId !== "string" || !value.outboxId || typeof value.status !== "string" || typeof value.replayed !== "boolean")
        throw new Error("排队回执无法核对。");
      setReceipt(value);
      setUncertain(false);
      discardReview();
    } catch (e) {
      if (!current()) return;
      if (e instanceof APIError && e.status < 500) {
        discardReview();
        setReloadRequired(true);
        setError(`${message(e)} 编辑已保留，请重新读取并再次预览。`);
      } else {
        setUncertain(true);
        setError("未取得本次排队结果。请查看投递记录，或重试同一次确认；不要重新撰写相同邮件。");
      }
    } finally {
      if (request === sequence.current) inFlight.current = false;
      if (current()) setBusy("");
    }
  };

  if (!scopeCurrent()) return null;
  const fieldsDisabled = loading || !!busy || !!review || uncertain;
  const blocking = context?.blockingReasons.filter(code => code !== "delivery-disabled") ?? [];
  const selectedCount = draft ? welcomeHeaders.flatMap(header => draft[header]).length : 0;
  const enabled = review?.deliveryEnabled ?? context?.deliveryEnabled ?? false;
  return (
    <Dialog open onOpenChange={open => !open && !inFlight.current && onClose()}>
      <DialogContent className="max-w-3xl [&>*]:min-w-0">
        <DialogTitle className="flex items-center gap-2 pr-6 text-lg font-semibold"><Mail className="size-5 shrink-0 text-primary" />欢迎邮件 · {context?.memberName ?? name}</DialogTitle>
        <DialogDescription>用于成员运营沟通。添加成员账号请使用成员邀请。</DialogDescription>
        <ErrorNotice message={error} />
        {loading && <Loading />}
        {receipt ? (
          <section aria-label="欢迎邮件排队回执" className="space-y-4 rounded-lg border p-4">
            <p role="status" className="font-medium text-primary">{receipt.replayed ? "已找到本次排队回执。" : "欢迎邮件已取得排队回执。"}</p>
            <Badge variant="outline">队列状态：{welcomeReceiptStatus(receipt.status)}</Badge>
            <p className="break-all text-xs text-muted-foreground">回执编号：{receipt.id}<br />队列编号：{receipt.outboxId}</p>
            <a className="text-sm text-primary underline" href="#notifications" onClick={onClose}>查看投递记录</a>
            <div className="flex justify-end"><Button onClick={onClose}>完成</Button></div>
          </section>
        ) : (
          <>
            {context && draft && (
              <>
                <div className="flex flex-wrap items-center gap-2 text-xs"><Badge variant={enabled ? "secondary" : "outline"}>{enabled ? "邮件投递已启用" : "邮件投递未启用"}</Badge>{!review && <span className="break-all text-muted-foreground">发件人：{context.from || "尚未配置"}</span>}</div>
                {!enabled && <p className="notice text-sm">邮件投递尚未启用。可以预览，启用后才能确认排队。</p>}
                {!review && (
                  <fieldset disabled={fieldsDisabled} className="min-w-0 space-y-4">
                    <p className="text-xs leading-6 text-muted-foreground">可选地址仅来自当前成员的 NOC／成员邮箱、联系人、关联成员账号及交换中心支持邮箱。To、CC、BCC 合计最多 10 个；选择其他栏已有地址会移动到本栏。</p>
                    {welcomeHeaders.map(header => (
                      <section className="space-y-2 rounded-lg border p-3" aria-label={`欢迎邮件 ${header.toUpperCase()}`} key={header}>
                        <h3 className="text-sm font-medium">{welcomeHeaderLabel[header]}</h3>
                        <div className="flex flex-wrap gap-2">{draft[header].map(email => {
                          const available = context.recipientOptions.some(option => normalizedWelcomeEmail(option.email) === normalizedWelcomeEmail(email));
                          return <span className={`flex max-w-full items-center gap-1 rounded border px-2 py-1 text-xs ${available ? "" : "text-destructive"}`} key={email}><span className="min-w-0 break-all">{email}{!available && " · 来源已不可用"}</span><Button type="button" size="icon" variant="ghost" className="size-6 shrink-0" aria-label={`移除 ${header.toUpperCase()} ${email}`} onClick={() => change({ [header]: draft[header].filter(value => value !== email) })}><X className="size-3" /></Button></span>;
                        })}{!draft[header].length && <p className="text-xs text-muted-foreground">未选择</p>}</div>
                        <label className="sr-only" htmlFor={`welcome-add-${header}`}>添加 {header.toUpperCase()} 地址</label>
                        <select id={`welcome-add-${header}`} value="" onChange={event => assign(header, event.target.value)}>
                          <option value="">从当前范围选择地址…</option>
                          {context.recipientOptions.map(option => {
                            const existingHeader = welcomeHeaders.find(key => draft[key].includes(option.email));
                            return <option key={option.id} value={option.email} disabled={existingHeader === header || (!existingHeader && selectedCount >= welcomeMaxRecipients)}>{welcomeKindLabel[option.kind]} · {option.label} · {option.email}{existingHeader && existingHeader !== header ? `（移自 ${existingHeader.toUpperCase()}）` : ""}</option>;
                          })}
                        </select>
                        {header === "to" && <p className="text-xs text-muted-foreground">至少一个 To 地址须来自本成员、联系人或成员账号。</p>}
                        {header === "bcc" && <p className="text-xs text-muted-foreground">密送地址不会写入其他收件人的邮件头。</p>}
                      </section>
                    ))}
                    <div className="field"><label htmlFor="welcome-subject">邮件主题</label><Input id="welcome-subject" value={draft.subject} maxLength={300} onChange={event => change({ subject: event.target.value })} /><p className="text-xs text-muted-foreground">{welcomeBytes(draft.subject)} / 300 字节</p></div>
                    <div className="field"><label htmlFor="welcome-text">纯文本正文</label><textarea id="welcome-text" rows={10} value={draft.text} onChange={event => change({ text: event.target.value })} /><p className="text-xs text-muted-foreground">{welcomeBytes(draft.text)} / 65536 字节，按纯文本发送。</p></div>
                  </fieldset>
                )}
                {blocking.length > 0 && <ul className="notice space-y-1 text-sm">{blocking.map(code => <li key={code}>{welcomeBlockLabel(code)}</li>)}</ul>}
              </>
            )}
            {review && (
              <section aria-label="欢迎邮件发送预览" className="min-w-0 space-y-4 rounded-lg border bg-muted/20 p-4">
                <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">核对本次邮件</h3><p className="text-xs text-muted-foreground">预览有效至 {time(review.expiresAt)}</p></div>
                <dl className="space-y-3 text-sm">
                  <div><dt className="font-medium">发件人</dt><dd className="mt-1 break-all">{review.from || "尚未配置"}</dd></div>
                  {welcomeHeaders.map(header => <div key={header}><dt className="font-medium">{welcomeHeaderLabel[header]}</dt><dd className="mt-1 space-y-1 break-all">{review.draft[header].length ? review.draft[header].map(email => <p key={email}>{email}</p>) : "无"}</dd></div>)}
                  <div><dt className="font-medium">邮件主题</dt><dd className="mt-1 break-words">{review.draft.subject}</dd></div>
                </dl>
                <pre className="min-w-0 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-background p-3 text-sm leading-6">{review.draft.text}</pre>
                {uncertain && <a className="text-sm text-primary underline" href="#notifications" onClick={onClose}>查看投递记录</a>}
                <div className="flex flex-wrap justify-end gap-2">
                  {!uncertain && <Button variant="outline" disabled={!!busy} onClick={() => { discardReview(); setError(""); }}>返回编辑</Button>}
                  <Button disabled={!!busy || !review.deliveryEnabled || blocking.length > 0} onClick={() => void queue()}><Send />{busy === "queue" ? "正在排队…" : uncertain ? "重试本次确认" : "确认排队"}</Button>
                </div>
              </section>
            )}
            {!review && <div className="flex flex-wrap justify-end gap-2"><Button variant="outline" disabled={loading || !!busy} onClick={() => void load(!!draft)}><RefreshCw />{draft ? "重新读取，保留编辑" : "重新读取资料"}</Button><Button disabled={!context || !draft || loading || !!busy || reloadRequired || blocking.length > 0} onClick={() => void preview()}>{busy === "preview" ? "正在预览…" : "预览欢迎邮件"}</Button></div>}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
