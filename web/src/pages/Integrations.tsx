import { useEffect, useState, type FormEvent } from "react";
import {
  Check,
  CloudDownload,
  FileJson,
  RefreshCw,
  ShieldCheck,
  Download,
} from "lucide-react";
import { api, save } from "@/lib/api";
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
type Provider = {
  id: string;
  label: string;
  enabled: boolean;
  readOnly: boolean;
};
type Proposal = {
  id: string;
  provider: string;
  memberId: string;
  memberName: string;
  connectionId?: string;
  asn: number;
  status: "pending" | "applied" | "dismissed";
  createdAt: string;
  checksum: string;
  snapshotChecksum?: string;
  fetchedAt?: string;
  baseChecksum: string;
  snapshot?: { fetchedAt?: string };
  sourceNote?: string;
  providerOrigin?: string;
  snapshotPruned?: boolean;
  snapshotUnavailable?: boolean;
  diff: {
    field: string;
    before: unknown;
    after: unknown;
    applyAllowed: boolean;
    reason?: string;
  }[];
};
const labels: Record<string, string> = {
  "member.name": "成员名称",
  "member.website": "成员网站",
  "member.peeringdbId": "PeeringDB 网络 ID",
  "member.irrASSet": "IRR AS-SET",
  "member.asn": "成员 ASN",
  "connection.peeringdbNetixlanId": "PeeringDB 接入 ID",
  "connection.ipv4": "接入 IPv4",
  "connection.ipv6": "接入 IPv6",
  "connection.speed": "接入速率",
};
const value = (v: unknown) =>
  v === null || v === undefined || v === ""
    ? "未设置"
    : typeof v === "boolean"
      ? v
        ? "是"
        : "否"
      : typeof v === "object"
        ? JSON.stringify(v)
        : String(v);
