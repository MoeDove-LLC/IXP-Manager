import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  Activity,
  Check,
  Download,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
} from "lucide-react";
import { api, save } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { rate } from "@/components/TrafficChart";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
type Identity = {
  name?: string;
  description?: string;
  objectId?: string;
  location?: string;
  contact?: string;
};
type Device = {
  switchId: string;
  name: string;
  status: "unknown" | "fresh" | "stale" | "error";
  lastObservedAt?: string;
  lastSuccessAt?: string;
  errorCode?: string;
  interfaceCount: number;
  identity?: Identity;
  warnings: string[];
};
type Diagnostic = {
  portId: string;
  name: string;
  ifIndex?: number;
  observedAt?: string;
  status: string;
  operStatus?: string;
  adminStatus?: string;
  speedMbps?: number;
  mtu?: number;
  physicalAddress?: string;
  inErrors?: string;
  outErrors?: string;
  inDiscards?: string;
  outDiscards?: string;
  inBps?: number | null;
  outBps?: number | null;
  warnings?: string[];
  sensors?: {
    name?: string;
    type?: number;
    value?: number | null;
    unit?: string;
    status: string;
  }[];
};
type Discovery = {
  id: string;
  switchId: string;
  observedAt: string;
  createdAt: string;
  checksum: string;
  status: "pending" | "applied" | "dismissed";
  appliedAt?: string;
  prunedAt?: string;
  size?: number;
};
type ObservationCache = {
  switchId: string;
  name: string;
  checksum: string;
  size: number;
  lastObservedAt?: string;
  deleted: boolean;
  stale: boolean;
};
type DeviceUsage = {
  discovery: {
    usedBytes: number;
    quotaBytes: number;
    fileCount: number;
    recordCount: number;
    recordLimit: number;
  };
  observations: {
    usedBytes: number;
    quotaBytes: number;
    fileCount: number;
    items: ObservationCache[];
  };
};
const storageSize = (n: number) =>
  n >= 1048576
    ? `${(n / 1048576).toFixed(2)} MiB`
    : `${(n / 1024).toFixed(1)} KiB`;
type Preview = {
  discovery: Discovery;
  baseChecksum: string;
  identity: Identity;
  ports: {
    ifIndex: number;
    name: string;
    alias?: string;
    description?: string;
    speedMbps: number;
    mtu: number;
    operStatus: string;
    existingPortId?: string;
    existingIfIndex?: number;
    identityChanged?: boolean;
    type?: number;
    physicalAddress?: string;
    engineIdentity?: string;
    previousIdentity?: {
      name?: string;
      type?: number;
      physicalAddress?: string;
      engineIdentity?: string;
    };
    conflicts: string[];
  }[];
  entities: Record<string, unknown>[];
  sensors: Record<string, unknown>[];
  fdb: Record<string, unknown>[];
  warnings: string[];
};
type Selection = {
  selected: boolean;
  portId: string;
  name: string;
  replaceMapping: boolean;
};
const statuses: Record<string, string> = {
  unknown: "尚无观测",
  fresh: "观测正常",
  stale: "观测过期",
  error: "采集失败",
  up: "启用 / 在线",
  down: "关闭 / 离线",
  testing: "测试状态",
};
const errorLabels: Record<string, string> = {
  timeout: "采集超时",
  authentication: "设备认证失败",
  transport: "采集传输失败",
  "storage-integrity": "观测缓存校验失败",
  "mapping-mismatch": "接口映射需要重新审查",
  "invalid-response": "设备响应无法解析",
};
const textValue = (value: unknown) =>
  value == null
    ? "—"
    : Array.isArray(value)
      ? value.join(", ")
      : typeof value === "object"
        ? JSON.stringify(value)
        : String(value);
