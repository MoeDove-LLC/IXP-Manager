import {
  lazy,
  Suspense,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  ArrowRight,
  Activity,
  ScanLine,
  Handshake,
  CloudDownload,
  BookOpen,
  Building2,
  Cable,
  ChevronDown,
  FileCode2,
  Globe2,
  FolderOpen,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  LogOut,
  Menu,
  Mail,
  MailPlus,
  Network,
  Search,
  Settings as SettingsIcon,
  Settings2,
  ShieldCheck,
  X,
} from "lucide-react";
import { api, save, setCSRF, APIError } from "@/lib/api";
import {
  resources,
  statusLabels,
  configureResources,
  type ServerSchema,
} from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { ErrorNotice, Loading, message } from "@/components/shared";
import Dashboard, { AuditPage } from "@/pages/Dashboard";
import Resources from "@/pages/Resources";
import Deployments from "@/pages/Deployments";
import Settings from "@/pages/Settings";
import Documents from "@/pages/Documents";
import Monitoring from "@/pages/Monitoring";
import Policy from "@/pages/Policy";
import MemberProfile from "@/pages/MemberProfile";
import IPAM from "@/pages/IPAM";
import Notifications from "@/pages/Notifications";
import Invitations from "@/pages/Invitations";
import AccountLinks from "@/pages/AccountLinks";
import TenantSwitcher from "@/components/TenantSwitcher";
const Patching = lazy(() => import("@/pages/Patching"));
const Security = lazy(() => import("@/pages/Security"));
const Devices = lazy(() => import("@/pages/Devices"));
const Integrations = lazy(() => import("@/pages/Integrations"));
const Peering = lazy(() => import("@/pages/Peering"));
const Provisioning = lazy(() => import("@/pages/Provisioning"));
const LookingGlass = lazy(() => import("@/pages/LookingGlass"));
const Atlas = lazy(() => import("@/pages/Atlas"));
const Core = lazy(() => import("@/pages/Core"));
const Traffic = lazy(() => import("@/pages/Traffic"));
const SFlow = lazy(() => import("@/pages/SFlow"));
const GlobalSearch = lazy(() => import("@/components/GlobalSearch"));
const secretRoutes = new Set(["reset-password", "accept-invitation"]);
function consumeHashRoute() {
  const raw = location.hash.slice(1),
    at = raw.indexOf("?"),
    page = (at < 0 ? raw : raw.slice(0, at)) || "dashboard";
  const token =
    secretRoutes.has(page) && at >= 0
      ? (new URLSearchParams(raw.slice(at + 1)).get("token") ?? "")
      : "";
  if (secretRoutes.has(page) && at >= 0)
    history.replaceState(
      history.state,
      "",
      `${location.pathname}${location.search}#${page}`,
    );
  return { page, token };
}
const initialRoute = consumeHashRoute();
export type Session = {
  user: {
    id: string;
    name: string;
    email: string;
    role: string;
    memberId?: string;
    memberIds?: string[];
    memberRole?: "member" | "member-admin";
    membershipDetached?: boolean;
  };
  csrfToken: string;
};
type SetupStatus = {
  required: boolean;
  rememberDeviceAvailable?: boolean;
  rememberDeviceDays?: number;
};

async function withSessionCookieLock<T>(action: () => Promise<T>): Promise<T> {
  if (navigator.locks) return await navigator.locks.request("moedove-session-cookie", action);
  return action();
}

