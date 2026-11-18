import { useEffect, useState, type FormEvent } from "react";
import { MailPlus, RefreshCw, Trash2 } from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
type Invite = {
  id: string;
  email: string;
  memberId: string;
  memberName: string;
  memberAdmin: boolean;
  status: string;
  createdAt: string;
  expiresAt: string;
};
const status: Record<string, string> = {
  pending: "等待接受",
  accepted: "已接受",
  expired: "已过期",
  revoked: "已撤销",
};
export default function Invitations({
  role,
  memberId,
}: {
  role: string;
  memberId?: string;
}) {
  const [items, setItems] = useState<Invite[]>([]),
    [members, setMembers] = useState<Row[]>([]),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [open, setOpen] = useState(false),
    [formError, setFormError] = useState(""),
    [mfaRequired, setMfaRequired] = useState(false),
    [revoke, setRevoke] = useState<Invite | null>(null);
  const load = async () => {
    setLoading(true);
    try {
      setItems((await api<{ items: Invite[] }>("/invitations")).items);
      if (role === "admin")
        setMembers((await api<{ items: Row[] }>("/resources/members")).items);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [memberId]);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    const form = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save("/invitations", {
        email: form.email,
        memberId: role === "admin" ? form.memberId : memberId,
        memberAdmin: form.memberAdmin === "on",
        currentPassword: form.currentPassword,
        code: form.code || undefined,
      });
      setOpen(false);
      setDone("邀请已创建并进入邮件发送队列。完整链接只通过邮件发送。");
      await load();
    } catch (e) {
      if (e instanceof APIError && e.data?.mfaRequired) setMfaRequired(true);
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!revoke) return;
    setBusy(true);
    setFormError("");
    try {
      await api(`/invitations/${revoke.id}`, { method: "DELETE" });
      setRevoke(null);
      setDone("邀请已撤销，邮件中的链接将不可再使用。");
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
        title="成员邀请"
        description="邀请用户加入指定成员网络，访问权限只作用于对应网络。"
      >
        <Button
          onClick={() => {
            setOpen(true);
            setFormError("");
            setMfaRequired(false);
          }}
        >
          <MailPlus />
          邀请用户
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5">
          {done}
        </p>
      )}
      <Card className="overflow-hidden">
        <div className="flex items-center justify-between border-b p-4">
          <span className="text-xs text-muted-foreground">
            邀请链接有有效期，列表不显示令牌或邮件内容。
          </span>
          <Button
            size="icon"
            variant="ghost"
            disabled={loading}
            aria-label="刷新邀请"
            onClick={() => void load()}
          >
            <RefreshCw />
          </Button>
        </div>
        {loading ? (
          <Loading />
        ) : !items.length ? (
          <Empty
            title="还没有成员邀请"
            description="邮件发送需要由部署环境启用；邀请不会授予工作人员或全局管理员权限。"
          />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>受邀邮箱</th>
                  <th>成员网络</th>
                  <th>成员权限</th>
                  <th>状态</th>
                  <th>有效期</th>
                  <th className="text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                {items.map((i) => (
                  <tr key={i.id}>
                    <td>{i.email}</td>
                    <td>{i.memberName}</td>
                    <td>{i.memberAdmin ? "成员管理员" : "成员只读"}</td>
                    <td>
                      <Badge
                        variant={i.status === "pending" ? "default" : "outline"}
                      >
                        {status[i.status] ?? i.status}
                      </Badge>
                    </td>
                    <td className="text-xs text-muted-foreground">
                      {time(i.expiresAt)}
                    </td>
                    <td className="text-right">
                      {i.status === "pending" && (
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`撤销邀请 ${i.email}`}
                          onClick={() => {
                            setRevoke(i);
                            setFormError("");
                          }}
                        >
                          <Trash2 />
                        </Button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            邀请成员用户
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            邀请通过邮件发送。已有账号需由受邀人登录确认，新账号只能以成员身份加入。
          </DialogDescription>
          <form onSubmit={submit} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="invite-email">受邀邮箱</label>
              <Input
                id="invite-email"
                name="email"
                type="email"
                required
                autoComplete="off"
              />
            </div>
            {role === "admin" && (
              <div className="field">
                <label htmlFor="invite-member">成员网络</label>
                <select
                  id="invite-member"
                  name="memberId"
                  required
                  defaultValue=""
                >
                  <option value="">选择成员网络</option>
                  {members.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </select>
              </div>
            )}
            <label className="flex items-start gap-2 text-sm">
              <input name="memberAdmin" type="checkbox" className="mt-1" />
              允许维护该网络的联络资料及成员邀请
            </label>
            <div className="field">
              <label htmlFor="invite-password">当前密码</label>
              <Input
                id="invite-password"
                name="currentPassword"
                type="password"
                autoComplete="current-password"
                required
              />
            </div>
            <div className="field">
              <label htmlFor="invite-code">
                验证器代码或恢复码（已开启时填写）
              </label>
              <Input
                id="invite-code"
                name="code"
                autoComplete="one-time-code"
                required={mfaRequired}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在创建…" : "发送成员邀请"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!revoke}
        onOpenChange={(v) => !busy && !v && setRevoke(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">撤销邀请</DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            撤销发给 {revoke?.email} 的邀请后，其邮件链接将立即失效。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setRevoke(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认撤销邀请
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
