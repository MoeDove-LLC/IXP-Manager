import { useEffect, useState, type FormEvent } from "react";
import {
  Check,
  FileJson,
  Play,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
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
type Snapshot = {
  id: string;
  kind: "irr" | "rpki";
  createdAt: string;
  checksum: string;
  source: string;
  fetchedAt: string;
  expiresAt: string;
  entryCount: number;
  asSet?: string;
};
type Decision = {
  prefix: string;
  originAsn: number;
  accepted: boolean;
  rpki: "valid" | "invalid" | "not-found" | "unavailable";
  reasons: string[];
  pathValidated: boolean;
};
type Proposal = {
  peerId: string;
  baseChecksum: string;
  prefixes: string[];
  added: string[];
  removed: string[];
  source: string;
  irrSnapshotId: string;
  includeASSetOrigins: boolean;
  allowedRoutes?: { prefix: string; originAsn: number }[];
};
const rpkiLabels = {
  valid: "ROA 有效",
  invalid: "ROA 无效",
  "not-found": "未找到 ROA",
  unavailable: "无可用 RPKI 数据",
};
export default function Policy({ role }: { role: string }) {
  const canWrite = role === "admin" || role === "operator";
  const [snapshots, setSnapshots] = useState<Snapshot[]>([]),
    [peers, setPeers] = useState<Row[]>([]),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [done, setDone] = useState("");
  const [peerId, setPeerId] = useState(""),
    [irrId, setIrrId] = useState(""),
    [rpkiId, setRpkiId] = useState(""),
    [requireIrr, setRequireIrr] = useState(false),
    [includeASSetOrigins, setIncludeASSetOrigins] = useState(false),
    [rejectNotFound, setRejectNotFound] = useState(false),
    [routeText, setRouteText] = useState(""),
    [decisions, setDecisions] = useState<Decision[] | null>(null),
    [proposal, setProposal] = useState<Proposal | null>(null),
    [confirm, setConfirm] = useState(false);
  const [importOpen, setImportOpen] = useState(false),
    [kind, setKind] = useState<"irr" | "rpki">("irr"),
    [content, setContent] = useState(""),
    [formError, setFormError] = useState(""),
    [deleting, setDeleting] = useState<Snapshot | null>(null);
  const load = async () => {
    setLoading(true);
    try {
      const [s, p] = await Promise.all([
        api<{ items: Snapshot[] }>("/policy/snapshots"),
        api<{ items: Row[] }>("/resources/peers"),
      ]);
      setSnapshots(s.items);
      setPeers(p.items);
      setIrrId((id) => (s.items.some((v) => v.id === id) ? id : ""));
      setRpkiId((id) => (s.items.some((v) => v.id === id) ? id : ""));
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
    setProposal(null);
    setDecisions(null);
    setDone("");
  }, [
    peerId,
    irrId,
    rpkiId,
    routeText,
    requireIrr,
    rejectNotFound,
    includeASSetOrigins,
  ]);
  const ingest = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setFormError("");
    try {
      const snapshot = JSON.parse(content);
      if (snapshot.version !== 1) throw new Error("快照 version 必须为 1。");
      await save("/policy/snapshots", { kind, snapshot });
      setImportOpen(false);
      setContent("");
      setDone("快照已保存。导入不会自动修改会话或下发配置。");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const simulate = async () => {
    setError("");
    setDecisions(null);
    setBusy(true);
    try {
      if (!peerId) throw new Error("请选择需要检查的 BGP 会话。");
      if (requireIrr && !irrId)
        throw new Error("要求 IRR 匹配时，请选择 IRR 快照。");
      const lines = routeText
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean);
      if (!lines.length) throw new Error("请至少输入一条待检查路由。");
      const routes = lines.map((line, index) => {
        const [prefix, origin, ...path] = line.split(/[\s,]+/);
        const parseAsn = (s: string) => {
          const value = Number(s?.replace(/^AS/i, ""));
          if (!Number.isInteger(value) || value < 1 || value > 4294967294)
            throw new Error(`第 ${index + 1} 行的 ASN 无效。`);
          return value;
        };
        return {
          prefix,
          originAsn: parseAsn(origin),
          ...(path.length ? { asPath: path.map(parseAsn) } : {}),
        };
      });
      const data = await save<{ decisions: Decision[] }>("/policy/simulate", {
        peerId,
        routes,
        ...(irrId ? { irrSnapshotId: irrId } : {}),
        ...(rpkiId ? { rpkiSnapshotId: rpkiId } : {}),
        requireIrr,
        rejectRpkiNotFound: rejectNotFound,
      });
      setDecisions(data.decisions);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const candidates = async () => {
    setError("");
    setBusy(true);
    setProposal(null);
    try {
      if (!peerId || !irrId) throw new Error("请先选择会话和 IRR 快照。");
      const data = await save<
        Omit<Proposal, "irrSnapshotId" | "includeASSetOrigins">
      >("/policy/candidates", {
        peerId,
        irrSnapshotId: irrId,
        includeASSetOrigins,
      });
      setProposal({ ...data, irrSnapshotId: irrId, includeASSetOrigins });
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const apply = async () => {
    if (!proposal) return;
    setBusy(true);
    setFormError("");
    try {
      await save("/policy/apply", {
        peerId: proposal.peerId,
        irrSnapshotId: proposal.irrSnapshotId,
        baseChecksum: proposal.baseChecksum,
        includeASSetOrigins: proposal.includeASSetOrigins,
      });
      setConfirm(false);
      setProposal(null);
      setDone("会话白名单已更新。请到配置中心生成并审查新的路由配置。");
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!deleting) return;
    setBusy(true);
    setFormError("");
    try {
      await api(`/policy/snapshots/${deleting.id}`, { method: "DELETE" });
      setDeleting(null);
      setProposal(null);
      await load();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="路由策略工作台"
        description="导入带时效的 IRR / ROA 快照，检查路由并审查前缀白名单变更。"
      >
        {canWrite && (
          <Button
            onClick={() => {
              setImportOpen(true);
              setFormError("");
            }}
          >
            <Plus />
            导入快照
          </Button>
        )}
      </Heading>
      <ErrorNotice message={error} />
      {done && (
        <p className="notice mb-5 flex items-center gap-2" role="status">
          <Check className="size-4" />
          {done}
        </p>
      )}
      <div className="notice mb-6 flex items-start gap-3">
        <ShieldCheck className="mt-1 size-4 shrink-0" />
        <span>
          快照模拟用于审查策略，不代表实时互联网状态。保存白名单仅更新会话资料；实际生效仍需生成、校验并应用路由配置。过期快照不能用于模拟或应用。
        </span>
      </div>
      <Card className="overflow-hidden">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>策略数据快照</CardTitle>
          <Button
            size="icon"
            variant="ghost"
            disabled={loading}
            aria-label="刷新策略快照"
            onClick={() => void load()}
          >
            <RefreshCw />
          </Button>
        </CardHeader>
        {loading ? (
          <Loading />
        ) : !snapshots.length ? (
          <Empty
            title="还没有策略快照"
            description="导入已获取的类型化 JSON，保留来源、获取时间与有效期。"
          />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>类型 / 来源</th>
                  <th>条目</th>
                  <th>获取时间</th>
                  <th>有效期</th>
                  <th>校验和</th>
                  {canWrite && <th className="text-right">操作</th>}
                </tr>
              </thead>
              <tbody>
                {snapshots.map((s) => (
                  <tr key={s.id}>
                    <td>
                      <div className="flex items-center gap-2">
                        <Badge variant="outline">
                          {s.kind === "irr" ? "IRR" : "ROA"}
                        </Badge>
                        <span>{s.source}</span>
                      </div>
                      {s.asSet && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {s.asSet}
                        </p>
                      )}
                    </td>
                    <td>{s.entryCount}</td>
                    <td className="text-xs text-muted-foreground">
                      {time(s.fetchedAt)}
                    </td>
                    <td>
                      <p className="text-xs">{time(s.expiresAt)}</p>
                      {Date.parse(s.expiresAt) <= Date.now() && (
                        <Badge variant="destructive" className="mt-1">
                          已过期
                        </Badge>
                      )}
                    </td>
                    <td className="mono text-xs" title={s.checksum}>
                      {s.checksum.slice(0, 12)}…
                    </td>
                    {canWrite && (
                      <td className="text-right">
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`删除快照 ${s.source}`}
                          onClick={() => {
                            setDeleting(s);
                            setFormError("");
                          }}
                        >
                          <Trash2 />
                        </Button>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <Card className="mt-6">
        <CardHeader>
          <CardTitle>路由模拟</CardTitle>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="form-grid">
            <div className="field">
              <label htmlFor="policy-peer">BGP 会话</label>
              <select
                id="policy-peer"
                value={peerId}
                onChange={(e) => setPeerId(e.target.value)}
              >
                <option value="">选择会话</option>
                {peers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name} · AS{String(p.asn)}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="policy-irr">IRR 快照</label>
              <select
                id="policy-irr"
                value={irrId}
                onChange={(e) => setIrrId(e.target.value)}
              >
                <option value="">不使用 IRR 快照</option>
                {snapshots
                  .filter((s) => s.kind === "irr")
                  .map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.source} · {time(s.fetchedAt)}
                      {Date.parse(s.expiresAt) <= Date.now()
                        ? "（已过期）"
                        : ""}
                    </option>
                  ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="policy-rpki">ROA 快照</label>
              <select
                id="policy-rpki"
                value={rpkiId}
                onChange={(e) => setRpkiId(e.target.value)}
              >
                <option value="">不使用 ROA 快照</option>
                {snapshots
                  .filter((s) => s.kind === "rpki")
                  .map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.source} · {time(s.fetchedAt)}
                      {Date.parse(s.expiresAt) <= Date.now()
                        ? "（已过期）"
                        : ""}
                    </option>
                  ))}
              </select>
            </div>
            <div className="flex flex-col justify-center gap-3">
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={requireIrr}
                  onChange={(e) => setRequireIrr(e.target.checked)}
                />
                要求 IRR 来源匹配
              </label>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={rejectNotFound}
                  onChange={(e) => setRejectNotFound(e.target.checked)}
                />
                拒绝未找到 ROA 的路由
              </label>
            </div>
          </div>
          <div className="field">
            <label htmlFor="policy-routes">待检查路由</label>
            <textarea
              id="policy-routes"
              className="mono min-h-28"
              value={routeText}
              onChange={(e) => setRouteText(e.target.value)}
              placeholder="198.51.100.0/24 64513\n203.0.113.0/24 64496 64513 64496"
            />
            <p className="text-xs leading-6 text-muted-foreground">
              每行填写：前缀、源 ASN、可选 AS
              路径，以空格或逗号分隔。省略路径只检查邻居自身起源的路由；传递其他源
              ASN 时需要提供路径。
            </p>
          </div>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={includeASSetOrigins}
              onChange={(e) => setIncludeASSetOrigins(e.target.checked)}
            />
            候选白名单包含 AS-SET 下游起源 ASN
          </label>
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy || !peerId} onClick={() => void simulate()}>
              <Play />
              {busy ? "处理中…" : "模拟路由"}
            </Button>
            <Button
              variant="outline"
              disabled={busy || !peerId || !irrId}
              onClick={() => void candidates()}
            >
              <FileJson />
              生成白名单候选
            </Button>
          </div>
        </CardContent>
      </Card>
      {decisions && (
        <Card className="mt-6 overflow-hidden">
          <CardHeader>
            <CardTitle>模拟结果</CardTitle>
            <p className="text-xs text-muted-foreground">
              通过 {decisions.filter((d) => d.accepted).length} /{" "}
              {decisions.length} 条路由 · 仅模拟，不修改配置
            </p>
          </CardHeader>
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  <th>前缀 / 源 ASN</th>
                  <th>决定</th>
                  <th>RPKI</th>
                  <th>路径</th>
                  <th>依据</th>
                </tr>
              </thead>
              <tbody>
                {decisions.map((d, i) => (
                  <tr key={i}>
                    <td className="mono text-xs">
                      {d.prefix}
                      <br />
                      AS{d.originAsn}
                    </td>
                    <td>
                      <Badge variant={d.accepted ? "default" : "destructive"}>
                        {d.accepted ? "允许" : "拒绝"}
                      </Badge>
                    </td>
                    <td>{rpkiLabels[d.rpki]}</td>
                    <td>{d.pathValidated ? "路径已检查" : "仅检查源 ASN"}</td>
                    <td className="max-w-sm whitespace-normal text-xs leading-6 text-muted-foreground">
                      {d.reasons.length
                        ? d.reasons.join("；")
                        : "满足当前策略条件"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
      {proposal && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle>白名单变更审查</CardTitle>
            <p className="text-xs text-muted-foreground">
              来源 {proposal.source} · 基于当前白名单{" "}
              {proposal.baseChecksum.slice(0, 12)}…
            </p>
          </CardHeader>
          <CardContent>
            <div className="grid gap-4 md:grid-cols-2">
              <div className="rounded-lg border border-green-200 bg-green-50/50 p-4">
                <h3 className="mb-3 text-sm font-medium text-green-800">
                  新增 {proposal.added.length} 个前缀
                </h3>
                <pre className="max-h-64 overflow-auto text-xs leading-6 text-green-800">
                  {proposal.added.join("\n") || "无新增"}
                </pre>
              </div>
              <div className="rounded-lg border border-red-200 bg-red-50/50 p-4">
                <h3 className="mb-3 text-sm font-medium text-red-800">
                  移除 {proposal.removed.length} 个前缀
                </h3>
                <pre className="max-h-64 overflow-auto text-xs leading-6 text-red-800">
                  {proposal.removed.join("\n") || "无移除"}
                </pre>
              </div>
            </div>
            <details className="mt-4 rounded-lg border p-4">
              <summary className="cursor-pointer text-xs">
                候选完整白名单（{proposal.prefixes.length} 个前缀）
              </summary>
              <p className="mt-3 text-xs text-muted-foreground">
                以完整的前缀与源 ASN 绑定为准；相同前缀可对应多个起源。
              </p>
              <pre className="mt-3 max-h-64 overflow-auto text-xs leading-6">
                {proposal.allowedRoutes?.length
                  ? proposal.allowedRoutes
                      .map((route) => `${route.prefix}  AS${route.originAsn}`)
                      .join("\n")
                  : proposal.prefixes.join("\n")}
              </pre>
            </details>
            {canWrite && (
              <div className="mt-5 flex justify-end">
                <Button
                  disabled={busy}
                  onClick={() => {
                    setConfirm(true);
                    setFormError("");
                  }}
                >
                  保存已审查的白名单
                </Button>
              </div>
            )}
          </CardContent>
        </Card>
      )}
      <Dialog open={importOpen} onOpenChange={(v) => !busy && setImportOpen(v)}>
        <DialogContent className="max-w-2xl">
          <DialogTitle className="text-lg font-semibold">
            导入策略快照
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            需要 version: 1、source、fetchedAt、expiresAt；IRR 包含 asSet / asns
            / routes，ROA 包含 roas。只接受结构化数据。
          </DialogDescription>
          <form onSubmit={ingest} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="snapshot-kind">快照类型</label>
              <select
                id="snapshot-kind"
                value={kind}
                onChange={(e) => setKind(e.target.value as "irr" | "rpki")}
              >
                <option value="irr">IRR 路由注册快照</option>
                <option value="rpki">RPKI ROA 快照</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="snapshot-file">
                选择 JSON 文件（也可直接粘贴）
              </label>
              <Input
                id="snapshot-file"
                type="file"
                accept=".json,application/json"
                onChange={async (e) => {
                  const f = e.target.files?.[0];
                  if (!f) return;
                  if (f.size > 128 * 1024 * 1024) {
                    setFormError("文件不能超过 128 MiB。");
                    return;
                  }
                  setContent(await f.text());
                  setFormError("");
                }}
              />
            </div>
            <div className="field">
              <label htmlFor="snapshot-json">快照 JSON</label>
              {content.length > 2 * 1024 * 1024 && (
                <p className="notice">
                  文件较大，仅预览前 4000 个字符；导入时会验证完整内容。
                  <button
                    type="button"
                    className="ml-2 underline"
                    onClick={() => setContent("")}
                  >
                    清空内容
                  </button>
                </p>
              )}
              <textarea
                id="snapshot-json"
                className="mono min-h-64 text-xs"
                required
                value={
                  content.length > 2 * 1024 * 1024
                    ? content.slice(0, 4000)
                    : content
                }
                readOnly={content.length > 2 * 1024 * 1024}
                onChange={(e) => setContent(e.target.value)}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setImportOpen(false)}
              >
                取消
              </Button>
              <Button disabled={busy}>
                {busy ? "正在验证…" : "验证并导入"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog open={confirm} onOpenChange={(v) => !busy && setConfirm(v)}>
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            保存白名单变更？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将按审查结果更新此会话的允许前缀。如果其他人已经修改白名单，会拒绝覆盖并要求重新审查。本次操作不会部署路由器配置。
          </DialogDescription>
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
              确认保存白名单
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleting}
        onOpenChange={(v) => !busy && !v && setDeleting(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">删除快照</DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            删除「{deleting?.source}
            」后，不能再使用此快照进行模拟或生成候选白名单。已保存的会话配置保持原样。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setDeleting(null)}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => void remove()}
            >
              确认删除快照
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}
