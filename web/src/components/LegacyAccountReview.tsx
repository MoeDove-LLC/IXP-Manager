import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { FileJson, MailPlus, RefreshCw, Search, X } from "lucide-react";
import { api, APIError, captureSessionContext } from "@/lib/api";
import {
  accountArchiveLimit,
  accountFinding,
  accountMembershipLimit,
  accountReviewBody,
  accountReviewRequest,
  accountSelectionLimit,
  checkedAccountReview,
  parseAccountArchive,
  sourceAccountEmail,
  sourceAccountName,
  type AccountReview,
  type AccountReviewRequest,
  type AccountSelection,
  type ExistingAccountCandidate,
  type LoadedAccountArchive,
  type SourceAccount,
  type SourceAccountMembership,
} from "@/lib/account-archive";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { Empty, ErrorNotice, Loading, message } from "@/components/shared";

type MemberChoice = { id: string; name: string; status?: string };
type Proof = {
  request: AccountReviewRequest;
  review: AccountReview;
  epoch: number;
  contextCurrent: () => boolean;
};
type Receipt = { email: string; memberName?: string; status: string };

function AccountRelationships({
  user,
  relationships,
  selected,
  onChange,
  onRemove,
  disabled,
}: {
  user: SourceAccount;
  relationships: SourceAccountMembership[];
  selected: AccountSelection;
  onChange: (ids: string[]) => void;
  onRemove: () => void;
  disabled: boolean;
}) {
  const [query, setQuery] = useState(""), [page, setPage] = useState(0);
  const filtered = relationships.filter(row =>
    `${row.sourceCustomerId} ${row.legacyId}`.includes(query.trim()),
  );
  const pages = Math.max(1, Math.ceil(filtered.length / 32));
  const shown = filtered.slice(Math.min(page, pages - 1) * 32, (Math.min(page, pages - 1) + 1) * 32);
  return (
    <article className="space-y-3 rounded-lg border p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h4 className="break-words text-sm font-medium">{sourceAccountName(user)}</h4>
          <p className="mt-1 break-all text-xs text-muted-foreground">{sourceAccountEmail(user)} · 旧账号 #{user.legacyId}</p>
        </div>
        <Button size="icon" variant="ghost" disabled={disabled} aria-label={`取消选择旧账号 ${user.legacyId}`} onClick={onRemove}><X /></Button>
      </div>
      <p className="text-xs text-muted-foreground">已选择 {selected.sourceMembershipIds.length} / {relationships.length} 条关系，每个账号最多 {accountMembershipLimit} 条。</p>
      {relationships.length > 32 && (
        <Input aria-label={`筛选账号 ${user.legacyId} 的旧组织关系`} placeholder="按旧组织 ID 或关系 ID 筛选" value={query} disabled={disabled} onChange={event => { setQuery(event.target.value); setPage(0); }} />
      )}
      {!relationships.length ? <p className="text-sm text-destructive">此账号没有归档中的组织关系，须核对来源。</p> : (
        <div className="max-h-56 space-y-2 overflow-auto">
          {shown.map(row => {
            const checked = selected.sourceMembershipIds.includes(row.legacyId);
            return (
              <label className="flex items-start gap-2 rounded p-2 text-sm hover:bg-muted/40" key={row.legacyId}>
                <input type="checkbox" className="mt-1" checked={checked} disabled={disabled || (!checked && selected.sourceMembershipIds.length >= accountMembershipLimit)} onChange={event => onChange(event.target.checked ? [...selected.sourceMembershipIds, row.legacyId] : selected.sourceMembershipIds.filter(id => id !== row.legacyId))} />
                <span className="min-w-0 break-words">旧组织 #{row.sourceCustomerId}<span className="ml-2 text-xs text-muted-foreground">关系 #{row.legacyId}</span>{user.facts.custid === row.sourceCustomerId && <Badge className="ml-2" variant="outline">旧默认组织</Badge>}</span>
              </label>
            );
          })}
        </div>
      )}
      {pages > 1 && <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground"><span>第 {Math.min(page, pages - 1) + 1} / {pages} 页</span><div className="flex gap-2"><Button size="sm" variant="outline" disabled={disabled || page === 0} onClick={() => setPage(value => value - 1)}>上一页</Button><Button size="sm" variant="outline" disabled={disabled || page >= pages - 1} onClick={() => setPage(value => value + 1)}>下一页</Button></div></div>}
    </article>
  );
}

export default function LegacyAccountReview({ onInvited }: { onInvited: () => void }) {
  const [loaded, setLoaded] = useState<LoadedAccountArchive | null>(null),
    [filename, setFilename] = useState(""),
    [fileLoading, setFileLoading] = useState(false),
    [members, setMembers] = useState<MemberChoice[]>([]),
    [memberLoading, setMemberLoading] = useState(true),
    [memberError, setMemberError] = useState(""),
    [selected, setSelected] = useState<AccountSelection[]>([]),
    [organizations, setOrganizations] = useState<Record<string, string>>({}),
    [candidates, setCandidates] = useState<Record<string, ExistingAccountCandidate>>({}),
    [query, setQuery] = useState(""),
    [page, setPage] = useState(0),
    [review, setReview] = useState<AccountReview | null>(null),
    [proof, setProof] = useState<Proof | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState<"" | "preview" | "invite">(""),
    [activateOpen, setActivateOpen] = useState(false),
    [activationError, setActivationError] = useState(""),
    [mfaRequired, setMfaRequired] = useState(false),
    [receipt, setReceipt] = useState<Receipt[] | null>(null);
  const epoch = useRef(0), fileEpoch = useRef(0), memberEpoch = useRef(0),
    pending = useRef<AbortController | null>(null),
    pendingMembers = useRef<AbortController | null>(null),
    submitting = useRef(false),
    scopeCurrent = useRef(captureSessionContext()).current;

  const invalidate = () => {
    epoch.current++;
    pending.current?.abort();
    setProof(null);
    setActivateOpen(false);
    setActivationError("");
    setBusy("");
    setReceipt(null);
  };
  const loadMembers = async () => {
    invalidate();
    pendingMembers.current?.abort();
    const controller = new AbortController();
    pendingMembers.current = controller;
    const request = ++memberEpoch.current;
    const current = () => !controller.signal.aborted && request === memberEpoch.current && scopeCurrent();
    setMemberLoading(true);
    setMemberError("");
    try {
      const data = await api<{ items: MemberChoice[] }>("/resources/members", { signal: controller.signal, cache: "no-store" });
      if (current()) setMembers(data.items);
    } catch (e) {
      if (current()) setMemberError(message(e));
    } finally {
      if (current()) setMemberLoading(false);
    }
  };
  useEffect(() => {
    void loadMembers();
    return () => {
      epoch.current++;
      fileEpoch.current++;
      memberEpoch.current++;
      pending.current?.abort();
      pendingMembers.current?.abort();
    };
  }, []);

  const selectFile = async (file?: File) => {
    if (!file || submitting.current) return;
    const request = ++fileEpoch.current;
    invalidate();
    setLoaded(null);
    setSelected([]);
    setOrganizations({});
    setCandidates({});
    setReview(null);
    setQuery("");
    setPage(0);
    setFilename("");
    setError("");
    setFileLoading(true);
    try {
      if (file.size > accountArchiveLimit) throw new Error("账号归档不能超过 32 MiB。");
      const raw = new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer());
      const archive = parseAccountArchive(raw);
      if (request !== fileEpoch.current || !scopeCurrent()) return;
      setLoaded(archive);
      setFilename(file.name);
    } catch (e) {
      if (request === fileEpoch.current && scopeCurrent()) setError(message(e));
    } finally {
      if (request === fileEpoch.current && scopeCurrent()) setFileLoading(false);
    }
  };
  const changeSelection = (next: AccountSelection[]) => {
    if (submitting.current) return;
    invalidate();
    setSelected(next);
    setError("");
  };
  const filtered = useMemo(() => {
    const term = query.trim().toLowerCase();
    return loaded?.archive.users.filter(user =>
      `${sourceAccountName(user)} ${sourceAccountEmail(user)} ${user.legacyId}`.toLowerCase().includes(term),
    ) ?? [];
  }, [loaded, query]);
  const pages = Math.max(1, Math.ceil(filtered.length / 40));
  const shown = filtered.slice(Math.min(page, pages - 1) * 40, (Math.min(page, pages - 1) + 1) * 40);
  const customerIds = [...new Set(selected.flatMap(account => account.sourceMembershipIds.flatMap(id => {
    const relationship = loaded?.memberships.get(id);
    return relationship ? [relationship.sourceCustomerId] : [];
  })))];
  const proofCurrent = !!proof && proof.epoch === epoch.current && proof.contextCurrent() && scopeCurrent();

  const preview = async () => {
    if (!loaded || submitting.current) return;
    invalidate();
    const requestEpoch = epoch.current, contextCurrent = captureSessionContext();
    const controller = new AbortController();
    pending.current = controller;
    const current = () => !controller.signal.aborted && requestEpoch === epoch.current && contextCurrent() && scopeCurrent();
    setBusy("preview");
    setError("");
    try {
      const request = accountReviewRequest(loaded, selected, organizations, candidates);
      const result = checkedAccountReview(await api<AccountReview>("/import/account-archive/preview", {
        method: "POST", signal: controller.signal, body: accountReviewBody(request),
      }), request, loaded.archive.checksum);
      if (!current()) return;
      setReview(result);
      setCandidates(Object.fromEntries(result.accounts.flatMap(account => account.existingCandidate ? [[account.sourceUserId, account.existingCandidate]] : [])));
      if (result.revision && result.accounts.every(account => account.status === "ready"))
        setProof({ request, review: result, epoch: requestEpoch, contextCurrent });
    } catch (e) {
      if (current()) setError(message(e));
    } finally {
      if (current()) setBusy("");
    }
  };
  const activate = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!proof || !proofCurrent || submitting.current) return;
    const reviewed = proof, form = event.currentTarget;
    const credentials = Object.fromEntries(new FormData(form));
    const controller = new AbortController();
    pending.current = controller;
    const current = () => !controller.signal.aborted && reviewed.epoch === epoch.current && reviewed.contextCurrent() && scopeCurrent();
    submitting.current = true;
    setBusy("invite");
    setActivationError("");
    try {
      const result = await api<{ items: Receipt[] }>("/import/account-archive/invite", {
        method: "POST", signal: controller.signal,
        body: accountReviewBody(reviewed.request, {
          revision: reviewed.review.revision,
          currentPassword: String(credentials.currentPassword ?? ""),
          ...(credentials.code ? { code: String(credentials.code) } : {}),
        }),
      });
      if (!current()) return;
      if (!Array.isArray(result.items) || result.items.length !== reviewed.request.selected.length ||
        result.items.some(item => typeof item.email !== "string" || typeof item.status !== "string"))
        throw new Error("返回结果无法完整核对，请刷新邀请列表，不要直接重复发送。");
      form.reset();
      setProof(null);
      setReview(null);
      setSelected([]);
      setActivateOpen(false);
      setReceipt(result.items);
      onInvited();
    } catch (e) {
      if (!current()) return;
      form.reset();
      if (e instanceof APIError && e.data?.mfaRequired) {
        setMfaRequired(true);
        setActivationError("请填写当前密码及验证器代码或恢复码后，再次确认。");
      } else {
        setProof(null);
        setActivateOpen(false);
        setError(`${message(e)} 请重新审核当前计划；系统不会自动重复发送。`);
      }
    } finally {
      submitting.current = false;
      if (current()) setBusy("");
    }
  };

  if (!scopeCurrent()) return <ErrorNotice message="登录上下文已变化，请重新打开旧账号批量审核。" />;
  const editingDisabled = busy === "invite";
  return (
    <Card className="mb-6" aria-label="旧账号批量审核">
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><FileJson className="size-5 text-primary" />旧账号批量审核</CardTitle>
        <CardDescription>选择归档、关联组织并核对计划，再创建激活邀请。权限在接收方接受邀请后生效。</CardDescription>
      </CardHeader>
      <CardContent className="space-y-6">
        <ErrorNotice message={error} />
        {receipt && <div className="notice space-y-2" role="status"><p>已创建 {receipt.length} 封激活邀请并进入邮件队列，等待本人接受。</p><ul className="space-y-1 text-sm">{receipt.map((item, index) => <li className="break-all" key={`${item.email}:${index}`}>{item.email} · {item.status === "pending" ? "等待接受" : item.status}</li>)}</ul></div>}
        <div className="field max-w-2xl">
          <label htmlFor="account-source-file">1. 选择账号归档</label>
          <Input id="account-source-file" type="file" accept=".json,application/json" disabled={editingDisabled} onChange={event => void selectFile(event.target.files?.[0])} />
          <p className="text-xs text-muted-foreground">使用 ixp-migrate --account-archive 生成的 JSON，最多 32 MiB。归档内容保持原样，服务器在审核时校验摘要。</p>
        </div>
        {fileLoading ? <Loading /> : loaded && (
          <>
            <p className="break-words text-sm text-muted-foreground">{filename} · {loaded.archive.users.length} 个旧账号 · {loaded.archive.memberships.length} 条组织关系</p>
            <section className="space-y-4" aria-label="选择旧账号">
              <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="font-medium">2. 选择旧账号</h3><Badge variant="outline">已选 {selected.length} / {accountSelectionLimit}</Badge></div>
              <div className="relative max-w-xl"><Search className="pointer-events-none absolute left-3 top-3 size-4 text-muted-foreground" /><Input className="pl-9" aria-label="筛选旧账号" placeholder="姓名、邮箱或旧账号 ID" value={query} onChange={event => { setQuery(event.target.value); setPage(0); }} /></div>
              <div className="max-h-80 divide-y overflow-auto rounded-lg border">
                {shown.length ? shown.map(user => {
                  const checked = selected.some(item => item.sourceUserId === user.legacyId);
                  return <label className="flex cursor-pointer items-start gap-3 p-3 hover:bg-muted/30" key={user.legacyId}>
                    <input className="mt-1" type="checkbox" aria-label={`选择旧账号 ${user.legacyId}`} checked={checked} disabled={editingDisabled || (!checked && selected.length >= accountSelectionLimit)} onChange={event => {
                      if (!event.target.checked) changeSelection(selected.filter(item => item.sourceUserId !== user.legacyId));
                      else {
                        const relationships = loaded.byUser.get(user.legacyId) ?? [];
                        changeSelection([...selected, { sourceUserId: user.legacyId, sourceMembershipIds: relationships.length <= accountMembershipLimit ? relationships.map(row => row.legacyId) : [] }]);
                      }
                    }} />
                    <span className="min-w-0"><span className="block break-words text-sm font-medium">{sourceAccountName(user)}</span><span className="mt-1 block break-all text-xs text-muted-foreground">{sourceAccountEmail(user)} · #{user.legacyId} · {loaded.byUser.get(user.legacyId)?.length ?? 0} 条关系</span></span>
                  </label>;
                }) : <Empty title="没有匹配的旧账号" description="请调整筛选条件。" />}
              </div>
              {pages > 1 && <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground"><span>{filtered.length} 个匹配 · 第 {Math.min(page, pages - 1) + 1} / {pages} 页</span><div className="flex gap-2"><Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage(value => value - 1)}>上一页</Button><Button size="sm" variant="outline" disabled={page >= pages - 1} onClick={() => setPage(value => value + 1)}>下一页</Button></div></div>}
              <div className="grid gap-4 xl:grid-cols-2">{selected.map(selection => {
                const user = loaded.users.get(selection.sourceUserId)!;
                return <AccountRelationships key={user.legacyId} user={user} relationships={loaded.byUser.get(user.legacyId) ?? []} selected={selection} disabled={editingDisabled}
                  onRemove={() => changeSelection(selected.filter(item => item.sourceUserId !== user.legacyId))}
                  onChange={ids => changeSelection(selected.map(item => item.sourceUserId === user.legacyId ? { ...item, sourceMembershipIds: ids } : item))} />;
              })}</div>
            </section>
            <section className="space-y-4" aria-label="关联旧组织">
              <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="font-medium">3. 关联组织</h3><Button size="sm" variant="outline" disabled={memberLoading || editingDisabled} onClick={() => void loadMembers()}><RefreshCw />重新读取当前组织</Button></div>
              <ErrorNotice message={memberError} />
              {memberLoading ? <Loading /> : !customerIds.length ? <p className="text-sm text-muted-foreground">选择账号及组织关系后，在这里关联对应的当前组织。</p> : <div className="grid gap-4 md:grid-cols-2">{customerIds.map(id => <div className="field" key={id}><label htmlFor={`account-organization-${id}`}>旧组织 #{id}</label><select id={`account-organization-${id}`} value={organizations[id] ?? ""} disabled={editingDisabled} onChange={event => { invalidate(); setOrganizations({ ...organizations, [id]: event.target.value }); }}><option value="">选择当前组织</option>{members.map(member => <option key={member.id} value={member.id}>{member.name}{member.status && member.status !== "active" ? "（未激活）" : ""}</option>)}</select></div>)}</div>}
            </section>
            <section className="space-y-4" aria-label="审核邀请计划">
              <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="font-medium">4. 审核邀请计划</h3><Button disabled={!selected.length || !!busy || fileLoading} onClick={() => void preview()}>{busy === "preview" ? "正在审核…" : "读取审核预览"}</Button></div>
              {review && !proofCurrent && <p className="text-sm text-muted-foreground">当前展示为核对信息；修改选择或关联后，必须重新读取审核预览。</p>}
              {review?.accounts.filter(account => selected.some(item => item.sourceUserId === account.sourceUserId)).map(account => {
                const choice = selected.find(item => item.sourceUserId === account.sourceUserId)!;
                const candidate = account.existingCandidate;
                return <article className="space-y-4 rounded-lg border p-4" key={account.sourceUserId}>
                  <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><h4 className="break-words font-medium">{account.name}</h4><p className="mt-1 break-all text-sm text-muted-foreground">{account.email} · 旧账号 #{account.sourceUserId}</p></div><Badge variant={account.status === "ready" && proofCurrent ? "default" : "outline"}>{account.status === "held" ? "待核对" : proofCurrent ? "可创建邀请" : "待重新审核"}</Badge></div>
                  {account.findings.length > 0 && <ul className="space-y-1 text-sm text-muted-foreground">{account.findings.map((finding, index) => <li key={`${finding}:${index}`}>{accountFinding(finding)}</li>)}</ul>}
                  {candidate && <div className="rounded-lg border bg-muted/20 p-3"><p className="break-words text-sm">现有成员账号：{candidate.name}</p><p className="mt-1 break-all text-xs text-muted-foreground">{candidate.email}</p><label className="mt-3 flex items-start gap-2 text-sm"><input className="mt-1" type="checkbox" disabled={editingDisabled} checked={choice.existingUserId === candidate.id} onChange={event => changeSelection(selected.map(item => item.sourceUserId === account.sourceUserId ? { ...item, existingUserId: event.target.checked ? candidate.id : undefined } : item))} /><span>已核对并关联此现有账号</span></label></div>}
                  {choice.existingUserId && choice.existingUserId !== candidate?.id && <Button size="sm" variant="outline" disabled={editingDisabled} onClick={() => changeSelection(selected.map(item => item.sourceUserId === account.sourceUserId ? { ...item, existingUserId: undefined } : item))}>取消失效的现有账号关联</Button>}
                  <ol className="space-y-2">{account.organizations.map((org, index) => <li className="flex flex-wrap justify-between gap-2 rounded bg-muted/30 p-3 text-sm" key={org.sourceMembershipId}><span className="break-words">{org.memberName}{index === 0 && !account.existingCandidate && <span className="ml-2 text-xs text-muted-foreground">默认组织</span>}</span><span>{org.currentMemberRole ? (org.currentMemberRole === "member-admin" ? "成员管理员 → " : "普通成员 → ") : ""}{org.memberAdmin ? "成员管理员" : "普通成员"}</span></li>)}</ol>
                </article>;
              })}
              {proofCurrent && <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-primary/30 bg-primary/5 p-4"><p className="text-sm">已审核 {proof.review.accounts.length} 个账号。创建邀请后，由本人接受并确认组织权限。</p><Button disabled={!!busy} onClick={() => { setActivateOpen(true); setActivationError(""); setMfaRequired(false); }}><MailPlus />确认创建激活邀请</Button></div>}
            </section>
          </>
        )}
      </CardContent>
      <Dialog open={activateOpen && proofCurrent} onOpenChange={open => !submitting.current && setActivateOpen(open)}>
        <DialogContent>
          <DialogTitle>确认创建激活邀请</DialogTitle>
          <DialogDescription>将为已审核的 {proof?.review.accounts.length ?? 0} 个旧账号创建邮件邀请。新账号由本人设置密码，现有账号需登录并验证身份；接受后才建立或升级所列组织权限。</DialogDescription>
          <form onSubmit={activate} className="space-y-4">
            <ErrorNotice message={activationError} />
            <fieldset className="space-y-4" disabled={busy === "invite"}>
              <div className="field"><label htmlFor="account-review-password">当前密码</label><Input id="account-review-password" name="currentPassword" type="password" autoComplete="current-password" required /></div>
              <div className="field"><label htmlFor="account-review-code">验证器代码或恢复码（已开启时填写）</label><Input id="account-review-code" name="code" autoComplete="one-time-code" required={mfaRequired} /></div>
              <div className="flex flex-wrap justify-end gap-2"><Button type="button" variant="outline" onClick={() => setActivateOpen(false)}>取消</Button><Button type="submit">{busy === "invite" ? "正在创建…" : "创建并进入邮件队列"}</Button></div>
            </fieldset>
          </form>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
