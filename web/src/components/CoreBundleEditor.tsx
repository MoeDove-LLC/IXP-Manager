import { useEffect, useRef, useState, type FormEvent } from "react";
import { Download, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import {
  coreDesired,
  corePorts,
  corePortLabel,
  coreTypes,
  coreEndpointStates,
  newCoreBundle,
  newCoreEndpoint,
  newCoreLink,
  type CoreBundle,
  type CoreBundleRecord,
  type CoreCapabilities,
  type CoreEndpoint,
  type CoreInventory,
  type CoreLegacyLink,
  type CoreList,
  type CorePreview,
} from "@/lib/core";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { ErrorNotice, Loading, message } from "@/components/shared";
import CorePlan from "@/components/CorePlan";

const emptyInventory: CoreInventory = { switches: [], ports: [], members: [] };
const pair = (a: string, b: string) => [a, b].sort().join("|");
type Conflict = {
  inventory: CoreInventory;
  current?: CoreBundleRecord;
  legacy: CoreLegacyLink[];
  deleted: boolean;
};
export default function CoreBundleEditor({
  initial,
  adopt = [],
  capabilities,
  onClose,
  onSaved,
}: {
  initial?: CoreBundleRecord;
  adopt?: CoreLegacyLink[];
  capabilities: CoreCapabilities;
  onClose: () => void;
  onSaved: (record: CoreBundleRecord) => void;
}) {
  const [baseline, setBaseline] = useState<CoreBundleRecord | undefined>(
      initial,
    ),
    [draft, setDraft] = useState<CoreBundle>(() =>
      initial ? coreDesired(initial) : newCoreBundle(),
    ),
    [inventory, setInventory] = useState<CoreInventory>(emptyInventory),
    [adoptions, setAdoptions] = useState<CoreLegacyLink[]>(adopt),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [step, setStep] = useState(1),
    [preview, setPreview] = useState<CorePreview | null>(null),
    [checked, setChecked] = useState(false),
    [conflict, setConflict] = useState<Conflict | null>(null),
    [conflictChecked, setConflictChecked] = useState(false),
    [targetType, setTargetType] = useState<string | null>(null),
    [portSearch, setPortSearch] = useState({ sideA: "", sideB: "" });
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    const controller = new AbortController();
    api<CoreInventory>(
      `/core/inventory${initial ? `?${new URLSearchParams({ bundleId: initial.id })}` : ""}`,
      { signal: controller.signal },
    )
      .then((data) => {
        if (controller.signal.aborted) return;
        setInventory(data);
        if (adopt.length) {
          const result = initial ? coreDesired(initial) : newCoreBundle();
          if (!initial) {
            const a = data.ports.find((port) => port.id === adopt[0].portAId),
              b = data.ports.find((port) => port.id === adopt[0].portBId);
            result.name = String(adopt[0].name ?? "");
            result.graphTitle = result.name;
            result.sideA.switchId = a?.switchId ?? "";
            result.sideB.switchId = b?.switchId ?? "";
            result.links = [];
          }
          for (const old of adopt) {
            let a = old.portAId,
              b = old.portBId;
            const left = data.ports.find((port) => port.id === a),
              right = data.ports.find((port) => port.id === b);
            if (
              left?.switchId === result.sideB.switchId &&
              right?.switchId === result.sideA.switchId
            )
              [a, b] = [b, a];
            if (
              data.ports.find((port) => port.id === a)?.switchId !==
                result.sideA.switchId ||
              data.ports.find((port) => port.id === b)?.switchId !==
                result.sideB.switchId
            )
              throw new Error("所选未归组链路不属于同一对交换机，请分开归组。");
            result.links.push({
              ...newCoreLink(),
              sideA: newCoreEndpoint(a),
              sideB: newCoreEndpoint(b),
            });
          }
          setDraft(result);
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) setError(message(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      alive.current = false;
      controller.abort();
    };
  }, []);
  const change = (next: CoreBundle) => {
    setDraft(next);
    setPreview(null);
    setChecked(false);
    setError("");
  };
  const changeType = (type: string) => {
    const incompatible =
      (type !== "ecmp" &&
        draft.links.some(
          (link) => link.bfd || link.ipv4Subnet || link.ipv6Subnet,
        )) ||
      (type !== "l3-lag" &&
        (draft.bfd || draft.ipv4Subnet || draft.ipv6Subnet)) ||
      (type !== "l2-lag" && draft.stp);
    if (incompatible) setTargetType(type);
    else change({ ...draft, type });
  };
  const applyType = () => {
    if (!targetType) return;
    change({
      ...draft,
      type: targetType,
      ...(targetType !== "l3-lag"
        ? { bfd: false, ipv4Subnet: "", ipv6Subnet: "" }
        : {}),
      ...(targetType !== "l2-lag" ? { stp: false } : {}),
      links: draft.links.map((link) =>
        targetType !== "ecmp"
          ? { ...link, bfd: false, ipv4Subnet: "", ipv6Subnet: "" }
          : link,
      ),
    });
    setTargetType(null);
  };
  const updateSide = (side: "sideA" | "sideB", field: string, value: unknown) =>
    change({
      ...draft,
      [side]: { ...draft[side], [field]: value },
      ...(field === "switchId"
        ? {
            links: draft.links.map((link) => ({
              ...link,
              [side]: { ...link[side], portId: "" },
            })),
          }
        : {}),
    });
  const updateLink = (index: number, field: string, value: unknown) =>
    change({
      ...draft,
      links: draft.links.map((link, i) =>
        i === index ? { ...link, [field]: value } : link,
      ),
    });
  const updateEndpoint = (
    index: number,
    side: "sideA" | "sideB",
    field: keyof CoreEndpoint,
    value: unknown,
  ) =>
    change({
      ...draft,
      links: draft.links.map((link, i) =>
        i === index
          ? { ...link, [side]: { ...link[side], [field]: value } }
          : link,
      ),
    });
  const adoptedPair = (a: string, b: string) =>
    adoptions.find((old) => pair(old.portAId, old.portBId) === pair(a, b));
  const removeLink = (index: number) => {
    if (draft.links.length === 1) {
      setError("Bundle 至少保留一条链路；退役最后一条请使用删除整个 Bundle。");
      return;
    }
    const removed = draft.links[index],
      old = adoptedPair(removed.sideA.portId, removed.sideB.portId);
    if (old)
      setAdoptions((values) => values.filter((value) => value.id !== old.id));
    change({ ...draft, links: draft.links.filter((_, i) => i !== index) });
  };
  const exportDraft = () => {
    const url = URL.createObjectURL(
        new Blob(
          [
            JSON.stringify(
              {
                bundle: coreDesired(draft),
                adoptLinkIds: adoptions.map((old) => old.id),
              },
              null,
              2,
            ),
          ],
          { type: "application/json" },
        ),
      ),
      link = document.createElement("a");
    link.href = url;
    link.download = "core-bundle-unsaved-draft.json";
    link.click();
    URL.revokeObjectURL(url);
  };
  const reloadForReview = async () => {
    setBusy(true);
    setError("");
    setPreview(null);
    setChecked(false);
    setConflictChecked(false);
    try {
      let current: CoreBundleRecord | undefined,
        deleted = false;
      if (baseline) {
        try {
          current = await api<CoreBundleRecord>(`/core/bundles/${baseline.id}`);
        } catch (error) {
          if (error instanceof APIError && error.status === 404) deleted = true;
          else throw error;
        }
      }
      const [fresh, all] = await Promise.all([
        api<CoreInventory>(
          `/core/inventory${current ? `?${new URLSearchParams({ bundleId: current.id })}` : ""}`,
        ),
        api<CoreList>("/core"),
      ]);
      if (alive.current)
        setConflict({
          current,
          inventory: fresh,
          legacy: all.unbundledLinks,
          deleted,
        });
    } catch (error) {
      if (alive.current) setError(message(error));
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const changedAdoption =
    conflict &&
    adoptions.some((old) => {
      const latest = conflict.legacy.find((row) => row.id === old.id);
      return (
        !latest ||
        pair(latest.portAId, latest.portBId) !==
          pair(old.portAId, old.portBId) ||
        latest.type !== old.type
      );
    });
  const takeBaseline = (useServer: boolean) => {
    if (!conflict || !conflictChecked || conflict.deleted || changedAdoption)
      return;
    if (conflict.current) setBaseline(conflict.current);
    setInventory(conflict.inventory);
    if (useServer && conflict.current) {
      setDraft(coreDesired(conflict.current));
      setAdoptions([]);
    } else
      setAdoptions((rows) =>
        rows.map(
          (old) => conflict.legacy.find((row) => row.id === old.id) ?? old,
        ),
      );
    setConflict(null);
    setPreview(null);
    setChecked(false);
    setStep(2);
    setNotice(
      "已采用你核对过的版本作为基线。请重新检查端口与协议，生成预览后再保存。",
    );
  };
  const validatePairs = () => {
    if (!draft.links.length || draft.links.length > capabilities.maxLinks)
      throw new Error(`需要 1–${capabilities.maxLinks} 条链路。`);
    const used = new Set<string>();
    for (const [index, link] of draft.links.entries()) {
      for (const side of ["sideA", "sideB"] as const) {
        const port = inventory.ports.find(
            (port) => port.id === link[side].portId,
          ),
          adopting = adoptedPair(link.sideA.portId, link.sideB.portId);
        if (!port || port.switchId !== draft[side].switchId)
          throw new Error(
            `请重新选择链路 ${index + 1} 的 ${side === "sideA" ? "A" : "B"} 端口。`,
          );
        if (used.has(port.id))
          throw new Error("同一个物理端口不能在方案中重复使用。");
        used.add(port.id);
        if (!port.available && !(adopting && port.adoptable))
          throw new Error(
            `${corePortLabel(port.id, inventory)} 当前不可用：${port.blockedReason}`,
          );
      }
    }
  };
  const doPreview = async () => {
    validatePairs();
    const body = {
      bundle: coreDesired(draft),
      ...(adoptions.length
        ? {
            adoptLinks: adoptions.map((old) => ({
              id: old.id,
              etag: old._etag,
            })),
          }
        : {}),
    };
    const response = await save<CorePreview>("/core/preview", body);
    if (!alive.current) return;
    setDraft(coreDesired(response.bundle));
    setPreview(response);
    setChecked(false);
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (conflict) return;
    setError("");
    setNotice("");
    if (step === 1) {
      if (draft.sideA.switchId === draft.sideB.switchId) {
        setError("两端必须选择不同交换机。");
        return;
      }
      setStep(2);
      return;
    }
    if (step === 2) {
      try {
        validatePairs();
        setStep(3);
      } catch (error) {
        setError(message(error));
      }
      return;
    }
    setBusy(true);
    try {
      if (!preview) {
        await doPreview();
        return;
      }
      if (!checked) throw new Error("请先核对服务端预览和库存变更范围。");
      const portEtags: Record<string, string> = {};
      for (const id of new Set([
        ...corePorts(baseline),
        ...corePorts(preview.bundle),
      ])) {
        const tag = inventory.ports.find((port) => port.id === id)?._etag;
        if (!tag)
          throw new Error("缺少核心库存专用端口版本，请核对最新库存后重试。");
        portEtags[id] = tag;
      }
      const result = await save<CoreBundleRecord>(
        `/core/bundles${baseline ? `/${baseline.id}` : ""}`,
        {
          bundle: coreDesired(preview.bundle),
          ...(baseline ? { expectedRevision: baseline.revision } : {}),
          portEtags,
          ...(adoptions.length
            ? {
                adoptLinks: adoptions.map((old) => ({
                  id: old.id,
                  etag: old._etag,
                })),
              }
            : {}),
        },
        baseline ? "PUT" : "POST",
      );
      if (alive.current) onSaved(result);
    } catch (error) {
      if (error instanceof APIError && [409, 428, 404].includes(error.status)) {
        setNotice(
          "库存或聚合版本已变化，本次没有写入。草稿已保留，请核对服务器当前图与端口用途。",
        );
        await reloadForReview();
      } else setError(message(error));
    } finally {
      if (alive.current) setBusy(false);
    }
  };
  const numberValue = (text: string) =>
      text === "" ? undefined : Number(text),
    allowedTypes = capabilities.types.filter(
      (type) =>
        !adoptions.length ||
        adoptions.every(
          (old) => (old.type === "layer2") === (type === "l2-lag"),
        ),
    );
  const portOptions = (side: "sideA" | "sideB", index: number) => {
    const link = draft.links[index],
      current = link[side].portId,
      legacy = adoptedPair(link.sideA.portId, link.sideB.portId),
      used = corePorts(draft);
    return inventory.ports
      .filter((port) => port.switchId === draft[side].switchId)
      .filter(
        (port) =>
          port.id === current ||
          corePortLabel(port.id, inventory)
            .toLowerCase()
            .includes(portSearch[side].toLowerCase()),
      )
      .map((port) => (
        <option
          key={port.id}
          value={port.id}
          disabled={
            (used.includes(port.id) && port.id !== current) ||
            (!port.available && !(legacy && port.adoptable))
          }
        >
          {port.name ?? port.id} ·{" "}
          {port.speed ? `${port.speed} Mbps` : "速率未知"}
          {port.available ? "" : ` · ${port.blockedReason}`}
        </option>
      ));
  };
  return (
    <Dialog open onOpenChange={(value) => !value && !busy && onClose()}>
      <DialogContent className="max-w-5xl">
        <DialogTitle className="text-lg font-semibold">
          {baseline ? "编辑核心互联" : "新增核心互联"}
          {adoptions.length ? " · 显式归组" : ""}
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          双端与所有链路作为一个库存事务保存；不修改在线设备配置。端口将保留核心预留标记，解除链路后也不会自动转为成员端口。
        </DialogDescription>
        <div className="flex flex-wrap gap-2" aria-label="核心互联编辑步骤">
          {["类型与双端", "成对物理链路", "协议与预览"].map((label, index) => (
            <Badge
              key={label}
              variant={step === index + 1 ? "default" : "outline"}
            >
              {index + 1}. {label}
            </Badge>
          ))}
        </div>
        <ErrorNotice message={error} />
        {notice && <p className="notice">{notice}</p>}
        {loading ? (
          <Loading />
        ) : conflict ? (
          <div
            className="space-y-4 rounded-lg border border-amber-200 bg-amber-50/30 p-4"
            aria-label="核心互联版本冲突"
          >
            <h3 className="font-semibold">整份草稿仍在，先核对服务器变化</h3>
            {conflict.deleted && (
              <p className="notice">
                服务器中的 Bundle
                已删除，不能将此编辑直接变成重新创建。可先保存草稿副本。
              </p>
            )}
            {changedAdoption && (
              <p className="notice">
                拟归组的旧记录已删除、换端口或改变层次。不能自动更新归组基线，请保存草稿并重新选择旧记录。
              </p>
            )}
            <details>
              <summary className="cursor-pointer text-sm font-medium">
                查看我的完整草稿
              </summary>
              <div className="mt-3">
                <CorePlan bundle={draft} inventory={inventory} compact />
              </div>
            </details>
            {conflict.current && (
              <details open>
                <summary className="cursor-pointer text-sm font-medium">
                  服务器当前完整方案
                </summary>
                <div className="mt-3">
                  <CorePlan
                    bundle={conflict.current}
                    inventory={conflict.inventory}
                    compact
                  />
                </div>
              </details>
            )}
            {adoptions.length > 0 && (
              <div className="rounded border bg-background p-3 text-xs leading-6">
                {adoptions.map((old) => {
                  const latest = conflict.legacy.find(
                    (row) => row.id === old.id,
                  );
                  return (
                    <p key={old.id}>
                      归组记录 {old.name ?? old.id}：
                      {latest
                        ? `${latest.name} · ${latest.type} · ${latest.notes || "无备注"}`
                        : "已不存在"}
                    </p>
                  );
                })}
              </div>
            )}
            <div className="max-h-48 overflow-auto rounded border bg-background p-3 text-xs leading-6">
              {[
                ...new Set([
                  ...corePorts(conflict.current),
                  ...corePorts(draft),
                ]),
              ].map((id) => {
                const port = conflict.inventory.ports.find(
                  (port) => port.id === id,
                );
                return (
                  <p key={id}>
                    {corePortLabel(id, conflict.inventory)} ·{" "}
                    {port?.available
                      ? "当前可用"
                      : port?.blockedReason || "端口不存在"}
                  </p>
                );
              })}
            </div>
            {conflict.current &&
              conflict.current.links.some(
                (link) => !draft.links.some((local) => local.id === link.id),
              ) && (
                <p className="notice">
                  保留我的整份草稿会移除服务器当前有、但草稿没有的链路。请逐一核对上方完整方案，避免覆盖其他人的新增链路。
                </p>
              )}
            <label className="flex items-start gap-2 text-sm leading-6">
              <input
                type="checkbox"
                className="mt-1"
                checked={conflictChecked}
                onChange={(event) => setConflictChecked(event.target.checked)}
              />
              我已核对完整拓扑差异、归组记录和双端占用，明确选择下面的草稿处理方式。
            </label>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                disabled={
                  !conflictChecked || conflict.deleted || !!changedAdoption
                }
                onClick={() => takeBaseline(false)}
              >
                保留整份草稿，重新核对端口
              </Button>
              {conflict.current && (
                <Button
                  type="button"
                  variant="outline"
                  disabled={
                    !conflictChecked || conflict.deleted || !!changedAdoption
                  }
                  onClick={() => takeBaseline(true)}
                >
                  采用服务器方案继续编辑
                </Button>
              )}
              <Button type="button" variant="outline" onClick={exportDraft}>
                <Download />
                保存草稿副本
              </Button>
              <Button
                type="button"
                variant="ghost"
                disabled={busy}
                onClick={() => void reloadForReview()}
              >
                <RefreshCw />
                重新读取供核对
              </Button>
              <Button type="button" variant="ghost" onClick={onClose}>
                放弃并关闭
              </Button>
            </div>
          </div>
        ) : (
          <form onSubmit={submit} className="space-y-5">
            <fieldset disabled={busy} className="space-y-5 min-w-0">
              {adoptions.length > 0 && (
                <div className="notice">
                  归组保留既有端口对；原名称与备注由服务端保留在审计中。请明确选择类型并补齐新方案，不能据旧
                  layer2/layer3 推断设备已有 LAG/ECMP。
                  {adoptions.map((old) => (
                    <p key={old.id} className="mt-1">
                      {old.name} · {old.type} · {old.notes || "无备注"}
                    </p>
                  ))}
                </div>
              )}
              {step === 1 && (
                <>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="core-name">名称 *</label>
                      <Input
                        id="core-name"
                        required
                        maxLength={255}
                        value={draft.name}
                        onChange={(event) =>
                          change({
                            ...draft,
                            name: event.target.value,
                            ...(!draft.graphTitle ||
                            draft.graphTitle === draft.name
                              ? { graphTitle: event.target.value }
                              : {}),
                          })
                        }
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="core-title">图表标题</label>
                      <Input
                        id="core-title"
                        maxLength={255}
                        value={draft.graphTitle}
                        onChange={(event) =>
                          change({ ...draft, graphTitle: event.target.value })
                        }
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="core-type">核心互联类型 *</label>
                      <select
                        id="core-type"
                        required
                        value={draft.type}
                        onChange={(event) => changeType(event.target.value)}
                      >
                        <option value="">请明确选择类型</option>
                        {allowedTypes.map((type) => (
                          <option key={type} value={type}>
                            {coreTypes[type] ?? type}
                          </option>
                        ))}
                      </select>
                    </div>
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={draft.enabled}
                        onChange={(event) =>
                          change({ ...draft, enabled: event.target.checked })
                        }
                      />
                      期望启用此 Bundle（库存意图）
                    </label>
                  </div>
                  <div className="grid gap-4 md:grid-cols-2">
                    {(["sideA", "sideB"] as const).map((side, index) => (
                      <div
                        key={side}
                        className="min-w-0 space-y-3 rounded-lg border p-4"
                      >
                        <h3 className="font-semibold">
                          端点 {index ? "B" : "A"}
                        </h3>
                        <div className="field">
                          <label htmlFor={`core-${side}-switch`}>
                            交换机 *
                          </label>
                          <select
                            id={`core-${side}-switch`}
                            required
                            disabled={adoptions.length > 0}
                            value={draft[side].switchId}
                            onChange={(event) =>
                              updateSide(side, "switchId", event.target.value)
                            }
                          >
                            <option value="">请选择交换机</option>
                            {inventory.switches.map((sw) => (
                              <option
                                key={sw.id}
                                value={sw.id}
                                disabled={
                                  sw.id ===
                                  draft[index ? "sideA" : "sideB"].switchId
                                }
                              >
                                {sw.name}
                              </option>
                            ))}
                          </select>
                        </div>
                        <div className="field">
                          <label htmlFor={`core-${side}-owner`}>
                            归属成员（可选）
                          </label>
                          <select
                            id={`core-${side}-owner`}
                            value={draft[side].ownerMemberId ?? ""}
                            onChange={(event) =>
                              updateSide(
                                side,
                                "ownerMemberId",
                                event.target.value,
                              )
                            }
                          >
                            <option value="">未指定</option>
                            {inventory.members.map((member) => (
                              <option key={member.id} value={member.id}>
                                {member.name}
                              </option>
                            ))}
                          </select>
                          <p className="text-xs text-muted-foreground">
                            通常为 IXP
                            内部成员；此归属不授予成员查看核心网络的权限。
                          </p>
                        </div>
                        <div className="field">
                          <label htmlFor={`core-${side}-interface`}>
                            逻辑接口名
                            {draft.type && draft.type !== "ecmp"
                              ? " *"
                              : "（ECMP 可留空）"}
                          </label>
                          <Input
                            id={`core-${side}-interface`}
                            required={!!draft.type && draft.type !== "ecmp"}
                            maxLength={255}
                            value={draft[side].interfaceName}
                            onChange={(event) =>
                              updateSide(
                                side,
                                "interfaceName",
                                event.target.value,
                              )
                            }
                          />
                        </div>
                        <div className="form-grid">
                          <div className="field">
                            <label htmlFor={`core-${side}-group`}>
                              聚合组号
                              {draft.type && draft.type !== "ecmp"
                                ? " *"
                                : "（可选）"}
                            </label>
                            <Input
                              id={`core-${side}-group`}
                              type="number"
                              min={0}
                              max={4294967295}
                              required={!!draft.type && draft.type !== "ecmp"}
                              value={draft[side].channelGroup ?? ""}
                              onChange={(event) =>
                                updateSide(
                                  side,
                                  "channelGroup",
                                  numberValue(event.target.value),
                                )
                              }
                            />
                          </div>
                          <div className="field">
                            <label htmlFor={`core-${side}-mtu`}>
                              MTU（0=未设）
                            </label>
                            <Input
                              id={`core-${side}-mtu`}
                              required
                              type="number"
                              min={0}
                              max={64000}
                              value={draft[side].mtu}
                              onChange={(event) =>
                                updateSide(
                                  side,
                                  "mtu",
                                  Number(event.target.value),
                                )
                              }
                            />
                          </div>
                        </div>
                        <div className="flex flex-wrap gap-3 text-xs">
                          {(
                            [
                              ["trunk", "802.1q trunk"],
                              ["fastLacp", "快速 LACP 意图"],
                              ["lagFraming", "LAG framing 意图"],
                            ] as const
                          ).map(([key, label]) => (
                            <label
                              key={key}
                              className="flex items-center gap-2"
                            >
                              <input
                                type="checkbox"
                                checked={draft[side][key]}
                                onChange={(event) =>
                                  updateSide(side, key, event.target.checked)
                                }
                              />
                              {label}
                            </label>
                          ))}
                        </div>
                        <div className="field">
                          <label htmlFor={`core-${side}-description`}>
                            端点说明
                          </label>
                          <textarea
                            id={`core-${side}-description`}
                            value={draft[side].description ?? ""}
                            maxLength={4096}
                            onChange={(event) =>
                              updateSide(
                                side,
                                "description",
                                event.target.value,
                              )
                            }
                          />
                        </div>
                      </div>
                    ))}
                  </div>
                </>
              )}
              {step === 2 && (
                <>
                  <div className="form-grid">
                    {(["sideA", "sideB"] as const).map((side, index) => (
                      <div key={side} className="field">
                        <label htmlFor={`core-search-${side}`}>
                          筛选 {index ? "B" : "A"} 侧端口
                        </label>
                        <Input
                          id={`core-search-${side}`}
                          value={portSearch[side]}
                          onChange={(event) =>
                            setPortSearch({
                              ...portSearch,
                              [side]: event.target.value,
                            })
                          }
                        />
                      </div>
                    ))}
                  </div>
                  {draft.links.map((link, index) => (
                    <div
                      key={link.id || index}
                      className="min-w-0 space-y-4 rounded-lg border p-4"
                      data-core-link-index={index}
                    >
                      <div className="flex flex-wrap justify-between gap-2">
                        <h3 className="font-semibold">
                          链路 #{index + 1}
                          {adoptedPair(
                            link.sideA.portId,
                            link.sideB.portId,
                          ) && (
                            <Badge className="ml-2" variant="outline">
                              显式归组旧端口对
                            </Badge>
                          )}
                        </h3>
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          disabled={draft.links.length === 1}
                          onClick={() => removeLink(index)}
                        >
                          <Trash2 />
                          移除此链路
                        </Button>
                      </div>
                      <div className="grid gap-4 md:grid-cols-2">
                        {(["sideA", "sideB"] as const).map(
                          (side, sideIndex) => (
                            <div key={side} className="min-w-0 space-y-3">
                              <div className="field">
                                <label htmlFor={`core-link-${index}-${side}`}>
                                  {sideIndex ? "B" : "A"} 端物理端口 *
                                </label>
                                <select
                                  id={`core-link-${index}-${side}`}
                                  required
                                  disabled={
                                    !!adoptedPair(
                                      link.sideA.portId,
                                      link.sideB.portId,
                                    )
                                  }
                                  value={link[side].portId}
                                  onChange={(event) =>
                                    updateEndpoint(
                                      index,
                                      side,
                                      "portId",
                                      event.target.value,
                                    )
                                  }
                                >
                                  <option value="">请选择端口</option>
                                  {portOptions(side, index)}
                                </select>
                                {link[side].portId && (
                                  <p className="break-all text-xs text-muted-foreground">
                                    {corePortLabel(
                                      link[side].portId,
                                      inventory,
                                    )}
                                  </p>
                                )}
                              </div>
                              <div className="form-grid">
                                <div className="field">
                                  <label
                                    htmlFor={`core-speed-${index}-${side}`}
                                  >
                                    声明速率（Mbps，可选）
                                  </label>
                                  <Input
                                    id={`core-speed-${index}-${side}`}
                                    type="number"
                                    min={1}
                                    max={10000000}
                                    value={link[side].speedMbps ?? ""}
                                    onChange={(event) =>
                                      updateEndpoint(
                                        index,
                                        side,
                                        "speedMbps",
                                        numberValue(event.target.value),
                                      )
                                    }
                                  />
                                </div>
                                <div className="field">
                                  <label
                                    htmlFor={`core-duplex-${index}-${side}`}
                                  >
                                    双工 *
                                  </label>
                                  <select
                                    id={`core-duplex-${index}-${side}`}
                                    value={link[side].duplex}
                                    onChange={(event) =>
                                      updateEndpoint(
                                        index,
                                        side,
                                        "duplex",
                                        event.target.value,
                                      )
                                    }
                                  >
                                    <option value="full">全双工</option>
                                    <option value="half">半双工</option>
                                  </select>
                                </div>
                              </div>
                              <label className="flex items-center gap-2 text-xs">
                                <input
                                  type="checkbox"
                                  checked={link[side].autoneg}
                                  onChange={(event) =>
                                    updateEndpoint(
                                      index,
                                      side,
                                      "autoneg",
                                      event.target.checked,
                                    )
                                  }
                                />
                                自协商意图
                              </label>
                              <details className="rounded border p-3">
                                <summary className="cursor-pointer text-xs font-medium">
                                  状态、限速与备注（库存意图）
                                </summary>
                                <div className="mt-3 space-y-3">
                                  <div className="field">
                                    <label
                                      htmlFor={`core-status-${index}-${side}`}
                                    >
                                      已登记状态
                                    </label>
                                    <select
                                      id={`core-status-${index}-${side}`}
                                      value={link[side].status ?? ""}
                                      onChange={(event) =>
                                        updateEndpoint(
                                          index,
                                          side,
                                          "status",
                                          event.target.value,
                                        )
                                      }
                                    >
                                      {Object.entries(coreEndpointStates).map(
                                        ([value, label]) => (
                                          <option key={value} value={value}>
                                            {label}
                                          </option>
                                        ),
                                      )}
                                    </select>
                                  </div>
                                  <div className="field">
                                    <label
                                      htmlFor={`core-rate-${index}-${side}`}
                                    >
                                      限速（Mbps，可选，0 原样保存）
                                    </label>
                                    <Input
                                      id={`core-rate-${index}-${side}`}
                                      type="number"
                                      min={0}
                                      max={10000000}
                                      value={link[side].rateLimitMbps ?? ""}
                                      onChange={(event) =>
                                        updateEndpoint(
                                          index,
                                          side,
                                          "rateLimitMbps",
                                          numberValue(event.target.value),
                                        )
                                      }
                                    />
                                  </div>
                                  <div className="field">
                                    <label
                                      htmlFor={`core-notes-${index}-${side}`}
                                    >
                                      端口备注
                                    </label>
                                    <textarea
                                      id={`core-notes-${index}-${side}`}
                                      maxLength={4096}
                                      value={link[side].notes ?? ""}
                                      onChange={(event) =>
                                        updateEndpoint(
                                          index,
                                          side,
                                          "notes",
                                          event.target.value,
                                        )
                                      }
                                    />
                                  </div>
                                </div>
                              </details>
                            </div>
                          ),
                        )}
                      </div>
                      <label className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={link.enabled}
                          onChange={(event) =>
                            updateLink(index, "enabled", event.target.checked)
                          }
                        />
                        期望启用此链路
                      </label>
                    </div>
                  ))}
                  <Button
                    type="button"
                    variant="outline"
                    disabled={draft.links.length >= capabilities.maxLinks}
                    onClick={() =>
                      change({
                        ...draft,
                        links: [...draft.links, newCoreLink()],
                      })
                    }
                  >
                    <Plus />
                    添加一对端口
                  </Button>
                  <p className="text-xs leading-6 text-muted-foreground">
                    每条链路保留明确的 A/B
                    端点。声明速率留空表示未设置，端口库存与实际观测速率分开显示。
                  </p>
                </>
              )}
              {step === 3 && (
                <>
                  <div className="form-grid">
                    <div className="field">
                      <label htmlFor="core-cost">协议 cost（可选）</label>
                      <Input
                        id="core-cost"
                        type="number"
                        min={0}
                        max={4294967295}
                        value={draft.cost ?? ""}
                        onChange={(event) =>
                          change({
                            ...draft,
                            cost: numberValue(event.target.value),
                          })
                        }
                      />
                    </div>
                    <div className="field">
                      <label htmlFor="core-preference">
                        协议 preference（可选）
                      </label>
                      <Input
                        id="core-preference"
                        type="number"
                        min={0}
                        max={4294967295}
                        value={draft.preference ?? ""}
                        onChange={(event) =>
                          change({
                            ...draft,
                            preference: numberValue(event.target.value),
                          })
                        }
                      />
                    </div>
                  </div>
                  {draft.type === "l2-lag" ? (
                    <label className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={draft.stp}
                        onChange={(event) =>
                          change({ ...draft, stp: event.target.checked })
                        }
                      />
                      期望配置 STP
                    </label>
                  ) : draft.type === "l3-lag" ? (
                    <div className="space-y-3 rounded-lg border p-4">
                      <h3 className="text-sm font-semibold">
                        Bundle 级三层参数
                      </h3>
                      <div className="form-grid">
                        <div className="field">
                          <label htmlFor="core-ipv4">
                            IPv4 子网（至少一族）
                          </label>
                          <Input
                            id="core-ipv4"
                            value={draft.ipv4Subnet ?? ""}
                            placeholder="10.0.0.0/31"
                            onChange={(event) =>
                              change({
                                ...draft,
                                ipv4Subnet: event.target.value,
                              })
                            }
                          />
                        </div>
                        <div className="field">
                          <label htmlFor="core-ipv6">IPv6 子网</label>
                          <Input
                            id="core-ipv6"
                            value={draft.ipv6Subnet ?? ""}
                            placeholder="2001:db8::/127"
                            onChange={(event) =>
                              change({
                                ...draft,
                                ipv6Subnet: event.target.value,
                              })
                            }
                          />
                        </div>
                      </div>
                      <label className="flex items-center gap-2 text-sm">
                        <input
                          type="checkbox"
                          checked={draft.bfd}
                          onChange={(event) =>
                            change({ ...draft, bfd: event.target.checked })
                          }
                        />
                        Bundle 级 BFD 意图
                      </label>
                    </div>
                  ) : (
                    draft.links.map((link, index) => (
                      <div
                        key={link.id || index}
                        className="space-y-3 rounded-lg border p-4"
                      >
                        <h3 className="text-sm font-semibold">
                          链路 #{index + 1} 三层参数
                        </h3>
                        <p className="text-xs text-muted-foreground">
                          {corePortLabel(link.sideA.portId, inventory)} ↔{" "}
                          {corePortLabel(link.sideB.portId, inventory)}
                        </p>
                        <div className="form-grid">
                          <div className="field">
                            <label htmlFor={`core-link-ipv4-${index}`}>
                              IPv4 子网（至少一族）
                            </label>
                            <Input
                              id={`core-link-ipv4-${index}`}
                              value={link.ipv4Subnet ?? ""}
                              placeholder="10.0.0.0/31"
                              onChange={(event) =>
                                updateLink(
                                  index,
                                  "ipv4Subnet",
                                  event.target.value,
                                )
                              }
                            />
                          </div>
                          <div className="field">
                            <label htmlFor={`core-link-ipv6-${index}`}>
                              IPv6 子网
                            </label>
                            <Input
                              id={`core-link-ipv6-${index}`}
                              value={link.ipv6Subnet ?? ""}
                              placeholder="2001:db8::/127"
                              onChange={(event) =>
                                updateLink(
                                  index,
                                  "ipv6Subnet",
                                  event.target.value,
                                )
                              }
                            />
                          </div>
                        </div>
                        <label className="flex items-center gap-2 text-sm">
                          <input
                            type="checkbox"
                            checked={link.bfd}
                            onChange={(event) =>
                              updateLink(index, "bfd", event.target.checked)
                            }
                          />
                          此链路 BFD 意图
                        </label>
                      </div>
                    ))
                  )}
                  <p className="notice">
                    地址和 BGP 邻居由服务端按规范子网与交换机 ASN/loopback
                    派生。ECMP 按链路，L3-LAG 按 Bundle；这里不创建成员
                    route-server peer，也不证明协议健康。
                  </p>
                  {preview && (
                    <>
                      <CorePlan
                        bundle={preview.bundle}
                        inventory={inventory}
                        derived={preview.derived}
                        warnings={preview.warnings}
                      />
                      <label className="flex items-start gap-2 text-sm leading-6">
                        <input
                          type="checkbox"
                          className="mt-1"
                          checked={checked}
                          onChange={(event) => setChecked(event.target.checked)}
                        />
                        我已核对双端、所有链路、派生地址和告警；保存只更新库存意图，仍需单独适配并应用设备配置。
                      </label>
                      <Button
                        type="button"
                        variant="outline"
                        onClick={() => {
                          setPreview(null);
                          setChecked(false);
                        }}
                      >
                        返回修改协议草案
                      </Button>
                    </>
                  )}
                </>
              )}
            </fieldset>
            <div className="flex flex-wrap justify-between gap-2 border-t pt-4">
              <div className="flex flex-wrap gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={onClose}
                >
                  取消
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={exportDraft}
                >
                  <Download />
                  草稿副本
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => void reloadForReview()}
                >
                  <RefreshCw />
                  核对最新库存
                </Button>
              </div>
              <div className="flex gap-2">
                {step > 1 && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      setStep(step - 1);
                      setError("");
                    }}
                  >
                    上一步
                  </Button>
                )}
                <Button
                  disabled={busy || (step === 3 && !!preview && !checked)}
                >
                  {busy
                    ? "正在处理…"
                    : step < 3
                      ? "下一步"
                      : preview
                        ? "保存这份库存方案"
                        : "检查并生成预览"}
                </Button>
              </div>
            </div>
          </form>
        )}
      </DialogContent>
      <Dialog
        open={!!targetType}
        onOpenChange={(value) => !value && setTargetType(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            切换类型并清除不适用的协议字段？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            ECMP 的链路级子网/BFD、L3-LAG 的 Bundle 级子网/BFD，以及 L2 专属 STP
            不能直接互换。切换后会清空不适用字段，需重新填写并预览；服务器记录尚未改变。
          </DialogDescription>
          <div className="flex justify-end gap-2">
            <Button variant="outline" onClick={() => setTargetType(null)}>
              保留原类型
            </Button>
            <Button onClick={applyType}>确认切换草稿类型</Button>
          </div>
        </DialogContent>
      </Dialog>
    </Dialog>
  );
}