function ObservationTable({
  label,
  items,
  columns,
}: {
  label: string;
  items: Record<string, unknown>[];
  columns: [string, string][];
}) {
  const [page, setPage] = useState(0);
  useEffect(() => setPage(0), [items]);
  return (
    <details className="mt-4 rounded-lg border">
      <summary className="cursor-pointer px-4 py-3 text-sm font-medium">
        {label} · 快照提供 {items.length} 条
      </summary>
      {!items.length ? (
        <p className="px-4 pb-4 text-xs text-muted-foreground">
          此快照未提供该类数据，不据此判断设备是否具备该功能。
        </p>
      ) : (
        <>
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  {columns.map(([key, title]) => (
                    <th key={key}>{title}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {items.slice(page * 50, (page + 1) * 50).map((item, index) => (
                  <tr key={index}>
                    {columns.map(([key]) => (
                      <td
                        key={key}
                        className="max-w-xs break-words whitespace-normal text-xs leading-6"
                      >
                        {textValue(item[key])}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {items.length > 50 && (
            <div className="flex items-center justify-end gap-2 border-t p-3 text-xs">
              <span>
                {page * 50 + 1}–{Math.min((page + 1) * 50, items.length)} /{" "}
                {items.length}
              </span>
              <Button
                size="sm"
                variant="ghost"
                disabled={!page}
                onClick={() => setPage((p) => p - 1)}
              >
                上一页
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={(page + 1) * 50 >= items.length}
                onClick={() => setPage((p) => p + 1)}
              >
                下一页
              </Button>
            </div>
          )}
        </>
      )}
    </details>
  );
}
export default function Devices({ role }: { role: string }) {
  const member = role === "member",
    canApply = role === "admin";
  const [devices, setDevices] = useState<Device[]>([]),
    [selected, setSelected] = useState(""),
    [diagnostics, setDiagnostics] = useState<Diagnostic[]>([]),
    [discoveries, setDiscoveries] = useState<Discovery[]>([]),
    [loading, setLoading] = useState(true),
    [portsLoading, setPortsLoading] = useState(false),
    [error, setError] = useState(""),
    [portError, setPortError] = useState(""),
    [done, setDone] = useState(""),
    [version, setVersion] = useState(0),
    [snapshotId, setSnapshotId] = useState(""),
    [preview, setPreview] = useState<Preview | null>(null),
    [inventory, setInventory] = useState<Row[]>([]),
    [previewLoading, setPreviewLoading] = useState(false),
    [previewError, setPreviewError] = useState(""),
    [selection, setSelection] = useState<Record<string, Selection>>({}),
    [search, setSearch] = useState(""),
    [page, setPage] = useState(0),
    [confirm, setConfirm] = useState(false),
    [resolution, setResolution] = useState<{
      item: Discovery;
      action: "dismiss" | "prune" | "delete";
    } | null>(null),
    [usage, setUsage] = useState<DeviceUsage | null>(null),
    [usageError, setUsageError] = useState(""),
    [clearError, setClearError] = useState(""),
    [clearing, setClearing] = useState<ObservationCache | null>(null),
    [busy, setBusy] = useState(false);
  const reviewVersion = useRef(0);
  const current = devices.find((d) => d.switchId === selected);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const data = await api<{ items: Device[] }>("/devices/status");
      setDevices(data.items);
      setSelected((id) =>
        data.items.some((d) => d.switchId === id)
          ? id
          : (data.items[0]?.switchId ?? ""),
      );
      if (!member) {
        const rows = (await api<{ items: Discovery[] }>("/devices/discoveries"))
          .items;
        setDiscoveries(rows);
        setSnapshotId((id) =>
          rows.find((d) => d.id === id)?.prunedAt ? "" : id,
        );
        setPreview((previous) => {
          if (!previous) return null;
          const metadata = rows.find((d) => d.id === previous.discovery.id);
          return !metadata || metadata.prunedAt
            ? null
            : { ...previous, discovery: metadata };
        });
        try {
          setUsage(await api<DeviceUsage>("/devices/usage"));
          setUsageError("");
        } catch (e) {
          setUsageError(message(e));
        }
      }
      setVersion((v) => v + 1);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 30000);
    return () => clearInterval(timer);
  }, [role]);
  useEffect(() => {
    setDiagnostics([]);
    setPortError("");
    if (!selected) return;
    const controller = new AbortController();
    setPortsLoading(true);
    api<{ items: Diagnostic[] }>(
      `/devices/ports?switchId=${encodeURIComponent(selected)}`,
      { signal: controller.signal },
    )
      .then((data) => setDiagnostics(data.items))
      .catch((e) => {
        if (!controller.signal.aborted) setPortError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setPortsLoading(false);
      });
    return () => controller.abort();
  }, [selected, version]);
  useEffect(() => {
    reviewVersion.current++;
    setSnapshotId("");
    setPreview(null);
    setSelection({});
    setConfirm(false);
  }, [selected]);
  useEffect(() => {
    if (!preview || preview.discovery.status !== "pending") setConfirm(false);
  }, [preview?.discovery.status]);
  useEffect(
    () => () => {
      reviewVersion.current++;
    },
    [],
  );
  const review = async (id: string) => {
    const version = ++reviewVersion.current;
    setSnapshotId(id);
    setPreview(null);
    setPreviewError("");
    setPreviewLoading(true);
    setSelection({});
    setSearch("");
    setPage(0);
    try {
      const [data, ports] = await Promise.all([
        api<Preview>(`/devices/discoveries/${id}/preview`),
        api<{ items: Row[] }>("/resources/ports"),
      ]);
      if (version !== reviewVersion.current) return;
      const normalized = {
        ...data,
        ports: (data.ports ?? []).map((p) => ({
          ...p,
          conflicts: p.conflicts ?? [],
        })),
        entities: data.entities ?? [],
        sensors: data.sensors ?? [],
        fdb: data.fdb ?? [],
        warnings: data.warnings ?? [],
      };
      setPreview(normalized);
      setInventory(
        ports.items.filter((p) => p.switchId === data.discovery.switchId),
      );
      setSelection(
        Object.fromEntries(
          (data.ports ?? []).map((p) => [
            String(p.ifIndex),
            {
              selected: false,
              portId: p.existingPortId ?? "",
              name: p.name,
              replaceMapping: false,
            },
          ]),
        ),
      );
    } catch (e) {
      if (version === reviewVersion.current) setPreviewError(message(e));
    } finally {
      if (version === reviewVersion.current) setPreviewLoading(false);
    }
  };
  const chosen =
    preview?.ports.filter((p) => selection[String(p.ifIndex)]?.selected) ?? [];
  const filtered =
    preview?.ports.filter((p) =>
      `${p.ifIndex} ${p.name} ${p.alias ?? ""} ${p.description ?? ""}`
        .toLowerCase()
        .includes(search.toLowerCase()),
    ) ?? [];
  useEffect(() => setPage(0), [search]);
  const setRow = (id: number, values: Partial<Selection>) =>
    setSelection((rows) => ({
      ...rows,
      [String(id)]: { ...rows[String(id)], ...values },
    }));
  const apply = async () => {
    if (!preview) return;
    setBusy(true);
    setPreviewError("");
    try {
      if (!chosen.length) throw new Error("请先选择需要写入库存的接口。");
      const ports = chosen.map((p) => {
        const row = selection[String(p.ifIndex)],
          existing = inventory.find((item) => item.id === row.portId),
          oldIndex = existing?.snmpIfIndex ?? p.existingIfIndex;
        if (
          row.portId &&
          ((oldIndex && Number(oldIndex) !== p.ifIndex) ||
            p.identityChanged === true) &&
          !row.replaceMapping
        )
          throw new Error(`接口 ${p.ifIndex} 需要明确确认替换原有 SNMP 映射。`);
        return {
          ifIndex: p.ifIndex,
          ...(row.portId
            ? {
                portId: row.portId,
                ...(row.replaceMapping ? { replaceMapping: true } : {}),
              }
            : { name: row.name }),
        };
      });
      await save(`/devices/discoveries/${preview.discovery.id}/apply`, {
        baseChecksum: preview.baseChecksum,
        ports,
      });
      setConfirm(false);
      setSnapshotId("");
      setPreview(null);
      setDone(
        "已更新选中的库存端口与接口映射。现有端口的名称、成员归属、速率及启停状态未被发现数据覆盖，设备运行配置没有改变。",
      );
      await load();
    } catch (e) {
      setPreviewError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const resolveSnapshot = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!resolution) return;
    setBusy(true);
    setPreviewError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      if (resolution.action === "delete")
        await save(
          `/devices/discoveries/${resolution.item.id}`,
          {
            checksum: resolution.item.checksum,
            reason: data.reason,
            acknowledge: true,
          },
          "DELETE",
        );
      else
        await save(
          `/devices/discoveries/${resolution.item.id}/${resolution.action}`,
          { reason: data.reason },
        );
      setResolution(null);
      reviewVersion.current++;
      setSnapshotId("");
      setPreview(null);
      setPreviewLoading(false);
      setDone(
        resolution.action === "dismiss"
          ? "已关闭待审查提案，库存和设备配置没有改变。"
          : resolution.action === "prune"
            ? "原始快照已清理，提案元数据、校验和与审查记录仍保留。"
            : "已删除清理后的提案列表记录，审计摘要仍保留。",
      );
      await load();
    } catch (e) {
      setPreviewError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const exportReview = (item: Discovery) => {
    const uri = URL.createObjectURL(
        new Blob([JSON.stringify(item, null, 2)], { type: "application/json" }),
      ),
      link = document.createElement("a");
    link.href = uri;
    link.download = `discovery-${item.id}-review.json`;
    link.click();
    URL.revokeObjectURL(uri);
  };
  const clearObservation = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!clearing) return;
    setBusy(true);
    setClearError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save("/devices/observations/clear", {
        switchId: clearing.switchId,
        checksum: clearing.checksum,
        reason: data.reason,
        acknowledge: true,
      });
      setClearing(null);
      setDone(
        "最新设备缓存已清理，历史遥测保持不变；采集器后续有效上报可重新生成缓存。",
      );
      await load();
    } catch (e) {
      setClearError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title={member ? "我的端口诊断" : "设备发现与诊断"}
        description={
          member
            ? "查看当前成员端口的安全观测数据。"
            : "检查采集器上报的设备与接口事实，审查后更新库存映射。"
        }
      >
        <Button
          variant="outline"
          disabled={loading}
          onClick={() => void load()}
        >
          <RefreshCw />
          刷新观测
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5 flex items-start gap-2">
          <Check className="mt-1 size-4 shrink-0" />
          {done}
        </p>
      )}
      <p className="notice mb-6">
        观测来自单独部署的只读采集器。本页面不保存设备认证凭据，不发起 SNMP
        扫描，也不修改设备运行配置。
      </p>
      {!member && (
        <details className="mb-6 rounded-xl border bg-card shadow-sm">
          <summary className="cursor-pointer p-4 text-sm font-semibold">
            存储用量与显式保留控制
          </summary>
          <div className="px-5 pb-5">
            <ErrorNotice message={usageError} />
            {usage && (
              <>
                <div className="grid gap-4 text-xs md:grid-cols-2">
                  <div className="rounded-lg border p-3">
                    <p className="font-medium">
                      发现快照：{storageSize(usage.discovery.usedBytes)} /{" "}
                      {storageSize(usage.discovery.quotaBytes)}
                    </p>
                    <p className="mt-2 text-muted-foreground">
                      原始文件 {usage.discovery.fileCount} 份 · 审查记录{" "}
                      {usage.discovery.recordCount} /{" "}
                      {usage.discovery.recordLimit}
                    </p>
                  </div>
                  <div className="rounded-lg border p-3">
                    <p className="font-medium">
                      运行观测缓存：{storageSize(usage.observations.usedBytes)}{" "}
                      / {storageSize(usage.observations.quotaBytes)}
                    </p>
                    <p className="mt-2 text-muted-foreground">
                      {usage.observations.fileCount}{" "}
                      份最新缓存；历史遥测独立保留。
                    </p>
                  </div>
                </div>
                <p className="mt-3 text-xs leading-6 text-muted-foreground">
                  待审查发现不会自动删除。先处理提案，再按需要清理原始数据；删除列表记录前请导出所需审查明细。
                </p>
                {usage.observations.items.length > 0 && (
                  <div className="mt-4 overflow-x-auto">
                    <table>
                      <thead>
                        <tr>
                          <th>缓存所属设备</th>
                          <th>大小</th>
                          <th>最近观测</th>
                          <th>状态</th>
                          {canApply && <th className="text-right">操作</th>}
                        </tr>
                      </thead>
                      <tbody>
                        {usage.observations.items.map((item) => (
                          <tr key={item.switchId}>
                            <td>{item.name || item.switchId}</td>
                            <td className="text-xs">
                              {storageSize(item.size)}
                            </td>
                            <td className="text-xs text-muted-foreground">
                              {time(item.lastObservedAt)}
                            </td>
                            <td>
                              <Badge variant="outline">
                                {item.deleted
                                  ? "设备已删除"
                                  : item.stale
                                    ? "观测过期"
                                    : "当前缓存"}
                              </Badge>
                            </td>
                            {canApply && (
                              <td className="text-right">
                                <Button
                                  variant="outline"
                                  size="sm"
                                  aria-label={`清理设备缓存 ${item.switchId}`}
                                  disabled={busy}
                                  onClick={() => {
                                    setClearing(item);
                                    setClearError("");
                                  }}
                                >
                                  清理最新缓存
                                </Button>
                              </td>
                            )}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </>
            )}
          </div>
        </details>
      )}
      {loading && !devices.length ? (
        <Loading />
      ) : !devices.length ? (
        <Card>
          <Empty
            title="还没有可见设备观测"
            description={
              member
                ? "当前成员没有可访问的设备端口数据。"
                : "登记交换机并部署带指定交换机绑定令牌的采集器后，可在这里查看状态。"
            }
          />
        </Card>
      ) : (
        <>
          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            {devices.map((d) => (
              <button
                key={d.switchId}
                data-switch-id={d.switchId}
                aria-pressed={selected === d.switchId}
                className={`rounded-xl border p-5 text-left ${selected === d.switchId ? "border-primary bg-primary/5" : "bg-card shadow-sm"}`}
                onClick={() => setSelected(d.switchId)}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="flex items-center gap-2 text-sm font-semibold">
                    <Network className="size-4 text-primary" />
                    {d.name}
                  </span>
                  <Badge
                    variant={
                      d.status === "error"
                        ? "destructive"
                        : d.status === "fresh"
                          ? "default"
                          : "outline"
                    }
                  >
                    {statuses[d.status]}
                  </Badge>
                </div>
                <p className="mt-3 text-xs text-muted-foreground">
                  {d.status === "unknown"
                    ? "等待首次运行观测"
                    : `上报接口 ${d.interfaceCount}`}
                </p>
                <p className="mt-2 text-xs text-muted-foreground">
                  最近成功：{time(d.lastSuccessAt)}
                </p>
                {d.errorCode && (
                  <p className="mt-2 text-xs text-destructive">
                    {errorLabels[d.errorCode] ?? "采集异常"}
                  </p>
                )}
              </button>
            ))}
          </div>
          <Card className="mt-6 overflow-hidden">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Activity className="size-4 text-primary" />
                {current?.name} · 接口诊断
              </CardTitle>
              <p className="text-xs text-muted-foreground">
                最后观测 {time(current?.lastObservedAt)}
                {current?.status === "error"
                  ? "；采集失败时保留最近成功数据，请结合时间判断。"
                  : ""}
              </p>
              {!member && (
                <p className="mono break-all text-[11px] text-muted-foreground">
                  采集器绑定 ID：{selected}
                </p>
              )}
              {!!current?.warnings?.length && (
                <div className="notice">{current?.warnings?.join("；")}</div>
              )}
            </CardHeader>
            <ErrorNotice message={portError} />
            {portsLoading ? (
              <Loading />
            ) : !diagnostics.length ? (
              <Empty
                title="暂无接口诊断"
                description="没有对应的已映射接口观测；未采样不会显示为零流量或离线。"
              />
            ) : (
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>库存端口 / 索引</th>
                      <th>采样状态</th>
                      <th>管理 / 运行</th>
                      <th>报告容量 / MTU</th>
                      <th>速率</th>
                      <th>端口传感器</th>
                      {!member && <th>错误 / 丢弃计数</th>}
                      <th>观测时间</th>
                    </tr>
                  </thead>
                  <tbody>
                    {diagnostics.map((d, i) => (
                      <tr key={d.portId ?? `${d.ifIndex}-${i}`}>
                        <td>
                          <p className="font-medium">{d.name}</p>
                          <p className="mt-1 text-xs text-muted-foreground">
                            ifIndex {d.ifIndex ?? "未映射"}
                          </p>
                        </td>
                        <td>
                          <Badge
                            variant={
                              d.status === "fresh" ? "default" : "outline"
                            }
                          >
                            {statuses[d.status] ?? d.status}
                          </Badge>
                        </td>
                        <td className="text-xs">
                          {d.adminStatus ?? "unknown"} /{" "}
                          {d.operStatus ?? "unknown"}
                        </td>
                        <td className="text-xs">
                          {d.speedMbps ? `${d.speedMbps} Mbps` : "容量未报告"}
                          <br />
                          MTU {d.mtu ?? "—"}
                        </td>
                        <td className="text-xs">
                          入口 {rate(d.inBps)}
                          <br />
                          出口 {rate(d.outBps)}
                        </td>
                        <td className="max-w-xs whitespace-normal text-xs leading-6">
                          {d.sensors?.length ? (
                            <details>
                              <summary className="cursor-pointer">
                                {d.sensors.length} 项观测
                              </summary>
                              {d.sensors.map((sensor, index) => (
                                <p key={index}>
                                  {sensor.name ?? `传感器 ${index + 1}`}：
                                  {sensor.value != null
                                    ? `${sensor.value} ${sensor.unit ?? ""}`
                                    : "未知"}{" "}
                                  · {sensor.status}
                                </p>
                              ))}
                            </details>
                          ) : (
                            "未提供"
                          )}
                          {!!d.warnings?.length && (
                            <p className="mt-1 text-muted-foreground">
                              {d.warnings.join("；")}
                            </p>
                          )}
                        </td>
                        {!member && (
                          <td className="mono text-xs">
                            错误 {d.inErrors ?? "—"} / {d.outErrors ?? "—"}
                            <br />
                            丢弃 {d.inDiscards ?? "—"} / {d.outDiscards ?? "—"}
                          </td>
                        )}
                        <td className="text-xs text-muted-foreground">
                          {time(d.observedAt)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      )}
      {!member && (
        <Card className="mt-6">
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <CardTitle>设备发现快照审查</CardTitle>
              <select
                className="!w-auto max-w-sm !text-xs"
                aria-label="选择设备发现快照"
                value={snapshotId}
                onChange={(e) => {
                  if (e.target.value) void review(e.target.value);
                  else {
                    setSnapshotId("");
                    setPreview(null);
                  }
                }}
              >
                <option value="">选择当前交换机的快照</option>
                {discoveries
                  .filter((d) => d.switchId === selected)
                  .map((d) => (
                    <option key={d.id} value={d.id} disabled={!!d.prunedAt}>
                      {time(d.observedAt)} ·{" "}
                      {d.status === "applied"
                        ? "已应用"
                        : d.status === "dismissed"
                          ? "已关闭"
                          : "待审查"}
                      {d.prunedAt ? "（原始已清理）" : ""} · {d.id.slice(0, 8)}
                    </option>
                  ))}
              </select>
            </div>
          </CardHeader>
          <CardContent>
            <details className="mb-5 rounded-lg border">
              <summary className="cursor-pointer p-3 text-sm font-medium">
                发现提案与保留状态
              </summary>
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      <th>提案 / 观测时间</th>
                      <th>状态</th>
                      <th>原始数据</th>
                      <th>校验和</th>
                      <th className="text-right">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {discoveries
                      .filter((d) => d.switchId === selected)
                      .map((d) => (
                        <tr key={d.id}>
                          <td className="text-xs">
                            {d.id.slice(0, 12)}
                            <p className="mt-1 text-muted-foreground">
                              {time(d.observedAt)}
                            </p>
                          </td>
                          <td>
                            <Badge variant="outline">
                              {d.status === "pending"
                                ? "待审查"
                                : d.status === "applied"
                                  ? "已应用"
                                  : "已关闭"}
                            </Badge>
                          </td>
                          <td className="text-xs">
                            {d.prunedAt
                              ? `已清理 · ${time(d.prunedAt)}`
                              : d.size
                                ? `${(d.size / 1024).toFixed(1)} KiB`
                                : "保留中"}
                          </td>
                          <td className="mono text-[10px]" title={d.checksum}>
                            {d.checksum.slice(0, 12)}…
                          </td>
                          <td>
                            <div className="flex justify-end gap-1">
                              <Button
                                size="sm"
                                variant="ghost"
                                disabled={busy || !!d.prunedAt}
                                onClick={() => void review(d.id)}
                              >
                                预览
                              </Button>
                              <Button
                                size="sm"
                                variant="ghost"
                                aria-label={`导出审查记录 ${d.id}`}
                                onClick={() => exportReview(d)}
                              >
                                <Download />
                                导出记录
                              </Button>
                              {canApply && d.status === "pending" && (
                                <Button
                                  size="sm"
                                  variant="outline"
                                  onClick={() => {
                                    setResolution({
                                      item: d,
                                      action: "dismiss",
                                    });
                                    setPreviewError("");
                                  }}
                                >
                                  保留库存并关闭
                                </Button>
                              )}
                              {canApply &&
                                d.status !== "pending" &&
                                !d.prunedAt && (
                                  <Button
                                    size="sm"
                                    variant="outline"
                                    onClick={() => {
                                      setResolution({
                                        item: d,
                                        action: "prune",
                                      });
                                      setPreviewError("");
                                    }}
                                  >
                                    清理原始快照
                                  </Button>
                                )}
                              {canApply &&
                                d.status !== "pending" &&
                                d.prunedAt && (
                                  <Button
                                    size="sm"
                                    variant="outline"
                                    aria-label={`删除提案记录 ${d.id}`}
                                    onClick={() => {
                                      setResolution({
                                        item: d,
                                        action: "delete",
                                      });
                                      setPreviewError("");
                                    }}
                                  >
                                    删除列表记录
                                  </Button>
                                )}
                            </div>
                          </td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
              {!discoveries.some((d) => d.switchId === selected) && (
                <p className="p-4 text-xs text-muted-foreground">
                  当前交换机暂无发现提案。
                </p>
              )}
            </details>
            <ErrorNotice message={previewError} />
            {previewLoading ? (
              <Loading />
            ) : !preview ? (
              <Empty
                title="选择一份发现快照"
                description="查看接口、实体、传感器与转发表，并明确选择新建端口或关联已有库存。"
              />
            ) : (
              <>
                <div className="notice mb-4 flex items-start gap-2">
                  <ShieldCheck className="mt-1 size-4 shrink-0" />
                  <span>
                    只更新所选库存及映射。已有端口的业务配置保持原样；接口索引或设备身份发生变化时需逐项确认替换，所有冲突由服务器再次核对。
                  </span>
                </div>
                <dl className="grid gap-4 rounded-lg border p-4 text-xs md:grid-cols-2">
                  {[
                    ["设备报告名称", preview.identity.name],
                    ["位置", preview.identity.location],
                    ["设备描述", preview.identity.description],
                    ["Object ID", preview.identity.objectId],
                  ].map(([label, value]) => (
                    <div key={label}>
                      <dt className="mb-1 text-muted-foreground">{label}</dt>
                      <dd className="break-words leading-6">
                        {value || "未报告"}
                      </dd>
                    </div>
                  ))}
                </dl>
                <p className="mono mt-3 break-all text-[11px] text-muted-foreground">
                  库存基线：{preview.baseChecksum}
                </p>
                {preview.warnings.length > 0 && (
                  <p className="notice mt-4">{preview.warnings.join("；")}</p>
                )}
                <div className="my-4 flex flex-wrap items-center justify-between gap-3">
                  <div className="relative w-full max-w-sm">
                    <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
                    <Input
                      className="pl-9"
                      aria-label="筛选发现接口"
                      placeholder="接口名称、别名或索引…"
                      value={search}
                      onChange={(e) => setSearch(e.target.value)}
                    />
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => void review(snapshotId)}
                    disabled={busy}
                  >
                    重新读取并审查
                  </Button>
                </div>
                <div className="overflow-x-auto rounded-lg border">
                  <table>
                    <thead>
                      <tr>
                        <th>选择 / 发现接口</th>
                        <th>观测信息</th>
                        <th>库存映射</th>
                        <th>检查事项</th>
                      </tr>
                    </thead>
                    <tbody>
                      {filtered.slice(page * 50, (page + 1) * 50).map((p) => {
                        const row = selection[String(p.ifIndex)] ?? {
                            selected: false,
                            portId: "",
                            name: p.name,
                            replaceMapping: false,
                          },
                          existing = inventory.find(
                            (item) => item.id === row.portId,
                          ),
                          oldIndex = existing?.snmpIfIndex ?? p.existingIfIndex,
                          remap =
                            !!row.portId &&
                            ((!!oldIndex && Number(oldIndex) !== p.ifIndex) ||
                              p.identityChanged === true);
                        return (
                          <tr key={p.ifIndex}>
                            <td>
                              <label className="flex items-center gap-2">
                                <input
                                  type="checkbox"
                                  aria-label={`选择发现接口 ${p.ifIndex}`}
                                  checked={row.selected}
                                  disabled={
                                    !canApply ||
                                    preview.discovery.status !== "pending"
                                  }
                                  onChange={(e) =>
                                    setRow(p.ifIndex, {
                                      selected: e.target.checked,
                                    })
                                  }
                                />
                                <span>
                                  {p.name}
                                  <span className="mt-1 block text-[11px] text-muted-foreground">
                                    ifIndex {p.ifIndex}
                                  </span>
                                </span>
                              </label>
                              {p.alias && (
                                <p className="mt-2 max-w-56 whitespace-normal text-xs text-muted-foreground">
                                  {p.alias}
                                </p>
                              )}
                            </td>
                            <td className="text-xs">
                              {p.speedMbps
                                ? `${p.speedMbps} Mbps`
                                : "容量未报告"}
                              <br />
                              MTU {p.mtu ?? "—"}
                              <br />
                              {p.operStatus}
                              {p.physicalAddress && (
                                <p className="mono mt-1 text-[10px]">
                                  {p.physicalAddress}
                                </p>
                              )}
                            </td>
                            <td>
                              <select
                                className="min-w-52 !text-xs"
                                aria-label={`接口 ${p.ifIndex} 的库存映射`}
                                value={row.portId}
                                disabled={
                                  !canApply ||
                                  preview.discovery.status !== "pending"
                                }
                                onChange={(e) =>
                                  setRow(p.ifIndex, {
                                    portId: e.target.value,
                                    replaceMapping: false,
                                  })
                                }
                              >
                                <option value="">新建未分配端口</option>
                                {inventory.map((item) => (
                                  <option key={item.id} value={item.id}>
                                    {item.name}
                                    {item.snmpIfIndex
                                      ? ` · ifIndex ${item.snmpIfIndex}`
                                      : ""}
                                  </option>
                                ))}
                              </select>
                              {!row.portId && (
                                <Input
                                  className="mt-2"
                                  aria-label={`接口 ${p.ifIndex} 的新端口名称`}
                                  value={row.name}
                                  disabled={
                                    !canApply ||
                                    preview.discovery.status !== "pending"
                                  }
                                  onChange={(e) =>
                                    setRow(p.ifIndex, { name: e.target.value })
                                  }
                                />
                              )}
                            </td>
                            <td className="max-w-xs whitespace-normal text-xs leading-6">
                              {p.conflicts.length > 0 ? (
                                <p className="text-destructive">
                                  {p.conflicts.join("；")}
                                </p>
                              ) : (
                                <span className="text-muted-foreground">
                                  未报告冲突
                                </span>
                              )}
                              {remap && (
                                <details className="mt-2 rounded border p-2 text-muted-foreground">
                                  <summary className="cursor-pointer">
                                    对比已确认的接口身份
                                  </summary>
                                  <p>
                                    名称：
                                    {textValue(
                                      existing?.snmpIfName ??
                                        p.previousIdentity?.name,
                                    )}{" "}
                                    → {p.name}
                                  </p>
                                  <p>
                                    类型：
                                    {textValue(
                                      existing?.snmpIfType ??
                                        p.previousIdentity?.type,
                                    )}{" "}
                                    → {p.type ?? "未报告"}
                                  </p>
                                  <p className="break-all">
                                    MAC：
                                    {textValue(
                                      existing?.snmpPhysicalAddress ??
                                        p.previousIdentity?.physicalAddress,
                                    )}{" "}
                                    → {p.physicalAddress || "未报告"}
                                  </p>
                                  <p className="mt-1 break-all mono text-[10px]">
                                    原设备摘要：
                                    {textValue(
                                      existing?.snmpEngineIdentity ??
                                        p.previousIdentity?.engineIdentity,
                                    )}
                                    <br />
                                    快照设备摘要：{p.engineIdentity || "未报告"}
                                  </p>
                                </details>
                              )}
                              {remap && (
                                <label className="mt-2 flex items-start gap-2 text-destructive">
                                  <input
                                    className="mt-1"
                                    type="checkbox"
                                    aria-label={`确认接口 ${p.ifIndex} 替换原映射`}
                                    checked={row.replaceMapping}
                                    disabled={
                                      !canApply ||
                                      preview.discovery.status !== "pending"
                                    }
                                    onChange={(e) =>
                                      setRow(p.ifIndex, {
                                        replaceMapping: e.target.checked,
                                      })
                                    }
                                  />
                                  <span>
                                    {oldIndex && Number(oldIndex) !== p.ifIndex
                                      ? `确认把原索引 ${String(oldIndex)} 改为 ${p.ifIndex}`
                                      : "确认采用变更后的接口身份"}
                                    {p.identityChanged &&
                                    oldIndex &&
                                    Number(oldIndex) !== p.ifIndex
                                      ? "，并采用新的接口身份"
                                      : ""}
                                  </span>
                                </label>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
                {filtered.length > 50 && (
                  <div className="mt-3 flex justify-end gap-2">
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={!page}
                      onClick={() => setPage((p) => p - 1)}
                    >
                      上一组接口
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={(page + 1) * 50 >= filtered.length}
                      onClick={() => setPage((p) => p + 1)}
                    >
                      下一组接口
                    </Button>
                  </div>
                )}
                <ObservationTable
                  label="设备实体"
                  items={preview.entities}
                  columns={[
                    ["name", "实体"],
                    ["manufacturer", "制造商"],
                    ["model", "型号"],
                    ["serial", "序列号"],
                    ["description", "描述"],
                  ]}
                />
                <ObservationTable
                  label="传感器"
                  items={preview.sensors}
                  columns={[
                    ["name", "传感器"],
                    ["value", "归一化读数"],
                    ["unit", "单位"],
                    ["rawValue", "原始读数"],
                    ["status", "状态"],
                  ]}
                />
                <ObservationTable
                  label="转发表"
                  items={preview.fdb}
                  columns={[
                    ["mac", "MAC"],
                    ["vlanIds", "报告 VLAN"],
                    ["ifIndex", "接口索引"],
                    ["bridgePort", "桥端口"],
                    ["status", "状态"],
                  ]}
                />
                {canApply && preview.discovery.status === "pending" && (
                  <div className="mt-5 flex justify-end">
                    <Button
                      disabled={busy || !chosen.length}
                      onClick={() => {
                        setConfirm(true);
                        setPreviewError("");
                      }}
                    >
                      审查并应用 {chosen.length} 个接口
                    </Button>
                  </div>
                )}
              </>
            )}
          </CardContent>
        </Card>
      )}
      <Dialog open={confirm} onOpenChange={(v) => !busy && setConfirm(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            确认库存发现变更
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将处理 {chosen.length}{" "}
            个接口。新增端口不会分配成员；已有端口只更新明确的 SNMP
            映射，成员归属与启停配置保持原样。不向设备发送配置。
          </DialogDescription>
          <ErrorNotice message={previewError} />
          <div className="max-h-60 space-y-2 overflow-auto rounded-lg border p-3 text-sm">
            {chosen.map((p) => {
              const row = selection[String(p.ifIndex)];
              return (
                <p key={p.ifIndex}>
                  ifIndex {p.ifIndex} →{" "}
                  {row.portId
                    ? (inventory.find((item) => item.id === row.portId)?.name ??
                      row.portId)
                    : `新建 ${row.name}`}
                  {row.replaceMapping ? "（明确替换映射）" : ""}
                </p>
              );
            })}
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirm(false)}
            >
              继续检查
            </Button>
            <Button disabled={busy} onClick={() => void apply()}>
              确认应用库存映射
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!resolution}
        onOpenChange={(v) => !busy && !v && setResolution(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {resolution?.action === "dismiss"
              ? "保持库存并关闭提案"
              : resolution?.action === "prune"
                ? "清理已处理提案的原始快照"
                : "删除已清理提案的列表记录"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {resolution?.action === "dismiss"
              ? "不修改任何库存或设备配置。提案、原始快照与校验和保留供后续审计。"
              : resolution?.action === "prune"
                ? "仅移除已应用或已关闭提案的原始接口、实体、传感器和转发表数据，保留元数据与校验和。待审查提案不会被清理。"
                : "仅允许删除已清理的终态提案。列表中的详细审查记录将移除，审计摘要仍保留；请先完成需要的导出或备份。"}
          </DialogDescription>
          <form onSubmit={resolveSnapshot} className="space-y-4">
            <ErrorNotice message={previewError} />
            {resolution?.action === "delete" && (
              <>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => exportReview(resolution.item)}
                >
                  <Download />
                  导出此审查记录
                </Button>
                <label className="flex items-start gap-2 text-xs leading-6">
                  <input className="mt-1" type="checkbox" required />
                  我已保存所需明细，理解删除列表记录后只保留审计摘要。
                </label>
              </>
            )}
            <div className="field">
              <label htmlFor="discovery-resolution-reason">处理原因</label>
              <textarea
                id="discovery-resolution-reason"
                name="reason"
                required
                maxLength={2000}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setResolution(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {resolution?.action === "dismiss"
                  ? "确认关闭提案"
                  : resolution?.action === "prune"
                    ? "确认清理原始快照"
                    : "确认删除列表记录"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!clearing}
        onOpenChange={(v) => !busy && !v && setClearing(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            清理最新设备观测缓存
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            仅清理 {clearing?.name || clearing?.switchId}{" "}
            的当前缓存。历史遥测与库存保持不变；采集器继续上报时会重新生成缓存。若缓存已更新，会拒绝清理旧版本。
          </DialogDescription>
          <form onSubmit={clearObservation} className="space-y-4">
            <ErrorNotice message={clearError} />
            <label className="flex items-start gap-2 text-xs leading-6">
              <input className="mt-1" type="checkbox" required />
              我理解当前诊断会暂时显示未知，历史遥测不会被删除。
            </label>
            <div className="field">
              <label htmlFor="observation-clear-reason">清理原因</label>
              <textarea
                id="observation-clear-reason"
                name="reason"
                required
                maxLength={2000}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setClearing(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>确认清理最新缓存</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
