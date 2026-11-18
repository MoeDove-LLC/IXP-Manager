import { useEffect, useRef, useState, type FormEvent } from "react";
import {
  CheckCheck,
  Play,
  Plus,
  RefreshCw,
  ShieldCheck,
  Square,
  Trash2,
} from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import type { Row } from "@/lib/resources";
import {
  atlasStatuses,
  type AtlasRun,
  type AtlasSummary,
  type AtlasProbeSnapshot,
} from "@/lib/atlas";
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
import AtlasProposal from "@/components/AtlasProposal";
import AtlasProbes, {
  probeKey,
  useAtlasProbes,
} from "@/components/AtlasProbes";
import AtlasResults from "@/components/AtlasResults";

type Action =
  "approve" | "reapprove" | "execute" | "resume" | "stop" | "prune" | "delete";
const actionTitles: Record<Action, string> = {
  approve: "审核测量范围与额度",
  reapprove: "重新批准尚未尝试的剩余项",
  execute: "提交已批准的外部测量",
  resume: "仅继续未发送的测量方向",
  stop: "请求停止 / 取消未发送项",
  prune: "清理测量结果快照",
  delete: "删除已清理的测量记录",
};
const terminal = new Set([
  "stopped",
  "completed",
  "failed",
  "archived",
  "cancelled",
]);
export default function Atlas({ role }: { role: string }) {
  const [probeDelete, setProbeDelete] = useState<AtlasProbeSnapshot | null>(
      null,
    ),
    [probeDeleteError, setProbeDeleteError] = useState("");
  const admin = role === "admin",
    canWrite = admin || role === "operator";
  const [summary, setSummary] = useState<AtlasSummary | null>(null),
    [members, setMembers] = useState<Row[]>([]),
    [connections, setConnections] = useState<Row[]>([]),
    [vlans, setVlans] = useState<Row[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [tab, setTab] = useState("runs"),
    [selected, setSelected] = useState(""),
    [run, setRun] = useState<AtlasRun | null>(null),
    [detailLoading, setDetailLoading] = useState(false),
    [detailError, setDetailError] = useState(""),
    [refresh, setRefresh] = useState(0),
    [resultsVersion, setResultsVersion] = useState(0),
    [newProposal, setNewProposal] = useState(false),
    [action, setAction] = useState<{ kind: Action; run: AtlasRun } | null>(
      null,
    ),
    [actionError, setActionError] = useState(""),
    [actionStale, setActionStale] = useState(false),
    [uncertainLocal, setUncertainLocal] = useState<string[]>([]),
    [probeMember, setProbeMember] = useState(""),
    [probeAF, setProbeAF] = useState(4);
  const alive = useRef(true),
    loadSequence = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      loadSequence.current++;
    };
  }, []);
  const probes = useAtlasProbes(),
    name = (id?: string) =>
      String(members.find((member) => member.id === id)?.name ?? id ?? "—");
  const load = async () => {
    const sequence = ++loadSequence.current;
    setLoading(true);
    setError("");
    try {
      const [data, m, c, v] = await Promise.all([
        api<AtlasSummary>("/atlas"),
        api<{ items: Row[] }>("/resources/members"),
        api<{ items: Row[] }>("/resources/connections"),
        api<{ items: Row[] }>("/resources/vlans"),
      ]);
      if (!alive.current || sequence !== loadSequence.current) return;
      setSummary({
        ...data,
        runs: [...data.runs].sort((a, b) =>
          b.createdAt.localeCompare(a.createdAt),
        ),
      });
      setMembers(m.items);
      setConnections(c.items);
      setVlans(v.items);
      setSelected((id) =>
        data.runs.some((item) => item.id === id)
          ? id
          : (data.runs[0]?.id ?? ""),
      );
    } catch (error) {
      if (alive.current && sequence === loadSequence.current)
        setError(message(error));
    } finally {
      if (alive.current && sequence === loadSequence.current) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    if (tab === "probes" && probeMember) void probes.load(probeMember, probeAF);
  }, [tab, probeMember, probeAF, probes.load]);
  useEffect(() => {
    setRun(null);
    setDetailError("");
    if (!selected) {
      setDetailLoading(false);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    setDetailLoading(true);
    const read = async () => {
      try {
        const data = await api<AtlasRun>(`/atlas/runs/${selected}`, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setRun(data);
        setDetailLoading(false);
        setSummary((value) =>
          value
            ? {
                ...value,
                runs: value.runs.map((row) =>
                  row.id === data.id ? { ...row, ...data } : row,
                ),
              }
            : value,
        );
        if (data.status !== "approved")
          setUncertainLocal((values) => values.filter((id) => id !== data.id));
        if (
          ["creating", "stopping", "partial"].includes(data.status) ||
          (data.stopRequested &&
            data.items?.some((item) => !terminal.has(item.status))) ||
          data.items?.some((item) =>
            ["creating", "stopping"].includes(item.status),
          )
        )
          timer = setTimeout(() => void read(), 3000);
      } catch (error) {
        if (!controller.signal.aborted) {
          setDetailError(message(error));
          setDetailLoading(false);
        }
      }
    };
    void read();
    return () => {
      controller.abort();
      if (timer) clearTimeout(timer);
    };
  }, [selected, refresh]);
  const openAction = (kind: Action) => {
    if (!run) return;
    setAction({ kind, run: structuredClone(run) });
    setActionError("");
    setActionStale(false);
  };
  const deleteProbe = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!probeDelete) return;
    setBusy(true);
    setProbeDeleteError("");
    const data = new FormData(event.currentTarget);
    try {
      await save(
        `/atlas/probes/${probeDelete.id}`,
        {
          checksum: probeDelete.checksum,
          reason: String(data.get("reason") ?? "").trim(),
          acknowledge: true,
        },
        "DELETE",
      );
      const deleted = probeDelete;
      setProbeDelete(null);
      await probes.load(deleted.memberId, deleted.af);
      void load();
    } catch (error) {
      setProbeDeleteError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const synchronize = async () => {
    if (!run) return;
    setBusy(true);
    setDetailError("");
    try {
      await save(`/atlas/runs/${run.id}/refresh`, {});
      setRefresh((value) => value + 1);
      setResultsVersion((value) => value + 1);
      void load();
    } catch (error) {
      setDetailError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const perform = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!action || actionStale) return;
    const form = event.currentTarget,
      data = new FormData(form),
      snapshot = action.run,
      kind = action.kind;
    setBusy(true);
    setActionError("");
    try {
      const body = {
        checksum: snapshot.checksum,
        ...(["approve", "reapprove"].includes(kind)
          ? {
              acknowledgeCredits: data.get("ackCredits") === "on",
              acknowledgePublic:
                snapshot.isPublic && data.get("ackPublic") === "on",
              currentPassword: String(data.get("currentPassword") ?? ""),
              ...(data.get("code") ? { code: String(data.get("code")) } : {}),
            }
          : {}),
        ...(["prune", "delete"].includes(kind)
          ? {
              acknowledge: true,
              reason: String(data.get("reason") ?? "").trim(),
            }
          : {}),
      };
      await save(
        `/atlas/runs/${snapshot.id}${kind === "delete" ? "" : `/${kind === "reapprove" ? "approve" : kind}`}`,
        body,
        kind === "delete" ? "DELETE" : "POST",
      );
      if (!alive.current) return;
      form.reset();
      setAction(null);
      if (kind === "delete") {
        setSelected("");
        setRun(null);
      } else setRefresh((value) => value + 1);
      setResultsVersion((value) => value + 1);
      void load();
    } catch (error) {
      if (!alive.current) return;
      const unknownSubmission =
        ["execute", "resume"].includes(kind) &&
        (!(error instanceof APIError) || error.status >= 500);
      if (unknownSubmission) {
        setUncertainLocal((values) => [...new Set([...values, snapshot.id])]);
        setAction(null);
        setDetailError(
          "提交响应未得到确认，请读取当前状态和审计记录。控制台不会自动重发测量创建请求。",
        );
        setRefresh((value) => value + 1);
      } else if (
        error instanceof APIError &&
        [409, 412, 404].includes(error.status)
      ) {
        form.reset();
        setActionStale(true);
        setActionError(
          `${message(error)}。请关闭此窗口，重新读取提案状态后核对；不会沿用本次密码或确认项。`,
        );
      } else setActionError(message(error));
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const items = run?.items ?? [],
    pureUnsent = items.every(
      (item) =>
        ["draft", "approved", "cancelled"].includes(item.status) &&
        !item.providerId &&
        !item.ownershipConfirmed,
    ),
    canStop =
      admin &&
      !run?.pruned &&
      !run?.stopRequested &&
      items.some((item) => !terminal.has(item.status)),
    canPrune =
      admin &&
      !!run &&
      !run.pruned &&
      items.every((item) => terminal.has(item.status)) &&
      items.length > 0;
  const selectedCache = probes.entries[probeKey(probeMember, probeAF)],
    uncertain =
      run?.status === "uncertain" ||
      (run?.uncertainCount ?? 0) > 0 ||
      (!!run && uncertainLocal.includes(run.id));
  return (
    <>
      <Heading
        className="flex-wrap"
        title="RIPE Atlas"
        description="选择探针和固定目标，审核样本与额度上界，再提交独立的外部测量。"
      >
        {canWrite && summary && (
          <Button onClick={() => setNewProposal(true)}>
            <Plus />
            新建测量提案
          </Button>
        )}
        <Button
          variant="outline"
          disabled={loading || busy}
          onClick={() => {
            void load();
            setRefresh((value) => value + 1);
          }}
        >
          <RefreshCw />
          刷新控制台记录
        </Button>
      </Heading>
      <ErrorNotice message={error} />
      {!summary ? (
        loading ? (
          <Loading />
        ) : null
      ) : (
        <>
          <div className="notice mb-5">
            {summary.enabled
              ? "Atlas provider 已配置。"
              : "Atlas provider 尚未启用；可以查看已存记录，发现探针和外部提交需要服务端私有配置。"}{" "}
            所有额度数字为服务端保守估算，不是 provider
            余额或实际消耗。提案默认私有，公开测量需要单独确认。
          </div>
          <div className="mb-5 flex gap-2">
            <Button
              variant={tab === "runs" ? "secondary" : "ghost"}
              aria-pressed={tab === "runs"}
              onClick={() => setTab("runs")}
            >
              测量与审核
            </Button>
            <Button
              variant={tab === "probes" ? "secondary" : "ghost"}
              aria-pressed={tab === "probes"}
              onClick={() => setTab("probes")}
            >
              探针快照
            </Button>
          </div>
          {tab === "probes" ? (
            <Card>
              <CardHeader>
                <CardTitle>按成员 ASN 查看探针快照</CardTitle>
              </CardHeader>
              <CardContent>
                <div className="form-grid mb-5">
                  <div className="field">
                    <label htmlFor="atlas-probe-member">成员</label>
                    <select
                      id="atlas-probe-member"
                      value={probeMember}
                      onChange={(event) => setProbeMember(event.target.value)}
                    >
                      <option value="">请选择成员</option>
                      {members
                        .filter((member) => Number(member.asn) > 0)
                        .map((member) => (
                          <option key={member.id} value={member.id}>
                            {member.name} · AS{String(member.asn)}
                          </option>
                        ))}
                    </select>
                  </div>
                  <div className="field">
                    <label htmlFor="atlas-probe-af">地址族</label>
                    <select
                      id="atlas-probe-af"
                      value={probeAF}
                      onChange={(event) =>
                        setProbeAF(Number(event.target.value))
                      }
                    >
                      <option value={4}>IPv4</option>
                      <option value={6}>IPv6</option>
                    </select>
                  </div>
                </div>
                {probeMember ? (
                  <AtlasProbes
                    cache={selectedCache}
                    loading={!!probes.loading[probeKey(probeMember, probeAF)]}
                    error={probes.errors[probeKey(probeMember, probeAF)]}
                    af={probeAF}
                    canDiscover={canWrite}
                    enabled={summary.enabled}
                    onDiscover={(anchor) =>
                      void probes.load(probeMember, probeAF, true, anchor)
                    }
                    label="成员的公开探针快照"
                  />
                ) : (
                  <Empty description="选择成员和地址族读取已保存的探针快照。" />
                )}
                {admin && selectedCache?.snapshot && (
                  <div className="mt-4 flex justify-end border-t pt-4">
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={busy}
                      onClick={() => {
                        setProbeDelete(
                          structuredClone(selectedCache.snapshot!),
                        );
                        setProbeDeleteError("");
                      }}
                    >
                      <Trash2 />
                      清除探针缓存
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          ) : (
            <div className="deployment-grid">
              <Card className="self-start overflow-hidden">
                <CardHeader>
                  <CardTitle>测量提案与记录</CardTitle>
                </CardHeader>
                {!summary.runs.length ? (
                  <Empty
                    title="还没有测量提案"
                    description="先获取真实探针快照，再选择单方向或有向矩阵。创建提案不会自动提交外部测量。"
                    action={canWrite ? "新建测量提案" : undefined}
                    onAction={() => setNewProposal(true)}
                  />
                ) : (
                  <div className="max-h-[700px] overflow-y-auto">
                    {summary.runs.map((item) => (
                      <button
                        key={item.id}
                        data-atlas-run={item.id}
                        onClick={() => setSelected(item.id)}
                        className={`w-full border-t px-5 py-4 text-left ${selected === item.id ? "bg-primary/5" : "hover:bg-muted/30"}`}
                      >
                        <p className="truncate text-sm font-medium">
                          {item.name}
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {item.mode === "matrix"
                            ? `${item.measurementCount ?? 0} 个有向测量`
                            : item.targetAddress || "单个方向"}{" "}
                          · {item.type} · IPv{item.af}
                        </p>
                        <div className="mt-2 flex flex-wrap gap-2">
                          <Badge variant="outline">
                            {atlasStatuses[item.status] ?? item.status}
                          </Badge>
                          <Badge variant="outline">
                            {item.isPublic ? "公开" : "私有"}
                          </Badge>
                          {item.pruned && (
                            <Badge variant="outline">快照已清理</Badge>
                          )}
                        </div>
                        <p className="mt-2 text-[11px] text-muted-foreground">
                          {time(item.createdAt)}
                        </p>
                      </button>
                    ))}
                  </div>
                )}
              </Card>
              <div className="min-w-0 space-y-5">
                <ErrorNotice message={detailError} />
                {detailLoading && !run ? (
                  <Loading />
                ) : !run ? (
                  <Card>
                    <Empty description="选择提案查看冻结目标、额度上界和 provider 状态。" />
                  </Card>
                ) : (
                  <>
                    <Card className="min-w-0">
                      <CardHeader>
                        <div className="flex flex-wrap justify-between gap-3">
                          <CardTitle>{run.name}</CardTitle>
                          <div className="flex gap-2">
                            <Badge variant="outline">
                              {atlasStatuses[run.status] ?? run.status}
                            </Badge>
                            <Badge
                              variant={run.isPublic ? "outline" : "default"}
                            >
                              {run.isPublic ? "公开结果" : "私有结果"}
                            </Badge>
                          </div>
                        </div>
                        <p className="mono mt-2 break-all text-[11px] text-muted-foreground">
                          审核标识：{run.checksum}
                        </p>
                      </CardHeader>
                      <CardContent>
                        <div className="space-y-4">
                          <div className="grid gap-3 sm:grid-cols-3">
                            {[
                              ["估算 credits 上界", run.estimatedCredits],
                              ["估算样本上界", run.estimatedSamples],
                              ["方向数", run.measurementCount ?? items.length],
                            ].map(([label, value]) => (
                              <div
                                key={String(label)}
                                className="rounded-lg border p-3"
                              >
                                <p className="text-xs text-muted-foreground">
                                  {label}
                                </p>
                                <p className="mt-2 text-xl font-semibold">
                                  {value}
                                </p>
                              </div>
                            ))}
                          </div>
                          <p className="text-xs leading-6 text-muted-foreground">
                            实际扣费未由此接口报告。
                            {run.isOneoff ? "一次性测量" : "周期测量"} ·{" "}
                            {run.type} · IPv{run.af}
                            {!run.isOneoff
                              ? ` · 间隔 ${run.intervalSeconds} 秒 · 持续 ${run.durationSeconds} 秒`
                              : ""}
                            。开始：
                            {run.startAt
                              ? time(run.startAt)
                              : "按提交时间由 provider 安排"}
                            。
                          </p>
                          <p className="text-xs text-muted-foreground">
                            提案创建：{time(run.createdAt)} · 审批：
                            {time(run.approvedAt)}
                          </p>
                          {run.notices
                            ?.filter((value) => typeof value === "string")
                            .map((notice, index) => (
                              <p
                                key={index}
                                className="text-xs leading-6 text-muted-foreground"
                              >
                                {String(notice)}
                              </p>
                            ))}
                          {uncertain && (
                            <p className="notice">
                              存在提交结果不确定或仍在提交的方向。不要重复创建这些测量。安全同步只能核对
                              provider
                              状态与已确证归属，不能把未知结果当作失败后自动重试。
                            </p>
                          )}
                          {(run.remainingCount ?? 0) > 0 &&
                            run.status !== "draft" && (
                              <p className="notice">
                                还有 {run.remainingCount} 个未发送方向。
                                {(run.uncertainCount ?? 0) > 0
                                  ? `另有 ${run.uncertainCount} 个方向状态未确认。`
                                  : ""}{" "}
                                只有服务端明确允许时，才能单独继续未发送项。
                              </p>
                            )}
                          {run.stopRequested && (
                            <p className="notice">
                              已记录停止意图；具体外部状态以下方各方向的确证结果为准。
                            </p>
                          )}
                          {run.approvalExpired && (
                            <p className="notice">
                              原审批已过期，不能沿用它继续创建测量。只有目标、探针和开始时间仍符合冻结提案时，才能重新审核尚未尝试的剩余项；否则请创建新提案。
                            </p>
                          )}
                          {run.resumeRequired &&
                            (run.remainingCount ?? 0) > 0 && (
                              <p className="notice">
                                剩余项已重新获得授权，仍需单独确认“继续未发送项”。已经尝试过的方向不会重发。
                              </p>
                            )}
                          <div className="overflow-x-auto rounded-lg border">
                            <table>
                              <thead>
                                <tr>
                                  <th>方向 / 冻结目标</th>
                                  <th>来源探针</th>
                                  <th>真实任务状态</th>
                                  <th>provider 归属</th>
                                </tr>
                              </thead>
                              <tbody>
                                {items.map((item) => (
                                  <tr key={item.id}>
                                    <td className="text-xs">
                                      {name(item.sourceMemberId)} →{" "}
                                      {name(item.targetMemberId)}
                                      <p className="mono mt-1 break-all text-muted-foreground">
                                        {item.targetAddress}
                                      </p>
                                    </td>
                                    <td className="mono text-xs">
                                      {item.probeIds
                                        .map((id) => `#${id}`)
                                        .join(", ")}
                                    </td>
                                    <td className="text-xs">
                                      <Badge variant="outline">
                                        {atlasStatuses[item.status] ??
                                          item.status}
                                      </Badge>
                                      {item.providerStatus && (
                                        <p className="mt-1">
                                          provider：{item.providerStatus}
                                        </p>
                                      )}
                                      {item.lastError && (
                                        <p className="mt-1 break-all text-destructive">
                                          {item.lastError}
                                        </p>
                                      )}
                                      <p className="mt-1 text-[10px] text-muted-foreground">
                                        最近同步 {time(item.lastSyncedAt)}
                                      </p>
                                    </td>
                                    <td className="text-xs">
                                      {item.providerId
                                        ? `#${item.providerId}`
                                        : [
                                              "draft",
                                              "approved",
                                              "cancelled",
                                            ].includes(item.status)
                                          ? "尚未提交 provider"
                                          : "ID 未确认"}
                                      <p className="mt-1 text-muted-foreground">
                                        {item.ownershipConfirmed
                                          ? "已确证为本实例创建"
                                          : [
                                                "draft",
                                                "approved",
                                                "cancelled",
                                              ].includes(item.status) &&
                                              !item.providerId
                                            ? "尚无外部测量"
                                            : "归属尚未确证"}
                                      </p>
                                    </td>
                                  </tr>
                                ))}
                              </tbody>
                            </table>
                          </div>
                          <div className="flex flex-wrap justify-end gap-2">
                            {canWrite && (
                              <Button
                                variant="outline"
                                disabled={
                                  busy ||
                                  !summary.enabled ||
                                  run.pruned ||
                                  run.status === "draft"
                                }
                                onClick={() => void synchronize()}
                              >
                                <RefreshCw />
                                同步 Atlas 状态与结果
                              </Button>
                            )}
                            {admin && run.status === "draft" && !run.pruned && (
                              <Button
                                disabled={busy || !summary.enabled}
                                onClick={() => openAction("approve")}
                              >
                                <ShieldCheck />
                                审核额度与范围
                              </Button>
                            )}
                            {admin && run.canReapprove && !run.pruned && (
                              <Button
                                variant="outline"
                                disabled={
                                  busy ||
                                  !summary.enabled ||
                                  typeof run.remainingEstimatedCredits !==
                                    "number" ||
                                  typeof run.remainingEstimatedSamples !==
                                    "number"
                                }
                                onClick={() => openAction("reapprove")}
                              >
                                <ShieldCheck />
                                重新批准剩余项
                              </Button>
                            )}
                            {admin &&
                              run.status === "approved" &&
                              !run.resumeRequired &&
                              !run.approvalExpired &&
                              !run.pruned && (
                                <Button
                                  disabled={
                                    busy ||
                                    !summary.enabled ||
                                    uncertainLocal.includes(run.id)
                                  }
                                  onClick={() => openAction("execute")}
                                >
                                  <Play />
                                  提交外部测量
                                </Button>
                              )}
                            {admin &&
                              run.canResume &&
                              !run.pruned &&
                              !items.some((item) =>
                                ["creating", "stopping"].includes(item.status),
                              ) &&
                              !["creating", "stopping"].includes(
                                run.status,
                              ) && (
                                <Button
                                  disabled={
                                    busy ||
                                    !summary.enabled ||
                                    uncertainLocal.includes(run.id)
                                  }
                                  onClick={() => openAction("resume")}
                                >
                                  <Play />
                                  继续未发送项
                                </Button>
                              )}
                            {canStop && (
                              <Button
                                variant="outline"
                                disabled={
                                  busy || (!summary.enabled && !pureUnsent)
                                }
                                onClick={() => openAction("stop")}
                              >
                                <Square />
                                请求停止 / 取消未发送项
                              </Button>
                            )}
                          </div>
                          {admin && (canPrune || run.pruned) && (
                            <div className="flex justify-end border-t pt-4">
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={busy}
                                onClick={() =>
                                  openAction(run.pruned ? "delete" : "prune")
                                }
                              >
                                <Trash2 />
                                {run.pruned
                                  ? "删除已清理记录"
                                  : "清理终态结果快照"}
                              </Button>
                            </div>
                          )}
                        </div>
                      </CardContent>
                    </Card>
                    <AtlasResults
                      key={run.id}
                      run={run}
                      members={members}
                      reload={resultsVersion}
                    />
                  </>
                )}
              </div>
            </div>
          )}
          {newProposal && (
            <AtlasProposal
              members={members}
              connections={connections}
              vlans={vlans}
              limits={summary.limits}
              enabled={summary.enabled}
              onClose={() => setNewProposal(false)}
              onCreated={(created) => {
                setNewProposal(false);
                setTab("runs");
                setSelected(created.id);
                setSummary((value) =>
                  value
                    ? {
                        ...value,
                        runs: [
                          created,
                          ...value.runs.filter(
                            (item) => item.id !== created.id,
                          ),
                        ],
                      }
                    : value,
                );
                setRefresh((value) => value + 1);
              }}
            />
          )}
        </>
      )}
      <Dialog
        open={!!action}
        onOpenChange={(value) => !value && !busy && setAction(null)}
      >
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            {action ? actionTitles[action.kind] : "审核操作"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {action?.kind === "reapprove"
              ? "保持原审核标识、目标、探针和公开范围不变，仅重新授权从未尝试的剩余项。请重新认证并核对额度；完成后仍需单独续行，不会重发已尝试的方向。"
              : action?.kind === "approve"
                ? "核对固定目标、探针、公开范围和总额度估算。批准本身不提交测量，之后还需明确执行。"
                : action?.kind === "execute"
                  ? "这一步会向配置的 RIPE Atlas provider 发出外部测量创建请求，并可能消耗 credits。"
                  : action?.kind === "resume"
                    ? "仅提交仍未发送的已批准子项；不会重发 creating、uncertain 或 failed 的测量。"
                    : action?.kind === "stop"
                      ? "记录停止意图并取消未发送方向；外部停止只针对本实例已确证归属的 measurement。不确定的创建仍保留待核对，不猜测外部 ID。"
                      : "此操作仅清理本地终态快照或记录，无法撤销；所有子项必须已停止或确认为终态，不确定的创建不能清理。"}
          </DialogDescription>
          <ErrorNotice message={actionError} />
          {action && (
            <form onSubmit={perform} className="space-y-4">
              <div className="rounded-lg border bg-muted/30 p-4 text-sm leading-7">
                <p className="font-semibold">{action.run.name}</p>
                <p>
                  {action.run.measurementCount ?? action.run.items?.length ?? 0}{" "}
                  个方向 · {action.run.type} · IPv{action.run.af} ·{" "}
                  {action.run.isPublic ? "公开结果" : "私有结果"}
                </p>
                <p>
                  {["reapprove", "resume"].includes(action.kind)
                    ? "原提案总估算上界"
                    : "估算上界"}
                  ：{action.run.estimatedCredits} credits /{" "}
                  {action.run.estimatedSamples} 样本
                </p>
                {["reapprove", "resume"].includes(action.kind) && (
                  <p>
                    本次剩余 {action.run.remainingCount ?? 0} 项估算上界：
                    {action.run.remainingEstimatedCredits ?? "未提供"} credits /{" "}
                    {action.run.remainingEstimatedSamples ?? "未提供"} 样本。
                  </p>
                )}
                {action.kind === "resume" && (
                  <p>
                    本次仅继续 {action.run.remainingCount ?? 0} 个未发送项；
                    {action.run.uncertainCount ?? 0} 个未确认项不会重发。
                  </p>
                )}
                <p className="mt-2 text-xs text-muted-foreground">
                  估算不代表已消耗或实际最终扣费。
                </p>
              </div>
              {["approve", "reapprove"].includes(action.kind) && (
                <>
                  <div className="max-h-48 overflow-auto rounded-lg border p-3 text-xs leading-6">
                    {action.run.items
                      ?.filter(
                        (item) =>
                          action.kind !== "reapprove" ||
                          item.status === "approved",
                      )
                      .map((item) => (
                        <p key={item.id}>
                          {name(item.sourceMemberId)} →{" "}
                          {name(item.targetMemberId)} ·{" "}
                          <span className="mono">{item.targetAddress}</span> ·
                          探针 {item.probeIds.map((id) => `#${id}`).join(", ")}
                        </p>
                      ))}
                  </div>
                  <label className="flex items-start gap-2 text-sm leading-6">
                    <input
                      required
                      type="checkbox"
                      name="ackCredits"
                      className="mt-1"
                    />
                    {action.kind === "reapprove"
                      ? "我已重新核对尚未尝试的剩余方向、冻结目标及剩余额度上界，授权后续续行使用这些"
                      : "我已核对所有方向、冻结目标及总额度上界，授权后续提交使用这些"}
                    credits。
                  </label>
                  {action.run.isPublic && (
                    <label className="flex items-start gap-2 text-sm leading-6">
                      <input
                        required
                        type="checkbox"
                        name="ackPublic"
                        className="mt-1"
                      />
                      我明确同意这些测量结果公开，并已核对目标和来源信息。
                    </label>
                  )}
                  <div className="field">
                    <label htmlFor="atlas-approval-password">
                      当前登录密码
                    </label>
                    <Input
                      id="atlas-approval-password"
                      name="currentPassword"
                      type="password"
                      autoComplete="current-password"
                      required
                      disabled={busy || actionStale}
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="atlas-approval-code">
                      验证器代码或恢复码（已开启时填写）
                    </label>
                    <Input
                      id="atlas-approval-code"
                      name="code"
                      autoComplete="one-time-code"
                      disabled={busy || actionStale}
                    />
                  </div>
                </>
              )}
              {["execute", "resume"].includes(action.kind) && (
                <label className="flex items-start gap-2 text-sm leading-6">
                  <input required type="checkbox" className="mt-1" />
                  我确认现在提交上述已批准范围，理解此操作会创建外部测量并可能消耗
                  credits。
                </label>
              )}
              {action.kind === "stop" && (
                <label className="flex items-start gap-2 text-sm leading-6">
                  <input required type="checkbox" className="mt-1" />
                  我确认取消未发送项，并请求在确证归属后停止相应外部测量；最终状态仍需同步核对。
                </label>
              )}
              {["prune", "delete"].includes(action.kind) && (
                <>
                  <div className="field">
                    <label htmlFor="atlas-retention-reason">
                      清理原因（3–500 字，单行）
                    </label>
                    <Input
                      id="atlas-retention-reason"
                      name="reason"
                      required
                      minLength={3}
                      maxLength={500}
                    />
                  </div>
                  <label className="flex items-start gap-2 text-sm leading-6">
                    <input required type="checkbox" className="mt-1" />
                    我已保留需要的结果，并确认这次本地清理无法撤销。
                  </label>
                </>
              )}
              <div className="flex justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => {
                    setAction(null);
                    if (actionStale) setRefresh((value) => value + 1);
                  }}
                >
                  {actionStale ? "关闭并重新读取" : "取消"}
                </Button>
                <Button
                  variant={
                    ["stop", "prune", "delete"].includes(action.kind)
                      ? "destructive"
                      : "default"
                  }
                  disabled={busy || actionStale}
                >
                  <CheckCheck />
                  {busy
                    ? "正在处理…"
                    : action.kind === "reapprove"
                      ? "确认重新批准剩余项"
                      : action.kind === "approve"
                        ? "确认审批"
                        : action.kind === "execute"
                          ? "确认提交外部测量"
                          : action.kind === "resume"
                            ? "确认仅继续未发送项"
                            : action.kind === "stop"
                              ? "确认停止 / 取消意图"
                              : "确认清理"}
                </Button>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!probeDelete}
        onOpenChange={(value) => !value && !busy && setProbeDelete(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            清除这份探针缓存？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            仅删除本地探针快照，不会停止已有外部测量。后续提案或尚未执行的审批需要重新发现探针。快照获取于{" "}
            {time(probeDelete?.fetchedAt)}。
          </DialogDescription>
          <ErrorNotice message={probeDeleteError} />
          <form onSubmit={deleteProbe} className="space-y-4">
            <div className="field">
              <label htmlFor="atlas-probe-retention-reason">
                清理原因（单行，3–500 字）
              </label>
              <Input
                id="atlas-probe-retention-reason"
                name="reason"
                required
                minLength={3}
                maxLength={500}
              />
            </div>
            <label className="flex items-start gap-2 text-sm leading-6">
              <input required type="checkbox" className="mt-1" />
              我确认清除这份缓存，并理解之后需要重新发现探针。
            </label>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setProbeDelete(null)}
              >
                取消
              </Button>
              <Button variant="destructive" disabled={busy}>
                {busy ? "正在清理…" : "确认清除探针缓存"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
