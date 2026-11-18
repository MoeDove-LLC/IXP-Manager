import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  Check,
  CheckCheck,
  ChevronRight,
  Copy,
  Download,
  FileCode2,
  Plus,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import DeploymentReadiness, { type DeploymentReadinessResult } from "@/components/DeploymentReadiness";
import AS112Guidance from "@/components/AS112Guidance";
import RouterOSHandoff from "@/components/RouterOSHandoff";
import { statusLabels, type Row } from "@/lib/resources";
import { singleRouterFamily } from "@/lib/router-platforms";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
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
  time,
} from "@/components/shared";
type Capability = {
  platform: string;
  label: string;
  render: boolean;
  validate: boolean;
  apply: boolean;
  rollback: boolean;
  validationLevel: string;
  notes: string;
  extension: string;
  roles?: string[];
  collectorPolicies?: string[];
  fetch?: boolean;
  nativeCheckerAvailable?: boolean;
  singleAddressFamily?: boolean;
  nativeCheckerFamilies?: { ipv4: boolean; ipv6: boolean };
  rfc1997Control?: boolean;
  rfc1997DefaultPassthrough?: boolean;
};
type CredentialChange = {
  peerId: string;
  change: "added" | "rotated" | "removed" | "revision";
  beforeRevision?: string;
  afterRevision?: string;
  beforeConfigured: boolean;
  afterConfigured: boolean;
};
type ConfigDiff = {
  baseId: string;
  changes: { type: "context" | "add" | "remove"; text: string }[];
  added: number;
  removed: number;
  truncated: boolean;
  redacted: boolean;
  baseUnavailable?: boolean;
  credentialChanges: CredentialChange[];
};
type Deployment = {
  id: string;
  platform?: string;
  routerRole?: string;
  collectorPolicy?: string;
  rfc1997Passthrough?: boolean;
  rfc1997Mode?: "native" | "explicit";
  routerId: string;
  status: string;
  createdAt: string;
  checksum: string;
  checksumKind?: string;
  validation?: string;
  pruned?: boolean;
  artifactUnavailable?: boolean;
  prunedAt?: string;
};
type DeploymentDetail = Deployment & { config: string; redacted: true };
const credentialLabels: Record<string, string> = {
  added: "新增认证",
  rotated: "轮换凭据",
  removed: "移除认证",
  revision: "修订更新（仍未配置）",
};
const shellQuote = (value: string) =>
  "'" + value.replace(/'/g, "'\"'\"'") + "'";
