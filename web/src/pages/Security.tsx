import { lazy, Suspense, useEffect, useState, type FormEvent } from "react";
import { useHashParameters } from "@/lib/hash-route";
import QRCode from "qrcode";
import {
  Check,
  Copy,
  Download,
  KeyRound,
  Laptop,
  Plus,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import type { Row } from "@/lib/resources";
import { api, save } from "@/lib/api";
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
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
const NoteNotifications = lazy(() => import("@/components/NoteNotifications"));
const LegacyNotesWorkspace = lazy(() => import("@/components/LegacyNotesWorkspace"));
const MailingLists = lazy(() => import("@/components/MailingLists"));
const LegacyMailingWorkspace = lazy(() => import("@/components/LegacyMailingWorkspace"));
const RememberedDevices = lazy(() => import("@/components/RememberedDevices"));
type AccountSecurity = {
  mfaEnabled: boolean;
  recoveryCodesRemaining: number;
  availableScopes: string[];
};
type LoginSession = {
  id: string;
  createdAt: string;
  expiresAt: string;
  ip: string;
  userAgent: string;
  current: boolean;
};
type Token = {
  id: string;
  name: string;
  scopes: string[];
  routerIds?: string[];
  switchIds?: string[];
  profileIds?: string[];
  expiresAt: string;
  createdAt: string;
};
type Enrollment = {
  secret: string;
  otpauthUrl: string;
  expiresAt: string;
  qr?: string;
};
type Action = "enroll" | "disable" | "recovery" | "revokeOthers" | "token";
const titles: Record<Action, string> = {
  enroll: "设置身份验证器",
  disable: "关闭双重验证",
  recovery: "重新生成恢复码",
  revokeOthers: "退出其他设备",
  token: "创建 API 令牌",
};
const scopeLabels: Record<string, string> = {
  "core:read": "读取授权交换机双端可见的核心互联",
  "looking-glass:read": "读取与创建授权范围的路由查询",
  "looking-glass:run": "采集器执行指定路由器的固定查询",
  "atlas:read": "读取 Atlas 探针与测量记录",
  "provisioning:read": "读取工具配置版本",
  "provisioning:write": "生成与校验指定规则的工具配置",
  "provisioning:pull": "下载指定规则的已批准工具配置",
  "devices:read": "读取指定交换机诊断",
  "devices:write": "采集器上报设备发现与观测",
  "integrations:read": "读取外部资料提案",
  "integrations:write": "获取与导入外部资料提案",
  "monitoring:read": "读取网络监测",
  "traffic:read": "读取授权归属的长期计数器流量",
  "sflow:read": "读取授权范围的 sFlow 采样估算",
  "sflow:write": "采集器上报指定交换机的 sFlow 元数据",
  "sflow:forward": "采集器为已审批接收器申请逐包转发授权",
  "monitoring:write": "采集器上报监测数据",
  "policy:read": "读取与模拟路由策略",
  "policy:write": "管理策略快照与白名单",
  "exports:read": "导出网络数据",
  "resources:read": "读取网络资源",
  "resources:write": "修改网络资源",
  "documents:read": "读取文档",
  "documents:write": "管理文档",
  "deployments:read": "读取脱敏配置版本",
  "deployments:pull": "拉取指定路由器的私密配置",
  "deployments:write": "生成与校验配置",
  "audit:read": "读取审计记录",
  "ixf:read": "导出 IX-F",
  "settings:read": "读取交换中心设置",
  "mailing-lists:read": "导出邮件列表订阅与退订意图",
};
function deviceName(agent: string) {
  const browser = /Edg\//.test(agent)
    ? "Edge"
    : /Firefox\//.test(agent)
      ? "Firefox"
      : /Chrome\//.test(agent)
        ? "Chrome"
        : /Safari\//.test(agent)
          ? "Safari"
          : "浏览器";
  const os = /Android/.test(agent)
    ? "Android"
    : /iPhone|iPad/.test(agent)
      ? "iOS"
      : /Windows/.test(agent)
        ? "Windows"
        : /Macintosh/.test(agent)
          ? "macOS"
          : /Linux/.test(agent)
            ? "Linux"
            : "未知系统";
  return agent ? `${browser} · ${os}` : "未知设备";
}
export default function Security({
  role,
  email,
}: {
  role: string;
  email: string;
}) {
  const route = useHashParameters("security");
  const notesSection = role !== "member" && route.get("section") === "notes";
  const legacyNotesSection = ["admin", "operator", "viewer"].includes(role) && route.get("section") === "legacy-notes";
  const mailingListsSection = route.get("section") === "mailing-lists";
  const legacyMailingSection = route.get("section") === "legacy-mailing";
  const needsSwitches = (scope: string) =>
    scope.startsWith("devices:") ||
    scope === "core:read" ||
    (role !== "member" && scope.startsWith("sflow:"));
  const [security, setSecurity] = useState<AccountSecurity | null>(null),
    [sessions, setSessions] = useState<LoginSession[]>([]),
    [tokens, setTokens] = useState<Token[]>([]),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [busy, setBusy] = useState(false),
    [loading, setLoading] = useState(true);
  const [action, setAction] = useState<Action | null>(null),
    [formError, setFormError] = useState(""),
    [enrollment, setEnrollment] = useState<Enrollment | null>(null),
    [revealed, setRevealed] = useState<{
      title: string;
      content: string;
      kind: "codes" | "token";
    } | null>(null),
    [saved, setSaved] = useState(false),
    [copied, setCopied] = useState(false),
    [selectedScopes, setSelectedScopes] = useState<string[]>([]),
    [routerOptions, setRouterOptions] = useState<Row[]>([]),
    [selectedRouters, setSelectedRouters] = useState<string[]>([]),
    [switchOptions, setSwitchOptions] = useState<Row[]>([]),
    [selectedSwitches, setSelectedSwitches] = useState<string[]>([]),
    [profileOptions, setProfileOptions] = useState<Row[]>([]),
    [selectedProfiles, setSelectedProfiles] = useState<string[]>([]),
    [revoke, setRevoke] = useState<{
      kind: "sessions" | "tokens";
      id: string;
      label: string;
      current?: boolean;
    } | null>(null);
  const changeScope = (scope: string, checked: boolean) => {
    setSelectedScopes((current) => {
      const next = new Set(current);
      if (checked) {
        next.add(scope);
        if (scope === "sflow:forward") next.add("sflow:write");
      } else {
        next.delete(scope);
        if (scope === "sflow:write") next.delete("sflow:forward");
      }
      return [...next];
    });
  };
  const load = async () => {
    setLoading(true);
    try {
      const [s, ls, ts] = await Promise.all([
        api<AccountSecurity>("/account/security"),
        api<{ items: LoginSession[] }>("/account/sessions"),
        api<{ items: Token[] }>("/account/tokens"),
      ]);
      setSecurity(s);
      setSessions(ls.items);
      setTokens(ts.items);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    if (!notesSection && !legacyNotesSection && !mailingListsSection && !legacyMailingSection) void load();
  }, [notesSection, legacyNotesSection, mailingListsSection, legacyMailingSection]);
  useEffect(() => {
    if (
      action !== "token" ||
      !security?.availableScopes.some((scope) =>
        ["monitoring:write", "deployments:pull", "looking-glass:run"].includes(
          scope,
        ),
      )
    )
      return;
    let active = true;
    api<{ items: Row[] }>("/resources/routers")
      .then((data) => active && setRouterOptions(data.items))
      .catch((e) => active && setFormError(message(e)));
    return () => {
      active = false;
    };
  }, [action, security?.availableScopes]);
  const loggedOut = () => window.dispatchEvent(new Event("session-expired"));
  useEffect(() => {
    if (action !== "token" || !security?.availableScopes.some(needsSwitches))
      return;
    let active = true;
    api<{ items: Row[] }>("/resources/switches")
      .then((data) => active && setSwitchOptions(data.items))
      .catch((e) => active && setFormError(message(e)));
    return () => {
      active = false;
    };
  }, [action, security?.availableScopes, role]);
  const open = (value: Action) => {
    setAction(value);
    setFormError("");
    setDone("");
    setSelectedRouters([]);
    setSelectedSwitches([]);
    setSelectedProfiles([]);
    setSelectedScopes(
      security?.availableScopes.includes("resources:read")
        ? ["resources:read"]
        : (security?.availableScopes.slice(0, 1) ?? []),
    );
  };
  useEffect(() => {
    if (
      action !== "token" ||
      !security?.availableScopes.some((scope) =>
        ["provisioning:write", "provisioning:pull"].includes(scope),
      )
    )
      return;
    const controller = new AbortController();
    api<{ items: Row[] }>("/resources/provisioning-profiles", {
      signal: controller.signal,
    })
      .then((data) => {
        if (!controller.signal.aborted) setProfileOptions(data.items);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setFormError(message(e));
      });
    return () => controller.abort();
  }, [action, security?.availableScopes]);
  const reveal = (title: string, content: string, kind: "codes" | "token") => {
    setRevealed({ title, content, kind });
    setSaved(false);
    setCopied(false);
    setFormError("");
  };
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
    } catch {
      setFormError("浏览器未允许复制，请手动选择并复制下方内容。");
    }
  };
  const downloadSecret = () => {
    if (!revealed) return;
    const url = URL.createObjectURL(
        new Blob([revealed.content + "\n"], {
          type: "text/plain;charset=utf-8",
        }),
      ),
      link = document.createElement("a");
    link.href = url;
    link.download =
      revealed.kind === "codes"
        ? "moedove-recovery-codes.txt"
        : "moedove-api-token.txt";
    link.click();
    URL.revokeObjectURL(url);
  };
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!action) return;
    setBusy(true);
    setFormError("");
    const form = Object.fromEntries(new FormData(e.currentTarget));
    try {
      switch (action) {
        case "enroll": {
          const data = await save<Enrollment>("/account/totp/enroll", form);
          let qr: string | undefined;
          try {
            qr = await QRCode.toDataURL(data.otpauthUrl, {
              width: 220,
              margin: 2,
              errorCorrectionLevel: "M",
            });
          } catch {
            /* manual key still works */
          }
          setEnrollment({ ...data, qr });
          break;
        }
        case "disable":
          await save("/account/totp/disable", form);
          setAction(null);
          loggedOut();
          return;
        case "recovery": {
          const data = await save<{ recoveryCodes: string[] }>(
            "/account/recovery-codes",
            form,
          );
          reveal("保存新的恢复码", data.recoveryCodes.join("\n"), "codes");
          await load();
          break;
        }
        case "revokeOthers":
          await save("/account/sessions/revoke-others", form);
          setDone("其他设备已退出登录。");
          await load();
          break;
        case "token": {
          if (
            selectedScopes.includes("sflow:forward") &&
            !selectedScopes.includes("sflow:write")
          )
            throw new Error(
              "sFlow 逐包授权需要同时具备采样上报权限，并绑定交换机。",
            );
          if (
            selectedScopes.some((scope) =>
              ["provisioning:write", "provisioning:pull"].includes(scope),
            ) &&
            !selectedProfiles.length
          )
            throw new Error("工具配置生成或下载令牌必须至少绑定一条生成规则。");
          if (selectedScopes.some(needsSwitches) && !selectedSwitches.length)
            throw new Error(
              "设备、核心互联或员工 sFlow 令牌必须至少绑定一台交换机。",
            );
          if (
            selectedScopes.some((scope) =>
              [
                "monitoring:write",
                "deployments:pull",
                "looking-glass:run",
              ].includes(scope),
            ) &&
            !selectedRouters.length
          )
            throw new Error(
              "监测上报、固定路由查询或私密配置拉取令牌必须至少绑定一台路由服务器。",
            );
          if (!selectedScopes.length)
            throw new Error("请至少选择一个令牌权限。");
          const data = await save<{ token: string; item: Token }>(
            "/account/tokens",
            {
              ...form,
              expiresInDays: Number(form.expiresInDays),
              scopes: selectedScopes,
              ...(selectedScopes.some((scope) =>
                ["provisioning:write", "provisioning:pull"].includes(scope),
              )
                ? { profileIds: selectedProfiles }
                : {}),
              ...(selectedScopes.some(needsSwitches)
                ? { switchIds: selectedSwitches }
                : {}),
              ...(selectedScopes.some((scope) =>
                [
                  "monitoring:write",
                  "deployments:pull",
                  "looking-glass:run",
                ].includes(scope),
              )
                ? { routerIds: selectedRouters }
                : {}),
            },
          );
          reveal("保存 API 令牌", data.token, "token");
          await load();
          break;
        }
      }
      setAction(null);
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const verify = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    const form = Object.fromEntries(new FormData(e.currentTarget));
    try {
      const data = await save<{ recoveryCodes: string[] }>(
        "/account/totp/verify",
        form,
      );
      setEnrollment(null);
      reveal("保存你的恢复码", data.recoveryCodes.join("\n"), "codes");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const password = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setDone("");
    const form = Object.fromEntries(new FormData(e.currentTarget));
    try {
      if (form.newPassword !== form.confirmPassword)
        throw new Error("两次输入的新密码不一致。");
      delete form.confirmPassword;
      await save("/account/password", form);
      loggedOut();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const revokeOne = async () => {
    if (!revoke) return;
    setBusy(true);
    setFormError("");
    try {
      await api(`/account/${revoke.kind}/${revoke.id}`, { method: "DELETE" });
      if (revoke.current) {
        setRevoke(null);
        loggedOut();
        return;
      }
      setRevoke(null);
      setDone(
        revoke.kind === "sessions" ? "该设备已退出登录。" : "API 令牌已撤销。",
      );
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  if (mailingListsSection)
    return <><Heading className="flex-col sm:flex-row [&>div:first-child]:min-w-0" title="我的邮件列表" description="选择当前账号邮箱的订阅或退订偏好；外部同步尚未确认。"><div className="flex flex-wrap gap-2 shrink-0"><Button variant="outline" onClick={() => { location.hash = "security?section=legacy-mailing"; }}>旧邮件订阅迁移</Button><Button variant="outline" onClick={() => { location.hash = "security"; }}>返回账号安全</Button></div></Heading><Suspense fallback={<Loading />}><MailingLists /></Suspense></>;
  if (legacyMailingSection)
    return <><Heading className="flex-col sm:flex-row [&>div:first-child]:min-w-0" title="旧邮件订阅迁移" description="明确关联来源账号和列表，由目标用户本人选择并确认。"><div className="flex flex-wrap gap-2 shrink-0"><Button variant="outline" onClick={() => { location.hash = "security?section=mailing-lists"; }}>当前邮件列表偏好</Button><Button variant="outline" onClick={() => { location.hash = "security"; }}>返回账号安全</Button></div></Heading><Suspense fallback={<Loading />}><LegacyMailingWorkspace role={role} /></Suspense></>;
  if (legacyNotesSection)
    return <><Heading title="旧备注与订阅迁移" description="核对只读来源，明确账号映射，并由本人选择当前订阅。"><Button variant="outline" onClick={() => { location.hash = "security"; }}>返回账号安全</Button></Heading><Suspense fallback={<Loading />}><LegacyNotesWorkspace role={role} /></Suspense></>;
  if (notesSection)
    return (
      <>
        <Heading
          title="备注通知与未读"
          description="管理当前账号的备注邮件订阅与已读状态。"
        >
          <Button
            variant="outline"
            onClick={() => {
              location.hash = "security";
            }}
          >
            返回账号安全
          </Button>
        </Heading>
        <Suspense fallback={<Loading />}>
          <NoteNotifications email={email} />
        </Suspense>
      </>
    );
  if (loading && !security)
    return (
      <>
        <Heading
          title="账号安全"
          description="管理你的密码、身份验证器与访问凭据。"
        />
        <ErrorNotice message={error} />
        {!error && <Loading />}
      </>
    );
  return (
    <>
      <Heading
        className="flex-col xl:flex-row"
        title="账号安全"
        description="管理你的密码、身份验证器与访问凭据。"
      >
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => { location.hash = "security?section=mailing-lists"; }}>邮件列表偏好</Button>
          {role !== "member" && <Button variant="outline" onClick={() => { location.hash = "security?section=legacy-notes"; }}>旧备注与订阅迁移</Button>}
          {role !== "member" && (
            <Button variant="outline" onClick={() => { location.hash = "security?section=notes"; }}>
              备注通知与未读
            </Button>
          )}
        </div>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5 flex items-center gap-2">
          <Check className="size-4" />
          {done}
        </p>
      )}
      {security && (
        <div className="space-y-6">
          <div className="grid grid-cols-1 gap-6 xl:grid-cols-2">
            <Card>
              <CardHeader>
                <div className="flex items-center justify-between gap-2">
                  <CardTitle className="flex items-center gap-2">
                    <ShieldCheck className="size-5 text-primary" />
                    双重验证
                  </CardTitle>
                  <Badge variant={security.mfaEnabled ? "default" : "outline"}>
                    {security.mfaEnabled ? "已开启" : "未开启"}
                  </Badge>
                </div>
                <CardDescription>
                  在密码之外，通过身份验证器验证登录。
                </CardDescription>
              </CardHeader>
              <CardContent>
                <p className="text-sm leading-7 text-muted-foreground">
                  {security.mfaEnabled
                    ? `双重验证正在保护你的账号。目前剩余 ${security.recoveryCodesRemaining} 个一次性恢复码。`
                    : "使用支持 TOTP 的身份验证器扫描二维码或输入密钥，完成后保存恢复码。"}
                </p>
                <div className="mt-5 flex flex-wrap gap-2">
                  {security.mfaEnabled ? (
                    <>
                      <Button
                        variant="outline"
                        onClick={() => open("recovery")}
                        disabled={busy}
                      >
                        <KeyRound />
                        更新恢复码
                      </Button>
                      <Button
                        variant="ghost"
                        onClick={() => open("disable")}
                        disabled={busy}
                      >
                        关闭双重验证
                      </Button>
                    </>
                  ) : (
                    <Button onClick={() => open("enroll")} disabled={busy}>
                      <ShieldCheck />
                      设置身份验证器
                    </Button>
                  )}
                </div>
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle>修改密码</CardTitle>
                <CardDescription>
                  修改后所有设备和 API 令牌会失效，请重新登录。
                </CardDescription>
              </CardHeader>
              <CardContent>
                <form onSubmit={password} className="space-y-4">
                  <div className="field">
                    <label htmlFor="current-password">当前密码</label>
                    <Input
                      id="current-password"
                      name="currentPassword"
                      type="password"
                      autoComplete="current-password"
                      required
                    />
                  </div>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="new-password">新密码</label>
                      <Input
                        id="new-password"
                        name="newPassword"
                        type="password"
                        minLength={12}
                        autoComplete="new-password"
                        required
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="confirm-password">确认新密码</label>
                      <Input
                        id="confirm-password"
                        name="confirmPassword"
                        type="password"
                        minLength={12}
                        autoComplete="new-password"
                        required
                      />
                    </div>
                  </div>
                  {security.mfaEnabled && (
                    <div className="field">
                      <label htmlFor="password-code">验证器代码或恢复码</label>
                      <Input
                        id="password-code"
                        name="code"
                        autoComplete="one-time-code"
                        required
                      />
                    </div>
                  )}
                  <Button disabled={busy} type="submit">
                    修改密码并重新登录
                  </Button>
                </form>
              </CardContent>
            </Card>
          </div>
          <Card className="overflow-hidden">
            <CardHeader className="flex-row flex-wrap items-center justify-between gap-3">
              <div>
                <CardTitle>登录设备</CardTitle>
                <CardDescription className="mt-2">
                  撤销不再使用的登录会话。
                </CardDescription>
              </div>
              <Button
                variant="outline"
                disabled={busy || sessions.length < 2}
                onClick={() => open("revokeOthers")}
              >
                <Laptop />
                退出其他设备
              </Button>
            </CardHeader>
            {!sessions.length ? (
              <Empty description="没有可显示的会话。" />
            ) : (
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>设备</th>
                      <th>IP 地址</th>
                      <th>登录时间</th>
                      <th>到期时间</th>
                      <th className="text-right">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sessions.map((s) => (
                      <tr key={s.id}>
                        <td>
                          <div
                            className="max-w-xs truncate text-sm"
                            title={s.userAgent}
                          >
                            {deviceName(s.userAgent)}
                          </div>
                          {s.current && (
                            <Badge className="mt-2">当前设备</Badge>
                          )}
                        </td>
                        <td className="mono text-xs">{s.ip || "—"}</td>
                        <td className="text-xs text-muted-foreground">
                          {time(s.createdAt)}
                        </td>
                        <td className="text-xs text-muted-foreground">
                          {time(s.expiresAt)}
                        </td>
                        <td className="text-right">
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy}
                            onClick={() => {
                              setRevoke({
                                kind: "sessions",
                                id: s.id,
                                label: s.current
                                  ? "当前设备"
                                  : s.ip || "所选设备",
                                current: s.current,
                              });
                              setFormError("");
                            }}
                          >
                            退出登录
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
          <Suspense fallback={<Loading />}>
            <RememberedDevices deviceName={deviceName} onSignedOut={loggedOut} />
          </Suspense>
          <Card className="overflow-hidden">
            <CardHeader className="flex-row flex-wrap items-center justify-between gap-3">
              <div>
                <CardTitle>API 访问令牌</CardTitle>
                <CardDescription className="mt-2">
                  为自动化任务分配明确权限与有效期，令牌只显示一次。
                </CardDescription>
              </div>
              <Button disabled={busy} onClick={() => open("token")}>
                <Plus />
                创建 API 令牌
              </Button>
            </CardHeader>
            {!tokens.length ? (
              <Empty description="还没有 API 令牌。为需要访问 API 的应用创建独立凭据。" />
            ) : (
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>名称</th>
                      <th>权限</th>
                      <th>创建时间</th>
                      <th>到期时间</th>
                      <th className="text-right">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {tokens.map((t) => (
                      <tr key={t.id}>
                        <td className="font-medium">
                          {t.name}
                          {!!t.routerIds?.length && (
                            <p className="mt-1 text-xs font-normal text-muted-foreground">
                              绑定 {t.routerIds.length} 台路由器
                            </p>
                          )}
                          {!!t.switchIds?.length && (
                            <p className="mt-1 text-xs font-normal text-muted-foreground">
                              绑定 {t.switchIds.length} 台交换机
                            </p>
                          )}
                          {!!t.profileIds?.length && (
                            <p className="mt-1 text-xs font-normal text-muted-foreground">
                              绑定 {t.profileIds.length} 条生成规则
                            </p>
                          )}
                        </td>
                        <td>
                          <div className="flex flex-wrap gap-1">
                            {t.scopes.map((scope) => (
                              <Badge key={scope} variant="outline">
                                {scopeLabels[scope] ?? scope}
                              </Badge>
                            ))}
                          </div>
                        </td>
                        <td className="text-xs text-muted-foreground">
                          {time(t.createdAt)}
                        </td>
                        <td className="text-xs text-muted-foreground">
                          {time(t.expiresAt)}
                        </td>
                        <td className="text-right">
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={`撤销令牌 ${t.name}`}
                            disabled={busy}
                            onClick={() => {
                              setRevoke({
                                kind: "tokens",
                                id: t.id,
                                label: t.name,
                              });
                              setFormError("");
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </div>
      )}
      <Dialog
        open={!!action}
        onOpenChange={(v) => !busy && !v && setAction(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {action ? titles[action] : "验证身份"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {action === "disable"
              ? "关闭后所有设备与 API 令牌会退出，请重新登录。"
              : action === "recovery"
                ? "生成新恢复码后，旧恢复码将立即失效。"
                : action === "token"
                  ? "选择此应用需要的权限，并设置令牌的有效期。"
                  : "请输入当前密码以确认是你本人。"}
          </DialogDescription>
          <form onSubmit={submit} className="space-y-4">
            <ErrorNotice message={formError} />
            {action === "token" && (
              <>
                <div className="form-grid">
                  <div className="field">
                    <label htmlFor="token-name">令牌名称</label>
                    <Input
                      id="token-name"
                      name="name"
                      required
                      maxLength={100}
                      placeholder="例如：监控系统"
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="token-days">有效期（天）</label>
                    <Input
                      id="token-days"
                      name="expiresInDays"
                      type="number"
                      min={1}
                      max={365}
                      required
                      defaultValue={30}
                    />
                  </div>
                </div>
                <fieldset className="rounded-lg border p-3">
                  <legend className="px-1 text-xs text-muted-foreground">
                    允许访问的范围
                  </legend>
                  <div className="grid gap-3 sm:grid-cols-2">
                    {security?.availableScopes.map((scope) => (
                      <label
                        key={scope}
                        className="flex items-center gap-2 text-xs"
                      >
                        <input
                          type="checkbox"
                          checked={selectedScopes.includes(scope)}
                          onChange={(e) => changeScope(scope, e.target.checked)}
                        />
                        {scopeLabels[scope] ?? scope}
                      </label>
                    ))}
                  </div>
                </fieldset>
                {selectedScopes.includes("sflow:forward") && (
                  <p className="notice text-xs leading-6">
                    逐包授权同时需要 sFlow
                    上报权限和明确的交换机范围；令牌不能批准接收器。本机采集器仍须显式允许转发和限定目的网段、端口，授权或本机
                    socket 提交都不证明远端收到。
                  </p>
                )}
                {selectedScopes.some((scope) =>
                  ["provisioning:write", "provisioning:pull"].includes(scope),
                ) && (
                  <fieldset className="rounded-lg border p-3">
                    <legend className="px-1 text-xs text-muted-foreground">
                      绑定的生成规则（必选）
                    </legend>
                    <p className="mb-3 text-xs leading-6 text-muted-foreground">
                      权限仅作用于所选规则生成的版本；令牌不能修改规则或批准版本，也不会应用目标服务配置。
                    </p>
                    <div className="max-h-48 space-y-2 overflow-y-auto">
                      {profileOptions.map((profile) => (
                        <label
                          key={profile.id}
                          className="flex items-center gap-2"
                        >
                          <input
                            type="checkbox"
                            checked={selectedProfiles.includes(profile.id)}
                            onChange={(e) =>
                              setSelectedProfiles(
                                e.target.checked
                                  ? [...selectedProfiles, profile.id]
                                  : selectedProfiles.filter(
                                      (id) => id !== profile.id,
                                    ),
                              )
                            }
                          />
                          {profile.name}
                        </label>
                      ))}
                    </div>
                    {!profileOptions.length && (
                      <p className="text-xs text-muted-foreground">
                        请先在“配置工具”建立生成规则。
                      </p>
                    )}
                  </fieldset>
                )}
                {selectedScopes.some((scope) =>
                  [
                    "monitoring:write",
                    "deployments:pull",
                    "looking-glass:run",
                  ].includes(scope),
                ) && (
                  <fieldset className="rounded-lg border p-3">
                    <legend className="px-1 text-xs text-muted-foreground">
                      绑定的路由服务器（必选）
                    </legend>
                    <p className="mb-3 text-xs leading-6 text-muted-foreground">
                      监测上报与固定路由查询仅限所选路由器。私密配置拉取权限允许下载包含真实
                      BGP 凭据的已批准配置，请妥善保护令牌。
                    </p>
                    <div className="max-h-48 space-y-2 overflow-y-auto">
                      {routerOptions.map((router) => (
                        <label
                          key={router.id}
                          className="flex items-center gap-2"
                        >
                          <input
                            type="checkbox"
                            value={router.id}
                            checked={selectedRouters.includes(router.id)}
                            onChange={(e) =>
                              setSelectedRouters(
                                e.target.checked
                                  ? [...selectedRouters, router.id]
                                  : selectedRouters.filter(
                                      (id) => id !== router.id,
                                    ),
                              )
                            }
                          />
                          {router.name}
                        </label>
                      ))}
                    </div>
                    {!routerOptions.length && (
                      <p className="text-xs text-muted-foreground">
                        请先添加路由服务器，再创建有此权限的令牌。
                      </p>
                    )}
                  </fieldset>
                )}
                {selectedScopes.some(needsSwitches) && (
                  <fieldset className="rounded-lg border p-3">
                    <legend className="px-1 text-xs text-muted-foreground">
                      绑定的交换机（必选）
                    </legend>
                    <p className="mb-3 text-xs leading-6 text-muted-foreground">
                      设备与员工 sFlow 读取、上报限定在所选交换机；核心 Bundle
                      的双端都必须被授权。此绑定不增加写权限，其他已选范围仍按各自权限执行。
                    </p>
                    <div className="max-h-48 space-y-2 overflow-y-auto">
                      {switchOptions.map((sw) => (
                        <label key={sw.id} className="flex items-center gap-2">
                          <input
                            type="checkbox"
                            value={sw.id}
                            checked={selectedSwitches.includes(sw.id)}
                            onChange={(e) =>
                              setSelectedSwitches(
                                e.target.checked
                                  ? [...selectedSwitches, sw.id]
                                  : selectedSwitches.filter(
                                      (id) => id !== sw.id,
                                    ),
                              )
                            }
                          />
                          {sw.name}
                        </label>
                      ))}
                    </div>
                    {!switchOptions.length && (
                      <p className="text-xs text-muted-foreground">
                        请先登记交换机。
                      </p>
                    )}
                  </fieldset>
                )}
              </>
            )}
            <div className="field">
              <label htmlFor="account-reauth-password">当前密码</label>
              <Input
                id="account-reauth-password"
                name="currentPassword"
                type="password"
                autoComplete="current-password"
                required
              />
            </div>
            {security?.mfaEnabled && action !== "enroll" && (
              <div className="field">
                <label htmlFor="account-reauth-code">验证器代码或恢复码</label>
                <Input
                  id="account-reauth-code"
                  name="code"
                  autoComplete="one-time-code"
                  required
                />
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setAction(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>{busy ? "正在处理…" : "继续"}</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!enrollment}
        onOpenChange={(v) => !busy && !v && setEnrollment(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            绑定身份验证器
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            扫描二维码或手动输入密钥，然后填写验证器显示的 6
            位代码。密钥仅在你的浏览器中生成二维码。
          </DialogDescription>
          <ErrorNotice message={formError} />
          {enrollment?.qr && (
            <img
              className="mx-auto rounded-lg border"
              src={enrollment.qr}
              alt="用于绑定身份验证器的二维码"
              width={220}
              height={220}
            />
          )}
          <div className="field">
            <label htmlFor="totp-secret">手动设置密钥</label>
            <div className="flex gap-2">
              <Input
                id="totp-secret"
                value={enrollment?.secret ?? ""}
                readOnly
                className="mono"
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label="复制验证器密钥"
                onClick={() => void copy(enrollment?.secret ?? "")}
              >
                <Copy />
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              绑定有效期至 {time(enrollment?.expiresAt)}。
            </p>
          </div>
          <form onSubmit={verify} className="space-y-4">
            <div className="field">
              <label htmlFor="totp-verify">6 位验证代码</label>
              <Input
                id="totp-verify"
                name="code"
                inputMode="numeric"
                pattern="[0-9]{6}"
                minLength={6}
                maxLength={6}
                autoComplete="one-time-code"
                required
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setEnrollment(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在验证…" : "验证并开启"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!revealed}
        onOpenChange={(v) => !busy && !v && saved && setRevealed(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {revealed?.title}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {revealed?.kind === "codes"
              ? "无法使用验证器时，可用恢复码登录。每个代码只能使用一次。"
              : "此令牌可用于 Authorization: Bearer 请求头，请妥善保存。"}
            关闭后无法再次查看，请保存在你的密码管理器中。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <pre
            className="max-h-72 overflow-auto break-all whitespace-pre-wrap rounded-lg border bg-muted/40 p-4 text-sm"
            aria-label={revealed?.kind === "codes" ? "恢复码列表" : "API 令牌"}
          >
            {revealed?.content}
          </pre>
          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => void copy(revealed?.content ?? "")}
            >
              <Copy />
              {copied ? "已复制" : "复制"}
            </Button>
            <Button variant="outline" size="sm" onClick={downloadSecret}>
              <Download />
              保存到文件
            </Button>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={saved}
              onChange={(e) => setSaved(e.target.checked)}
            />
            我已妥善保存
          </label>
          <div className="flex justify-end">
            <Button disabled={!saved} onClick={() => setRevealed(null)}>
              完成
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!revoke}
        onOpenChange={(v) => !busy && !v && setRevoke(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {revoke?.kind === "tokens" ? "撤销 API 令牌" : "退出登录设备"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {revoke?.kind === "tokens"
              ? `撤销「${revoke.label}」后，使用此令牌的应用将立即失去访问权限。`
              : `「${revoke?.label}」将退出登录。`}
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
              onClick={() => void revokeOne()}
            >
              确认撤销
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
