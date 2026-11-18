import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { Check, Mail, Plus, Save, Trash2, Pencil } from "lucide-react";
import { api, captureSessionContext, save } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
} from "@/components/shared";
const MemberLogo = lazy(() => import("@/components/MemberLogo"));
const MemberUsers = lazy(() => import("@/components/MemberUsers"));
const fields = [
  { key: "name", label: "网络名称", required: true },
  { key: "email", label: "联系邮箱", type: "email" },
  { key: "website", label: "网站", type: "url" },
  { key: "phone", label: "电话" },
  { key: "address", label: "地址" },
  { key: "city", label: "城市" },
  { key: "country", label: "国家 / 地区" },
  { key: "postalCode", label: "邮政编码" },
  { key: "registrationNumber", label: "注册编号" },
  { key: "vatNumber", label: "税号" },
];
export default function MemberProfile({
  memberId,
  memberRole,
  tenantSwitchPending = false,
  onChange,
}: {
  memberId?: string;
  memberRole?: string;
  tenantSwitchPending?: boolean;
  onChange: () => void;
}) {
  const canEdit = memberRole === "member-admin";
  const [profile, setProfile] = useState<Record<string, unknown> | null>(null),
    [contacts, setContacts] = useState<Row[]>([]),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [busy, setBusy] = useState(false),
    [loading, setLoading] = useState(true),
    [open, setOpen] = useState(false),
    [editing, setEditing] = useState<Row | null>(null),
    [form, setForm] = useState<Record<string, string>>({}),
    [formError, setFormError] = useState(""),
    [deleting, setDeleting] = useState<Row | null>(null);
  const loadSequence = useRef(0),
    pendingLoad = useRef<AbortController | null>(null),
    profileContext = useRef<() => boolean>(() => false);
  const load = useCallback(async () => {
    pendingLoad.current?.abort();
    const controller = new AbortController();
    pendingLoad.current = controller;
    const request = ++loadSequence.current,
      contextCurrent = captureSessionContext();
    const current = () =>
      !controller.signal.aborted &&
      request === loadSequence.current &&
      contextCurrent();
    setLoading(true);
    setError("");
    try {
      if (!memberId) throw new Error("当前账号尚未选定成员组织。");
      const [p, c] = await Promise.all([
        api<Record<string, unknown>>("/member/profile", {
          signal: controller.signal,
          cache: "no-store",
        }),
        api<{ items: Row[] }>("/member/contacts", {
          signal: controller.signal,
          cache: "no-store",
        }),
      ]);
      if (!current()) return;
      if (
        p.id !== memberId ||
        !Array.isArray(c.items) ||
        c.items.some((contact) => contact.memberId !== memberId)
      )
        throw new Error("资料与当前成员组织不一致，请重新加载。");
      profileContext.current = contextCurrent;
      setProfile(p);
      setContacts(c.items);
    } catch (e) {
      if (current()) setError(message(e));
    } finally {
      if (current()) setLoading(false);
    }
  }, [memberId]);
  useEffect(() => {
    void load();
    return () => {
      loadSequence.current++;
      pendingLoad.current?.abort();
    };
  }, [load]);
  const update = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setDone("");
    try {
      setProfile(
        await save<Record<string, unknown>>(
          "/member/profile",
          Object.fromEntries(
            fields.map((f) => [f.key, String(profile?.[f.key] ?? "")]),
          ),
          "PUT",
        ),
      );
      setDone("网络资料已更新。");
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const edit = (contact?: Row) => {
    setEditing(contact ?? null);
    setForm(
      Object.fromEntries(
        ["name", "email", "phone", "role"].map((key) => [
          key,
          String(contact?.[key] ?? ""),
        ]),
      ),
    );
    setFormError("");
    setOpen(true);
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    try {
      await save(
        `/member/contacts${editing ? `/${editing.id}` : ""}`,
        form,
        editing ? "PUT" : "POST",
      );
      setOpen(false);
      await load();
      setDone("联系人已保存。");
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    setFormError("");
    try {
      await api(`/member/contacts/${deleting.id}`, { method: "DELETE" });
      setDeleting(null);
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="我的网络"
        description={
          canEdit
            ? "维护当前成员网络的公开联络资料与联系人。"
            : "查看当前成员网络的资料；如需修改，请联系成员管理员。"
        }
      />
      <ErrorNotice message={error} />
      {done && (
        <p className="notice mb-5 flex items-center gap-2" role="status">
          <Check className="size-4" />
          {done}
        </p>
      )}
      {loading && !profile ? (
        <Loading />
      ) : (
        profile && profile.id === memberId && profileContext.current() && (
          <>
            <div className="mb-6">
              <Suspense fallback={<Loading />}>
                <MemberLogo key={String(profile.id)} />
              </Suspense>
            </div>
            <Card>
              <CardHeader>
                <CardTitle>{String(profile.name ?? "网络资料")}</CardTitle>
                <p className="text-xs text-muted-foreground">
                  AS{String(profile.asn ?? "")} · 当前成员工作空间
                </p>
              </CardHeader>
              <CardContent>
                <form onSubmit={update} className="space-y-5">
                  <div className="form-grid">
                    {fields.map((f) => (
                      <div key={f.key} className="field">
                        <label htmlFor={`member-${f.key}`}>{f.label}</label>
                        <Input
                          id={`member-${f.key}`}
                          type={f.type ?? "text"}
                          required={f.required}
                          readOnly={!canEdit}
                          value={String(profile[f.key] ?? "")}
                          onChange={(e) =>
                            setProfile({ ...profile, [f.key]: e.target.value })
                          }
                        />
                      </div>
                    ))}
                  </div>
                  {canEdit && (
                    <div className="flex justify-end border-t pt-4">
                      <Button disabled={busy}>
                        <Save />
                        {busy ? "正在保存…" : "保存网络资料"}
                      </Button>
                    </div>
                  )}
                </form>
              </CardContent>
            </Card>
            {canEdit && !loading && !tenantSwitchPending && (
              <Suspense fallback={<Loading />}>
                <MemberUsers
                  key={memberId}
                  memberId={String(profile.id)}
                  memberName={String(profile.name ?? "当前组织")}
                />
              </Suspense>
            )}
            <Card className="mt-6 overflow-hidden">
              <CardHeader className="flex-row items-center justify-between">
                <CardTitle className="flex items-center gap-2">
                  <Mail className="size-4 text-primary" />
                  网络联系人
                </CardTitle>
                {canEdit && (
                  <Button size="sm" onClick={() => edit()}>
                    <Plus />
                    新增联系人
                  </Button>
                )}
              </CardHeader>
              {!contacts.length ? (
                <Empty
                  title="还没有联系人"
                  description="记录网络运维与业务联络方式，让交换中心能够及时联系你。"
                />
              ) : (
                <div className="overflow-x-auto">
                  <table>
                    <thead>
                      <tr>
                        <th>姓名</th>
                        <th>邮箱</th>
                        <th>电话</th>
                        <th>职责</th>
                        {canEdit && <th className="text-right">操作</th>}
                      </tr>
                    </thead>
                    <tbody>
                      {contacts.map((c) => (
                        <tr key={c.id}>
                          <td className="font-medium">{c.name}</td>
                          <td>{String(c.email ?? "—")}</td>
                          <td>{String(c.phone ?? "—")}</td>
                          <td>{String(c.role ?? "—")}</td>
                          {canEdit && (
                            <td>
                              <div className="flex justify-end">
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label={`编辑联系人 ${c.name}`}
                                  onClick={() => edit(c)}
                                >
                                  <Pencil />
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label={`删除联系人 ${c.name}`}
                                  onClick={() => {
                                    setDeleting(c);
                                    setFormError("");
                                  }}
                                >
                                  <Trash2 />
                                </Button>
                              </div>
                            </td>
                          )}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Card>
          </>
        )
      )}
      {!loading && error && (
        <Button className="mt-4" variant="outline" onClick={() => void load()}>
          重新加载网络资料
        </Button>
      )}
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {editing ? "编辑联系人" : "新增联系人"}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            此联系人仅属于当前选择的成员网络。
          </DialogDescription>
          <form className="space-y-4" onSubmit={submit}>
            <ErrorNotice message={formError} />
            {[
              { key: "name", label: "姓名", required: true },
              { key: "email", label: "邮箱", required: true, type: "email" },
              { key: "phone", label: "电话" },
              { key: "role", label: "职责" },
            ].map((f) => (
              <div key={f.key} className="field">
                <label htmlFor={`contact-${f.key}`}>{f.label}</label>
                <Input
                  id={`contact-${f.key}`}
                  type={f.type ?? "text"}
                  required={f.required}
                  value={form[f.key] ?? ""}
                  onChange={(e) =>
                    setForm({ ...form, [f.key]: e.target.value })
                  }
                />
              </div>
            ))}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy}>保存联系人</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleting}
        onOpenChange={(v) => !busy && !v && setDeleting(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            删除联系人
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            确定删除当前网络的联系人「{deleting?.name}」？
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDeleting(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认删除联系人
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