async function readSession(restore: boolean, isCurrent: () => boolean) {
  const read = async () => {
    try {
      return await api<Session>("/session");
    } catch (error) {
      if (!(error instanceof APIError && error.status === 401)) throw error;
      return null;
    }
  };
  const existing = await read();
  if (existing || !restore || !isCurrent()) return existing;

  const restoreSession = async () => {
    if (!isCurrent()) return null;
    // Another tab may have finished rotation while this tab waited for the lock.
    const current = await read();
    if (current || !isCurrent()) return current;
    try {
      return await save<Session>("/session/restore", {});
    } catch (error) {
      if (error instanceof APIError && error.status === 401 && error.data?.retrySession && isCurrent()) return read();
      return null; // A failed optional restoration leaves password login available.
    }
  };
  return withSessionCookieLock(restoreSession);
}
const getNavigation = () => [
  { id: "core", label: "核心互联", icon: Network, group: "基础设施" },
  {
    id: "looking-glass",
    label: "Looking Glass",
    icon: Search,
    group: "配置与运维",
  },
  { id: "atlas", label: "RIPE Atlas", icon: Activity, group: "配置与运维" },
  { id: "devices", label: "设备诊断", icon: ScanLine, group: "配置与运维" },
  {
    id: "integrations",
    label: "外部资料核对",
    icon: CloudDownload,
    group: "配置与运维",
  },
  { id: "peering", label: "成员对等", icon: Handshake, group: "成员与互联" },
  { id: "ipam", label: "地址分配", icon: Globe2, group: "基础设施" },
  { id: "patching", label: "配线工作台", icon: Cable, group: "工作空间" },
  { id: "invitations", label: "成员邀请", icon: MailPlus, group: "我的账号" },
  { id: "notifications", label: "通知与任务", icon: Mail, group: "系统管理" },
  {
    id: "member-profile",
    label: "我的网络",
    icon: Building2,
    group: "工作空间",
  },
  { id: "monitoring", label: "网络监测", icon: Activity, group: "工作空间" },
  { id: "traffic", label: "长期流量", icon: Activity, group: "工作空间" },
  { id: "sflow", label: "sFlow 采样", icon: Activity, group: "工作空间" },
  { id: "policy", label: "路由策略", icon: BookOpen, group: "配置与运维" },
  {
    id: "dashboard",
    label: "网络概览",
    icon: LayoutDashboard,
    group: "工作空间",
  },
  { id: "documents", label: "文档库", icon: FolderOpen, group: "工作空间" },
  ...Object.entries(resources)
    .filter(([id]) => id !== "users" && id !== "core-links")
    .map(([id, r]) => ({ id, label: r.label, icon: r.icon, group: r.group })),
  {
    id: "deployments",
    label: "配置中心",
    icon: FileCode2,
    group: "配置与运维",
  },
  {
    id: "provisioning",
    label: "配置工具",
    icon: Settings2,
    group: "配置与运维",
  },
  { id: "audit", label: "操作审计", icon: ShieldCheck, group: "配置与运维" },
  { id: "security", label: "账号安全", icon: KeyRound, group: "我的账号" },
  { id: "users", label: "用户与权限", icon: ShieldCheck, group: "系统管理" },
  {
    id: "settings",
    label: "交换中心设置",
    icon: SettingsIcon,
    group: "系统管理",
  },
];
function Auth({
  setup,
  rememberDeviceDays,
  onSuccess,
}: {
  setup: boolean;
  rememberDeviceDays: number | null;
  onSuccess: (s: Session) => void;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [mfaRequired, setMfaRequired] = useState(false);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setError("");
    setBusy(true);
    const data = Object.fromEntries(new FormData(e.currentTarget));
    const payload = setup
      ? { ...data, asn: Number(data.asn) }
      : { ...data, ...(rememberDeviceDays === null ? {} : { rememberDevice: data.rememberDevice === "on" }) };
    try {
      onSuccess(
        await withSessionCookieLock(() => save<Session>(setup ? "/setup" : "/login", payload)),
      );
    } catch (e) {
      if (e instanceof APIError && e.data?.mfaRequired) {
        setMfaRequired(true);
        setError("请输入验证器代码或一次性恢复码，完成登录。");
      } else setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="auth-shell">
      <div className="auth-art">
        <img
          src="/moedove-logo.png"
          alt="MoeDove"
          className="mb-14 w-44 object-contain"
        />
        <div className="mb-6 flex items-center gap-2 text-xs tracking-[.18em] text-primary">
          <span className="h-px w-8 bg-primary/50" />
          MOEDOVE INTERNET EXCHANGE
        </div>
        <h1 className="text-[44px] font-semibold leading-[1.3] tracking-tight text-[#41384f]">
          连接彼此，
          <br />
          让互联更简单。
        </h1>
        <p className="mt-6 max-w-sm text-sm leading-7 text-[#82778f]">
          把成员、基础设施与路由配置放在同一个工作空间，专注每一次稳定的连接。
        </p>
        <div className="auth-network mt-14 flex items-center gap-6 text-primary/50">
          <div className="rounded-2xl border border-primary/20 p-4">
            <Network className="size-8" />
          </div>
          <span className="h-px w-14 bg-primary/20" />
          <div className="rounded-full border border-primary/20 p-3">
            <ShieldCheck className="size-6" />
          </div>
          <span className="h-px w-10 bg-primary/20" />
          <div className="size-3 rounded-full bg-primary/20" />
        </div>
        <p className="mt-16 text-[11px] tracking-wide text-[#a196ab]">
          清晰管理 · 安全变更 · 可靠互联
        </p>
      </div>
      <div className="auth-panel">
        <div className="auth-form">
          <img
            src="/moedove-logo.png"
            alt="MoeDove"
            className="mb-8 w-28 md:hidden"
          />
          <div className="mb-8">
            <p className="mb-3 text-xs font-medium tracking-widest text-primary">
              MOEDOVE IXP
            </p>
            <h2 className="text-2xl font-semibold tracking-tight">
              {setup ? "开启你的互联工作空间" : "欢迎回来"}
            </h2>
            <p className="mt-3 text-sm leading-6 text-muted-foreground">
              {setup
                ? "设置交换中心，并创建第一个管理员账号。"
                : "登录控制台，继续管理你的交换网络。"}
            </p>
          </div>
          <form className="space-y-4" onSubmit={submit}>
            <ErrorNotice message={error} />
            {setup && (
              <>
                <div className="field">
                  <label htmlFor="ixpName">交换中心名称</label>
                  <Input
                    id="ixpName"
                    name="ixpName"
                    placeholder="MoeDove IXP"
                    required
                    autoComplete="organization"
                  />
                </div>
                <div className="field">
                  <label htmlFor="asn">交换中心 ASN</label>
                  <Input
                    id="asn"
                    name="asn"
                    type="number"
                    min={1}
                    max={4294967295}
                    placeholder="64512"
                    required
                  />
                </div>
                <div className="field">
                  <label htmlFor="setupToken">初始化令牌</label>
                  <Input
                    id="setupToken"
                    name="setupToken"
                    type="password"
                    autoComplete="off"
                    required
                    placeholder="输入服务启动时显示的令牌"
                  />
                </div>
                <div className="field">
                  <label htmlFor="name">管理员姓名</label>
                  <Input id="name" name="name" required autoComplete="name" />
                </div>
              </>
            )}
            <div className="field">
              <label htmlFor="email">邮箱地址</label>
              <Input
                id="email"
                name="email"
                type="email"
                required
                autoComplete="username"
                placeholder="you@example.com"
              />
            </div>
            <div className="field">
              <label htmlFor="password">密码</label>
              <Input
                id="password"
                name="password"
                type="password"
                required
                minLength={setup ? 12 : undefined}
                autoComplete={setup ? "new-password" : "current-password"}
                placeholder={setup ? "至少 12 位字符" : "输入你的密码"}
              />
            </div>
            {mfaRequired && !setup && (
              <div className="field">
                <label htmlFor="login-code">验证器代码或恢复码</label>
                <Input
                  id="login-code"
                  name="code"
                  autoComplete="one-time-code"
                  required
                  autoFocus
                  placeholder="输入 6 位代码或恢复码"
                />
              </div>
            )}
            {!setup && rememberDeviceDays !== null && (
              <label className="flex items-start gap-3 text-sm">
                <input className="mt-1 accent-primary" type="checkbox" name="rememberDevice" disabled={busy} />
                <span>
                  记住这台设备
                  <span className="mt-1 block text-xs leading-5 text-muted-foreground">
                    在此浏览器保持登录 {rememberDeviceDays} 天。请只在个人设备上使用。
                  </span>
                </span>
              </label>
            )}
            <Button className="mt-3 w-full" size="lg" disabled={busy}>
              {busy ? (
                <LoaderCircle className="animate-spin" />
              ) : (
                <>
                  {setup ? "创建工作空间" : "登录控制台"}
                  <ArrowRight />
                </>
              )}
            </Button>
          </form>
          <p className="mt-7 text-center text-xs leading-6 text-muted-foreground">
            {setup
              ? "初始化完成后，仅受邀用户可以访问控制台。"
              : "账号由交换中心管理员创建和管理。"}
          </p>
          {!setup && (
            <div className="mt-4 text-center">
              <a
                className="text-xs text-primary underline-offset-4 hover:underline"
                href="#forgot-password"
              >
                忘记密码？
              </a>
            </div>
          )}
          {!setup && (
            <div className="mt-4 text-center">
              <a
                className="text-xs text-primary underline-offset-4 hover:underline"
                href="#public-documents"
              >
                查看公开文档
              </a>
            </div>
          )}
          <p className="mt-12 text-center text-[10px] tracking-[.12em] text-[#afb3bf]">
            MOEDOVE · CONNECT WITH CARE
          </p>
        </div>
      </div>
    </div>
  );
}
export default function App() {
  const sessionChannel = useRef<BroadcastChannel | null>(null);
  const sessionEpoch = useRef(0);
  const refreshSessionContext = useRef<() => Promise<void>>(async () => {});
  const [session, setSession] = useState<Session | null>(null),
    [setup, setSetup] = useState(false),
    [rememberDeviceDays, setRememberDeviceDays] = useState<number | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [page, setPage] = useState(initialRoute.page),
    [authToken, setAuthToken] = useState(initialRoute.token),
    [authPurpose, setAuthPurpose] = useState(
      initialRoute.token ? initialRoute.page : "",
    ),
    [authReturnRoute, setAuthReturnRoute] = useState(""),
    [mobile, setMobile] = useState(false),
    [version, setVersion] = useState(0),
    [ixp, setIxp] = useState("MoeDove IXP"),
    [navSearch, setNavSearch] = useState(""),
    [globalSearchOpen, setGlobalSearchOpen] = useState(false),
    [viewVersion, setViewVersion] = useState(0),
    [pendingMemberSwitches, setPendingMemberSwitches] = useState(0),
    [tenantName, setTenantName] = useState("");
  useEffect(() => {
    setGlobalSearchOpen(false);
  }, [
    session?.user.id,
    session?.user.memberId,
    session?.user.role,
    session?.user.memberRole,
  ]);
  useEffect(() => {
    if (!session) return;
    const shortcut = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setGlobalSearchOpen((open) => !open);
      }
    };
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  }, [session]);
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [page, session?.user.memberId]);
  const notifySessionContext = () =>
    sessionChannel.current?.postMessage({ type: "session-context-changed" });
  const accept = (s: Session, schema?: ServerSchema) => {
    const epoch = sessionEpoch.current;
    setCSRF(s.csrfToken);
    if (schema) configureResources(schema);
    else if (!s.user.membershipDetached) void api<ServerSchema>("/schema")
      .then((currentSchema) => {
        if (epoch !== sessionEpoch.current) return;
        configureResources(currentSchema);
        setVersion((v) => v + 1);
      })
      .catch((e) => {
        if (epoch === sessionEpoch.current) setError(message(e));
      });
    setSession(s);
    setSetup(false);
    setError("");
  };
  const init = async () => {
    const epoch = ++sessionEpoch.current;
    setLoading(true);
    setError("");
    try {
      const status = await api<SetupStatus>("/setup");
      if (epoch !== sessionEpoch.current) return;
      setSetup(status.required);
      const days = status.rememberDeviceDays;
      const remember = status.rememberDeviceAvailable === true && typeof days === "number" && Number.isSafeInteger(days) && days > 0;
      setRememberDeviceDays(remember ? days : null);
      if (!status.required) {
        const s = await readSession(remember, () => epoch === sessionEpoch.current);
        if (s && epoch === sessionEpoch.current) {
          const schema = s.user.membershipDetached ? undefined : await api<ServerSchema>("/schema");
          if (epoch === sessionEpoch.current) accept(s, schema);
        }
      }
    } catch (e) {
      if (epoch === sessionEpoch.current) setError(message(e));
    } finally {
      if (epoch === sessionEpoch.current) setLoading(false);
    }
  };
  useEffect(() => {
    void init();
    let closed = false;
    const synchronize = async () => {
      const epoch = ++sessionEpoch.current;
      setLoading(true);
      setSession(null);
      setTenantName("");
      setCSRF("");
      setError("");
      try {
        const current = await api<Session>("/session");
        if (!closed && epoch === sessionEpoch.current) accept(current);
      } catch (e) {
        if (
          !closed &&
          epoch === sessionEpoch.current &&
          !(e instanceof APIError && e.status === 401)
        )
          setError("会话上下文已变化，请重新连接以读取当前账号。" + message(e));
      } finally {
        if (!closed && epoch === sessionEpoch.current) setLoading(false);
      }
    };
    refreshSessionContext.current = synchronize;
    if (typeof BroadcastChannel !== "undefined") {
      const channel = new BroadcastChannel("moedove-session-context");
      sessionChannel.current = channel;
      channel.onmessage = (event) => {
        if (event.data?.type !== "session-context-changed") return;
        void synchronize();
      };
    }
    const expired = () => {
      sessionEpoch.current++;
      setLoading(false);
      setError("");
      setSession(null);
      setTenantName("");
      setCSRF("");
      notifySessionContext();
    };
    const hash = () => {
      const next = consumeHashRoute();
      setPage(next.page);
      if (next.token) {
        setAuthToken(next.token);
        setAuthPurpose(next.page);
      }

      setMobile(false);
    };
    window.addEventListener("session-expired", expired);
    window.addEventListener("hashchange", hash);
    return () => {
      closed = true;
      sessionEpoch.current++;
      refreshSessionContext.current = async () => {};
      sessionChannel.current?.close();
      sessionChannel.current = null;
      window.removeEventListener("session-expired", expired);
      window.removeEventListener("hashchange", hash);
    };
  }, []);
  useEffect(() => {
    if (session && session.user.role !== "member")
      api<{ name: string }>("/settings")
        .then((s) => setIxp(s.name || "MoeDove IXP"))
        .catch(() => {});
  }, [session, version]);
  const navigate = (p: string) => {
    const target = p.startsWith("#") ? p.slice(1) : p;
    setViewVersion((value) => value + 1);
    location.hash = target;
    setPage(target.split("?", 1)[0] || "dashboard");
    setMobile(false);
    setGlobalSearchOpen(false);
    setNavSearch("");
  };
  const changed = () => setVersion((v) => v + 1);
  const logout = async () => {
    try {
      await withSessionCookieLock(() => save("/logout", {}));
      sessionEpoch.current++;
      setSession(null);
      setTenantName("");
      setCSRF("");
      notifySessionContext();
    } catch (e) {
      if (!(e instanceof APIError && e.status === 401)) setError(message(e));
    }
  };
  if (["forgot-password", "reset-password", "accept-invitation"].includes(page))
    return loading ? (
      <Loading />
    ) : (
      <AccountLinks
        key={page}
        page={page}
        token={authPurpose === page ? authToken : ""}
        session={session}
        onSessionReset={() => {
          sessionEpoch.current++;
          setLoading(false);
          setSession(null);
          setCSRF("");
          setTenantName("");
          setAuthToken("");
          setAuthPurpose("");
          setAuthReturnRoute("");
          notifySessionContext();
        }}
        onBack={() => {
          setAuthReturnRoute("");
          setAuthToken("");
          setAuthPurpose("");
          navigate("dashboard");
        }}
        onLogin={async () => {
          if (session) {
            await withSessionCookieLock(() => save("/logout", {}));
            sessionEpoch.current++;
            setSession(null);
            setTenantName("");
            setCSRF("");
            notifySessionContext();
          }
          setAuthReturnRoute(page);
          navigate("dashboard");
        }}
      />
    );
  if (page === "public-documents")
    return (
      <div className="mx-auto max-w-6xl px-5 py-8">
        <header className="mb-8 flex items-center justify-between">
          <img src="/moedove-logo.png" alt="MoeDove" className="w-28" />
          <Button variant="outline" onClick={() => navigate("dashboard")}>
            {session ? "返回控制台" : "登录控制台"}
          </Button>
        </header>
        <Documents role="public" />
      </div>
    );
  if (loading) return <Loading />;
  if (!session)
    return error ? (
      <div className="mx-auto max-w-lg space-y-4 p-10">
        <ErrorNotice message={error} />
        <Button onClick={() => void init()}>重新连接</Button>
      </div>
    ) : (
      <Auth
        setup={setup}
        rememberDeviceDays={rememberDeviceDays}
        onSuccess={(s) => {
          sessionEpoch.current++;
          accept(s);
          notifySessionContext();
          const destination = authReturnRoute || "dashboard";
          setAuthReturnRoute("");
          navigate(destination);
        }}
      />
    );
  if (session.user.membershipDetached)
    return (
      <main className="mx-auto w-full max-w-lg px-4 py-12">
        <Card>
          <CardHeader>
            <h1 className="text-lg font-semibold">当前账号没有组织</h1>
            <p className="text-sm text-muted-foreground">
              打开组织管理员发出的邀请链接，即可重新加入成员网络。
            </p>
          </CardHeader>
          <CardContent>
            <ErrorNotice message={error} />
            <Button variant="outline" onClick={() => void logout()}>
              <LogOut />
              退出登录
            </Button>
          </CardContent>
        </Card>
      </main>
    );
  const navigation = getNavigation();
  const isMember = session.user.role === "member";
  const canInvite =
    session.user.role === "admin" ||
    (isMember && session.user.memberRole === "member-admin");
  const memberPages = [
    "dashboard",
    "member-profile",
    "patching",
    "devices",
    "peering",
    "invitations",
    "monitoring",
    "traffic",
    "sflow",
    "looking-glass",
    "documents",
    "security",
    "members",
    "connections",
    "contacts",
    "equipment",
    "member-tags",
    "tags",
    "notes",
  ];
  const visibleNav = navigation.filter(
    (n) =>
      (!isMember || memberPages.includes(n.id)) &&
      (n.id !== "member-profile" || isMember) &&
      (n.id !== "notifications" || session.user.role === "admin") &&
      (n.id !== "invitations" || canInvite) &&
      (n.id !== "users" || session.user.role === "admin") &&
      (!navSearch || n.label.includes(navSearch)),
  );
  const active =
      visibleNav.find((n) => n.id === page) ??
      navigation.find((n) => n.id === page),
    groups = [
      "工作空间",
      "成员与互联",
      "配置与运维",
      "地址与路由策略",
      "基础设施",
      "我的账号",
      "系统管理",
    ].filter((group) => visibleNav.some((n) => n.group === group));
  const navigationPriority: Record<string, number> = {
    dashboard: 0,
    monitoring: 1,
    "member-profile": 2,
    patching: 3,
    documents: 4,
  };
  return (
    <div className="shell">
      {mobile && (
        <button
          aria-label="关闭导航"
          className="fixed inset-0 z-30 bg-slate-900/30"
          onClick={() => setMobile(false)}
        />
      )}
      <aside className={`sidebar ${mobile ? "open" : ""}`}>
        <div className="brand">
          <img src="/moedove-logo.png" className="brand-logo" alt="MoeDove" />
          <button
            aria-label="关闭导航"
            className="mobile-menu ml-auto"
            onClick={() => setMobile(false)}
          >
            <X className="size-5" />
          </button>
        </div>
        {isMember ? (
          <TenantSwitcher
            session={session}
            version={version}
            onLabel={setTenantName}
            onSwitchPending={(pending) =>
              setPendingMemberSwitches((count) => count + (pending ? 1 : -1))
            }
            onSwitch={(s) => {
              setCSRF(s.csrfToken);
              notifySessionContext();
              void refreshSessionContext.current();
            }}
          />
        ) : (
          <div className="mx-2 mb-2 rounded-lg border bg-[#fafafd] p-3">
            <div className="flex items-center gap-2">
              <span className="flex size-6 shrink-0 items-center justify-center rounded bg-primary/10 text-[10px] font-semibold text-primary">
                IX
              </span>
              <span className="truncate text-xs font-medium">{ixp}</span>
              <ChevronDown className="ml-auto size-3 text-muted-foreground" />
            </div>
            <div className="mt-2 text-[10px] text-muted-foreground">
              交换中心管理控制台
            </div>
          </div>
        )}
        <nav
          className="min-h-0 flex-1 overflow-y-auto pb-4"
          aria-label="主导航"
        >
          {groups.map((group) => (
            <div key={group}>
              <div className="nav-label">{group}</div>
              {visibleNav
                .filter((n) => n.group === group)
                .sort(
                  (a, b) =>
                    (navigationPriority[a.id] ?? 100) -
                    (navigationPriority[b.id] ?? 100),
                )
                .map((n) => (
                  <button
                    key={n.id}
                    className={`nav-item ${page === n.id ? "active" : ""}`}
                    aria-current={page === n.id ? "page" : undefined}
                    onClick={() => navigate(n.id)}
                  >
                    <n.icon />
                    {n.label}
                  </button>
                ))}
            </div>
          ))}
        </nav>
        <div className="mt-auto border-t px-3 pt-4">
          <p className="text-[10px] tracking-[.12em] text-muted-foreground">
            MOEDOVE IXP
          </p>
          <p className="mt-1 text-[10px] text-[#a7acb9]">Connect with care.</p>
        </div>
      </aside>
      <div className="min-w-0">
        <header className="topbar">
          <div className="flex min-w-0 items-center gap-4">
            <Button
              size="icon"
              variant="ghost"
              className="mobile-menu"
              aria-label="打开导航"
              onClick={() => setMobile(true)}
            >
              <Menu />
            </Button>
            <div className="relative hidden md:block">
              <Search className="absolute left-0 top-0.5 size-4 text-[#a0a5b3]" />
              <input
                aria-label="查找功能"
                placeholder="查找功能…"
                className="w-44 bg-transparent pl-7 text-xs outline-none"
                value={navSearch}
                onChange={(e) => setNavSearch(e.target.value)}
              />
            </div>
            <span className="text-xs text-muted-foreground md:hidden">
              {isMember
                ? tenantName || "成员网络"
                : (active?.label ?? "控制台")}
            </span>
          </div>
          <div className="flex items-center gap-4">
            <Button
              variant="outline"
              size="sm"
              aria-label="全局搜索"
              onClick={() => setGlobalSearchOpen(true)}
            >
              <Search />
              <span className="hidden lg:inline">搜索资料</span>
              <kbd className="hidden text-[10px] text-muted-foreground xl:inline">
                Ctrl K
              </kbd>
            </Button>
            <span className="hidden items-center gap-1.5 border-r pr-5 text-[11px] text-muted-foreground sm:flex">
              <span className="size-1.5 rounded-full bg-[#81a68d]" />
              已登录
            </span>
            <div className="flex items-center gap-3">
              <div className="flex size-8 items-center justify-center rounded-full bg-[#eee9f7] text-xs font-semibold text-primary">
                {session.user.name.slice(0, 1).toUpperCase()}
              </div>
              <div className="hidden sm:block">
                <p className="text-xs font-medium">{session.user.name}</p>
                <p className="mt-1 text-[10px] text-muted-foreground">
                  {statusLabels[session.user.memberRole ?? session.user.role] ??
                    session.user.role}
                </p>
              </div>
            </div>
            <Button
              size="icon"
              variant="ghost"
              aria-label="退出登录"
              onClick={() => void logout()}
            >
              <LogOut className="text-muted-foreground" />
            </Button>
          </div>
        </header>
        <main
          className="main-content"
          key={`${session.user.id}:${session.user.memberId ?? "staff"}:${session.user.memberRole ?? session.user.role}:${viewVersion}`}
        >
          <ErrorNotice message={error} />
          {(isMember && !memberPages.includes(page)) ||
          (page === "invitations" && !canInvite) ||
          (page === "notifications" && session.user.role !== "admin") ? (
            <p>你没有此页面的访问权限。</p>
          ) : page === "dashboard" ? (
            <Dashboard
              navigate={navigate}
              version={version}
              role={session.user.role}
            />
          ) : page === "devices" ? (
            <Suspense fallback={<Loading />}>
              <Devices role={session.user.role} />
            </Suspense>
          ) : page === "integrations" ? (
            <Suspense fallback={<Loading />}>
              <Integrations role={session.user.role} />
            </Suspense>
          ) : page === "peering" ? (
            <Suspense fallback={<Loading />}>
              <Peering
                role={session.user.role}
                memberId={session.user.memberId}
              />
            </Suspense>
          ) : page === "ipam" ? (
            <IPAM role={session.user.role} />
          ) : page === "patching" ? (
            <Suspense fallback={<Loading />}>
              <Patching role={session.user.role} />
            </Suspense>
          ) : page === "notifications" ? (
            <Notifications />
          ) : page === "invitations" ? (
            <Invitations
              role={session.user.role}
              memberId={session.user.memberId}
            />
          ) : page === "monitoring" ? (
            <Monitoring role={session.user.role} />
          ) : page === "traffic" ? (
            <Suspense fallback={<Loading />}>
              <Traffic role={session.user.role} />
            </Suspense>
          ) : page === "sflow" ? (
            <Suspense fallback={<Loading />}>
              <SFlow role={session.user.role} />
            </Suspense>
          ) : page === "looking-glass" ? (
            <Suspense fallback={<Loading />}>
              <LookingGlass role={session.user.role} />
            </Suspense>
          ) : page === "atlas" ? (
            <Suspense fallback={<Loading />}>
              <Atlas role={session.user.role} />
            </Suspense>
          ) : page === "core" ? (
            <Suspense fallback={<Loading />}>
              <Core role={session.user.role} />
            </Suspense>
          ) : page === "policy" ? (
            <Policy role={session.user.role} />
          ) : page === "member-profile" ? (
            <MemberProfile
              memberId={session.user.memberId}
              memberRole={session.user.memberRole}
              tenantSwitchPending={pendingMemberSwitches > 0}
              onChange={changed}
            />
          ) : page === "deployments" ? (
            <Deployments role={session.user.role} onChange={changed} />
          ) : page === "provisioning" ? (
            <Suspense fallback={<Loading />}>
              <Provisioning role={session.user.role} onChange={changed} />
            </Suspense>
          ) : page === "audit" ? (
            <AuditPage isAdmin={session.user.role === "admin"} />
          ) : page === "documents" ? (
            <Documents role={session.user.role} />
          ) : page === "security" ? (
            <Suspense fallback={<Loading />}>
              <Security role={session.user.role} email={session.user.email} />
            </Suspense>
          ) : page === "settings" ? (
            <Settings role={session.user.role} onChange={changed} />
          ) : resources[page] &&
            (page !== "users" || session.user.role === "admin") ? (
            <Resources
              key={page}
              kind={page}
              role={session.user.role}
              onChange={changed}
            />
          ) : (
            <div className="space-y-4">
              <p>此页面不存在，或你没有访问权限。</p>
              <Button onClick={() => navigate("dashboard")}>返回概览</Button>
            </div>
          )}
          <footer className="mt-10 flex items-center justify-between gap-4 border-t pt-5 text-[10px] text-[#a4a9b7]">
            <span>© {new Date().getFullYear()} MoeDove · 让互联更简单</span>
            <span>交换中心控制台</span>
          </footer>
        </main>
      </div>
      {globalSearchOpen && (
        <Suspense fallback={null}>
          <GlobalSearch
            key={`${session.user.id}:${session.user.memberId ?? "staff"}:${session.user.role}`}
            role={session.user.role}
            open={globalSearchOpen}
            onOpenChange={setGlobalSearchOpen}
            onNavigate={navigate}
          />
        </Suspense>
      )}
    </div>
  );
}
