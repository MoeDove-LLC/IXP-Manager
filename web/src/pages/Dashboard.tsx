import { lazy, Suspense, useEffect, useState } from "react";
import {
  ArrowRight,
  Cable,
  Check,
  ChevronRight,
  Circle,
  Network,
  Plus,
  Router,
  ShieldCheck,
  Users,
} from "lucide-react";
import { api } from "@/lib/api";
import { resources } from "@/lib/resources";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
export type Audit = {
  id: string;
  action: string;
  resource?: string;
  resourceId?: string;
  actor?: string;
  actorName?: string;
  actorId?: string;
  createdAt?: string;
  time?: string;
  details?: string;
};
export type Overview = { counts: Record<string, number>; recentAudit: Audit[] };
export function AuditRows({ items }: { items: Audit[] }) {
  return (
    <div className="divide-y">
      {items.map((item) => (
        <div key={item.id} className="flex gap-3 px-6 py-4">
          <div className="mt-1 flex size-8 shrink-0 items-center justify-center rounded-full bg-muted">
            <ShieldCheck className="size-3.5 text-muted-foreground" />
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-sm font-medium">
              {auditLabel(item.action)}
              {item.resource && (
                <span className="ml-2 font-normal text-muted-foreground">
                  {resources[item.resource]?.label ??
                    (
                      {
                        settings: "交换中心设置",
                        deployments: "配置版本",
                        resources: "网络资源",
                        documents: "文档",
                        directories: "文档目录",
                        "legacy-audit": "历史摘要",
                      } as Record<string, string>
                    )[item.resource] ??
                    item.resource}
                </span>
              )}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              {item.actorName ?? item.actor ?? item.actorId ?? "系统"} ·{" "}
              {time(item.createdAt ?? item.time)}
            </p>
          </div>
        </div>
      ))}
    </div>
  );
}
function auditLabel(action: string) {
  const labels: Record<string, string> = {
    create: "创建记录",
    post: "创建记录",
    put: "更新记录",
    preview: "生成配置",
    import: "导入数据",
    "import-legacy-audit": "导入旧审计摘要",
    upload: "上传文件",
    "enable-mfa": "开启双重验证",
    "totp/enroll": "设置身份验证器",
    "totp/disable": "关闭双重验证",
    "recovery-codes": "更新恢复码",
    "revoke-token": "撤销 API 令牌",
    tokens: "创建 API 令牌",
    password: "修改密码",
    "sessions/revoke-others": "退出其他设备",
    update: "更新记录",
    delete: "删除记录",
    login: "登录控制台",
    logout: "退出登录",
    setup: "初始化交换中心",
    approve: "批准配置",
    validate: "校验配置",
  };
  return labels[action] ?? action;
}
export default function Dashboard({
  navigate,
  version,
  role,
}: {
  navigate: (page: string) => void;
  version: number;
  role: string;
}) {
  const [data, setData] = useState<Overview | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    api<Overview>("/dashboard")
      .then(setData)
      .catch((e) => setError(message(e)));
  }, [version]);
  const steps = [
    {
      key: "members",
      title: "添加成员网络",
      description: "登记 ASN 和联系信息。",
    },
    {
      key: "locations",
      title: "登记基础设施",
      description: "添加站点、交换机及端口。",
    },
    {
      key: "connections",
      title: "建立接入连接",
      description: "分配 VLAN 与互联地址。",
    },
    {
      key: "peers",
      title: "配置路由会话",
      description: "设置邻居和允许的前缀。",
    },
  ];
  if (role === "member")
    return (
      <>
        <Heading
          title="成员工作空间"
          description="查看属于你的网络、接入连接与联络信息。"
        />
        <ErrorNotice message={error} />
        {!data ? (
          <Loading />
        ) : (
          <div className="stat-grid">
            {["members", "connections", "contacts", "equipment"].map((k) => (
              <Card key={k} className="stat-card">
                <p className="text-xs text-muted-foreground">
                  {resources[k]?.label}
                </p>
                <div className="stat-value mt-3">{data.counts[k] ?? 0}</div>
                <Button
                  variant="link"
                  className="mt-3 p-0"
                  onClick={() => navigate(k)}
                >
                  查看详情
                  <ArrowRight />
                </Button>
              </Card>
            ))}
          </div>
        )}
      </>
    );
  return (
    <>
      <Heading title="网络概览" description="每一次连接，从清晰的管理开始。">
        <Button onClick={() => navigate("members")}>
          <Plus />
          管理成员
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {!data ? (
        !error && <Loading />
      ) : (
        <>
          <div className="stat-grid">
            {[
              {
                key: "members",
                label: "成员网络",
                icon: Users,
                unit: "个网络",
              },
              {
                key: "connections",
                label: "接入连接",
                icon: Cable,
                unit: "条连接",
              },
              {
                key: "peers",
                label: "BGP 会话",
                icon: Network,
                unit: "个已配置会话",
              },
              {
                key: "routers",
                label: "路由服务器",
                icon: Router,
                unit: "台服务器",
              },
            ].map((s) => (
              <Card key={s.key} className="stat-card">
                <div className="mb-4 flex items-center justify-between">
                  <span className="text-xs text-muted-foreground">
                    {s.label}
                  </span>
                  <div className="stat-icon">
                    <s.icon />
                  </div>
                </div>
                <div className="stat-value">{data.counts[s.key] ?? 0}</div>
                <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
                  <span>{s.unit}</span>
                  <button
                    className="text-primary"
                    aria-label={`查看${s.label}`}
                    onClick={() => navigate(s.key)}
                  >
                    <ArrowRight className="size-4" />
                  </button>
                </div>
              </Card>
            ))}
          </div>
          <div className="overview-grid">
            <div className="space-y-6">
              <Card>
                <CardHeader className="flex-row items-center justify-between">
                  <CardTitle>接入工作台</CardTitle>
                  <Badge variant="outline">
                    {steps.filter((s) => data.counts[s.key] > 0).length} / 4
                    已开始
                  </Badge>
                </CardHeader>
                <CardContent>
                  <p className="mb-5 text-xs leading-6 text-muted-foreground">
                    按顺序建立网络资源，关联信息后即可生成路由配置。
                  </p>
                  <div className="space-y-2">
                    {steps.map((s, i) => (
                      <button
                        key={s.key}
                        className="group flex w-full items-center gap-4 rounded-lg border border-transparent p-3 text-left transition hover:border-border hover:bg-muted/50"
                        onClick={() => navigate(s.key)}
                      >
                        <div
                          className={`flex size-9 shrink-0 items-center justify-center rounded-full text-sm ${data.counts[s.key] > 0 ? "bg-primary/10 text-primary" : "border text-muted-foreground"}`}
                        >
                          {data.counts[s.key] > 0 ? (
                            <Check className="size-4" />
                          ) : (
                            String(i + 1).padStart(2, "0")
                          )}
                        </div>
                        <div className="flex-1">
                          <p className="text-sm font-medium">{s.title}</p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            {s.description}
                          </p>
                        </div>
                        <ChevronRight className="size-4 text-muted-foreground transition group-hover:translate-x-1" />
                      </button>
                    ))}
                  </div>
                </CardContent>
              </Card>
              <Card className="bg-[#f3f0fa] border-[#e8e0f5]">
                <CardContent className="flex items-start gap-4 pt-6">
                  <div className="rounded-xl bg-white p-3 text-primary">
                    <ShieldCheck className="size-6" />
                  </div>
                  <div>
                    <h3 className="text-sm font-semibold">让配置变更更安心</h3>
                    <p className="mt-2 text-xs leading-6 text-muted-foreground">
                      从生成预览到语法校验，再到批准版本，每一步都留下记录。显式前缀白名单帮助控制路由传播范围。
                    </p>
                    <Button
                      className="mt-3 h-auto p-0 text-xs"
                      variant="link"
                      onClick={() => navigate("deployments")}
                    >
                      前往配置中心
                      <ArrowRight />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </div>
            <Card>
              <CardHeader className="flex-row items-center justify-between border-b">
                <CardTitle>最近活动</CardTitle>
                <button
                  className="text-xs text-primary"
                  onClick={() => navigate("audit")}
                >
                  全部记录
                </button>
              </CardHeader>
              {data.recentAudit?.length ? (
                <AuditRows
                  items={[...data.recentAudit].reverse().slice(0, 6)}
                />
              ) : (
                <Empty
                  title="操作记录将在这里出现"
                  description="创建和修改资源后，可在这里追踪最近的变更。"
                />
              )}
              <div className="mx-6 mb-5 mt-4 flex items-center gap-2 border-t pt-4 text-xs text-muted-foreground">
                <Circle className="size-2 fill-primary text-primary" />
                仅展示已记录的数据，非实时链路监控
              </div>
            </Card>
          </div>
        </>
      )}
    </>
  );
}
const LegacyAuditArchive = lazy(() => import("@/components/LegacyAuditArchive"));

export function AuditPage({ isAdmin }: { isAdmin: boolean }) {
  const [items, setItems] = useState<Audit[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    api<{ items: Audit[] }>("/audit", { signal: controller.signal })
      .then(result => { if (!controller.signal.aborted) setItems([...result.items].reverse()); })
      .catch(error => { if (!controller.signal.aborted) setError(message(error)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [revision]);
  return (
    <>
      <Heading
        title="操作审计"
        description="追踪配置、资源和账号变更，保留可回溯的操作记录。"
      />
      <ErrorNotice message={error} />
      <Card>
        {loading ? (
          <Loading />
        ) : items.length ? (
          <AuditRows items={items} />
        ) : (
          <Empty description="还没有已记录的操作。" />
        )}
      </Card>
      {isAdmin && <Suspense fallback={<Loading />}><LegacyAuditArchive onImported={() => setRevision(n => n + 1)} /></Suspense>}
    </>
  );
}
