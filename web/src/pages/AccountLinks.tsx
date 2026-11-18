import { useEffect, useState, type FormEvent } from "react";
import { ArrowLeft, Check, KeyRound, Mail, ShieldCheck } from "lucide-react";
import { APIError, save } from "@/lib/api";
import type { Session } from "@/App";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorNotice, Loading, message, time } from "@/components/shared";
type InviteInfo = {
  email: string;
  memberId: string;
  memberName: string;
  expiresAt: string;
  accountExists: boolean;
};
export default function AccountLinks({
  page,
  token,
  session,
  onLogin,
  onSessionReset,
  onBack,
}: {
  page: string;
  token: string;
  session: Session | null;
  onLogin: () => Promise<void>;
  onSessionReset: () => void;
  onBack: () => void;
}) {
  const [info, setInfo] = useState<InviteInfo | null>(null),
    [loading, setLoading] = useState(page === "accept-invitation"),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [success, setSuccess] = useState(""),
    [mfaRequired, setMfaRequired] = useState(false);
  const invite = page === "accept-invitation",
    forgot = page === "forgot-password";
  useEffect(() => {
    if (success && !token) return;
    setInfo(null);
    setSuccess("");
    setError("");
    setMfaRequired(false);
    if (!invite) {
      setLoading(false);
      if (!forgot && !token)
        setError("恢复链接缺少令牌，请重新打开邮件中的完整链接。");
      return;
    }
    if (!token) {
      setError("邀请链接缺少令牌，请重新打开邮件中的完整链接。");
      setLoading(false);
      return;
    }
    let active = true;
    setLoading(true);
    save<InviteInfo>("/invitations/inspect", { token })
      .then((data) => active && setInfo(data))
      .catch((e) => active && setError(message(e)))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [page, token]);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    const form = Object.fromEntries(new FormData(e.currentTarget));
    try {
      if (forgot) {
        const data = await save<{ message?: string }>("/forgot-password", {
          email: form.email,
        });
        setSuccess(
          data.message ?? "如果账号存在且邮件服务已启用，您将收到恢复邮件。",
        );
      } else if (invite) {
        if (!info) throw new Error("邀请尚未验证。");
        if (!info.accountExists && form.password !== form.confirmPassword)
          throw new Error("两次输入的密码不一致。");
        await save(
          "/invitations/accept",
          info.accountExists
            ? {
                token,
                currentPassword: form.currentPassword,
                code: form.code || undefined,
              }
            : { token, name: form.name, password: form.password },
        );
        setSuccess("邀请已接受。请重新登录，进入受邀成员网络。");
        onSessionReset();
      } else {
        if (!token)
          throw new Error("恢复链接缺少令牌，请重新打开邮件中的完整链接。");
        if (form.newPassword !== form.confirmPassword)
          throw new Error("两次输入的新密码不一致。");
        await save("/reset-password", {
          token,
          newPassword: form.newPassword,
          code: form.code || undefined,
        });
        setSuccess("密码已更新，原有会话和 API 令牌已失效。请重新登录。");
        onSessionReset();
      }
    } catch (e) {
      if (e instanceof APIError && e.data?.mfaRequired) {
        setMfaRequired(true);
        setError("此账号已开启双重验证，请补充验证器代码或恢复码。");
      } else setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const login = async () => {
    setBusy(true);
    setError("");
    try {
      await onLogin();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const matching =
    !!session && session.user.email.toLowerCase() === info?.email.toLowerCase();
  return (
    <div className="min-h-dvh bg-[#f8f9fc] px-5 py-10">
      <div className="mx-auto max-w-lg">
        <img
          src="/moedove-logo.png"
          alt="MoeDove"
          className="mx-auto mb-8 w-32"
        />
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-xl">
              {invite ? (
                <ShieldCheck className="size-5 text-primary" />
              ) : forgot ? (
                <Mail className="size-5 text-primary" />
              ) : (
                <KeyRound className="size-5 text-primary" />
              )}
              {invite ? "接受成员邀请" : forgot ? "找回密码" : "设置新密码"}
            </CardTitle>
            <p className="text-sm leading-6 text-muted-foreground">
              {invite
                ? "核对受邀网络与邮箱，确认后建立成员访问权限。"
                : forgot
                  ? "输入你的登录邮箱，我们会在账号存在且邮件服务启用时发送恢复邮件。"
                  : "恢复密码不会关闭双重验证；已开启验证器的账号仍需提供验证码。"}
            </p>
          </CardHeader>
          <CardContent className="space-y-5">
            <ErrorNotice message={error} />
            {loading ? (
              <Loading />
            ) : success ? (
              <>
                <p className="notice flex items-start gap-2" role="status">
                  <Check className="mt-0.5 size-4 shrink-0" />
                  {success}
                </p>
                <Button className="w-full" onClick={onBack}>
                  返回登录
                </Button>
              </>
            ) : (
              <>
                {info && (
                  <div className="rounded-lg border bg-muted/30 p-4 text-sm">
                    <p className="font-medium">{info.memberName}</p>
                    <p className="mt-2 break-all text-muted-foreground">
                      受邀邮箱：{info.email}
                    </p>
                    <p className="mt-2 text-xs text-muted-foreground">
                      有效期至 {time(info.expiresAt)}
                    </p>
                  </div>
                )}
                {invite && info?.accountExists && !matching ? (
                  <div className="space-y-4">
                    <p className="text-sm leading-6 text-muted-foreground">
                      此邮箱已有账号。
                      {session
                        ? `当前登录为 ${session.user.email}，请切换至受邀邮箱对应账号。`
                        : "请先使用受邀邮箱登录，再确认加入网络。"}
                    </p>
                    <Button
                      className="w-full"
                      disabled={busy}
                      onClick={() => void login()}
                    >
                      使用受邀账号登录
                    </Button>
                  </div>
                ) : (
                  (!invite || info) && (
                    <form onSubmit={submit} className="space-y-4">
                      {forgot ? (
                        <div className="field">
                          <label htmlFor="recovery-email">登录邮箱</label>
                          <Input
                            id="recovery-email"
                            name="email"
                            type="email"
                            autoComplete="email"
                            required
                            maxLength={254}
                          />
                        </div>
                      ) : invite && info?.accountExists ? (
                        <>
                          <div className="field">
                            <label htmlFor="invitation-current-password">
                              当前密码
                            </label>
                            <Input
                              id="invitation-current-password"
                              name="currentPassword"
                              type="password"
                              autoComplete="current-password"
                              required
                            />
                          </div>
                          <div className="field">
                            <label htmlFor="invitation-code">
                              验证器代码或恢复码（已开启时填写）
                            </label>
                            <Input
                              id="invitation-code"
                              name="code"
                              autoComplete="one-time-code"
                              required={mfaRequired}
                            />
                          </div>
                        </>
                      ) : (
                        <>
                          {invite && (
                            <div className="field">
                              <label htmlFor="invitation-name">你的姓名</label>
                              <Input
                                id="invitation-name"
                                name="name"
                                autoComplete="name"
                                required
                                maxLength={100}
                              />
                            </div>
                          )}
                          <div className="field">
                            <label htmlFor="recovery-password">
                              {invite ? "账号密码" : "新密码"}
                            </label>
                            <Input
                              id="recovery-password"
                              name={invite ? "password" : "newPassword"}
                              type="password"
                              autoComplete="new-password"
                              minLength={12}
                              maxLength={128}
                              required
                            />
                          </div>
                          <div className="field">
                            <label htmlFor="recovery-confirm">确认密码</label>
                            <Input
                              id="recovery-confirm"
                              name="confirmPassword"
                              type="password"
                              autoComplete="new-password"
                              minLength={12}
                              maxLength={128}
                              required
                            />
                          </div>
                          {!invite && (
                            <div className="field">
                              <label htmlFor="recovery-code">
                                验证器代码或恢复码（已开启时填写）
                              </label>
                              <Input
                                id="recovery-code"
                                name="code"
                                autoComplete="one-time-code"
                                required={mfaRequired}
                              />
                            </div>
                          )}
                        </>
                      )}
                      <Button
                        className="w-full"
                        disabled={busy || (!forgot && !token)}
                      >
                        {busy
                          ? "正在处理…"
                          : forgot
                            ? "发送恢复邮件"
                            : invite
                              ? "确认接受邀请"
                              : "更新密码"}
                      </Button>
                    </form>
                  )
                )}
              </>
            )}
            <Button variant="ghost" size="sm" onClick={onBack}>
              <ArrowLeft />
              返回控制台登录
            </Button>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
