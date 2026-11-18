import { useEffect, useState, type FormEvent } from "react";
import {
  ArrowRight,
  Check,
  ChevronLeft,
  ChevronRight,
  Grid2X2,
  Handshake,
  Plus,
  RefreshCw,
  Search,
} from "lucide-react";
import { api, save } from "@/lib/api";
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
type Member = { id: string; name: string; asn?: number };
type Vlan = {
  id: string;
  name: string;
  tag: number;
  infrastructureName?: string;
  fabricName?: string;
};
type Partner = Member & { sharedVlans: Vlan[] };
type PeeringRequest = {
  id: string;
  fromMemberId: string;
  fromMemberName: string;
  toMemberId: string;
  toMemberName: string;
  vlanId: string;
  vlanName: string;
  status: "pending" | "accepted" | "rejected" | "revoked";
  message: string;
  createdAt: string;
  canDecide: boolean;
  canRevoke: boolean;
  topologyValid: boolean;
  fromAddresses?: { ipv4?: string; ipv6?: string }[];
  toAddresses?: { ipv4?: string; ipv6?: string }[];
  events: {
    id: string;
    kind: string;
    at: string;
    actorMemberId?: string;
    message?: string;
  }[];
};
type Matrix = {
  members: Member[];
  vlans: Vlan[];
  routeServer: {
    peerId: string;
    memberId: string;
    routerId: string;
    routerName?: string;
    vlanId: string;
    address: string;
    configuredEnabled: boolean;
  }[];
  bilateral: PeeringRequest[];
};
const statuses = {
  pending: "等待回应",
  accepted: "已同意对等",
  rejected: "已拒绝",
  revoked: "已撤回",
};
const eventNames: Record<string, string> = {
  proposed: "发起请求",
  accepted: "接受请求",
  rejected: "拒绝请求",
  revoked: "撤回请求",
};
const vlanLabel = (v: Vlan) =>
  `${v.name} · VLAN ${v.tag}${(v.infrastructureName ?? v.fabricName) ? ` · ${v.infrastructureName ?? v.fabricName}` : ""}`;
