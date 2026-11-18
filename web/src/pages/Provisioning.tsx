import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import {
  CheckCheck,
  Download,
  FileCode2,
  Plus,
  RefreshCw,
  Settings2,
  ShieldCheck,
  Upload,
} from "lucide-react";
import { api, save, APIError, captureSessionContext } from "@/lib/api";
import { statusLabels } from "@/lib/resources";
import {
  provisioningLabels,
  verifyProvisioningDownload,
  type ProvisioningRevision,
  type ProvisioningDiff,
  type ProvisioningProfile,
  type ProvisioningCapability,
} from "@/lib/provisioning";
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
import ArtifactDiff from "@/components/ArtifactDiff";
import ProvisioningProfiles from "@/components/ProvisioningProfiles";
import AS112BundlePreview from "@/components/AS112BundlePreview";
import { readAS112Bundle, type AS112Bundle } from "@/lib/as112";
import { useHashParameters } from "@/lib/hash-route";
import { RFC2317_MERGE_KIND, RFC2317_MERGE_MAX_BYTES, isRFC2317Metadata, sameRFC2317Metadata, type RFC2317Metadata } from "@/lib/rfc2317";

type Detail = ProvisioningRevision & { config: string; redacted: true };

function revisionLabel(kind: string) {
  return kind === RFC2317_MERGE_KIND ? "RFC 2317 父区和子区" : provisioningLabels[kind] ?? kind;
}

function sameReview(current: ProvisioningRevision | undefined, response: ProvisioningRevision) {
  return !!current && !!response && response.id === current.id && response.profileId === current.profileId &&
    response.checksum === current.checksum && response.kind === current.kind && response.routerId === current.routerId &&
    (current.kind !== RFC2317_MERGE_KIND || sameRFC2317Metadata(current.rfc2317, response.rfc2317));
}

function RFC2317Summary({ pair }: { pair: RFC2317Metadata }) {
  return (
    <dl aria-label="RFC 2317 父区与子区" className="mb-4 grid gap-3 rounded-lg border p-4 text-sm sm:grid-cols-2">
      <div className="min-w-0">
        <dt className="font-medium">父区</dt>
        <dd className="mono mt-1 break-all text-xs">{pair.parentZone}</dd>
        <dd className="mt-1 text-xs leading-5 text-muted-foreground">SOA {pair.previousParentSerial} → {pair.parentSerial} · 新增 {pair.addedRecords} 条记录</dd>
      </div>
      <div className="min-w-0">
        <dt className="font-medium">子区</dt>
        <dd className="mono mt-1 break-all text-xs">{pair.childZone}</dd>
        <dd className="mt-1 text-xs leading-5 text-muted-foreground">{pair.newChildZone ? `新建区 · SOA ${pair.childSerial}` : `SOA ${pair.previousChildSerial} → ${pair.childSerial}`}</dd>
      </div>
    </dl>
  );
}

