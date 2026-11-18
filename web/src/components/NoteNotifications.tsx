import { useEffect, useRef, useState } from "react";
import { Bell, CheckCheck, Mail, RefreshCw, Search, X } from "lucide-react";
import { api, APIError, save } from "@/lib/api";
import {
  noteDraft,
  noteDraftKey,
  noteModeLabels,
  notePreferenceBody,
  watchKey,
  type NoteDraft,
  type NoteMode,
  type NotePreferences,
  type NoteSummary,
  type NoteWatch,
} from "@/lib/note-notifications";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Loading,
  message,
  time,
} from "@/components/shared";

type Option = { id: string; createdAt: string; name: string; detail?: string };
function WatchPicker({
  label,
  options,
  selected,
  onChange,
  disabled,
}: {
  label: string;
  options: Option[];
  selected: NoteWatch[];
  onChange: (next: NoteWatch[]) => void;
  disabled: boolean;
}) {
  const [search, setSearch] = useState("");
  const selectedIDs = new Set(selected.map((w) => w.id)),
    query = search.trim().toLowerCase(),
    matches = options.filter(
      (option) =>
        !selectedIDs.has(option.id) &&
        `${option.name} ${option.detail ?? ""} ${option.id}`
          .toLowerCase()
          .includes(query),
    );
  return (
    <section className="space-y-3" aria-label={`${label}订阅选择`}>
      <div className="flex justify-between gap-3">
        <h3 className="text-sm font-medium">{label}</h3>
        <span className="text-xs text-muted-foreground">
          {selected.length} / 256
        </span>
      </div>
      {selected.length > 0 && (
        <ul className="max-h-60 space-y-2 overflow-auto rounded-lg border p-2">
          {selected.map((watch) => {
            const current = options.find(
                (o) => watchKey(o) === watchKey(watch),
              ),
              stale = watch.stale === true || !current;
            return (
              <li
                key={watchKey(watch)}
                className="flex items-start gap-3 rounded-md bg-muted/40 p-2"
              >
                <div className="min-w-0 flex-1">
                  <p className="break-words text-sm">
                    {current?.name ?? watch.name ?? watch.id}
                  </p>
                  {stale ? (
                    <p className="mt-1 text-xs text-amber-700 dark:text-amber-300">
                      对象已删除或身份已变化 ·
                      旧订阅无效；取消后可重新选择当前对象
                    </p>
                  ) : (
                    current?.detail && (
                      <p className="text-xs text-muted-foreground">
                        {current.detail}
                      </p>
                    )
                  )}
                  {stale && (
                    <p className="mt-1 break-all text-xs text-muted-foreground">
                      {watch.id} · 原创建时间 {time(watch.createdAt)}
                    </p>
                  )}
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={`取消订阅 ${current?.name ?? watch.name ?? watch.id}`}
                  disabled={disabled}
                  onClick={() =>
                    onChange(
                      selected.filter((w) => watchKey(w) !== watchKey(watch)),
                    )
                  }
                >
                  <X className="size-4" />
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      <div className="relative">
        <Search className="pointer-events-none absolute left-3 top-3 size-4 text-muted-foreground" />
        <Input
          className="pl-9"
          aria-label={`搜索可订阅${label}`}
          placeholder={`搜索${label}`}
          value={search}
          disabled={disabled}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>
      <div className="max-h-48 space-y-1 overflow-auto rounded-lg border p-2">
        {matches.length ? (
          matches.slice(0, 40).map((option) => (
            <button
              type="button"
              key={watchKey(option)}
              disabled={disabled || selected.length >= 256}
              onClick={() =>
                onChange([
                  ...selected,
                  {
                    id: option.id,
                    createdAt: option.createdAt,
                    name: option.name,
                  },
                ])
              }
              className="flex w-full items-start justify-between gap-3 rounded p-2 text-left hover:bg-muted disabled:opacity-40"
            >
              <span className="min-w-0">
                <span className="block break-words text-sm">{option.name}</span>
                {option.detail && (
                  <span className="text-xs text-muted-foreground">
                    {option.detail}
                  </span>
                )}
              </span>
              <span className="shrink-0 text-xs text-primary">订阅</span>
            </button>
          ))
        ) : (
          <p className="p-2 text-xs text-muted-foreground">
            没有匹配的可订阅对象。
          </p>
        )}
      </div>
      {matches.length > 40 && (
        <p className="text-xs text-muted-foreground">
          显示前 40 个匹配对象，请缩小搜索范围。
        </p>
      )}
    </section>
  );
}

export default function NoteNotifications({ email }: { email: string }) {
  const [prefs, setPrefs] = useState<NotePreferences | null>(null),
    [draft, setDraft] = useState<NoteDraft | null>(null),
    [summary, setSummary] = useState<NoteSummary | null>(null),
    [conflict, setConflict] = useState<NotePreferences | null>(null),
    [conflictPending, setConflictPending] = useState(false),
    [error, setError] = useState(""),
    [summaryError, setSummaryError] = useState(""),
    [done, setDone] = useState(""),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [summaryBusy, setSummaryBusy] = useState(false),
    [ack, setAck] = useState(false),
    [query, setQuery] = useState(""),
    [memberFilter, setMemberFilter] = useState(""),
    [unreadOnly, setUnreadOnly] = useState(true),
    [page, setPage] = useState(0),
    [marking, setMarking] = useState<{
      revision: string;
      memberId?: string;
      count: number;
      label: string;
    } | null>(null);
  const alive = useRef(true);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const [p, s] = await Promise.all([
        api<NotePreferences>("/account/note-preferences"),
        api<NoteSummary>("/account/note-summary"),
      ]);
      if (alive.current) {
        setPrefs(p);
        setDraft(noteDraft(p));
        setSummary(s);
        setConflict(null);
        setConflictPending(false);
        setAck(false);
      }
    } catch (e) {
      if (alive.current) setError(message(e));
    } finally {
      if (alive.current) setLoading(false);
    }
  };
  useEffect(() => {
    alive.current = true;
    void load();
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => setPage(0), [query, memberFilter, unreadOnly]);
  const currentOptions = summary
    ? {
        members: summary.members.map((m) => ({
          id: m.id,
          createdAt: m.createdAt,
          name: m.name,
        })),
        notes: summary.items.map((n) => ({
          id: n.id,
          createdAt: n.createdAt,
          name: n.name,
          detail: n.memberName,
        })),
      }
    : { members: [], notes: [] };
  const refreshSummary = async () => {
    setSummaryBusy(true);
    setSummaryError("");
    try {
      const latest = await api<NoteSummary>("/account/note-summary");
      if (alive.current) setSummary(latest);
    } catch (e) {
      if (alive.current) setSummaryError(message(e));
    } finally {
      if (alive.current) setSummaryBusy(false);
    }
  };
  const readConflict = async () => {
    setBusy(true);
    setConflictPending(true);
    try {
      const [p, s] = await Promise.all([
        api<NotePreferences>("/account/note-preferences"),
        api<NoteSummary>("/account/note-summary"),
      ]);
      if (alive.current) {
        setConflict(p);
        setSummary(s);
        setConflictPending(false);
      }
    } catch (e) {
      if (alive.current)
        setError(`草稿已保留，但无法读取服务器当前偏好：${message(e)}`);
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const submit = async () => {
    if (
      !prefs ||
      !draft ||
      conflict ||
      conflictPending ||
      (draft.mode !== "none" && !ack)
    )
      return;
    setBusy(true);
    setError("");
    setDone("");
    try {
      const saved = await save<NotePreferences>(
        "/account/note-preferences",
        notePreferenceBody(draft, prefs.revision),
        "PUT",
      );
      if (!alive.current) return;
      setPrefs(saved);
      setDraft(noteDraft(saved));
      setAck(false);
      setDone("备注通知偏好已保存。");
      await refreshSummary();
    } catch (e) {
      if (!alive.current) return;
      setError(message(e));
      if (e instanceof APIError && e.status === 409) await readConflict();
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const markRead = async () => {
    if (!marking) return;
    setSummaryBusy(true);
    setSummaryError("");
    try {
      const next = await save<NoteSummary>("/account/note-read", {
        revision: marking.revision,
        ...(marking.memberId ? { memberId: marking.memberId } : {}),
      });
      if (alive.current) {
        setSummary(next);
        setMarking(null);
      }
    } catch (e) {
      if (alive.current) {
        setMarking(null);
        if (e instanceof APIError && e.status === 409) {
          try {
            setSummary(await api<NoteSummary>("/account/note-summary"));
            setSummaryError(
              "备注摘要已有新变化，未覆盖新版本的未读状态。请核对刷新后的摘要，再次确认标记已读。",
            );
          } catch (refreshError) {
            setSummaryError(`摘要已变化且刷新失败：${message(refreshError)}`);
          }
        } else setSummaryError(message(e));
      }
    } finally {
      if (alive.current) setSummaryBusy(false);
    }
  };
  if (loading && !prefs)
    return (
      <>
        <ErrorNotice message={error} />
        {error ? (
          <Button variant="outline" onClick={() => void load()}>
            重新加载备注设置
          </Button>
        ) : (
          <Loading />
        )}
      </>
    );
  if (!prefs || !draft || !summary)
    return <ErrorNotice message={error || "备注设置暂不可用。"} />;
  const dirty = noteDraftKey(draft) !== noteDraftKey(prefs),
    filtered = summary.items.filter(
      (n) =>
        (!unreadOnly || n.unread) &&
        (!memberFilter || n.memberId === memberFilter) &&
        `${n.name} ${n.memberName}`.toLowerCase().includes(query.toLowerCase()),
    ),
    pages = Math.max(1, Math.ceil(filtered.length / 20)),
    shown = filtered.slice(
      Math.min(page, pages - 1) * 20,
      (Math.min(page, pages - 1) + 1) * 20,
    );
  const watchNames = (watches: NoteWatch[], options: Option[]) =>
    watches.length
      ? watches
          .map(
            (w) =>
              options.find((o) => watchKey(o) === watchKey(w))?.name ??
              w.name ??
              w.id,
          )
          .join("、")
      : "无";
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Bell className="size-5 text-primary" />
            自己的备注通知
          </CardTitle>
          <CardDescription>
            仅管理当前账号的订阅。新账号默认不发送；旧系统偏好没有自动转成通知授权。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <ErrorNotice message={error} />
          {done && (
            <p className="notice" role="status">
              {done}
            </p>
          )}
          <div className="rounded-lg border bg-muted/30 p-4 text-sm leading-6">
            <p className="flex flex-wrap items-center gap-2">
              <Mail className="size-4 text-muted-foreground" />
              通知接收邮箱：
              <span className="break-all font-medium">
                {email || "当前账号邮箱"}
              </span>
            </p>
            <p className="mt-1 text-muted-foreground">
              开启后，后续符合订阅条件的备注变更会发送到当前账号邮箱。订阅不改变备注的访问权限。
            </p>
            {!prefs.deliveryEnabled && (
              <p className="mt-2 text-amber-700 dark:text-amber-300">
                邮件服务当前未启用。可以保存偏好；关闭期间的变更不会补发，已读状态仍可使用。
              </p>
            )}
          </div>
          {(conflict || conflictPending) && (
            <section
              className="space-y-4 rounded-lg border border-amber-400/50 p-4"
              aria-label="备注偏好冲突"
            >
              <p className="text-sm font-medium">
                服务器偏好或订阅对象已有变化，草稿已保留。
              </p>
              {conflict ? (
                <>
                  <div className="grid gap-3 text-sm md:grid-cols-2">
                    {[
                      { title: "我的草稿", value: draft },
                      {
                        title: `服务器版本 ${conflict.revision}`,
                        value: conflict,
                      },
                    ].map(({ title, value }) => (
                      <div
                        className="min-w-0 space-y-2 rounded border bg-muted/20 p-3"
                        key={title}
                      >
                        <h3 className="font-medium">{title}</h3>
                        <p>{noteModeLabels[value.mode]}</p>
                        <p className="break-words text-xs">
                          成员：
                          {watchNames(
                            value.memberWatches,
                            currentOptions.members,
                          )}
                        </p>
                        <p className="break-words text-xs">
                          备注：
                          {watchNames(value.noteWatches, currentOptions.notes)}
                        </p>
                      </div>
                    ))}
                  </div>
                  <p className="text-xs leading-5 text-muted-foreground">
                    保留草稿会以刚核对的服务器版本作为新基线；仍需点击保存。已删除或身份变化的对象不会自动绑定到新对象。
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        setPrefs(conflict);
                        setConflict(null);
                        setAck(false);
                        setError("");
                      }}
                    >
                      已核对，保留草稿继续编辑
                    </Button>
                    <Button
                      variant="ghost"
                      disabled={busy}
                      onClick={() => {
                        setPrefs(conflict);
                        setDraft(noteDraft(conflict));
                        setConflict(null);
                        setAck(false);
                        setError("");
                      }}
                    >
                      放弃草稿，采用服务器偏好
                    </Button>
                  </div>
                </>
              ) : (
                <Button
                  variant="outline"
                  disabled={busy}
                  onClick={() => void readConflict()}
                >
                  重新读取服务器偏好
                </Button>
              )}
            </section>
          )}
          <fieldset
            disabled={busy || !!conflict || conflictPending}
            className="space-y-5 disabled:opacity-60"
          >
            <div className="field max-w-xl">
              <label htmlFor="note-notification-mode">邮件通知范围</label>
              <select
                id="note-notification-mode"
                value={draft.mode}
                onChange={(e) => {
                  setDraft({ ...draft, mode: e.target.value as NoteMode });
                  setAck(false);
                  setDone("");
                }}
              >
                {Object.entries(noteModeLabels).map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </select>
              <p className="text-xs text-muted-foreground">
                选择“不发送”会停止后续通知；订阅列表仍保留，便于以后重新启用。
              </p>
            </div>
            {draft.mode !== "watched" && (
              <p className="text-xs leading-5 text-muted-foreground">
                以下订阅仅在“仅通知订阅的成员或备注”模式生效；你仍可维护列表。
              </p>
            )}
            <div className="grid gap-6 lg:grid-cols-2">
              <WatchPicker
                label="成员"
                options={currentOptions.members}
                selected={draft.memberWatches}
                disabled={busy}
                onChange={(next) => {
                  setDraft({ ...draft, memberWatches: next });
                  setAck(false);
                  setDone("");
                }}
              />
              <WatchPicker
                label="备注"
                options={currentOptions.notes}
                selected={draft.noteWatches}
                disabled={busy}
                onChange={(next) => {
                  setDraft({ ...draft, noteWatches: next });
                  setAck(false);
                  setDone("");
                }}
              />
            </div>
            {draft.mode !== "none" && (
              <label className="flex items-start gap-3 text-sm leading-6">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={ack}
                  onChange={(e) => setAck(e.target.checked)}
                />
                <span>
                  我同意按所选范围，将后续备注变更发送到我的当前账号邮箱。
                </span>
              </label>
            )}
            <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-4">
              <span className="text-xs text-muted-foreground">
                {dirty ? "有尚未保存的修改" : "当前偏好已保存"} · 版本{" "}
                {prefs.revision}
              </span>
              <Button
                disabled={!dirty || (draft.mode !== "none" && !ack)}
                onClick={() => void submit()}
              >
                {busy ? "正在保存…" : "保存备注通知偏好"}
              </Button>
            </div>
          </fieldset>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <CardTitle className="flex items-center gap-2">
                <CheckCheck className="size-5 text-primary" />
                备注未读摘要
                <Badge variant="outline">{summary.unreadCount} 条未读</Badge>
              </CardTitle>
              <CardDescription className="mt-2">
                标记已读只影响当前账号。打开备注或改变订阅不会自动标记已读。
              </CardDescription>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={summaryBusy}
                onClick={() => void refreshSummary()}
              >
                <RefreshCw className={summaryBusy ? "animate-spin" : ""} />
                刷新摘要
              </Button>
              <Button
                size="sm"
                disabled={summaryBusy || summary.unreadCount === 0}
                onClick={() =>
                  setMarking({
                    revision: summary.revision,
                    count: summary.unreadCount,
                    label: "所有当前备注",
                  })
                }
              >
                全部标记已读
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          <ErrorNotice message={summaryError} />
          <div className="flex flex-wrap gap-3">
            <Input
              className="min-w-0 flex-1"
              aria-label="搜索备注摘要"
              placeholder="搜索备注或成员"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <select
              className="!w-full sm:!w-auto"
              aria-label="按成员筛选备注摘要"
              value={memberFilter}
              onChange={(e) => setMemberFilter(e.target.value)}
            >
              <option value="">所有成员</option>
              {summary.members.map((m) => (
                <option key={watchKey(m)} value={m.id}>
                  {m.name} · {m.unreadCount} 未读
                </option>
              ))}
            </select>
            <label className="flex shrink-0 items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={unreadOnly}
                onChange={(e) => setUnreadOnly(e.target.checked)}
              />
              仅看未读
            </label>
          </div>
          {memberFilter && (
            <Button
              size="sm"
              variant="outline"
              disabled={
                summaryBusy ||
                !summary.members.find((m) => m.id === memberFilter)?.unreadCount
              }
              onClick={() => {
                const member = summary.members.find(
                  (m) => m.id === memberFilter,
                );
                if (member)
                  setMarking({
                    revision: summary.revision,
                    memberId: member.id,
                    count: member.unreadCount,
                    label: member.name,
                  });
              }}
            >
              将所选成员的当前备注标记已读
            </Button>
          )}
          {!summary.items.length ? (
            <Empty
              title="还没有成员备注"
              description="后续备注会显示在这里；通知偏好不会自动启用。"
            />
          ) : !filtered.length ? (
            <Empty
              title="没有匹配的备注"
              description="可关闭仅看未读，或调整成员与搜索条件。"
            />
          ) : (
            <ul className="divide-y rounded-lg border">
              {shown.map((note) => (
                <li
                  className="flex flex-wrap items-start gap-3 p-4"
                  key={watchKey(note)}
                >
                  <div className="min-w-0 flex-1">
                    <a
                      className="break-words text-sm font-medium text-primary hover:underline"
                      href={`#notes?id=${encodeURIComponent(note.id)}`}
                    >
                      {note.name}
                    </a>
                    <p className="mt-1 break-words text-xs text-muted-foreground">
                      {note.memberName} ·{" "}
                      {note.visibility === "internal" ? "内部备注" : "成员可见"}{" "}
                      · 更新于 {time(note.updatedAt)}
                    </p>
                  </div>
                  <div className="flex gap-2">
                    <Badge variant={note.unread ? "default" : "outline"}>
                      {note.unread ? "未读" : "已读"}
                    </Badge>
                    {note.watched && <Badge variant="outline">已订阅</Badge>}
                  </div>
                </li>
              ))}
            </ul>
          )}
          {filtered.length > 20 && (
            <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
              <span>
                {filtered.length} 条 · 第 {Math.min(page, pages - 1) + 1} /{" "}
                {pages} 页
              </span>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={page === 0}
                  onClick={() => setPage((p) => p - 1)}
                >
                  上一页
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={page >= pages - 1}
                  onClick={() => setPage((p) => p + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          )}
        </CardContent>
      </Card>
      <Dialog
        open={!!marking}
        onOpenChange={(open) => {
          if (!open && !summaryBusy) setMarking(null);
        }}
      >
        <DialogContent>
          <DialogTitle>确认标记当前摘要已读</DialogTitle>
          <DialogDescription>
            {marking?.label}：将所查看摘要中的 {marking?.count ?? 0}{" "}
            条未读备注标记为已读。若期间出现新编辑，服务器会拒绝旧摘要，请重新核对。
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={summaryBusy}
              onClick={() => setMarking(null)}
            >
              取消
            </Button>
            <Button disabled={summaryBusy} onClick={() => void markRead()}>
              确认标记已读
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
