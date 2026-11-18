import LegacyPatching from "@/components/LegacyPatching";
import PatchNotifications from "@/components/PatchNotifications";
import { useEffect, useState, type FormEvent } from "react";
import { useHashParameters } from "@/lib/hash-route";
import {
  Cable,
  ChevronRight,
  Download,
  FileCheck2,
  History,
  Link2,
  RefreshCw,
  Search,
  Unplug,
} from "lucide-react";
import { api, save, downloadFile, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
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
type Attachment = {
  legacyPatch?: boolean; readOnly?: boolean;
  id: string;
  name: string;
  visibility: string;
  checksum: string;
  version: number;
};
type Port = {
  id: string;
  name: string;
  panelId: string;
  number: number;
  status: string;
  memberId?: string;
  memberName?: string;
  circuitId?: string;
  panelName: string;
  rackName?: string;
  locationName?: string;
  switchPortId?: string;
  switchPortName?: string;
  switchName?: string;
  duplexPeerId?: string;
  allocationId?: string;
  assignedAt?: string;
  connectedAt?: string;
  ceaseRequestedAt?: string;
  ceasedAt?: string;
  revision: string;
  availableActions: string[];
  attachments: Attachment[];
};
type LOA = {
  id: string;
  portIds: string[];
  memberId: string;
  memberName: string;
  allocationId: string;
  createdAt: string;
  expiresAt: string;
  revokedAt?: string;
  status: "issued" | "expired" | "revoked" | "stale";
  checksum: string;
  htmlChecksum: string;
};
type PortHistory = {
  id: string;
  portIds: string[];
  allocationId: string;
  memberId: string;
  action: string;
  actorId: string;
  reason: string;
  effectiveAt: string;
  recordedAt: string;
  before: Row[];
  after: Row[];
};
type DocumentRef = {
  legacyPatch?: boolean; readOnly?: boolean;
  id: string;
  name: string;
  memberId?: string;
  visibility: string;
};
const states: Record<string, string> = {
  available: "空闲",
  prewired: "已预布线",
  reserved: "已预留",
  "awaiting-connect": "待连接",
  connected: "已连接",
  "awaiting-cease": "待终止",
  ceased: "已终止",
  broken: "故障",
};
const actions: Record<string, { label: string; description: string }> = {
  reserve: {
    label: "预留端口",
    description: "将空闲端口预留给成员；双芯配对端口会一起分配。",
  },
  "request-connect": {
    label: "申请连接",
    description: "记录连接申请，保留当前成员分配。",
  },
  connect: {
    label: "确认连接",
    description:
      "登记实际连接、交换机端口及电路编号。此操作会使旧的连接前 LOA 失效。",
  },
  "request-cease": {
    label: "申请终止",
    description: "记录终止申请，当前成员分配保持不变。",
  },
  cease: {
    label: "确认终止",
    description: "记录实际终止；释放之前仍保留当前分配。",
  },
  release: {
    label: "释放端口",
    description:
      "释放预布线、当前预留或已终止连接，清除成员、电路、附件与旧 LOA 的访问资格。",
  },
  prewire: {
    label: "预布线",
    description: "记录预先连接的交换机端口，暂不分配成员。",
  },
  pair: {
    label: "双芯配对",
    description:
      "同一配线架上的两个空闲端口组成双芯对，后续分配与状态变更一同处理。",
  },
  unpair: {
    label: "解除配对",
    description: "只允许对空闲或预布线端口解除双芯关系。",
  },
  "mark-broken": {
    label: "标记故障",
    description: "将空闲端口标记为故障，暂停分配。",
  },
  repair: {
    label: "恢复可用",
    description: "确认维修完成并恢复可用状态；已有交换机绑定会保留。",
  },
};
const letterStates = {
  issued: "有效授权",
  expired: "已过期",
  revoked: "已撤销",
  stale: "配置已变化",
};
export default function Patching({ role }: { role: string }) {
  const route = useHashParameters("patching");
  const targetId = route.get("id") ?? "";
  const member = role === "member",
    write = role === "admin" || role === "operator";
  const [ports, setPorts] = useState<Port[]>([]),
    [loas, setLoas] = useState<LOA[]>([]),
    [members, setMembers] = useState<Row[]>([]),
    [switchPorts, setSwitchPorts] = useState<Row[]>([]),
    [switches, setSwitches] = useState<Row[]>([]),
    [documents, setDocuments] = useState<DocumentRef[]>([]),
    [selectedId, setSelectedId] = useState(""),
    [panel, setPanel] = useState(""),
    [search, setSearch] = useState(""),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState("");
  const [history, setHistory] = useState<PortHistory[]>([]),
    [historyLoading, setHistoryLoading] = useState(false),
    [historyError, setHistoryError] = useState(""),
    [transition, setTransition] = useState<{
      port: Port;
      action: string;
    } | null>(null),
    [form, setForm] = useState<Record<string, string>>({}),
    [formError, setFormError] = useState(""),
    [editDocuments, setEditDocuments] = useState(false),
    [documentIds, setDocumentIds] = useState<string[]>([]),
    [keepPrewired, setKeepPrewired] = useState(false),
    [issue, setIssue] = useState<Port | null>(null),
    [revoke, setRevoke] = useState<LOA | null>(null);
  const selected = ports.find((p) => p.id === selectedId);
  useEffect(() => {
    if (targetId && !loading) {
      const exists = ports.some((port) => port.id === targetId);
      setSelectedId(exists ? targetId : "");
      setError(exists ? "" : "此配线记录已删除，或你已失去访问权限。");
      setPanel("");
      setSearch("");
    }
  }, [targetId, ports, loading]);
  const load = async () => {
    setLoading(true);
    try {
      const [p, l] = await Promise.all([
        api<{ items: Port[] }>("/patching/ports"),
        api<{ items: LOA[] }>("/patching/loas"),
      ]);
      setPorts(p.items);
      setLoas(l.items);
      setSelectedId((id) => (p.items.some((port) => port.id === id) ? id : ""));
      if (write) {
        const [m, sp, sw, d] = await Promise.all([
          api<{ items: Row[] }>("/resources/members"),
          api<{ items: Row[] }>("/resources/ports"),
          api<{ items: Row[] }>("/resources/switches"),
          api<{ items: DocumentRef[] }>("/documents"),
        ]);
        setMembers(m.items);
        setSwitchPorts(sp.items);
        setSwitches(sw.items);
        setDocuments(d.items);
      }
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [role]);
  useEffect(() => {
    setHistory([]);
    setHistoryError("");
    if (member || !selectedId) return;
    const controller = new AbortController();
    setHistoryLoading(true);
    api<{ items: PortHistory[] }>(
      `/patching/history?portId=${encodeURIComponent(selectedId)}`,
      { signal: controller.signal },
    )
      .then((data) => setHistory(data.items))
      .catch((e) => {
        if (!controller.signal.aborted) setHistoryError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setHistoryLoading(false);
      });
    return () => controller.abort();
  }, [selectedId, selected?.revision, member]);
  const panelNames = [
    ...new Map(ports.map((p) => [p.panelId, p.panelName])).entries(),
  ];
  const visible = ports.filter(
    (p) =>
      (!panel || p.panelId === panel) &&
      (!search ||
        `${p.name} ${p.number} ${p.panelName} ${p.memberName ?? ""} ${p.circuitId ?? ""}`
          .toLowerCase()
          .includes(search.toLowerCase())),
  );
  const letters = loas.filter(
    (l) => !selectedId || l.portIds.includes(selectedId),
  );
  const start = (port: Port, action: string) => {
    setTransition({ port, action });
    setForm({
      memberId: port.memberId ?? "",
      duplexPeerId: port.duplexPeerId ?? "",
      switchPortId: port.switchPortId ?? "",
      circuitId: port.circuitId ?? "",
      reason: "",
      effectiveAt: "",
    });
    setDocumentIds((port.attachments ?? []).filter((d) => !d.legacyPatch && !d.readOnly).map((d) => d.id));
    setEditDocuments(false);
    setKeepPrewired(false);
    setFormError("");
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!transition) return;
    setBusy(true);
    setFormError("");
    try {
      const payload: Record<string, unknown> = {
        action: transition.action,
        revision: transition.port.revision,
        reason: form.reason,
      };
      if (form.effectiveAt)
        payload.effectiveAt = new Date(form.effectiveAt).toISOString();
      if (transition.action === "reserve") payload.memberId = form.memberId;
      if (
        ["pair", "reserve", "prewire"].includes(transition.action) &&
        form.duplexPeerId &&
        form.duplexPeerId !== transition.port.duplexPeerId
      ) {
        payload.duplexPeerId = form.duplexPeerId;
        payload.duplexPeerRevision = form.duplexPeerRevision;
      }
      if (
        ["reserve", "prewire", "connect"].includes(transition.action) &&
        form.switchPortId
      )
        payload.switchPortId = form.switchPortId;
      if (["reserve", "connect"].includes(transition.action) && form.circuitId)
        payload.circuitId = form.circuitId;
      if (editDocuments) payload.documentIds = documentIds;
      if (transition.action === "release") payload.keepPrewired = keepPrewired;
      await save(`/patching/ports/${transition.port.id}/transition`, payload);
      setTransition(null);
      setDone("配线状态已更新，相关双芯端口与授权文件状态已同步检查。");
      await load();
    } catch (e) {
      setFormError(
        e instanceof APIError &&
          e.status === 409 &&
          /revision|changed|stale/i.test(e.message)
          ? "端口状态已变化，本次操作未提交。请关闭此窗口并刷新，再审查最新状态。"
          : message(e),
      );
    } finally {
      setBusy(false);
    }
  };
  const createLOA = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!issue) return;
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save(`/patching/ports/${issue.id}/loas`, {
        revision: issue.revision,
        expiresAt: new Date(String(data.expiresAt)).toISOString(),
        reason: data.reason,
      });
      setIssue(null);
      setDone(
        "LOA 已生成。文件固定保存生成时的配置，后续端口状态变化会使其失效。",
      );
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const revokeLOA = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!revoke) return;
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save(`/patching/loas/${revoke.id}/revoke`, { reason: data.reason });
      setRevoke(null);
      setDone("授权已撤销。");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const download = async (letter: LOA, format: "pdf" | "html") => {
    setError("");
    try {
      await downloadFile(
        `/patching/loas/${letter.id}/download?format=${format}`,
        `MoeDove-LOA-${letter.id}.${format}`,
      );
    } catch (e) {
      setError(message(e));
      await load();
    }
  };
  const pairs = transition
    ? ports.filter(
        (p) =>
          p.id !== transition.port.id &&
          p.panelId === transition.port.panelId &&
          ["available", "prewired"].includes(p.status) &&
          (!p.duplexPeerId || p.duplexPeerId === transition.port.id),
      )
    : [];
  const eligibleDocs = documents.filter(
    (d) =>
      !d.legacyPatch && !d.readOnly && ["staff", "member"].includes(d.visibility) &&
      (!d.memberId ||
        d.memberId === (form.memberId || transition?.port.memberId)),
  );
  if (!member && route.get("view") === "legacy") return <LegacyPatching role={role} />;
  return (
    <>
      <Heading
        className="flex-wrap"
        title={member ? "我的配线与授权" : "配线工作台"}
        description={
          member
            ? "查看当前分配给你的端口与仍然有效的授权文件。"
            : "管理预留、接通、终止与释放；双芯关系和 LOA 有效性随配置同步检查。"
        }
      >
        {!member && <Button variant="outline" onClick={() => { location.hash = "patching?view=legacy"; }}><History />遗留归档与待核对</Button>}
        <Button
          variant="outline"
          disabled={loading}
          onClick={() => {
            setError("");
            void load();
          }}
        >
          <RefreshCw />
          刷新
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5">
          {done}
        </p>
      )}
      {write && (
        <div className="mb-5 flex flex-wrap gap-4 text-xs text-primary">
          <a href="#patch-panels" className="underline">
            登记配线架
          </a>
          <a href="#patch-ports" className="underline">
            维护端口库存
          </a>
          <a href="#documents" className="underline">
            管理关联资料
          </a>
        </div>
      )}
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b p-4">
          <select
            className="!w-auto max-w-xs"
            aria-label="选择配线架"
            value={panel}
            onChange={(e) => setPanel(e.target.value)}
          >
            <option value="">全部可访问配线架</option>
            {panelNames.map(([id, name]) => (
              <option key={id} value={id}>
                {name}
              </option>
            ))}
          </select>
          <div className="relative w-full max-w-sm">
            <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
            <Input
              aria-label="搜索配线端口"
              className="pl-9"
              placeholder="端口、成员或电路编号…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
        </div>
        {loading && !ports.length ? (
          <Loading />
        ) : !visible.length ? (
          <Empty
            title="没有匹配的配线端口"
            description={
              member
                ? "只有当前分配给你的端口会在这里展示。"
                : "先登记配线架和物理端口，再通过此工作台进行状态操作。"
            }
          />
        ) : (
          <div className="grid gap-3 p-5 sm:grid-cols-2 xl:grid-cols-4">
            {visible.map((p) => (
              <button
                key={p.id}
                aria-label={`查看配线端口 ${p.name}`}
                onClick={() => {
                  setSelectedId(p.id);
                  setDone("");
                }}
                className={`rounded-lg border p-4 text-left transition hover:border-primary/50 ${selectedId === p.id ? "border-primary bg-primary/5" : "bg-background"}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="flex items-center gap-2 text-sm font-semibold">
                    <Cable className="size-4 text-primary" />
                    {p.name}
                  </span>
                  <Badge
                    variant={
                      p.status === "broken"
                        ? "destructive"
                        : p.status === "available"
                          ? "outline"
                          : "default"
                    }
                  >
                    {states[p.status] ?? p.status}
                  </Badge>
                </div>
                <p className="mt-2 text-xs text-muted-foreground">
                  {p.panelName} · 端口 {p.number}
                </p>
                <p className="mt-2 truncate text-xs">
                  {p.memberName || "未分配成员"}
                </p>
                {p.duplexPeerId && (
                  <p className="mt-2 flex items-center gap-1 text-[11px] text-muted-foreground">
                    <Link2 className="size-3" />
                    双芯配对：
                    {ports.find((v) => v.id === p.duplexPeerId)?.name ??
                      "关联端口"}
                  </p>
                )}
              </button>
            ))}
          </div>
        )}
      </Card>
      {selected && (
        <Card className="mt-6">
          <CardHeader>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <CardTitle>{selected.name} · 配线详情</CardTitle>
              <Badge variant="outline">
                {states[selected.status] ?? selected.status}
              </Badge>
            </div>
            <p className="text-xs leading-6 text-muted-foreground">
              {[selected.locationName, selected.rackName, selected.panelName]
                .filter(Boolean)
                .join(" / ")}
            </p>
          </CardHeader>
          <CardContent>
            <dl className="grid gap-5 text-sm sm:grid-cols-2 xl:grid-cols-4">
              {[
                ["当前成员", selected.memberName || "未分配"],
                ["电路编号", selected.circuitId || "—"],
                [
                  "交换机端口",
                  [selected.switchName, selected.switchPortName]
                    .filter(Boolean)
                    .join(" / ") || "未绑定",
                ],
                ["分配时间", time(selected.assignedAt)],
                ["接通时间", time(selected.connectedAt)],
                ["终止申请", time(selected.ceaseRequestedAt)],
                ["终止时间", time(selected.ceasedAt)],
                [
                  "双芯端口",
                  ports.find((p) => p.id === selected.duplexPeerId)?.name ??
                    "未配对",
                ],
              ].map(([label, value]) => (
                <div key={label}>
                  <dt className="mb-2 text-xs text-muted-foreground">
                    {label}
                  </dt>
                  <dd className="break-words">{value}</dd>
                </div>
              ))}
            </dl>
            {selected.attachments?.length > 0 && (
              <div className="mt-5 border-t pt-4">
                <h3 className="mb-3 text-sm font-medium">关联资料</h3>
                {selected.attachments.some((item) => item.legacyPatch) && <p className="mb-3 text-xs leading-6 text-muted-foreground">遗留附件保持只读，不属于可编辑附件列表；旧 PDF 不能替代下方新签发的有效 LOA。</p>}
                <div className="flex flex-wrap gap-2">
                  {selected.attachments.map((d) => (
                    <Button
                      key={d.id}
                      className="h-auto max-w-full whitespace-normal break-all py-2"
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        void downloadFile(
                          `/documents/${d.id}/download`,
                          d.name,
                        ).catch((e) => setError(message(e)))
                      }
                    >
                      <Download />
                      {d.name} · {d.legacyPatch ? "遗留只读附件" : `v${d.version}`}
                    </Button>
                  ))}
                </div>
              </div>
            )}
            {[...letters].reverse().find((l) => l.status === "issued") && (
              <div className="mt-5">
                <Button
                  onClick={() =>
                    void download(
                      [...letters]
                        .reverse()
                        .find((l) => l.status === "issued")!,
                      "pdf",
                    )
                  }
                >
                  <Download />
                  下载最新有效 LOA
                </Button>
              </div>
            )}
            {write && (
              <div className="mt-6 flex flex-wrap gap-2 border-t pt-5">
                {selected.availableActions.map(
                  (action) =>
                    actions[action] && (
                      <Button
                        key={action}
                        variant={
                          ["release", "mark-broken"].includes(action)
                            ? "outline"
                            : "secondary"
                        }
                        onClick={() => start(selected, action)}
                      >
                        {actions[action].label}
                      </Button>
                    ),
                )}
                <Button
                  variant="outline"
                  disabled={
                    !["reserved", "awaiting-connect"].includes(selected.status)
                  }
                  title={
                    !["reserved", "awaiting-connect"].includes(selected.status)
                      ? "仅预留或待连接端口可签发 LOA"
                      : undefined
                  }
                  onClick={() => {
                    setIssue(selected);
                    setFormError("");
                  }}
                >
                  <FileCheck2 />
                  签发 LOA
                </Button>
                {role === "admin" && <PatchNotifications key={selected.id} portId={selected.id} portName={selected.name} revision={selected.revision} allocated={!!selected.memberId} />}
              </div>
            )}
            {member && (
              <p className="notice mt-5">
                此视图只展示当前分配；历史成员资料和失效 LOA 不会提供下载。
              </p>
            )}
          </CardContent>
        </Card>
      )}
      <Card className="mt-6 overflow-hidden">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <FileCheck2 className="size-4 text-primary" />
            {selected ? `${selected.name} 的授权文件` : "授权文件"}
          </CardTitle>
          <p className="text-xs leading-6 text-muted-foreground">
            授权文件是生成时的固定版本；连接、终止、释放或配置变化后，旧 LOA
            不再允许下载。
          </p>
        </CardHeader>
        {!letters.length ? (
          <Empty
            title="没有可显示的 LOA"
            description={
              member
                ? "当前有效授权文件签发后将在这里提供。"
                : "选择预留或待连接端口可签发带有效期的 LOA。"
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>授权成员 / 端口</th>
                  <th>状态</th>
                  <th>签发时间</th>
                  <th>有效期</th>
                  <th className="text-right">操作</th>
                </tr>
              </thead>
              <tbody>
                {letters.map((l) => (
                  <tr key={l.id}>
                    <td>
                      <p className="font-medium">{l.memberName}</p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {l.portIds
                          .map(
                            (id) => ports.find((p) => p.id === id)?.name ?? id,
                          )
                          .join(" / ")}
                      </p>
                    </td>
                    <td>
                      <Badge
                        variant={
                          l.status === "issued"
                            ? "default"
                            : l.status === "stale"
                              ? "destructive"
                              : "outline"
                        }
                      >
                        {letterStates[l.status]}
                      </Badge>
                      {l.status === "stale" && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          需按当前配置重新签发
                        </p>
                      )}
                    </td>
                    <td className="text-xs text-muted-foreground">
                      {time(l.createdAt)}
                    </td>
                    <td className="text-xs text-muted-foreground">
                      {time(l.expiresAt)}
                    </td>
                    <td>
                      <div className="flex justify-end gap-2">
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={l.status !== "issued"}
                          onClick={() => void download(l, "pdf")}
                        >
                          <Download />
                          PDF
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={l.status !== "issued"}
                          onClick={() => void download(l, "html")}
                        >
                          HTML
                        </Button>
                        {write && l.status === "issued" && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              setRevoke(l);
                              setFormError("");
                            }}
                          >
                            撤销
                          </Button>
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      {selected && !member && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <History className="size-4 text-primary" />
              不可变配线历史
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ErrorNotice message={historyError} />
            {historyLoading ? (
              <Loading />
            ) : !history.length ? (
              <Empty description="尚无状态变更记录。" />
            ) : (
              <div className="divide-y">
                {[...history].reverse().map((h) => (
                  <div key={h.id} className="py-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <h3 className="text-sm font-medium">
                        {actions[h.action]?.label ??
                          (
                            {
                              "inventory-post": "登记库存",
                              "inventory-put": "更新库存资料",
                              "inventory-delete": "删除库存",
                            } as Record<string, string>
                          )[h.action] ??
                          h.action}
                      </h3>
                      <span className="text-xs text-muted-foreground">
                        生效 {time(h.effectiveAt)} · 记录 {time(h.recordedAt)}
                      </span>
                    </div>
                    <p className="mt-2 text-sm leading-6">
                      {h.reason === "Inventory metadata changed"
                        ? "库存资料已更新"
                        : h.reason}
                    </p>
                    <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
                      {h.after.map((after) => {
                        const before = h.before.find((b) => b.id === after.id);
                        return (
                          <span
                            key={after.id}
                            className="flex items-center gap-1"
                          >
                            {String(
                              after.name ??
                                ports.find((p) => p.id === after.id)?.name ??
                                after.id,
                            )}
                            ：
                            {states[String(before?.status)] ??
                              String(before?.status ?? "—")}
                            <ChevronRight className="size-3" />
                            {states[String(after.status)] ??
                              String(after.status ?? "—")}
                          </span>
                        );
                      })}
                    </div>
                    <p className="mt-2 text-[11px] text-muted-foreground">
                      操作者 ID：{h.actorId}
                    </p>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
      <Dialog
        open={!!transition}
        onOpenChange={(v) => !busy && !v && setTransition(null)}
      >
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            {transition ? actions[transition.action]?.label : "配线操作"}：
            {transition?.port.name}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {transition && actions[transition.action]?.description}
            提交时会核对当前版本，避免覆盖其他人的变更。
          </DialogDescription>
          <form onSubmit={submit} className="space-y-4">
            <ErrorNotice message={formError} />
            {transition?.port.duplexPeerId && (
              <p className="notice">
                此端口已组成双芯对；状态变更会作用于配对端口。
              </p>
            )}
            {transition?.action === "reserve" && (
              <div className="field">
                <label htmlFor="patch-member">预留成员</label>
                <select
                  id="patch-member"
                  required
                  value={form.memberId}
                  onChange={(e) => {
                    setForm({ ...form, memberId: e.target.value });
                    setDocumentIds((ids) =>
                      ids.filter((id) => {
                        const d = documents.find((v) => v.id === id);
                        return (
                          d && (!d.memberId || d.memberId === e.target.value)
                        );
                      }),
                    );
                  }}
                >
                  <option value="">选择成员</option>
                  {members.map((m) => (
                    <option
                      key={m.id}
                      value={m.id}
                      disabled={["suspended", "closed"].includes(
                        String(m.status),
                      )}
                    >
                      {m.name}
                      {["suspended", "closed"].includes(String(m.status))
                        ? "（不可分配）"
                        : ""}
                    </option>
                  ))}
                </select>
              </div>
            )}
            {transition &&
              ["pair", "reserve", "prewire"].includes(transition.action) && (
                <div className="field">
                  <label htmlFor="patch-pair">
                    双芯配对端口
                    {transition.action === "pair" ? " *" : "（可选）"}
                  </label>
                  <select
                    id="patch-pair"
                    required={transition.action === "pair"}
                    value={form.duplexPeerId}
                    disabled={!!transition.port.duplexPeerId}
                    onChange={(e) =>
                      setForm({
                        ...form,
                        duplexPeerId: e.target.value,
                        duplexPeerRevision:
                          pairs.find((p) => p.id === e.target.value)
                            ?.revision ?? "",
                      })
                    }
                  >
                    <option value="">不新增配对</option>
                    {pairs.map((p) => (
                      <option key={p.id} value={p.id}>
                        {p.name} · {states[p.status]}
                      </option>
                    ))}
                  </select>
                  <p className="text-xs text-muted-foreground">
                    仅列出同一配线架中空闲或预布线、未被其他端口占用的候选端口。
                  </p>
                </div>
              )}
            {transition &&
              ["reserve", "prewire", "connect"].includes(transition.action) && (
                <div className="field">
                  <label htmlFor="patch-switch">
                    交换机物理端口
                    {["prewire", "connect"].includes(transition.action)
                      ? " *"
                      : "（可选）"}
                  </label>
                  <select
                    id="patch-switch"
                    required={["prewire", "connect"].includes(
                      transition.action,
                    )}
                    value={form.switchPortId}
                    onChange={(e) =>
                      setForm({ ...form, switchPortId: e.target.value })
                    }
                  >
                    <option value="">未绑定</option>
                    {switchPorts.map((p) => (
                      <option key={p.id} value={p.id}>
                        {String(
                          switches.find((s) => s.id === p.switchId)?.name ??
                            p.switchId ??
                            "交换机",
                        )}{" "}
                        / {p.name}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            {transition &&
              ["reserve", "connect"].includes(transition.action) && (
                <div className="field">
                  <label htmlFor="patch-circuit">
                    电路编号
                    {transition.action === "connect" ? " *" : "（可选）"}
                  </label>
                  <Input
                    id="patch-circuit"
                    required={transition.action === "connect"}
                    value={form.circuitId}
                    onChange={(e) =>
                      setForm({ ...form, circuitId: e.target.value })
                    }
                  />
                </div>
              )}
            {transition?.action === "release" ? (
              <label className="notice flex items-start gap-2">
                <input
                  type="checkbox"
                  checked={keepPrewired}
                  onChange={(e) => setKeepPrewired(e.target.checked)}
                  className="mt-1"
                />
                保留交换机绑定与双芯关系，释放后标记为预布线；否则一并清除。
              </label>
            ) : (
              <>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    type="checkbox"
                    checked={editDocuments}
                    onChange={(e) => setEditDocuments(e.target.checked)}
                  />
                  更新关联资料
                </label>
                {editDocuments && (
                  <fieldset className="max-h-44 space-y-2 overflow-y-auto rounded-lg border p-3">
                    <legend className="px-1 text-xs text-muted-foreground">
                      完整关联集合（全不选将清空）
                    </legend>
                    {eligibleDocs.map((d) => (
                      <label
                        key={d.id}
                        className="flex items-center gap-2 text-sm"
                      >
                        <input
                          type="checkbox"
                          value={d.id}
                          checked={documentIds.includes(d.id)}
                          onChange={(e) =>
                            setDocumentIds(
                              e.target.checked
                                ? [...documentIds, d.id]
                                : documentIds.filter((id) => id !== d.id),
                            )
                          }
                        />
                        {d.name}
                      </label>
                    ))}
                    {!eligibleDocs.length && (
                      <p className="text-xs text-muted-foreground">
                        没有可关联资料，请先在文档库上传。
                      </p>
                    )}
                  </fieldset>
                )}
              </>
            )}
            <div className="field">
              <label htmlFor="patch-effective">实际生效时间（可选）</label>
              <Input
                id="patch-effective"
                type="datetime-local"
                value={form.effectiveAt}
                onChange={(e) =>
                  setForm({ ...form, effectiveAt: e.target.value })
                }
              />
              <p className="text-xs text-muted-foreground">
                按浏览器本地时区输入；留空使用服务器当前时间。
              </p>
            </div>
            <div className="field">
              <label htmlFor="patch-reason">操作原因 / 工单说明 *</label>
              <textarea
                id="patch-reason"
                required
                value={form.reason}
                onChange={(e) => setForm({ ...form, reason: e.target.value })}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setTransition(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在提交…" : "确认配线操作"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!issue}
        onOpenChange={(v) => !busy && !v && setIssue(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            签发 LOA：{issue?.name}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            仅用于当前预留或待连接分配，有效期最长 30 天。PDF 和 HTML
            固定记录生成时的配置与成员。
          </DialogDescription>
          <form onSubmit={createLOA} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="loa-expires">授权截止时间</label>
              <Input
                id="loa-expires"
                name="expiresAt"
                type="datetime-local"
                required
              />
            </div>
            <div className="field">
              <label htmlFor="loa-reason">签发原因</label>
              <textarea id="loa-reason" name="reason" required />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setIssue(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在生成…" : "确认签发 LOA"}
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
          <DialogTitle className="text-lg font-semibold">撤销授权</DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            撤销后将停止提供此授权文件的下载，历史记录仍保留。
          </DialogDescription>
          <form onSubmit={revokeLOA} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="loa-revoke-reason">撤销原因</label>
              <textarea id="loa-revoke-reason" name="reason" required />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setRevoke(null)}
              >
                取消
              </Button>
              <Button variant="destructive" disabled={busy}>
                确认撤销授权
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