export default function Integrations({ role }: { role: string }) {
  const canPropose = role === "admin" || role === "operator",
    canApply = role === "admin";
  const [providers, setProviders] = useState<Provider[]>([]),
    [proposals, setProposals] = useState<Proposal[]>([]),
    [members, setMembers] = useState<Row[]>([]),
    [connections, setConnections] = useState<Row[]>([]),
    [selected, setSelected] = useState(""),
    [fields, setFields] = useState<string[]>([]),
    [detail, setDetail] = useState<Proposal | null>(null),
    [hasMore, setHasMore] = useState(false),
    [offset, setOffset] = useState(0),
    [detailLoading, setDetailLoading] = useState(false),
    [reviewVersion, setReviewVersion] = useState(0),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState(""),
    [open, setOpen] = useState<"fetch" | "import" | null>(null),
    [memberId, setMemberId] = useState(""),
    [connectionId, setConnectionId] = useState(""),
    [documentText, setDocumentText] = useState(""),
    [formError, setFormError] = useState(""),
    [confirm, setConfirm] = useState(false),
    [dismissOpen, setDismissOpen] = useState(false),
    [dismissReason, setDismissReason] = useState(""),
    [cleanup, setCleanup] = useState<"prune" | "delete" | null>(null);
  const metadata = proposals.find((p) => p.id === selected);
  const current =
      metadata && detail?.id === selected
        ? {
            ...detail,
            ...metadata,
            snapshot:
              metadata.snapshotPruned || metadata.snapshotUnavailable
                ? undefined
                : detail.snapshot,
          }
        : metadata,
    provider = providers.find((p) => p.id === "peeringdb");
  const load = async () => {
    setLoading(true);
    try {
      const [p, rows, m, c] = await Promise.all([
        api<{ providers: Provider[] }>("/integrations"),
        api<{ items: Proposal[]; hasMore: boolean }>(
          "/integrations/proposals?offset=0&limit=100",
        ),
        api<{ items: Row[] }>("/resources/members"),
        api<{ items: Row[] }>("/resources/connections"),
      ]);
      setProviders(p.providers);
      setProposals(rows.items);
      setOffset(rows.items.length);
      setHasMore(rows.hasMore);
      setReviewVersion((v) => v + 1);
      setMembers(m.items);
      setConnections(c.items);
      setSelected((id) =>
        rows.items.some((r) => r.id === id) ? id : (rows.items[0]?.id ?? ""),
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    setFields([]);
    setDetail(null);
    if (!selected) return;
    setDetailLoading(true);
    const controller = new AbortController();
    api<Proposal>(`/integrations/proposals/${selected}`, {
      signal: controller.signal,
    })
      .then((data) => {
        if (!controller.signal.aborted) setDetail(data);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [selected, reviewVersion]);
  const loadMore = async () => {
    setLoading(true);
    try {
      const data = await api<{ items: Proposal[]; hasMore: boolean }>(
        `/integrations/proposals?offset=${offset}&limit=100`,
      );
      setProposals((rows) => [
        ...rows,
        ...data.items.filter((item) => !rows.some((old) => old.id === item.id)),
      ]);
      setOffset(offset + data.items.length);
      setHasMore(data.hasMore);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  const start = (mode: "fetch" | "import") => {
    setOpen(mode);
    setFormError("");
    setMemberId("");
    setConnectionId("");
    setDocumentText("");
  };
  const propose = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      let result: Proposal;
      if (open === "import") {
        if (!crypto.subtle)
          throw new Error(
            "导入校验需要 HTTPS 或 localhost 访问，请使用安全连接。",
          );
        const bytes = new TextEncoder().encode(documentText),
          digest = await crypto.subtle.digest("SHA-256", bytes),
          checksum = Array.from(new Uint8Array(digest), (b) =>
            b.toString(16).padStart(2, "0"),
          ).join("");
        result = await save<Proposal>("/integrations/proposals/import", {
          memberId,
          ...(connectionId ? { connectionId } : {}),
          document: documentText,
          checksum,
        });
      } else {
        result = await save<Proposal>("/integrations/peeringdb/propose", {
          memberId,
          ...(connectionId ? { connectionId } : {}),
          ...(data.ixId ? { ixId: Number(data.ixId) } : {}),
          ...(data.facilityId ? { facilityId: Number(data.facilityId) } : {}),
        });
      }
      setOpen(null);
      await load();
      setSelected(result.id);
      setDone("提案已保存，尚未修改任何成员或接入资料。");
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const apply = async () => {
    if (!current) return;
    setBusy(true);
    setFormError("");
    try {
      const result = await save<Proposal>(
        `/integrations/proposals/${current.id}/apply`,
        {
          checksum: current.checksum,
          baseChecksum: current.baseChecksum,
          fields,
        },
      );
      setDetail(result);
      const { snapshot, ...metadata } = result;
      setProposals((rows) =>
        rows.map((p) => (p.id === result.id ? metadata : p)),
      );
      setMembers((await api<{ items: Row[] }>("/resources/members")).items);
      setConfirm(false);
      setDone(
        "已更新明确选中的资料字段。路由、地址分配与设备运行配置保持原样。",
      );
      setFields([]);
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const dismiss = async () => {
    if (!current) return;
    setBusy(true);
    setFormError("");
    try {
      const result = await save<Proposal>(
        `/integrations/proposals/${current.id}/dismiss`,
        { checksum: current.checksum, reason: dismissReason },
      );
      setDetail({ ...current, ...result });
      setProposals((rows) =>
        rows.map((p) => (p.id === result.id ? result : p)),
      );
      setDismissOpen(false);
      setDone("已保留本地资料并关闭提案，原始审查记录仍保留。");
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const exportProposal = (snapshotOnly: boolean) => {
    if (!current) return;
    const data = snapshotOnly ? current.snapshot : current;
    if (!data) return;
    const uri = URL.createObjectURL(
        new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }),
      ),
      link = document.createElement("a");
    link.href = uri;
    link.download = `peeringdb-${snapshotOnly ? "snapshot" : "review"}-${current.id}.json`;
    link.click();
    URL.revokeObjectURL(uri);
  };
  const cleanupProposal = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!current || !cleanup) return;
    setBusy(true);
    setFormError("");
    const data = Object.fromEntries(new FormData(e.currentTarget));
    try {
      if (cleanup === "prune") {
        await save(`/integrations/proposals/${current.id}/prune`, {
          checksum: current.checksum,
          acknowledge: true,
        });
        const result = await api<Proposal>(
          `/integrations/proposals/${current.id}`,
        );
        setDetail({ ...result, snapshot: undefined });
        setProposals((rows) =>
          rows.map((p) => (p.id === result.id ? result : p)),
        );
        setDone("已清理此终态提案的快照数据，差异与校验和继续保留。");
      } else {
        await save(
          `/integrations/proposals/${current.id}`,
          {
            checksum: current.checksum,
            reason: data.reason,
            acknowledge: true,
          },
          "DELETE",
        );
        setDetail(null);
        setSelected("");
        setDone("已删除清理后的提案列表记录，审计摘要仍保留。");
        await load();
      }
      setCleanup(null);
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="外部资料核对"
        description="从 PeeringDB 读取公开资料或导入类型化快照，审查差异后选择可更新字段。"
        className="document-heading"
      >
        {canPropose && (
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => start("import")}>
              <FileJson />
              导入快照
            </Button>
            <Button
              disabled={!provider?.enabled}
              onClick={() => start("fetch")}
            >
              <CloudDownload />
              获取 PeeringDB 提案
            </Button>
          </div>
        )}
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p role="status" className="notice mb-5 flex items-center gap-2">
          <Check className="size-4" />
          {done}
        </p>
      )}
      <div className="notice mb-6 flex items-start gap-3">
        <ShieldCheck className="mt-1 size-4 shrink-0" />
        <span>
          {provider?.enabled
            ? "PeeringDB 读取已启用。"
            : "PeeringDB 在线读取尚未启用，可由管理员配置服务端连接或导入快照。"}
          外部资料不会自动覆盖 ASN、互联地址、速率或 IRR 策略；此平台不向
          PeeringDB 写入。
        </span>
      </div>
      {loading && !proposals.length ? (
        <Loading />
      ) : !proposals.length ? (
        <Card>
          <Empty
            title="还没有资料提案"
            description="先选择本地成员并获取或导入该 ASN 的快照，再逐项核对差异。"
          />
        </Card>
      ) : (
        <div className="deployment-grid">
          <Card className="self-start overflow-hidden">
            <div className="flex items-center justify-between border-b p-4">
              <h2 className="text-sm font-semibold">资料提案</h2>
              <Button
                size="icon"
                variant="ghost"
                aria-label="刷新资料提案"
                disabled={loading}
                onClick={() => void load()}
              >
                <RefreshCw />
              </Button>
            </div>
            <div className="max-h-[680px] overflow-auto">
              {proposals.map((p) => (
                <button
                  key={p.id}
                  className={`w-full border-b p-4 text-left ${p.id === selected ? "bg-primary/5" : "hover:bg-muted/40"}`}
                  onClick={() => setSelected(p.id)}
                >
                  <p className="text-sm font-medium">
                    {p.memberName} · AS{p.asn}
                  </p>
                  <p className="mt-2 text-xs text-muted-foreground">
                    {time(p.createdAt)}
                  </p>
                  <Badge
                    variant={p.status === "applied" ? "default" : "outline"}
                    className="mt-2"
                  >
                    {p.status === "applied"
                      ? "已应用"
                      : p.status === "dismissed"
                        ? "保持本地"
                        : "待审查"}
                  </Badge>
                </button>
              ))}
              {hasMore && (
                <Button
                  variant="ghost"
                  className="m-3"
                  disabled={loading}
                  onClick={() => void loadMore()}
                >
                  加载更多提案
                </Button>
              )}
            </div>
          </Card>
          {current && (
            <Card className="min-w-0">
              <CardHeader>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <CardTitle>{current.memberName} 的资料差异</CardTitle>
                  <Badge variant="outline">{current.provider}</Badge>
                </div>
                <p className="text-xs text-muted-foreground">
                  采集时间：
                  {time(current.fetchedAt ?? current.snapshot?.fetchedAt)}
                </p>
                {current.sourceNote && (
                  <p className="text-xs leading-6 text-muted-foreground">
                    {current.sourceNote}
                    {current.providerOrigin
                      ? ` · ${current.providerOrigin}`
                      : ""}
                  </p>
                )}
                <p className="mono break-all text-[11px] leading-5 text-muted-foreground">
                  提案 SHA-256：{current.checksum}
                </p>
                {current.snapshotUnavailable && (
                  <p className="notice">
                    快照数据已清理；当前仅展示保留的提案差异和校验和。
                  </p>
                )}
              </CardHeader>
              <CardContent>
                {!current.diff.length ? (
                  <Empty
                    title="没有资料差异"
                    description="此快照未提供需要更新的字段。"
                  />
                ) : (
                  <div className="overflow-x-auto">
                    <table>
                      <thead>
                        <tr>
                          <th>选择</th>
                          <th>字段</th>
                          <th>当前资料</th>
                          <th>提案资料</th>
                        </tr>
                      </thead>
                      <tbody>
                        {current.diff.map((d) => (
                          <tr key={d.field}>
                            <td>
                              {d.applyAllowed &&
                              canApply &&
                              current.status === "pending" ? (
                                <input
                                  type="checkbox"
                                  disabled={
                                    detailLoading || detail?.id !== selected
                                  }
                                  aria-label={`应用 ${labels[d.field] ?? d.field}`}
                                  checked={fields.includes(d.field)}
                                  onChange={(e) =>
                                    setFields(
                                      e.target.checked
                                        ? [...fields, d.field]
                                        : fields.filter((f) => f !== d.field),
                                    )
                                  }
                                />
                              ) : (
                                <Badge variant="outline">
                                  {d.applyAllowed ? "只读" : "受保护"}
                                </Badge>
                              )}
                            </td>
                            <td>
                              <p className="text-xs font-medium">
                                {labels[d.field] ?? d.field}
                              </p>
                              {d.reason && (
                                <p className="mt-1 max-w-48 whitespace-normal text-xs leading-5 text-muted-foreground">
                                  {d.reason}
                                </p>
                              )}
                            </td>
                            <td className="max-w-64 break-all whitespace-normal text-xs leading-6">
                              {value(d.before)}
                            </td>
                            <td className="max-w-64 break-all whitespace-normal text-xs leading-6">
                              {value(d.after)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                <p className="notice mt-5">
                  保存时会核对提案与当前资料的校验和。当前资料若已变化，请重新生成并审查提案。
                </p>
                <div className="mt-4 flex flex-wrap justify-end gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => exportProposal(false)}
                  >
                    <Download />
                    导出审查记录
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={!current.snapshot || current.snapshotUnavailable}
                    onClick={() => exportProposal(true)}
                  >
                    <Download />
                    导出快照数据
                  </Button>
                  {canApply && current.status !== "pending" && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        setCleanup(current.snapshotPruned ? "delete" : "prune");
                        setFormError("");
                      }}
                    >
                      {current.snapshotPruned
                        ? "删除已清理提案记录"
                        : "清理终态快照数据"}
                    </Button>
                  )}
                </div>
                {canApply && current.status === "pending" && (
                  <div className="mt-4 flex flex-wrap justify-end gap-2">
                    <Button
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        setDismissReason("");
                        setFormError("");
                        setDismissOpen(true);
                      }}
                    >
                      保持本地并关闭提案
                    </Button>
                    <Button
                      disabled={
                        busy ||
                        detailLoading ||
                        detail?.id !== selected ||
                        !fields.length
                      }
                      onClick={() => {
                        setConfirm(true);
                        setFormError("");
                      }}
                    >
                      应用选中的 {fields.length} 个字段
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          )}
        </div>
      )}
      <Dialog open={!!open} onOpenChange={(v) => !busy && !v && setOpen(null)}>
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            {open === "fetch"
              ? "获取 PeeringDB 资料"
              : "导入 PeeringDB 类型化快照"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            选择本地 ASN
            对应成员。连接可选；地址和速率差异只用于核对，不自动重新分配。
          </DialogDescription>
          <form onSubmit={propose} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="form-grid">
              <div className="field">
                <label htmlFor="integration-member">本地成员</label>
                <select
                  id="integration-member"
                  value={memberId}
                  required
                  onChange={(e) => {
                    setMemberId(e.target.value);
                    setConnectionId("");
                  }}
                >
                  <option value="">选择有 ASN 的成员</option>
                  {members.map((m) => (
                    <option key={m.id} value={m.id} disabled={!Number(m.asn)}>
                      {m.name} · {m.asn ? `AS${m.asn}` : "无 ASN"}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="integration-connection">关联接入（可选）</label>
                <select
                  id="integration-connection"
                  value={connectionId}
                  onChange={(e) => setConnectionId(e.target.value)}
                >
                  <option value="">仅核对成员资料</option>
                  {connections
                    .filter((c) => c.memberId === memberId)
                    .map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                      </option>
                    ))}
                </select>
              </div>
            </div>
            {open === "fetch" ? (
              <div className="form-grid">
                <div className="field">
                  <label htmlFor="integration-ix">
                    PeeringDB IX ID（可选）
                  </label>
                  <Input
                    id="integration-ix"
                    name="ixId"
                    type="number"
                    min={1}
                  />
                </div>
                <div className="field">
                  <label htmlFor="integration-facility">
                    PeeringDB Facility ID（可选）
                  </label>
                  <Input
                    id="integration-facility"
                    name="facilityId"
                    type="number"
                    min={1}
                  />
                </div>
              </div>
            ) : (
              <>
                <div className="field">
                  <label htmlFor="integration-file">类型化 JSON 文件</label>
                  <Input
                    id="integration-file"
                    type="file"
                    accept=".json,application/json"
                    onChange={async (e) => {
                      const file = e.target.files?.[0];
                      if (!file) return;
                      try {
                        if (file.size > 6 * 1024 * 1024)
                          throw new Error("快照文件不能超过 6 MiB。");
                        setDocumentText(
                          new TextDecoder("utf-8", {
                            fatal: true,
                            ignoreBOM: true,
                          }).decode(await file.arrayBuffer()),
                        );
                        setFormError("");
                      } catch (e) {
                        setFormError(message(e));
                      }
                    }}
                  />
                </div>
                <div className="field">
                  <label htmlFor="integration-json">快照内容</label>
                  <textarea
                    id="integration-json"
                    required
                    className="mono min-h-56 text-xs"
                    value={documentText}
                    onChange={(e) => setDocumentText(e.target.value)}
                  />
                  <p className="text-xs leading-6 text-muted-foreground">
                    格式为 version: 1、provider:
                    peeringdb、fetchedAt、network、netixlans、exchanges、facilities。提交时校验这份文档的精确
                    UTF-8 内容。
                  </p>
                </div>
              </>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setOpen(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在读取并验证…" : "创建审查提案"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog open={confirm} onOpenChange={(v) => !busy && setConfirm(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            确认更新选中的资料
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            只应用选中的允许字段。受保护的
            ASN、地址、容量与策略不在本次更新中；设备不会收到配置下发。
          </DialogDescription>
          <ul className="list-inside list-disc text-sm leading-7">
            {fields.map((f) => (
              <li key={f}>{labels[f] ?? f}</li>
            ))}
          </ul>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setConfirm(false)}
            >
              继续检查
            </Button>
            <Button disabled={busy} onClick={() => void apply()}>
              确认应用资料
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={dismissOpen}
        onOpenChange={(v) => !busy && setDismissOpen(v)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            保留本地资料
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            关闭这份提案，不修改任何成员或接入字段。原始快照与差异仍保留供审计。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="field">
            <label htmlFor="proposal-dismiss-reason">审查说明（可选）</label>
            <textarea
              id="proposal-dismiss-reason"
              value={dismissReason}
              onChange={(e) => setDismissReason(e.target.value)}
              maxLength={2000}
            />
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDismissOpen(false)}
            >
              取消
            </Button>
            <Button disabled={busy} onClick={() => void dismiss()}>
              确认保持本地
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!cleanup}
        onOpenChange={(v) => !busy && !v && setCleanup(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {cleanup === "prune"
              ? "清理已处理提案的快照数据"
              : "删除已清理的提案列表记录"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {cleanup === "prune"
              ? "只处理已应用或已关闭的提案，保留差异、内容摘要与审查记录；待审查提案不清理。需要留存快照时，请先导出数据或备份服务器。"
              : "只允许删除已清理快照的终态提案。列表中的详细记录会移除，差异与校验和保留在审计摘要中；请先导出需要的资料。"}
          </DialogDescription>
          <form onSubmit={cleanupProposal} className="space-y-4">
            <ErrorNotice message={formError} />
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={cleanup === "prune" && !current?.snapshot}
              onClick={() => exportProposal(cleanup === "prune")}
            >
              <Download />
              {cleanup === "prune" ? "导出当前快照数据" : "导出当前审查记录"}
            </Button>
            <label className="flex items-start gap-2 text-xs leading-6">
              <input className="mt-1" type="checkbox" required />
              我已保存所需资料，理解此次清理不会修改本地网络配置。
            </label>
            {cleanup === "delete" && (
              <div className="field">
                <label htmlFor="proposal-cleanup-reason">删除原因</label>
                <textarea
                  id="proposal-cleanup-reason"
                  name="reason"
                  required
                  maxLength={2000}
                />
              </div>
            )}
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setCleanup(null)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {cleanup === "prune" ? "确认清理快照数据" : "确认删除提案记录"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