export default function Deployments({
  role,
  onChange,
}: {
  role: string;
  onChange: () => void;
}) {
  const [tab, setTab] = useState("config"),
    [diff, setDiff] = useState<ConfigDiff | null>(null),
    [diffId, setDiffId] = useState(""),
    [diffError, setDiffError] = useState(""),
    [detail, setDetail] = useState<DeploymentDetail | null>(null),
    [detailLoading, setDetailLoading] = useState(false),
    [detailError, setDetailError] = useState(""),
    [viewVersion, setViewVersion] = useState(0);
  const [capabilities, setCapabilities] = useState<Capability[]>([]),
    [items, setItems] = useState<Deployment[]>([]),
    [routers, setRouters] = useState<Row[]>([]),
    [peers, setPeers] = useState<Row[]>([]),
    [selected, setSelected] = useState(""),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [router, setRouter] = useState(""),
    [open, setOpen] = useState(false),
    [confirm, setConfirm] = useState(false),
    [receipt, setReceipt] = useState<{ id: string; sha256: string } | null>(
      null,
    ),
    [copyNote, setCopyNote] = useState(""),
    [cleanup, setCleanup] = useState<"prune" | "delete" | null>(null),
    [cleanupError, setCleanupError] = useState("");
  const [readiness, setReadiness] = useState<DeploymentReadinessResult | null>(null),
    [readinessLoading, setReadinessLoading] = useState(false),
    [readinessError, setReadinessError] = useState(""),
    [readinessVersion, setReadinessVersion] = useState(-1);
  const alive = useRef(true);
  const readinessEpoch = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const current = items.find((d) => d.id === selected),
    canWrite = role === "admin" || role === "operator";
  const readinessReady = !!(current && readiness && !loading && !readinessLoading && readinessVersion === viewVersion && readiness.id === current.id && readiness.routerId === current.routerId && readiness.platform === (current.platform ?? "bird2") && readiness.reviewIdentifier === current.checksum && readiness.status === current.status);
  const handoffEpoch = readinessEpoch.current;
  const allowed = (action: "validate" | "approve" | "download" | "generate") => readinessReady && readiness?.actions[action].allowed === true;
  const unavailable = !!(current?.pruned || current?.artifactUnavailable),
    reviewReady =
      detail?.id === selected &&
      detail.routerId === current?.routerId &&
      (detail.platform ?? "bird2") === (current?.platform ?? "bird2") &&
      detail.checksum === current?.checksum &&
      detail.redacted === true &&
      !detailLoading &&
      !unavailable;
  const currentCapability = capabilities.find(
      (c) => c.platform === (current?.platform ?? "bird2"),
    ),
    selectedCapability = capabilities.find(
      (c) =>
        c.platform ===
        (routers.find((r) => r.id === router)?.platform ?? "bird2"),
    );
  const selectedRouter = routers.find((r) => r.id === router);
  const selectedSingleFamily = selectedCapability?.singleAddressFamily === true;
  const selectedFamily = selectedSingleFamily ? singleRouterFamily(selectedRouter) : null;
  const selectedCheckerAvailable = selectedSingleFamily
    ? (selectedFamily === 4 ? selectedCapability.nativeCheckerFamilies?.ipv4 :
      selectedFamily === 6 ? selectedCapability.nativeCheckerFamilies?.ipv6 : undefined)
    : selectedCapability?.nativeCheckerAvailable;
  const nativeCheckerMissing =
    currentCapability?.validationLevel === "native" &&
    (readinessReady ? readiness?.nativeCheckerAvailable === false :
      !currentCapability.singleAddressFamily && currentCapability.nativeCheckerAvailable === false);
  const selectedRouterRole = String(selectedRouter?.role || "route-server");
  const selectedCollectorPolicy = String(
    selectedRouter?.collectorPolicy || "observe",
  );
  const selectedRoleSupported =
    (selectedCapability?.roles ?? ["route-server"]).includes(
      selectedRouterRole,
    ) &&
    (selectedRouterRole !== "route-collector" ||
      (selectedCapability?.collectorPolicies ?? []).includes(
        selectedCollectorPolicy,
      ));
  const fetchOrigin =
    location.protocol === "https:"
      ? location.origin
      : "https://control.example";
  const fetchCommand = current
    ? `ixp-agent fetch --server ${shellQuote(fetchOrigin)} --deployment ${shellQuote(current.id)} --router ${shellQuote(current.routerId)} --platform ${shellQuote(current.platform ?? "bird2")} --output reviewed.conf --token-file /etc/moedove/deploy.token`
    : "";
  const load = async () => {
    readinessEpoch.current++;
    setLoading(true);
    setReadiness(null);
    setReadinessVersion(-1);
    setReadinessError("");
    setReadinessLoading(false);
    try {
      const [d, r, c, p] = await Promise.all([
        api<{ items: Deployment[] }>("/deployments"),
        api<{ items: Row[] }>("/resources/routers"),
        api<{ items: Capability[] }>("/router-platforms"),
        api<{ items: Row[] }>("/resources/peers"),
      ]);
      setCapabilities(c.items);
      setItems(d.items.sort((a, b) => b.createdAt.localeCompare(a.createdAt)));
      setRouters(r.items);
      setPeers(p.items);
      setSelected((id) =>
        d.items.some((v) => v.id === id) ? id : (d.items[0]?.id ?? ""),
      );
      setViewVersion((v) => v + 1);
    } catch (e) {
      readinessEpoch.current++;
      setReadiness(null);
      setReadinessVersion(-1);
      setReadinessLoading(false);
      setError(message(e));
      setReadinessError("配置版本刷新失败，当前操作已停用；请重新检查。");
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    const epoch = ++readinessEpoch.current;
    setReadiness(null);
    setReadinessVersion(-1);
    setReadinessError("");
    if (!selected || !current) {
      setReadinessLoading(false);
      return;
    }
    const controller = new AbortController();
    setReadinessLoading(true);
    api<DeploymentReadinessResult>(`/deployments/${selected}/readiness`, { signal: controller.signal })
      .then(value => {
        if (controller.signal.aborted || epoch !== readinessEpoch.current) return;
        const actions = ["validate", "approve", "download", "generate"] as const;
        if (value.addressFamily !== undefined && value.addressFamily !== 4 && value.addressFamily !== 6) {
          throw new Error("版本的地址族无法识别，请重新检查。");
        }
        if (value.id !== selected || value.routerId !== current.routerId || value.platform !== (current.platform ?? "bird2") || value.reviewIdentifier !== current.checksum || value.targetChecked !== false || !["draft", "validated", "approved"].includes(value.status) || !["current", "changed", "unavailable"].includes(value.inputs) || !["verified", "pruned", "unavailable"].includes(value.artifact) || !["native", "structural", "none"].includes(value.validationLevel) || typeof value.nativeCheckerAvailable !== "boolean" || !Number.isFinite(new Date(value.checkedAt).getTime()) || !value.actions || actions.some(action => typeof value.actions[action]?.allowed !== "boolean" || !Array.isArray(value.actions[action]?.reasons) || !value.actions[action].reasons.every(reason => typeof reason === "string"))) {
          throw new Error("就绪检查未与当前版本正确绑定，已禁用后续操作；请重新检查。");
        }
        const handoff = value.actions.routerosHandoff;
        if (value.platform === "routeros7" && handoff !== undefined &&
            (typeof handoff?.allowed !== "boolean" || !Array.isArray(handoff?.reasons) || !handoff.reasons.every(reason => typeof reason === "string"))) {
          throw new Error("RouterOS 交接许可无法核对，请重新检查。");
        }
        setItems(rows => rows.map(row => row.id === value.id ? { ...row, status: value.status } : row));
        setReadiness(value);
        setReadinessVersion(viewVersion);
      })
      .catch(e => {
        if (controller.signal.aborted || epoch !== readinessEpoch.current) return;
        setReadiness(null);
        setReadinessError(`${message(e)} 当前版本的校验、批准和下载已停用，请重新检查。`);
      })
      .finally(() => { if (!controller.signal.aborted && epoch === readinessEpoch.current) setReadinessLoading(false); });
    return () => controller.abort();
  }, [selected, viewVersion, current?.checksum, current?.routerId, current?.platform]);
  useEffect(() => {
    setDetail(null);
    setDetailError("");
    setDiff(null);
    setDiffId("");
    setDiffError("");
    setReceipt(null);
    setCopyNote("");
    setConfirm(false);
    if (!selected || unavailable) {
      setDetailLoading(false);
      return;
    }
    const controller = new AbortController();
    setDetailLoading(true);
    api<DeploymentDetail>(`/deployments/${selected}`, {
      signal: controller.signal,
    })
      .then((value) => {
        if (controller.signal.aborted) return;
        if (value.redacted !== true || typeof value.config !== "string")
          throw new Error("服务端未提供经过脱敏的预览，已拒绝显示配置。");
        if (value.id !== selected || value.routerId !== current?.routerId || (value.platform ?? "bird2") !== (current?.platform ?? "bird2") || value.checksum !== current?.checksum)
          throw new Error("配置预览未与选中版本的目标和审阅标识绑定，已禁用后续操作。");
        setDetail(value);
      })
      .catch((e) => {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 410) {
          setItems((rows) =>
            rows.map((row) =>
              row.id === selected
                ? { ...row, pruned: true, artifactUnavailable: true }
                : row,
            ),
          );
          setDetailError(
            "此版本的配置文件已清理，保留的审查标识与元数据仍可查看。",
          );
        } else setDetailError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [selected, viewVersion, unavailable]);
  useEffect(() => {
    if (tab !== "diff" || !selected || unavailable) return;
    const controller = new AbortController();
    setDiff(null);
    setDiffId("");
    setDiffError("");
    api<ConfigDiff>(`/deployments/${selected}/diff`, {
      signal: controller.signal,
    })
      .then((value) => {
        if (controller.signal.aborted) return;
        if (value.redacted !== true)
          throw new Error("服务端未提供脱敏差异，已拒绝显示。");
        setDiff({ ...value, credentialChanges: value.credentialChanges ?? [] });
        setDiffId(selected);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setDiffError(message(e));
      });
    return () => controller.abort();
  }, [selected, tab, viewVersion, unavailable]);
  const create = async () => {
    if (!selectedRoleSupported || (selectedSingleFamily && !selectedFamily)) return;
    setError("");
    setBusy(true);
    try {
      const data = await save<Deployment>("/deployments", { routerId: router });
      setItems((rows) => [data, ...rows.filter((d) => d.id !== data.id)]);
      setSelected(data.id);
      setTab("config");
      setOpen(false);
      onChange();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const regenerate = async () => {
    if (!current || !allowed("generate")) return;
    setError(""); setBusy(true);
    try {
      const data = await save<Deployment>("/deployments", { routerId: current.routerId });
      setItems(rows => [data, ...rows.filter(item => item.id !== data.id)]);
      setSelected(data.id); setTab("config"); setViewVersion(value => value + 1); onChange();
    } catch (e) { setError(message(e)); setReadiness(null); setReadinessVersion(-1); }
    finally { setBusy(false); }
  };
  const action = async (type: "validate" | "approve") => {
    if (!current || !reviewReady || !allowed(type)) return;
    setError("");
    setBusy(true);
    try {
      const data = await save<Deployment>(
        `/deployments/${current.id}/${type}`,
        type === "approve" ? { checksum: current.checksum } : {},
      );
      setItems((rows) => rows.map((d) => (d.id === data.id ? data : d)));
      setConfirm(false);
      setViewVersion((v) => v + 1);
      onChange();
    } catch (e) {
      setError(message(e));
      setConfirm(false);
      setReadiness(null);
      setReadinessVersion(-1);
    } finally {
      setBusy(false);
    }
  };
  const download = async () => {
    if (!current || !canWrite || current.status !== "approved" || unavailable || !reviewReady || !allowed("download"))
      return;
    setError("");
    setReceipt(null);
    setBusy(true);
    try {
      if (!crypto.subtle)
        throw new Error(
          "此浏览器需要 HTTPS 或 localhost 才能核对私密配置；也可使用 ixp-agent fetch。",
        );
      const response = await fetch(`/api/deployments/${current.id}/config`, {
        credentials: "same-origin",
        redirect: "error",
      });
      if (!response.ok) {
        let error = `下载失败 (${response.status})`;
        try {
          error = (await response.json()).error ?? error;
        } catch {}
        throw new Error(error);
      }
      const sha = response.headers.get("X-Config-SHA256") ?? "";
      if (
        !/^[a-f0-9]{64}$/i.test(sha) ||
        response.headers.get("X-Config-Revision") !== current.id ||
        response.headers.get("X-Config-Router-ID") !== current.routerId ||
        response.headers.get("X-Config-Platform") !==
          (current.platform ?? "bird2")
      )
        throw new Error("下载缺少匹配的目标与文件校验信息，未保存文件。");
      const blob = await response.blob(),
        digest = await crypto.subtle.digest(
          "SHA-256",
          await blob.arrayBuffer(),
        ),
        actual = Array.from(new Uint8Array(digest), (v) =>
          v.toString(16).padStart(2, "0"),
        ).join("");
      if (actual !== sha.toLowerCase())
        throw new Error("下载内容校验失败，未保存文件。");
      if (!alive.current) return;
      const url = URL.createObjectURL(blob),
        link = document.createElement("a");
      link.href = url;
      link.download = `${current.platform ?? "bird2"}-${current.id}.${currentCapability?.extension ?? "conf"}`;
      link.click();
      URL.revokeObjectURL(url);
      setReceipt({ id: current.id, sha256: actual });
    } catch (e) {
      if (alive.current) {
        setError(message(e));
        setReadiness(null);
        setReadinessVersion(-1);
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const copy = async (text: string, label: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopyNote(`${label}已复制`);
    } catch {
      setError("浏览器未允许复制，请手动选择并复制显示的内容。");
    }
  };
  const cleanArtifact = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!current || !cleanup) return;
    setBusy(true);
    setCleanupError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      const body = {
        checksum: current.checksum,
        acknowledge: true,
        reason: data.reason || "",
      };
      if (cleanup === "prune") {
        const result = await save<Deployment>(
          `/deployments/${current.id}/prune`,
          body,
        );
        setItems((rows) => rows.map((d) => (d.id === result.id ? result : d)));
        setViewVersion((v) => v + 1);
      } else {
        await save(`/deployments/${current.id}`, body, "DELETE");
        setItems((rows) => rows.filter((d) => d.id !== current.id));
        setSelected("");
        await load();
      }
      setCleanup(null);
      setReceipt(null);
      onChange();
    } catch (e) {
      setCleanupError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="配置中心"
        description="生成、检查并批准路由配置，让每一次变更都有依据。"
      >
        {canWrite && (
          <Button
            onClick={() => {
              setOpen(true);
              setError("");
            }}
          >
            <Plus />
            生成配置
          </Button>
        )}
      </Heading>
      <div className="notice mb-5 flex items-start gap-3">
        <ShieldCheck className="mt-0.5 size-5 shrink-0" />
        <span>
          批准后可下载通过校验的版本。实际应用、验证和回退按目标路由器的平台流程执行。
        </span>
      </div>
      <ErrorNotice message={open ? "" : error} />
      {loading && !items.length ? (
        <Loading />
      ) : !items.length ? (
        <Card>
          <Empty
            title="生成你的第一份路由配置"
            description="先添加路由服务器和 BGP 会话，然后生成可预览、可校验的 平台配置。"
            action={canWrite ? "生成配置" : undefined}
            onAction={() => setOpen(true)}
          />
        </Card>
      ) : (
        <div className="deployment-grid mt-5">
          <Card className="self-start overflow-hidden">
            <div className="flex items-center justify-between border-b px-5 py-4">
              <h2 className="text-sm font-semibold">配置版本</h2>
              <Button
                variant="ghost"
                size="icon"
                aria-label="刷新配置版本"
                onClick={() => void load()}
                disabled={busy || loading}
              >
                <RefreshCw />
              </Button>
            </div>
            <div className="max-h-[650px] overflow-y-auto">
              {items.map((d) => (
                <button
                  key={d.id}
                  data-deployment-id={d.id}
                  onClick={() => setSelected(d.id)}
                  className={`flex w-full items-center gap-3 border-b px-5 py-4 text-left ${selected === d.id ? "bg-primary/5" : "hover:bg-muted/40"}`}
                >
                  <FileCode2 className="size-5 shrink-0 text-primary" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">
                      {routers.find((r) => r.id === d.routerId)?.name ??
                        d.routerId}
                    </p>
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      {time(d.createdAt)}
                    </p>
                    <Badge
                      variant={d.status === "draft" ? "outline" : "default"}
                      className="mt-2 text-[10px]"
                    >
                      {statusLabels[d.status] ?? d.status}
                    </Badge>
                    {d.pruned && (
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        配置文件已清理
                      </p>
                    )}
                  </div>
                  <ChevronRight className="size-4 text-muted-foreground" />
                </button>
              ))}
            </div>
          </Card>
          {current && (
            <Card className="min-w-0">
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <CardTitle>配置预览</CardTitle>
                  <div className="flex flex-wrap gap-2">
                    <Badge variant="outline">
                      {currentCapability?.label ?? current.platform ?? "BIRD 2"}
                    </Badge>
                    <Badge variant="outline">
                      {statusLabels[current.routerRole || "route-server"] ??
                        current.routerRole}
                    </Badge>
                  </div>
                </div>
                <p className="mt-2 break-all text-[11px] leading-5 text-muted-foreground mono">
                  版本校验标识：{current.checksum}
                </p>
                <p className="text-xs leading-6 text-muted-foreground">
                  此标识用于审批核对，不是配置文件
                  SHA-256。页面预览与差异均由服务端脱敏，不能直接用于部署。
                </p>
              </CardHeader>
              <CardContent>
                <DeploymentReadiness value={readinessReady ? readiness : null} loading={loading || readinessLoading} error={readinessError} busy={busy} onRefresh={() => setViewVersion(value => value + 1)} onGenerate={() => void regenerate()} />
                {current.platform === "routeros7" && canWrite && (
                  <RouterOSHandoff
                    binding={{ deploymentId: current.id, routerId: current.routerId, reviewIdentifier: current.checksum }}
                    contextKey={JSON.stringify([current.id, current.routerId, current.checksum, current.status, viewVersion, role])}
                    enabled={!busy && !unavailable && current.status === "draft" && reviewReady && readinessReady && readiness?.inputs === "current" && readiness.artifact === "verified" && readiness.actions.routerosHandoff?.allowed === true}
                    stillBound={() => handoffEpoch === readinessEpoch.current}
                    onFailure={note => {
                      readinessEpoch.current++;
                      setReadiness(null);
                      setReadinessVersion(-1);
                      setReadinessLoading(false);
                      setReadinessError(note);
                    }}
                  />
                )}
                {current.routerRole === "as112" && <div className="mb-4"><AS112Guidance platform={current.platform} /></div>}
                <div className="notice mb-4">
                  {currentCapability?.notes ?? "正在获取平台能力…"}
                  {currentCapability && !currentCapability.validate && (
                    <p>当前平台暂不支持自动语法校验、批准与生产配置下载。</p>
                  )}
                </div>
                {nativeCheckerMissing && (
                  <p className="notice mb-4">
                    服务端尚未找到此平台的原生校验器。请在宿主机配置对应 binary
                    路径后刷新；实际校验仍会检查工具版本和配置，找到文件不代表校验通过。
                  </p>
                )}
                {typeof current.rfc1997Passthrough === "boolean" && (
                  <p className="notice mb-4" aria-label="此版本 RFC 1997 语义">
                    此版本 RFC 1997：
                    {current.rfc1997Mode === "explicit"
                      ? "显式设置"
                      : "生成时的平台默认"}{" "}
                    ·{" "}
                    {current.rfc1997Passthrough
                      ? "NO_EXPORT / NO_ADVERTISE 等保留给客户端处理"
                      : "由服务器执行 NO_EXPORT / NO_ADVERTISE 等限制"}
                    。
                    {current.routerRole === "route-collector" &&
                      "收集器仍禁止导出路由。"}
                  </p>
                )}
                {currentCapability?.rfc1997Control &&
                  typeof current.rfc1997Passthrough !== "boolean" && (
                    <p className="mb-4 text-xs leading-6 text-muted-foreground">
                      历史版本未记录独立的 RFC 1997
                      语义元数据，请核对配置正文；当前路由器设置不会改变已有版本。
                    </p>
                  )}
                {current.routerRole === "route-collector" && (
                  <p className="notice mb-4">
                    此版本为路由收集器：始终禁止导出路由，不安装到内核转发表。
                    {(current.collectorPolicy || "observe") === "observe"
                      ? "观察模式保留被路由策略过滤的路由供本机诊断；授权路由表仍按前缀、起源与 RPKI 策略筛选，协议安全拒绝项不保证保留。"
                      : "仅授权模式只收取符合导入策略的路由，不承诺保留被拒绝的路由。"}
                  </p>
                )}
                <div className="mb-5 flex flex-wrap items-center gap-2 text-xs">
                  <span className="flex items-center gap-1 text-primary">
                    <Check className="size-3" />
                    已生成
                  </span>
                  <ChevronRight className="size-3 text-muted-foreground" />
                  <span
                    className={
                      current.status !== "draft"
                        ? "text-primary"
                        : "text-muted-foreground"
                    }
                  >
                    {currentCapability?.validationLevel === "structural"
                      ? "结构校验"
                      : "原生语法校验"}
                  </span>
                  <ChevronRight className="size-3 text-muted-foreground" />
                  <span
                    className={
                      current.status === "approved"
                        ? "text-primary"
                        : "text-muted-foreground"
                    }
                  >
                    批准版本
                  </span>
                </div>
                <div className="mb-3 flex gap-2" aria-label="配置查看方式">
                  <Button
                    size="sm"
                    variant={tab === "config" ? "secondary" : "ghost"}
                    aria-pressed={tab === "config"}
                    onClick={() => setTab("config")}
                  >
                    完整配置
                  </Button>
                  <Button
                    size="sm"
                    variant={tab === "diff" ? "secondary" : "ghost"}
                    aria-pressed={tab === "diff"}
                    onClick={() => setTab("diff")}
                  >
                    变更对比
                  </Button>
                </div>
                {unavailable ? (
                  <p className="notice">
                    此版本配置文件已明确清理。仅保留元数据与审查标识，不再提供预览、校验或下载。
                  </p>
                ) : tab === "config" ? (
                  detailError ? (
                    <ErrorNotice message={detailError} />
                  ) : !reviewReady ? (
                    <Loading />
                  ) : (
                    <pre className="max-h-[540px] overflow-auto rounded-lg bg-[#242635] p-5 text-xs leading-6 text-[#dfddec]">
                      <code>{detail?.config}</code>
                    </pre>
                  )
                ) : diffError ? (
                  <ErrorNotice message={diffError} />
                ) : !diff || diffId !== selected ? (
                  <Loading />
                ) : (
                  <>
                    <div className="mb-3 flex flex-wrap gap-3 text-xs text-muted-foreground">
                      <span>
                        {diff.baseId
                          ? `对比已批准版本 ${diff.baseId.slice(0, 12)}`
                          : "尚无已批准版本，全部内容为新增"}
                      </span>
                      {!diff.baseUnavailable && (
                        <>
                          <span className="text-green-700">+{diff.added}</span>
                          <span className="text-red-700">−{diff.removed}</span>
                        </>
                      )}
                    </div>
                    {diff.baseUnavailable && (
                      <p className="notice mb-3">
                        旧基线文件已清理，下方不能视为完整的配置差异。凭据修订变化仍可从保留元数据核对。
                      </p>
                    )}
                    {diff.credentialChanges.length > 0 && (
                      <div className="mb-4 rounded-lg border p-4">
                        <h3 className="mb-3 text-sm font-semibold">
                          BGP 凭据修订变化
                        </h3>
                        <div className="space-y-3">
                          {diff.credentialChanges.map((change) => (
                            <div
                              key={change.peerId}
                              className="text-xs leading-6"
                            >
                              <div className="flex flex-wrap items-center gap-2">
                                <span className="font-medium">
                                  {peers.find((p) => p.id === change.peerId)
                                    ?.name ?? change.peerId}
                                </span>
                                <Badge
                                  variant={
                                    change.change === "removed"
                                      ? "outline"
                                      : "default"
                                  }
                                >
                                  {credentialLabels[change.change] ??
                                    change.change}
                                </Badge>
                              </div>
                              <p className="mono break-all text-muted-foreground">
                                {change.beforeConfigured ? "已配置" : "未配置"}{" "}
                                · {change.beforeRevision || "无修订"} →{" "}
                                {change.afterConfigured ? "已配置" : "未配置"} ·{" "}
                                {change.afterRevision || "无修订"}
                              </p>
                            </div>
                          ))}
                        </div>
                        <p className="mt-2 text-xs text-muted-foreground">
                          此处只展示修订标识，不展示任何密钥值。请协调两端后重新发布配置。
                        </p>
                      </div>
                    )}
                    {diff.truncated && (
                      <p className="notice mb-3">
                        对比内容较长，部分行已截断。请同时检查完整配置。
                      </p>
                    )}
                    <pre className="max-h-[540px] overflow-auto rounded-lg border bg-muted/30 py-3 text-xs leading-6">
                      <code>
                        {diff.changes.map((line, i) => (
                          <div
                            key={i}
                            className={`px-4 ${line.type === "add" ? "bg-green-50 text-green-800" : line.type === "remove" ? "bg-red-50 text-red-800" : "text-muted-foreground"}`}
                          >
                            {line.type === "add"
                              ? "+ "
                              : line.type === "remove"
                                ? "− "
                                : "  "}
                            {line.text}
                          </div>
                        ))}
                      </code>
                    </pre>
                  </>
                )}

                {current.validation && (
                  <div className="mt-4 rounded-md border bg-muted/40 p-3 text-xs leading-5 whitespace-pre-wrap">
                    {current.validation}
                  </div>
                )}
                <div className="mt-5 flex flex-wrap justify-end gap-2">
                  {canWrite && (
                    <Button
                      variant="outline"
                      disabled={
                        busy || current.status !== "approved" || unavailable || !reviewReady || !allowed("download")
                      }
                      title={
                        current.status !== "approved"
                          ? "通过校验并由管理员批准后可下载"
                          : undefined
                      }
                      onClick={() => void download()}
                    >
                      <Download />
                      下载配置
                    </Button>
                  )}
                  {canWrite && current.status === "draft" && !unavailable && (
                    <Button
                      disabled={
                        busy ||
                        !reviewReady ||
                        !allowed("validate") ||
                        !currentCapability?.validate ||
                        nativeCheckerMissing
                      }
                      onClick={() => void action("validate")}
                    >
                      <ShieldCheck />
                      {busy
                        ? "校验中…"
                        : currentCapability?.validationLevel === "structural"
                          ? "检查配置结构"
                          : "校验配置"}
                    </Button>
                  )}
                  {role === "admin" &&
                    current.status === "validated" &&
                    !unavailable && (
                      <Button
                        disabled={busy || !reviewReady || !allowed("approve")}
                        onClick={() => setConfirm(true)}
                      >
                        <CheckCheck />
                        批准版本
                      </Button>
                    )}
                </div>
                {canWrite && (
                  <p className="mt-3 text-xs leading-6 text-muted-foreground">
                    授权下载包含真实认证凭据（如已配置），请按私密文件保存。只读用户只能查看脱敏内容。
                  </p>
                )}
                {canWrite && receipt?.id === current.id && (
                  <div className="mt-4 rounded-lg border p-4">
                    <h3 className="text-sm font-semibold">
                      本次下载文件 SHA-256
                    </h3>
                    <p
                      className="mono mt-2 break-all text-xs"
                      aria-label="下载文件真实 SHA-256"
                    >
                      {receipt.sha256}
                    </p>
                    <p className="mt-2 text-xs leading-6 text-muted-foreground">
                      已核对响应目标、文件摘要与下载内容。此 SHA
                      仅用于本次私密文件，不要用上方的审批标识代替。
                    </p>
                    <Button
                      className="mt-3"
                      variant="outline"
                      size="sm"
                      onClick={() => void copy(receipt.sha256, "文件 SHA-256")}
                    >
                      <Copy />
                      复制文件 SHA
                    </Button>
                  </div>
                )}
                {canWrite &&
                  current.status === "approved" &&
                  !unavailable &&
                  allowed("download") &&
                  currentCapability?.fetch === true && (
                    <details className="mt-4 rounded-lg border p-4">
                      <summary className="cursor-pointer text-sm font-medium">
                        在目标路由器安全拉取配置
                      </summary>
                      <p className="mt-3 text-xs leading-6 text-muted-foreground">
                        使用绑定此路由器的 deployments:pull
                        令牌；命令只下载并验证文件，不应用配置，以 0600
                        权限原子保存且不覆盖已有文件。
                      </p>
                      {location.protocol !== "https:" && (
                        <p className="notice mt-3">
                          当前为 HTTP 开发地址，请把命令中的
                          https://control.example 换成 HTTPS 生产控制台地址。
                        </p>
                      )}
                      <pre
                        className="mono mt-3 overflow-x-auto rounded bg-muted/50 p-3 text-xs whitespace-pre-wrap break-all"
                        aria-label="私密配置安全拉取命令"
                      >
                        {fetchCommand}
                      </pre>
                      <Button
                        className="mt-3"
                        variant="outline"
                        size="sm"
                        onClick={() => void copy(fetchCommand, "拉取命令")}
                      >
                        <Copy />
                        复制拉取命令
                      </Button>
                    </details>
                  )}
                {copyNote && (
                  <p role="status" className="mt-3 text-xs text-primary">
                    {copyNote}
                  </p>
                )}
                {role === "admin" && (
                  <div className="mt-5 flex justify-end border-t pt-4">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy}
                      onClick={() => {
                        setCleanup(current.pruned ? "delete" : "prune");
                        setCleanupError("");
                      }}
                    >
                      {current.pruned
                        ? "删除已清理版本记录"
                        : "清理不再需要的配置文件"}
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          )}
        </div>
      )}
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            生成配置预览
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            根据已保存的服务器和 BGP
            会话生成版本。后续资源修改不会自动改变此版本。
          </DialogDescription>
          <ErrorNotice message={error} />
          <div className="field">
            <label htmlFor="deployment-router">路由服务器</label>
            <select
              id="deployment-router"
              value={router}
              onChange={(e) => setRouter(e.target.value)}
            >
              <option value="">请选择路由服务器</option>
              {routers.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.name}
                </option>
              ))}
            </select>
            {selectedCapability && (
              <p className="text-xs leading-6 text-muted-foreground">
                {selectedCapability.label} · {selectedCapability.notes}
              </p>
            )}
            {selectedSingleFamily && !selectedFamily && (
              <p className="notice">请在路由器设置中填写一个本地 IPv4 或 IPv6 地址，另一项留空。</p>
            )}
            {selectedSingleFamily && selectedFamily && (
              <p className="text-xs leading-6 text-muted-foreground">将生成 IPv{selectedFamily} 配置。</p>
            )}
            {selectedCapability?.validationLevel === "native" &&
              selectedCheckerAvailable === false && (
                <p className="notice">
                  此平台的原生校验器未配置。可以生成预览，完成检查器 binary
                  配置后才能校验和批准。
                </p>
              )}
            {selectedRouter && selectedCapability?.rfc1997Control && (
              <p className="text-xs leading-6 text-muted-foreground">
                RFC 1997：
                {typeof selectedRouter.rfc1997Passthrough === "boolean"
                  ? "显式设置"
                  : "跟随平台默认"}{" "}
                ·{" "}
                {(
                  typeof selectedRouter.rfc1997Passthrough === "boolean"
                    ? selectedRouter.rfc1997Passthrough
                    : selectedCapability.rfc1997DefaultPassthrough
                )
                  ? "NO_EXPORT / NO_ADVERTISE 等保留给客户端处理"
                  : "由服务器执行 NO_EXPORT / NO_ADVERTISE 等限制"}
                。
                {selectedRouterRole === "route-collector" &&
                  "收集器仍不导出路由。"}
              </p>
            )}
            {selectedRouter && (
              <p className="notice">
                {selectedRouterRole === "route-collector"
                  ? `路由收集器 · ${statusLabels[selectedCollectorPolicy] ?? selectedCollectorPolicy}。始终不导出路由，不写入内核转发表。`
                  : selectedRouterRole === "as112" ? "AS112 节点只宣告选定服务的本源前缀，固定拒收 BGP 路由。DNS 与服务地址须先准备，主机返回路径另行配置。"
                  : "路由服务器按配置的导入与导出策略向成员交换路由。"}
                {!selectedRoleSupported &&
                  " 当前平台不支持该角色或收集策略，无法生成配置。请返回路由服务器记录调整。"}
              </p>
            )}
            {!routers.length && (
              <p className="text-xs text-muted-foreground">
                请先在「路由服务器」中添加记录。
              </p>
            )}
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setOpen(false)}
            >
              取消
            </Button>
            <Button
              disabled={
                !router ||
                busy ||
                !selectedCapability?.render ||
                (selectedSingleFamily && !selectedFamily) ||
                !selectedRoleSupported
              }
              onClick={() => void create()}
            >
              {busy ? "正在生成…" : "生成预览"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog open={confirm} onOpenChange={(v) => !busy && setConfirm(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            批准此配置版本？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            请确认已检查邻居、前缀白名单和会话数量。本次审批绑定当前版本校验标识，批准后可下载此版本。
            {current?.platform === "bird1" && !currentCapability?.apply && " BIRD 1 的在线应用与回退仍由目标机运维流程负责。"}
            {current?.platform === "quagga" && " Quagga 由目标机本机守护程序处理。暂存和回退都会中断 BGP 会话。"}
            {current?.routerRole === "route-collector" &&
              " 此版本是只收集、不导出的路由收集器，请确认目标设备角色一致。"}
            {currentCapability?.validationLevel === "structural" &&
              " 当前校验仅检查 XML 结构，部署工具仍须在目标设备验证并执行限时确认提交。"}
          </DialogDescription>
          <p className="break-all rounded bg-muted p-3 text-xs mono">
            {current?.checksum}
          </p>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirm(false)}
            >
              返回检查
            </Button>
            <Button disabled={busy || !reviewReady || !allowed("approve")} onClick={() => void action("approve")}>
              {busy ? "正在批准…" : "确认批准"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={cleanup !== null}
        onOpenChange={(value) => !value && !busy && setCleanup(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {cleanup === "delete"
              ? "删除已清理版本记录？"
              : "清理此版本配置文件？"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {cleanup === "delete"
              ? "此操作移除版本列表记录，审计摘要仍保留。请先备份需要留存的版本信息。"
              : "此操作永久移除私密配置文件，保留审查标识和版本元数据。此版本将无法预览、下载或用于完整差异对比；请先完成所需的安全备份。当前批准版本及服务端判定仍需保留的文件不能清理。"}
          </DialogDescription>
          <ErrorNotice message={cleanupError} />
          <form className="space-y-4" onSubmit={cleanArtifact}>
            <div className="field">
              <label htmlFor="artifact-cleanup-reason">
                操作原因{cleanup === "delete" ? "（必填）" : "（可选）"}
              </label>
              <textarea
                id="artifact-cleanup-reason"
                name="reason"
                required={cleanup === "delete"}
                disabled={busy}
                maxLength={1000}
              />
            </div>
            <label className="flex items-start gap-2 text-sm leading-6">
              <input
                className="mt-1"
                type="checkbox"
                required
                disabled={busy}
              />
              我已完成需要的备份，并理解此操作无法撤销。
            </label>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setCleanup(null)}
              >
                取消
              </Button>
              <Button variant="destructive" disabled={busy}>
                {busy
                  ? "正在处理…"
                  : cleanup === "delete"
                    ? "确认删除版本记录"
                    : "确认清理配置文件"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