export default function Provisioning({
  role,
  onChange,
}: {
  role: string;
  onChange: () => void;
}) {
  const targetProfile = useHashParameters("provisioning").get("profileId") ?? "";
  const canWrite = role === "admin" || role === "operator";
  const [section, setSection] = useState("revisions"),
    [view, setView] = useState("config"),
    [items, setItems] = useState<ProvisioningRevision[]>([]),
    [profiles, setProfiles] = useState<ProvisioningProfile[]>([]),
    [capabilities, setCapabilities] = useState<ProvisioningCapability[]>([]),
    [selected, setSelected] = useState(""),
    [profileId, setProfileId] = useState(""),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [version, setVersion] = useState(0);
  const [detail, setDetail] = useState<Detail | null>(null),
    [detailVersion, setDetailVersion] = useState(-1),
    [detailError, setDetailError] = useState(""),
    [diff, setDiff] = useState<(ProvisioningDiff & { id: string }) | null>(
      null,
    ),
    [diffError, setDiffError] = useState(""),
    [generate, setGenerate] = useState(false),
    [confirm, setConfirm] = useState<{ id: string; checksum: string } | null>(null),
    [importOpen, setImportOpen] = useState(false),
    [importFile, setImportFile] = useState<File | null>(null),
    [cleanup, setCleanup] = useState<"prune" | "delete" | null>(null),
    [formError, setFormError] = useState(""),
    [receipt, setReceipt] = useState<{ id: string; sha256: string } | null>(
      null,
    );
  const alive = useRef(true);
  const detailEpoch = useRef(0);
  useLayoutEffect(() => {
    detailEpoch.current++;
    return () => { detailEpoch.current++; };
  }, [role]);
  useEffect(() => {
    if (targetProfile) {
      detailEpoch.current++;
      setConfirm(null);
      setSection("profiles");
    }
  }, [targetProfile]);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const current = items.find((item) => item.id === selected),
    unavailable = !!(current?.pruned || current?.artifactUnavailable),
    selectedProfile = profiles.find((profile) => profile.id === profileId);
  const isRFC2317 = current?.kind === RFC2317_MERGE_KIND;
  const rfc2317Pair = isRFC2317 && isRFC2317Metadata(current?.rfc2317) ? current.rfc2317 : null;
  const displayView = isRFC2317 ? "config" : view;
  const canImport = canWrite && capabilities.some(capability => capability.kind === RFC2317_MERGE_KIND);
  let as112Bundle: AS112Bundle | null = null;
  if (current?.kind === "as112-dns" && detail?.id === current.id) {
    try { as112Bundle = readAS112Bundle(detail.config, current.routerId ?? ""); } catch { /* Failed detail validation is displayed below. */ }
  }
  const ready = !!(current && detail && !loading && !unavailable && detailVersion === version && sameReview(current, detail) && (current.kind !== "as112-dns" || as112Bundle));
  const currentCapability = capabilities.find(
    (capability) => capability.kind === current?.kind,
  );
  const nativeUnavailable = isRFC2317
    ? currentCapability?.validationLevel !== "native" || !currentCapability.nativeCheckerAvailable
    : currentCapability?.validationLevel === "native" && !currentCapability.nativeCheckerAvailable;
  const selectRevision = (id: string) => {
    if (id === selected) return;
    detailEpoch.current++;
    setConfirm(null);
    setReceipt(null);
    setSelected(id);
  };
  const showProfiles = () => {
    if (section !== "profiles") {
      detailEpoch.current++;
      setConfirm(null);
      setSection("profiles");
    }
  };
  const captureReviewContext = () => {
    const epoch = detailEpoch.current;
    const sessionCurrent = captureSessionContext();
    return () => alive.current && epoch === detailEpoch.current && sessionCurrent();
  };
  const load = async () => {
    detailEpoch.current++;
    setDetail(null);
    setDetailVersion(-1);
    setLoading(true);
    setError("");
    try {
      const [versions, rules, caps] = await Promise.all([
        api<{ items: ProvisioningRevision[] }>("/provisioning"),
        api<{ items: ProvisioningProfile[] }>(
          "/resources/provisioning-profiles",
        ),
        api<{ items: ProvisioningCapability[] }>("/provisioning/capabilities"),
      ]);
      if (!alive.current) return;
      setItems(
        versions.items.sort((a, b) => b.createdAt.localeCompare(a.createdAt)),
      );
      setProfiles(rules.items);
      setCapabilities(caps.items);
      setSelected((id) =>
        versions.items.some((row) => row.id === id)
          ? id
          : (versions.items[0]?.id ?? ""),
      );
      setVersion((v) => v + 1);
    } catch (e) {
      if (alive.current) { detailEpoch.current++; setDetail(null); setDetailVersion(-1); setError(message(e)); }
    } finally {
      if (alive.current) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    const epoch = ++detailEpoch.current;
    setDetail(null);
    setDetailVersion(-1);
    setDetailError("");
    setReceipt(null);
    setConfirm(null);
    if (!selected || unavailable) return;
    const controller = new AbortController();
    api<Detail>(`/provisioning/${selected}`, { signal: controller.signal })
      .then((data) => {
        if (controller.signal.aborted || epoch !== detailEpoch.current) return;
        if (data.redacted !== true || typeof data.config !== "string")
          throw new Error("服务端未提供经过安全处理的配置预览。");
        if (!sameReview(current, data))
          throw new Error("配置预览的规则、目标或审阅标识不匹配，已禁用后续操作。");
        if (data.kind === "as112-dns") readAS112Bundle(data.config, data.routerId ?? "");
        setDetail(data);
        setDetailVersion(version);
      })
      .catch((e) => {
        if (controller.signal.aborted || epoch !== detailEpoch.current) return;
        if (e instanceof APIError && e.status === 410)
          setItems((rows) =>
            rows.map((row) =>
              row.id === selected
                ? { ...row, pruned: true, artifactUnavailable: true }
                : row,
            ),
          );
        else setDetailError(message(e));
      });
    return () => controller.abort();
  }, [selected, version, unavailable, current?.checksum, current?.routerId, role]);
  useEffect(() => {
    setDiff(null);
    setDiffError("");
    if (displayView !== "diff" || !selected || unavailable) return;
    const controller = new AbortController();
    api<ProvisioningDiff>(`/provisioning/${selected}/diff`, {
      signal: controller.signal,
    })
      .then((data) => {
        if (!controller.signal.aborted) {
          if (data.redacted !== true)
            throw new Error("服务端未提供经过安全处理的差异。");
          setDiff({ ...data, id: selected });
        }
      })
      .catch((e) => {
        if (!controller.signal.aborted) setDiffError(message(e));
      });
    return () => controller.abort();
  }, [selected, displayView, version, unavailable]);
  const create = async () => {
    if (!profileId) return;
    setBusy(true);
    setFormError("");
    try {
      const data = await save<ProvisioningRevision>("/provisioning", {
        profileId,
      });
      setItems((rows) => [data, ...rows]);
      selectRevision(data.id);
      setView("config");
      setGenerate(false);
      onChange();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const uploadRFC2317 = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canImport || !importOpen || !importFile || busy || loading) return;
    if (importFile.size === 0 || importFile.size > RFC2317_MERGE_MAX_BYTES) {
      setFormError("请选择一个非空的 RFC 2317 JSON 合并包，大小不超过 16 MiB。");
      return;
    }
    const stillCurrent = captureReviewContext();
    setBusy(true);
    setFormError("");
    try {
      const data = await api<ProvisioningRevision>("/provisioning/rfc2317-import", {
        method: "POST",
        body: importFile,
        redirect: "error",
      });
      if (!stillCurrent()) return;
      if (!data || typeof data.id !== "string" || !data.id || typeof data.profileId !== "string" || !data.profileId ||
        data.kind !== RFC2317_MERGE_KIND || data.status !== "draft" || data.checksumKind !== "hmac-review-token" ||
        typeof data.checksum !== "string" || !/^[0-9a-f]{64}$/i.test(data.checksum) || !isRFC2317Metadata(data.rfc2317))
        throw new Error("服务端未返回绑定两个区的待审版本，请刷新版本列表核对。");
      setItems((rows) => [data, ...rows.filter(row => row.id !== data.id)]);
      selectRevision(data.id);
      setView("config");
      setImportOpen(false);
      setImportFile(null);
      onChange();
    } catch (e) {
      if (stillCurrent()) setFormError(message(e));
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const canApproveCurrent = !!(role === "admin" && current && current.status === "validated" && ready && confirm?.id === current.id && confirm.checksum === current.checksum);
  const action = async (type: "validate" | "approve") => {
    if (!current || !ready || busy || !canWrite ||
      (type === "validate" ? current.status !== "draft" || nativeUnavailable : !canApproveCurrent)) return;
    const stillCurrent = captureReviewContext();
    setBusy(true);
    setError("");
    try {
      const data = await save<ProvisioningRevision>(
        `/provisioning/${current.id}/${type}`,
        type === "approve" ? { checksum: current.checksum } : {},
      );
      if (!stillCurrent()) return;
      if (isRFC2317 && (!sameReview(current, data) || data.status !== (type === "approve" ? "approved" : "validated")))
        throw new Error("服务端返回的审核结果与当前父区和子区不匹配，请刷新后核对。");
      setItems((rows) => rows.map((row) => (row.id === data.id ? data : row)));
      setConfirm(null);
      setVersion((v) => v + 1);
      onChange();
    } catch (e) {
      if (stillCurrent()) {
        setError(message(e));
        setConfirm(null);
        setDetail(null);
        setDetailVersion(-1);
      }
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const download = async () => {
    if (!current || !ready || busy || !canWrite || current.status !== "approved" || unavailable)
      return;
    const stillCurrent = captureReviewContext();
    setBusy(true);
    setError("");
    setReceipt(null);
    try {
      if (!crypto.subtle)
        throw new Error("需要 HTTPS 或 localhost 才能核对下载文件。");
      const response = await fetch(`/api/provisioning/${current.id}/config`, {
        credentials: "same-origin",
        redirect: "error",
      });
      if (!stillCurrent()) return;
      if (!response.ok) {
        let reason = `下载失败 (${response.status})`;
        try {
          reason = (await response.json()).error || reason;
        } catch {}
        throw new Error(reason);
      }
      const blob = await response.blob();
      if (!stillCurrent()) return;
      if (isRFC2317 && blob.size > RFC2317_MERGE_MAX_BYTES)
        throw new Error("RFC 2317 下载包超过 16 MiB，未保存文件。");
      const actual = await verifyProvisioningDownload(current, response.headers, await blob.arrayBuffer());
      if (!stillCurrent()) return;
      const url = URL.createObjectURL(blob),
        link = document.createElement("a");
      link.href = url;
      link.download =
        current.filename && /^[A-Za-z0-9_.-]+$/.test(current.filename)
          ? current.filename
          : `${current.kind}-${current.id}.${["switch-core", "as112-dns", RFC2317_MERGE_KIND].includes(current.kind) ? "json" : "conf"}`;
      link.click();
      URL.revokeObjectURL(url);
      setReceipt({ id: current.id, sha256: actual });
    } catch (e) {
      if (stillCurrent()) { setError(message(e)); setDetail(null); setDetailVersion(-1); }
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const clean = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (!current || !cleanup) return;
    setBusy(true);
    setFormError("");
    const form = new FormData(e.currentTarget);
    try {
      const body = {
        checksum: current.checksum,
        acknowledge: true,
        reason: String(form.get("reason") || ""),
      };
      if (cleanup === "prune") {
        const data = await save<ProvisioningRevision>(
          `/provisioning/${current.id}/prune`,
          body,
        );
        setItems((rows) =>
          rows.map((row) => (row.id === data.id ? data : row)),
        );
        setVersion((v) => v + 1);
      } else {
        await save(`/provisioning/${current.id}`, body, "DELETE");
        await load();
      }
      setCleanup(null);
      setReceipt(null);
      onChange();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Heading
        title="配置工具"
        description="从网络库存生成 DNS、监测配置或交换机核心网络计划，经版本审查后供目标环境使用。"
      >
        {canWrite && section === "revisions" && (
          <div className="flex flex-wrap gap-2">
            {canImport && (
              <Button variant="outline" disabled={busy || loading} onClick={() => {
                setImportFile(null);
                setFormError("");
                setImportOpen(true);
              }}>
                <Upload />
                导入 RFC 2317 合并包
              </Button>
            )}
            <Button onClick={() => {
              setGenerate(true);
              setFormError("");
            }}>
              <Plus />
              生成工具配置
            </Button>
          </div>
        )}
      </Heading>
      <div className="notice mb-5">
        本工具只生成和审核文件，不连接目标服务，也不修改在线配置。Nagios 与
        Smokeping 生成的是依赖目标环境的片段，批准后仍须在目标完整配置中校验。
        交换机计划是供目标适配器处理的 JSON，需要另行生成并验证设备原生配置。
      </div>
      <div className="mb-5 flex gap-2" aria-label="配置工具工作区">
        <Button
          variant={section === "revisions" ? "secondary" : "ghost"}
          aria-pressed={section === "revisions"}
          onClick={() => {
            setSection("revisions");
            void load();
          }}
        >
          <FileCode2 />
          生成版本
        </Button>
        <Button
          variant={section === "profiles" ? "secondary" : "ghost"}
          aria-pressed={section === "profiles"}
          onClick={showProfiles}
        >
          <Settings2 />
          生成规则
        </Button>
      </div>
      <ErrorNotice message={error} />
      {targetProfile && section === "profiles" && (
        <div className="notice mb-4 flex flex-wrap items-center justify-between gap-2" role="status">
          <span>{loading ? "正在定位生成规则…" : profiles.some((profile) => profile.id === targetProfile) ? "已定位搜索结果" : "此生成规则已删除，或你已失去访问权限。"}</span>
          <Button variant="outline" size="sm" onClick={() => { location.hash = "provisioning"; }}>查看全部规则</Button>
        </div>
      )}
      {section === "profiles" ? (
        loading && !profiles.length ? (
          <Loading />
        ) : (
          <ProvisioningProfiles
            profiles={targetProfile ? profiles.filter((profile) => profile.id === targetProfile) : profiles}
            role={role}
            onChange={() => {
              void load();
              onChange();
            }}
          />
        )
      ) : (
        <>
          {loading && !items.length ? (
            <Loading />
          ) : !items.length ? (
            <Card>
              <Empty
                title="还没有工具配置版本"
                description="先建立生成规则，选择工具、网络范围和固定选项，再生成首个可审核版本。"
                action={canWrite ? "管理生成规则" : undefined}
                onAction={showProfiles}
              />
            </Card>
          ) : (
            <div className="deployment-grid mt-5">
              <Card className="self-start overflow-hidden">
                <div className="flex items-center justify-between border-b px-5 py-4">
                  <h2 className="text-sm font-semibold">工具配置版本</h2>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label="刷新工具配置版本"
                    disabled={busy || loading}
                    onClick={() => void load()}
                  >
                    <RefreshCw />
                  </Button>
                </div>
                <div className="max-h-[650px] overflow-y-auto">
                  {items.map((item) => (
                    <button
                      key={item.id}
                      data-provisioning-id={item.id}
                      onClick={() => selectRevision(item.id)}
                      className={`flex w-full gap-3 border-b px-5 py-4 text-left ${selected === item.id ? "bg-primary/5" : "hover:bg-muted/40"}`}
                    >
                      <FileCode2 className="mt-1 size-5 shrink-0 text-primary" />
                      <div className="min-w-0">
                        <p className="truncate text-sm font-medium">
                          {item.profileName || item.profileId}
                        </p>
                        <p className="mt-1 text-xs text-muted-foreground">
                          {revisionLabel(item.kind)} ·{" "}
                          {time(item.createdAt)}
                        </p>
                        <Badge
                          className="mt-2"
                          variant={
                            item.status === "draft" ? "outline" : "default"
                          }
                        >
                          {statusLabels[item.status] ?? item.status}
                        </Badge>
                        {item.pruned && (
                          <p className="mt-1 text-xs text-muted-foreground">
                            文件已清理
                          </p>
                        )}
                      </div>
                    </button>
                  ))}
                </div>
              </Card>
              {current && (
                <Card className="min-w-0">
                  <CardHeader>
                    <div className="flex flex-wrap justify-between gap-2">
                      <CardTitle>
                        {current.profileName || "工具配置预览"}
                      </CardTitle>
                      <Badge variant="outline">
                        {revisionLabel(current.kind)}
                      </Badge>
                    </div>
                    <p className="mono mt-2 break-all text-[11px] leading-5 text-muted-foreground">
                      版本校验标识：{current.checksum}
                    </p>
                    <p className="text-xs leading-6 text-muted-foreground">
                      此标识绑定审批版本，不能代替下载文件的 SHA-256。
                    </p>
                  </CardHeader>
                  <CardContent>
                    <p className="notice mb-4">
                      {current.validationNotice ||
                        "校验范围以服务端返回的结果为准，下载后仍需核对目标环境。"}
                    </p>
                    {current.kind === "as112-dns" && as112Bundle && ready && <AS112BundlePreview bundle={as112Bundle} />}
                    {rfc2317Pair && <RFC2317Summary pair={rfc2317Pair} />}
                    {current.kind === "dns-arpa" && (
                      <p className="mb-4 text-xs leading-6 text-muted-foreground">
                        DNS 序列号由服务端按 RFC 1982 前进分配，最大值后回绕到 1。为防止 SOA
                        回退，旧批准文件可能不能再次下载；需要回滚内容时，请修改规则或库存并重新生成、校验和批准新的版本。
                      </p>
                    )}
                    {current.kind === "switch-core" && (
                      <div className="mb-4 rounded-lg border p-4 text-xs leading-6">
                        <p>目标交换机 ID：<span className="mono break-all" aria-label="计划目标交换机">{current.switchId || "目标标识缺失，不能下载"}</span></p>
                        <p className="mt-1 text-muted-foreground">包含完整 IPv4 / IPv6 核心接口与 BGP 计划；仅完成类型和关联结构校验，必须交由目标适配器生成设备配置后再原生验证。下载和批准均不表示已经安装。</p>
                      </div>
                    )}
                    {nativeUnavailable && (
                      <p className="notice mb-4">
                        {current.kind === "as112-dns" ? "服务端尚未配置全部 AS112 原生校验器。可以生成预览；配置 PowerDNS Authoritative、BIND 后端和 named-checkzone 后刷新，实际解析成功才可批准。" : isRFC2317 ? "服务端缺少已确认的 named-checkzone 校验器。预览可用，校验和审批需要先配置校验器。" : "服务端尚未配置可用的 DNS 原生校验器。可以生成和查看预览；启用 named-checkzone 后刷新此页，完成原生检查才可批准。"}
                      </p>
                    )}
                    <div className="mb-4 flex flex-wrap gap-2">
                      <Badge variant="outline">
                        {current.validationLevel === "native"
                          ? current.status === "draft" ? "等待原生语法校验" : "原生语法校验通过"
                          : current.validationLevel === "structural"
                            ? current.kind === "switch-core" ? "计划结构校验" : "片段结构校验"
                            : current.validationLevel === "native-available"
                              ? "等待原生语法校验"
                              : "尚未完成校验"}
                      </Badge>
                      <Badge variant="outline">
                        {statusLabels[current.status] ?? current.status}
                      </Badge>
                      {current.kind === "dns-arpa" &&
                        typeof current.dnsSerial === "number" && (
                          <Badge variant="outline" aria-label="DNS SOA 序列号">
                            SOA serial {current.dnsSerial}
                          </Badge>
                        )}
                      {!isRFC2317 && typeof current.targetCount === "number" && (
                        <Badge variant="outline">
                          {current.targetCount} 个生成目标
                        </Badge>
                      )}
                      {typeof current.complete === "boolean" && (
                        <Badge variant="outline">
                          {isRFC2317 ? "两份完整区文件" : current.kind === "as112-dns" ? "多文件 JSON 服务包 · 尚未安装" : current.kind === "switch-core" ? "JSON 计划 · 需要目标适配器" : current.complete ? "完整文件" : "目标配置片段"}
                        </Badge>
                      )}
                    </div>
                    {!!current.notices?.length && (
                      <details
                        className="mb-4 rounded-lg border p-4"
                        open={current.targetCount === 0}
                      >
                        <summary className="cursor-pointer text-sm font-medium">
                          生成说明与跳过项（{current.notices.length}）
                        </summary>
                        <ul className="mt-3 space-y-2 text-xs leading-6 text-muted-foreground">
                          {current.notices.map((notice, index) => (
                            <li key={`${notice.code}-${index}`}>
                              <span className="font-medium">
                                {notice.severity === "warning" ? "注意：" : ""}
                              </span>
                              {notice.message}
                              {notice.targetId && (
                                <span className="mono ml-2 break-all">
                                  {notice.targetId}
                                </span>
                              )}
                            </li>
                          ))}
                        </ul>
                      </details>
                    )}
                    {isRFC2317 ? (
                      <h3 className="mb-3 text-sm font-medium">子区正文与父区计划变更</h3>
                    ) : <div className="mb-3 flex gap-2">
                      <Button
                        size="sm"
                        variant={view === "config" ? "secondary" : "ghost"}
                        aria-pressed={view === "config"}
                        onClick={() => setView("config")}
                      >
                        配置正文
                      </Button>
                      <Button
                        size="sm"
                        variant={view === "diff" ? "secondary" : "ghost"}
                        aria-pressed={view === "diff"}
                        onClick={() => setView("diff")}
                      >
                        版本差异
                      </Button>
                    </div>}
                    {unavailable ? (
                      <p className="notice">
                        此版本文件已清理，仅保留版本元数据和审查标识，不能继续预览、校验或下载。
                      </p>
                    ) : displayView === "config" ? (
                      detailError ? (
                        <ErrorNotice message={detailError} />
                      ) : !ready ? (
                        <Loading />
                      ) : (
                        <pre className="max-h-[540px] overflow-auto rounded-lg bg-[#242635] p-5 text-xs leading-6 text-[#dfddec]">
                          <code>{detail?.config}</code>
                        </pre>
                      )
                    ) : diffError ? (
                      <ErrorNotice message={diffError} />
                    ) : diff?.id === selected ? (
                      <ArtifactDiff diff={diff} />
                    ) : (
                      <Loading />
                    )}
                    {current.validation && (
                      <p className="mt-4 whitespace-pre-wrap rounded-lg border bg-muted/30 p-3 text-xs leading-6">
                        {current.validation}
                      </p>
                    )}
                    <div className="mt-5 flex flex-wrap justify-end gap-2">
                      {canWrite && (
                        <Button
                          variant="outline"
                          disabled={
                            busy || !ready || current.status !== "approved" || unavailable || (current.kind === "switch-core" && !current.switchId)
                          }
                          onClick={() => void download()}
                        >
                          <Download />
                          {isRFC2317 ? "下载已批准区包" : "下载已批准文件"}
                        </Button>
                      )}
                      {canWrite &&
                        current.status === "draft" &&
                        !unavailable && (
                          <Button
                            disabled={busy || !ready || nativeUnavailable}
                            onClick={() => void action("validate")}
                          >
                            <ShieldCheck />
                            {busy
                              ? "正在校验…"
                              : current.kind === "dns-arpa"
                                ? "运行 DNS 原生校验"
                                : isRFC2317 ? "原生校验父区和子区"
                                : current.kind === "as112-dns" ? "运行 AS112 原生校验"
                                : current.kind === "switch-core" ? "检查计划结构" : "检查片段结构"}
                          </Button>
                        )}
                      {role === "admin" &&
                        current.status === "validated" &&
                        !unavailable && (
                          <Button
                            disabled={busy || !ready}
                            onClick={() => setConfirm({ id: current.id, checksum: current.checksum })}
                          >
                            <CheckCheck />
                            审核批准
                          </Button>
                        )}
                    </div>
                    {receipt?.id === current.id && (
                      <div className="mt-4 rounded-lg border p-4">
                        <h3 className="text-sm font-semibold">
                          {isRFC2317 ? "已核对合并包和两份区文件的 SHA-256" : "已核对下载文件 SHA-256"}
                        </h3>
                        <p
                          aria-label="工具配置真实 SHA-256"
                          className="mono mt-2 break-all text-xs"
                        >
                          {receipt.sha256}
                        </p>
                      </div>
                    )}
                    {role === "admin" && (
                      <div className="mt-5 flex justify-end border-t pt-4">
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={busy}
                          onClick={() => {
                            setCleanup(current.pruned ? "delete" : "prune");
                            setFormError("");
                          }}
                        >
                          {current.pruned ? "删除已清理记录" : "清理旧版本文件"}
                        </Button>
                      </div>
                    )}
                  </CardContent>
                </Card>
              )}
            </div>
          )}
        </>
      )}
      <Dialog
        open={generate}
        onOpenChange={(value) => !busy && setGenerate(value)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            生成工具配置
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            使用已保存的规则与当前库存创建不可变版本。之后修改规则或库存，不会自动重写已有文件。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <div className="field">
            <label htmlFor="provisioning-profile">生成规则</label>
            <select
              id="provisioning-profile"
              value={profileId}
              onChange={(e) => setProfileId(e.target.value)}
            >
              <option value="">请选择生成规则</option>
              {profiles.map((profile) => (
                <option key={profile.id} value={profile.id}>
                  {profile.name} ·{" "}
                  {provisioningLabels[String(profile.options.kind)] ??
                    String(profile.options.kind)}
                </option>
              ))}
            </select>
            {selectedProfile && (
              <p className="text-xs leading-6 text-muted-foreground">
                {
                  capabilities.find(
                    (capability) =>
                      capability.kind === selectedProfile.options.kind,
                  )?.validationNotice
                }
              </p>
            )}
            {selectedProfile?.options.kind === "switch-core" && (
              <p className="text-xs leading-6 text-muted-foreground">目标交换机 ID：<span className="mono break-all">{selectedProfile.options.switchId}</span>。计划保留两个地址族，不使用监测范围筛选。</p>
            )}
            {!profiles.length && (
              <p className="notice">
                还没有生成规则。请先在“生成规则”中保存工具选项与网络范围。
              </p>
            )}
          </div>
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => setGenerate(false)}
            >
              取消
            </Button>
            <Button disabled={busy || !profileId} onClick={() => void create()}>
              {busy ? "正在生成…" : "生成待审版本"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={canImport && importOpen}
        onOpenChange={(value) => {
          if (!value && !busy) {
            setImportOpen(false);
            setImportFile(null);
          }
        }}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">导入 RFC 2317 合并包</DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            选择 <code>ixp-provision rfc2317-merge</code> 生成的原始 JSON 文件，创建父区和子区的待审版本。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <form onSubmit={uploadRFC2317} className="space-y-4">
            <div className="field">
              <label htmlFor="rfc2317-merge-file">RFC 2317 合并包</label>
              <input id="rfc2317-merge-file" type="file" accept=".json,application/json" required disabled={busy} className="min-w-0 w-full" onChange={event => setImportFile(event.currentTarget.files?.[0] ?? null)} />
              <p className="text-xs text-muted-foreground">最多 16 MiB</p>
            </div>
            <div className="flex flex-wrap justify-end gap-2">
              <Button type="button" variant="outline" disabled={busy} onClick={() => { setImportOpen(false); setImportFile(null); }}>取消</Button>
              <Button type="submit" disabled={busy || loading || !importFile}>{busy ? "正在导入…" : "创建待审版本"}</Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={confirm !== null && canApproveCurrent}
        onOpenChange={(value) => !value && !busy && setConfirm(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {isRFC2317 ? "联合批准父区和子区？" : "批准此工具配置版本？"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {isRFC2317 ? "本次审批绑定这两份区文件和当前版本校验标识。部署前仍需在目标服务器核对当前 SOA。" : "请核对地址、名称、网络范围及目标依赖。批准绑定当前版本校验标识，不表示已经应用到服务。"}
          </DialogDescription>
          {rfc2317Pair && <RFC2317Summary pair={rfc2317Pair} />}
          <p className="notice">{current?.validationNotice}</p>
          <p className="mono break-all rounded-lg bg-muted/40 p-3 text-xs">
            {current?.checksum}
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void action("approve");
            }}
            className="space-y-4"
          >
            <label className="flex items-start gap-2 text-sm leading-6">
              <input required type="checkbox" className="mt-1" />
              {isRFC2317 ? "我已审阅父区变更和子区记录。" : "我已完成版本审查，并了解目标完整配置仍需独立校验。"}
            </label>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => setConfirm(null)}
              >
                返回检查
              </Button>
              <Button disabled={busy || !canApproveCurrent}>
                {busy ? "正在批准…" : "确认审核批准"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={cleanup !== null}
        onOpenChange={(value) => !value && !busy && setCleanup(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {cleanup === "delete" ? "删除已清理版本记录？" : "清理旧版本文件？"}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            清理无法撤销，可能影响历史差异对比。请先备份需要留存的文件与版本信息。服务端保护当前批准版本，删除记录前必须先清理文件。
          </DialogDescription>
          <ErrorNotice message={formError} />
          <form onSubmit={clean} className="space-y-4">
            <div className="field">
              <label htmlFor="provisioning-cleanup-reason">
                操作原因{cleanup === "delete" ? "（必填）" : "（可选）"}
              </label>
              <textarea
                id="provisioning-cleanup-reason"
                name="reason"
                required={cleanup === "delete"}
                maxLength={1000}
              />
            </div>
            <label className="flex items-start gap-2 text-sm leading-6">
              <input required type="checkbox" className="mt-1" />
              已完成需要的备份，理解此次清理无法撤销。
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
                    ? "确认删除记录"
                    : "确认清理文件"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