export default function Peering({
  role,
  memberId,
}: {
  role: string;
  memberId?: string;
}) {
  const tenant = role === "member";
  const [partners, setPartners] = useState<Partner[]>([]),
    [requests, setRequests] = useState<PeeringRequest[]>([]),
    [matrix, setMatrix] = useState<Matrix | null>(null),
    [mailEnabled, setMailEnabled] = useState(false),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [open, setOpen] = useState(false),
    [target, setTarget] = useState(""),
    [vlan, setVlan] = useState(""),
    [formError, setFormError] = useState(""),
    [detail, setDetail] = useState<PeeringRequest | null>(null),
    [decision, setDecision] = useState<{
      item: PeeringRequest;
      action: "accept" | "reject" | "revoke";
    } | null>(null),
    [vlanFilter, setVlanFilter] = useState(""),
    [search, setSearch] = useState(""),
    [rowPage, setRowPage] = useState(0),
    [colPage, setColPage] = useState(0),
    [pairFilter, setPairFilter] = useState<string[] | null>(null);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const [r, m, p] = await Promise.all([
        api<{ items: PeeringRequest[] }>("/peering/requests"),
        api<Matrix>("/peering/matrix"),
        tenant
          ? api<{ members: Partner[]; mailEnabled: boolean }>(
              "/peering/partners",
            )
          : Promise.resolve({ members: [], mailEnabled: false }),
      ]);
      setRequests(r.items);
      setMatrix(m);
      setPartners(p.members);
      setMailEnabled(p.mailEnabled);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [memberId]);
  useEffect(() => {
    setRowPage(0);
    setColPage(0);
  }, [search, vlanFilter]);
  const submit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save("/peering/requests", {
        toMemberId: target,
        vlanId: vlan,
        message: data.message,
        notify: data.notify === "on",
      });
      setOpen(false);
      setDone("对等请求已记录。请求本身不会建立 BGP 会话或修改设备配置。");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const decide = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!decision) return;
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      await save(
        `/peering/requests/${decision.item.id}/${decision.action === "revoke" ? "revoke" : "decision"}`,
        {
          ...(decision.action !== "revoke"
            ? { decision: decision.action }
            : {}),
          message: data.message,
          notify: data.notify === "on",
        },
      );
      setDecision(null);
      setDetail(null);
      setDone("对等请求状态已更新。实际 BGP 配置与运行状态需双方另行核对。");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const filteredMembers =
    matrix?.members.filter((m) =>
      `${m.name} ${m.asn ?? ""}`.toLowerCase().includes(search.toLowerCase()),
    ) ?? [];
  const rowMembers = tenant
      ? (matrix?.members.filter((m) => m.id === memberId) ?? [])
      : filteredMembers.slice(rowPage * 16, (rowPage + 1) * 16),
    colMembers = filteredMembers.slice(colPage * 16, (colPage + 1) * 16);
  useEffect(() => {
    const max = Math.max(0, Math.ceil(filteredMembers.length / 16) - 1);
    setRowPage((p) => Math.min(p, max));
    setColPage((p) => Math.min(p, max));
  }, [filteredMembers.length]);
  const visibleRequests = requests.filter(
    (r) =>
      (!vlanFilter || r.vlanId === vlanFilter) &&
      (!pairFilter ||
        (pairFilter.includes(r.fromMemberId) &&
          pairFilter.includes(r.toMemberId))),
  );
  const chooseCell = (from: string, to: string) => {
    setPairFilter([from, to]);
    document
      .getElementById("peering-requests")
      ?.scrollIntoView({ behavior: "smooth", block: "start" });
  };
  const startDecision = (
    item: PeeringRequest,
    action: "accept" | "reject" | "revoke",
  ) => {
    setDecision({ item, action });
    setFormError("");
  };
  return (
    <>
      <Heading
        title="成员对等互联"
        description="在共享交换网络中沟通双边互联意向，并查看平台已记录的配置与协议关系。"
        className="document-heading"
      >
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={loading}
            onClick={() => void load()}
          >
            <RefreshCw />
            刷新
          </Button>
          {tenant && (
            <Button
              disabled={!partners.length}
              onClick={() => {
                setOpen(true);
                setTarget("");
                setVlan("");
                setFormError("");
              }}
            >
              <Plus />
              发起对等请求
            </Button>
          )}
        </div>
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5 flex items-start gap-2">
          <Check className="mt-1 size-4 shrink-0" />
          {done}
        </p>
      )}
      <div className="notice mb-6">
        “已同意对等”表示双方在平台确认意向，不表示 BGP
        已建立。矩阵空白仅表示未提供平台关系数据；实际运行状态可在
        <a className="mx-1 underline" href="#monitoring">
          网络监测
        </a>
        中核对。
      </div>
      {loading && !matrix ? (
        <Loading />
      ) : (
        matrix && (
          <>
            <Card>
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <CardTitle className="flex items-center gap-2">
                    <Grid2X2 className="size-4 text-primary" />
                    对等关系矩阵
                  </CardTitle>
                  <div className="flex flex-wrap gap-2">
                    <select
                      className="!w-auto max-w-xs !text-xs"
                      aria-label="对等矩阵 VLAN"
                      value={vlanFilter}
                      onChange={(e) => setVlanFilter(e.target.value)}
                    >
                      <option value="">全部可见 VLAN</option>
                      {matrix.vlans.map((v) => (
                        <option key={v.id} value={v.id}>
                          {vlanLabel(v)}
                        </option>
                      ))}
                    </select>
                    <div className="relative">
                      <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
                      <Input
                        className="pl-9"
                        aria-label="筛选矩阵成员"
                        placeholder="网络名称或 ASN…"
                        value={search}
                        onChange={(e) => setSearch(e.target.value)}
                      />
                    </div>
                  </div>
                </div>
                <p className="text-xs text-muted-foreground">
                  {tenant
                    ? "只展示当前成员的关系；其他网络之间的私有关系不会显示。"
                    : "按当前可见的库存与对等请求展示，不推测平台外的连接。"}
                </p>
              </CardHeader>
              {!rowMembers.length || !colMembers.length ? (
                <Empty
                  title="当前没有可展示的矩阵记录"
                  description="可见性取决于成员与 VLAN 的互联权限，以及当前有效的共享接入。"
                />
              ) : (
                <>
                  <div className="overflow-x-auto">
                    <table>
                      <thead>
                        <tr>
                          <th>本网络 / 对方网络</th>
                          {colMembers.map((m) => (
                            <th key={m.id}>
                              <p className="max-w-36 truncate" title={m.name}>
                                {m.name}
                              </p>
                              <p className="mt-1">
                                {m.asn ? `AS${m.asn}` : "无 ASN"}
                              </p>
                            </th>
                          ))}
                        </tr>
                      </thead>
                      <tbody>
                        {rowMembers.map((from) => (
                          <tr key={from.id}>
                            <td className="font-medium">
                              {from.name}
                              <p className="mt-1 text-xs font-normal text-muted-foreground">
                                {from.asn ? `AS${from.asn}` : "无 ASN"}
                              </p>
                            </td>
                            {colMembers.map((to) => {
                              if (from.id === to.id)
                                return (
                                  <td
                                    key={to.id}
                                    className="text-muted-foreground"
                                  >
                                    本网络
                                  </td>
                                );
                              const entries = matrix.bilateral.filter(
                                (r) =>
                                  (!vlanFilter || r.vlanId === vlanFilter) &&
                                  ((r.fromMemberId === from.id &&
                                    r.toMemberId === to.id) ||
                                    (r.fromMemberId === to.id &&
                                      r.toMemberId === from.id)),
                              );
                              return (
                                <td key={to.id}>
                                  {!entries.length ? (
                                    <span className="text-xs text-muted-foreground">
                                      未提供
                                    </span>
                                  ) : (
                                    <button
                                      aria-label={`查看 ${from.name} 与 ${to.name} 的平台记录`}
                                      className="space-y-1 text-left"
                                      onClick={() => chooseCell(from.id, to.id)}
                                    >
                                      {entries.slice(0, 2).map((r) => (
                                        <div key={r.id}>
                                          <Badge
                                            variant={
                                              r.topologyValid === false
                                                ? "destructive"
                                                : r.status === "accepted"
                                                  ? "default"
                                                  : "outline"
                                            }
                                          >
                                            {r.topologyValid === false
                                              ? "拓扑待复核"
                                              : statuses[r.status]}
                                          </Badge>
                                        </div>
                                      ))}
                                      {entries.length > 2 && (
                                        <span className="text-[10px] text-muted-foreground">
                                          共 {entries.length} 条记录
                                        </span>
                                      )}
                                    </button>
                                  )}
                                </td>
                              );
                            })}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  <div className="flex flex-wrap items-center justify-between gap-3 border-t px-5 py-3 text-xs text-muted-foreground">
                    <span>当前范围 {filteredMembers.length} 个网络</span>
                    <div className="flex gap-3">
                      {!tenant && filteredMembers.length > 16 && (
                        <div className="flex items-center gap-2">
                          <span>行 {rowPage + 1}</span>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="上一组矩阵行"
                            disabled={rowPage === 0}
                            onClick={() => setRowPage((p) => p - 1)}
                          >
                            <ChevronLeft />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="下一组矩阵行"
                            disabled={
                              (rowPage + 1) * 16 >= filteredMembers.length
                            }
                            onClick={() => setRowPage((p) => p + 1)}
                          >
                            <ChevronRight />
                          </Button>
                        </div>
                      )}
                      {filteredMembers.length > 16 && (
                        <div className="flex items-center gap-2">
                          <span>列 {colPage + 1}</span>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="上一组矩阵列"
                            disabled={colPage === 0}
                            onClick={() => setColPage((p) => p - 1)}
                          >
                            <ChevronLeft />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="下一组矩阵列"
                            disabled={
                              (colPage + 1) * 16 >= filteredMembers.length
                            }
                            onClick={() => setColPage((p) => p + 1)}
                          >
                            <ChevronRight />
                          </Button>
                        </div>
                      )}
                    </div>
                  </div>
                </>
              )}
            </Card>
            <Card id="peering-requests" className="mt-6 overflow-hidden">
              <CardHeader>
                <CardTitle className="flex items-center gap-2">
                  <Handshake className="size-4 text-primary" />
                  双边对等请求
                </CardTitle>
                {pairFilter && (
                  <p className="text-xs text-muted-foreground">
                    仅查看选中的两方记录。
                    <button
                      className="ml-2 text-primary underline"
                      onClick={() => setPairFilter(null)}
                    >
                      清除筛选
                    </button>
                  </p>
                )}
              </CardHeader>
              {!visibleRequests.length ? (
                <Empty
                  title="没有可显示的对等请求"
                  description={
                    tenant
                      ? "可向当前可见的共享 VLAN 伙伴发起请求。成员发现权限与公开 IX-F 导出设置相互独立。"
                      : "成员可在自己的网络上下文发起请求，工作人员可核对记录与异常状态。"
                  }
                />
              ) : (
                <div className="overflow-x-auto">
                  <table>
                    <thead>
                      <tr>
                        <th>请求双方</th>
                        <th>交换 VLAN</th>
                        <th>状态</th>
                        <th>发起时间</th>
                        <th className="text-right">操作</th>
                      </tr>
                    </thead>
                    <tbody>
                      {visibleRequests.map((r) => (
                        <tr key={r.id}>
                          <td>
                            <div className="flex items-center gap-2">
                              <span>{r.fromMemberName}</span>
                              <ArrowRight className="size-3 text-muted-foreground" />
                              <span>{r.toMemberName}</span>
                            </div>
                          </td>
                          <td>{r.vlanName}</td>
                          <td>
                            <Badge
                              variant={
                                r.status === "accepted" ? "default" : "outline"
                              }
                            >
                              {statuses[r.status]}
                            </Badge>
                            {r.topologyValid === false && (
                              <p className="mt-1 text-xs text-destructive">
                                当前共享拓扑不再满足条件
                              </p>
                            )}
                          </td>
                          <td className="text-xs text-muted-foreground">
                            {time(r.createdAt)}
                          </td>
                          <td>
                            <div className="flex justify-end gap-1">
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`查看对等请求 ${r.id}`}
                                onClick={() => setDetail(r)}
                              >
                                详情
                              </Button>
                              {r.canDecide && (
                                <>
                                  <Button
                                    variant="secondary"
                                    size="sm"
                                    aria-label={`接受对等请求 ${r.id}`}
                                    onClick={() => startDecision(r, "accept")}
                                  >
                                    接受
                                  </Button>
                                  <Button
                                    variant="outline"
                                    size="sm"
                                    aria-label={`拒绝对等请求 ${r.id}`}
                                    onClick={() => startDecision(r, "reject")}
                                  >
                                    拒绝
                                  </Button>
                                </>
                              )}
                              {r.canRevoke && (
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  aria-label={`撤回对等请求 ${r.id}`}
                                  onClick={() => startDecision(r, "revoke")}
                                >
                                  撤回
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
            <Card className="mt-6 overflow-hidden">
              <CardHeader>
                <CardTitle>路由服务器配置关系</CardTitle>
                <p className="text-xs text-muted-foreground">
                  仅展示已登记的会话与启用配置，不将其推断为已建立的 BGP 会话。
                </p>
              </CardHeader>
              {!matrix.routeServer.length ? (
                <Empty
                  title="没有可显示的路由服务器关系"
                  description="当前范围内未提供已配置的路由服务器会话。"
                />
              ) : (
                <div className="overflow-x-auto">
                  <table>
                    <thead>
                      <tr>
                        <th>成员网络</th>
                        <th>路由服务器</th>
                        <th>VLAN</th>
                        <th>邻居地址</th>
                        <th>配置状态</th>
                      </tr>
                    </thead>
                    <tbody>
                      {matrix.routeServer
                        .filter((r) => !vlanFilter || r.vlanId === vlanFilter)
                        .map((r) => (
                          <tr key={r.peerId}>
                            <td>
                              {matrix.members.find((m) => m.id === r.memberId)
                                ?.name ?? r.memberId}
                            </td>
                            <td>{r.routerName ?? r.routerId}</td>
                            <td>
                              {matrix.vlans.find((v) => v.id === r.vlanId)
                                ?.name ?? r.vlanId}
                            </td>
                            <td className="mono text-xs">{r.address}</td>
                            <td>
                              <Badge
                                variant={
                                  r.configuredEnabled ? "default" : "outline"
                                }
                              >
                                {r.configuredEnabled ? "启用配置" : "停用配置"}
                              </Badge>
                            </td>
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
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            发起双边对等请求
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            请求从当前选择的成员网络发出，只能选择共享且允许成员互联的 VLAN。
          </DialogDescription>
          <form onSubmit={submit} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="peering-target">对方网络</label>
              <select
                id="peering-target"
                required
                value={target}
                onChange={(e) => {
                  setTarget(e.target.value);
                  const rows =
                    partners.find((p) => p.id === e.target.value)
                      ?.sharedVlans ?? [];
                  setVlan(rows.length === 1 ? rows[0].id : "");
                }}
              >
                <option value="">选择可互联伙伴</option>
                {partners.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} · AS{p.asn}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="peering-vlan">共享交换 VLAN</label>
              <select
                id="peering-vlan"
                required
                value={vlan}
                onChange={(e) => setVlan(e.target.value)}
              >
                <option value="">选择共享 VLAN</option>
                {partners
                  .find((p) => p.id === target)
                  ?.sharedVlans.map((v) => (
                    <option key={v.id} value={v.id}>
                      {vlanLabel(v)}
                    </option>
                  ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="peering-message">互联说明（可选）</label>
              <textarea id="peering-message" name="message" maxLength={4000} />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input name="notify" type="checkbox" disabled={!mailEnabled} />
              通过邮件通知对方
            </label>
            {!mailEnabled && (
              <p className="text-xs text-muted-foreground">
                邮件投递未启用，请通过其他方式提醒对方在平台查看请求。
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy || !target || !vlan}>确认发起请求</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog open={!!detail} onOpenChange={(v) => !v && setDetail(null)}>
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            {detail?.fromMemberName} 与 {detail?.toMemberName}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {detail?.vlanName} · {detail && statuses[detail.status]}
            。所示地址来自当前有效接入资料，实际会话需双方另行配置。
          </DialogDescription>
          {detail?.topologyValid === false ? (
            <p className="notice">
              当前拓扑已变化，互联地址不再提供。请检查双方有效接入与 VLAN 权限。
            </p>
          ) : (
            <div className="grid gap-4 sm:grid-cols-2">
              {[
                {
                  label: detail?.fromMemberName,
                  addresses: detail?.fromAddresses,
                },
                { label: detail?.toMemberName, addresses: detail?.toAddresses },
              ].map((side, i) => (
                <div key={i} className="rounded-lg border p-3">
                  <p className="mb-2 text-sm font-medium">{side.label}</p>
                  {side.addresses?.map((address, j) => (
                    <div key={j} className="mono break-all text-xs leading-6">
                      {address.ipv4 && <p>{address.ipv4}</p>}
                      {address.ipv6 && <p>{address.ipv6}</p>}
                    </div>
                  ))}
                </div>
              ))}
            </div>
          )}
          {detail?.message && (
            <p className="whitespace-pre-wrap rounded-lg bg-muted/30 p-3 text-sm leading-6">
              {detail.message}
            </p>
          )}
          <div className="max-h-64 divide-y overflow-auto">
            {detail?.events.map((event) => (
              <div key={event.id} className="py-3">
                <p className="text-sm font-medium">
                  {eventNames[event.kind] ?? event.kind}
                  <span className="ml-3 text-xs font-normal text-muted-foreground">
                    {time(event.at)}
                  </span>
                </p>
                {event.message && (
                  <p className="mt-2 whitespace-pre-wrap text-xs leading-6 text-muted-foreground">
                    {event.message}
                  </p>
                )}
              </div>
            ))}
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!decision}
        onOpenChange={(v) => !busy && !v && setDecision(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {decision?.action === "accept"
              ? "接受对等请求"
              : decision?.action === "reject"
                ? "拒绝对等请求"
                : "撤回对等请求"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将更新 {decision?.item.fromMemberName} 与{" "}
            {decision?.item.toMemberName} 的平台记录，不修改任何设备运行配置。
          </DialogDescription>
          <form onSubmit={decide} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="peering-decision-message">说明（可选）</label>
              <textarea
                id="peering-decision-message"
                name="message"
                maxLength={4000}
              />
            </div>
            {tenant && (
              <>
                <label className="flex items-center gap-2 text-sm">
                  <input
                    name="notify"
                    type="checkbox"
                    disabled={!mailEnabled}
                  />
                  通过邮件通知对方
                </label>
                {!mailEnabled && (
                  <p className="text-xs text-muted-foreground">
                    邮件投递未启用，本次仅更新平台记录。
                  </p>
                )}
              </>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setDecision(null)}
              >
                取消
              </Button>
              <Button
                disabled={busy}
                variant={decision?.action === "accept" ? "default" : "outline"}
              >
                确认更新对等记录
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
