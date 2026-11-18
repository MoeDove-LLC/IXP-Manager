import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError } from "@/lib/api";
import type {
  SFlowCatalog,
  SFlowReceiver,
  SFlowReceiverSpec,
} from "@/lib/sflow";
import { ErrorNotice, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";

const desired = (item?: SFlowReceiver): SFlowReceiverSpec =>
  item
    ? {
        memberId: item.spec.memberId,
        connectionIds: [...item.spec.connectionIds],
        vlanIds: [...item.spec.vlanIds],
        address: item.spec.address,
        port: item.spec.port,
        enabled: item.spec.enabled,
      }
    : {
        memberId: "",
        connectionIds: [],
        vlanIds: [],
        address: "",
        port: 6343,
        enabled: false,
      };

export default function SFlowReceiverEditor({
  item,
  catalog,
  role,
  onClose,
  onSaved,
}: {
  item?: SFlowReceiver;
  catalog: SFlowCatalog;
  role: string;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [baseline, setBaseline] = useState(item),
    [name, setName] = useState(item?.name ?? "");
  const [spec, setSpec] = useState<SFlowReceiverSpec>(() => ({
    ...desired(item),
    memberId:
      item?.spec.memberId ??
      (role === "member" ? (catalog.selectors.members[0]?.id ?? "") : ""),
  }));
  const [inventory, setInventory] = useState(catalog),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [latest, setLatest] = useState<SFlowReceiver | null>(null),
    [unavailable, setUnavailable] = useState(false),
    [conflict, setConflict] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  const choices = inventory.selectors.connections.filter(
    (c) => !c.memberId || c.memberId === spec.memberId,
  );
  const selectedConnections = choices.filter((c) =>
    spec.connectionIds.includes(c.id),
  );
  const knownVLANs = new Set(
    selectedConnections.map((c) => c.vlanId).filter(Boolean),
  );
  const vlanChoices = inventory.selectors.vlans.filter(
    (v) =>
      knownVLANs.has(v.id) ||
      spec.vlanIds.includes(v.id) ||
      selectedConnections.some((c) => !c.vlanId),
  );
  const toggle = (key: "connectionIds" | "vlanIds", id: string) =>
    setSpec((s) => ({
      ...s,
      [key]: s[key].includes(id)
        ? s[key].filter((value) => value !== id)
        : [...s[key], id],
    }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || conflict || unavailable) return;
    if (!spec.memberId || !spec.connectionIds.length || !spec.vlanIds.length) {
      setError("请选择成员、至少一条连接及一个 VLAN。");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      await api(`/sflow/receivers${baseline ? `/${baseline.id}` : ""}`, {
        method: baseline ? "PUT" : "POST",
        signal: controller.signal,
        body: JSON.stringify({
          name,
          spec,
          ...(baseline ? { expectedRevision: baseline.revision } : {}),
        }),
      });
      if (!controller.signal.aborted) {
        onClose();
        await onSaved();
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && e.status === 409 && baseline) {
        setConflict(true);
        setError(
          "记录或授权范围已变化，本次保存未提交；草稿保留，请核对当前记录。",
        );
        try {
          const current = await api<SFlowCatalog>("/sflow", {
            signal: controller.signal,
          });
          if (controller.signal.aborted) return;
          setInventory(current);
          const record = current.receivers?.find((r) => r.id === baseline.id);
          if (record?.canEdit) setLatest(record);
          else setUnavailable(true);
        } catch (readError) {
          if (!controller.signal.aborted)
            setError(
              `无法读取最新记录：${message(readError)}。请保留草稿后重新打开。`,
            );
        }
      } else if (e instanceof APIError && [403, 404].includes(e.status))
        setUnavailable(true);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const show = (value: SFlowReceiverSpec) =>
    `${value.address} · UDP ${value.port} · ${value.enabled ? "申请启用" : "停用"} · 连接 ${value.connectionIds.join("、")} · VLAN ${value.vlanIds.join("、")}`;
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="max-w-2xl"
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
        onPointerDownOutside={(event) => busy && event.preventDefault()}
      >
        <DialogTitle className="text-lg font-semibold">
          {baseline ? "编辑接收器申请" : "新增接收器申请"}
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          选择准确的 UDP
          目的和成员范围。保存修改会清除此前审批；勾选申请启用后，仍须管理员审阅批准。
        </DialogDescription>
        <ErrorNotice message={error} />
        <form onSubmit={submit} className="space-y-4">
          <fieldset disabled={busy || unavailable} className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="field">
                <label htmlFor="receiver-name">接收器名称</label>
                <Input
                  id="receiver-name"
                  required
                  maxLength={200}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              </div>
              <div className="field">
                <label htmlFor="receiver-member">所属成员</label>
                <select
                  id="receiver-member"
                  required
                  disabled={role === "member" || !!baseline}
                  value={spec.memberId}
                  onChange={(e) =>
                    setSpec((s) => ({
                      ...s,
                      memberId: e.target.value,
                      connectionIds: [],
                      vlanIds: [],
                    }))
                  }
                >
                  <option value="">请选择成员</option>
                  {inventory.selectors.members.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.label || m.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="field">
                <label htmlFor="receiver-address">目的 IP 地址</label>
                <Input
                  id="receiver-address"
                  required
                  autoComplete="off"
                  placeholder="192.0.2.10 或 2001:db8::10"
                  value={spec.address}
                  onChange={(e) =>
                    setSpec((s) => ({ ...s, address: e.target.value }))
                  }
                />
              </div>
              <div className="field">
                <label htmlFor="receiver-port">目的 UDP 端口</label>
                <Input
                  id="receiver-port"
                  type="number"
                  required
                  min={1}
                  max={65535}
                  step={1}
                  value={spec.port}
                  onChange={(e) =>
                    setSpec((s) => ({ ...s, port: Number(e.target.value) }))
                  }
                />
              </div>
            </div>
            <fieldset className="space-y-2">
              <legend className="mb-2 text-sm font-medium">允许的连接</legend>
              {!choices.length && (
                <p className="text-xs text-muted-foreground">
                  此成员暂无可选择的连接。
                </p>
              )}
              {choices.map((c) => (
                <label
                  key={c.id}
                  className="flex items-start gap-2 rounded-lg border p-3"
                >
                  <input
                    className="mt-1"
                    type="checkbox"
                    name="receiver-connection"
                    value={c.id}
                    checked={spec.connectionIds.includes(c.id)}
                    onChange={() => toggle("connectionIds", c.id)}
                  />
                  <span className="min-w-0 break-all">
                    {c.label || c.name}
                    <span className="mt-1 block text-[11px] text-muted-foreground">
                      {c.id}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>
            <fieldset className="space-y-2">
              <legend className="mb-2 text-sm font-medium">允许的 VLAN</legend>
              {!vlanChoices.length && (
                <p className="text-xs text-muted-foreground">
                  先选择连接，再确认对应的 VLAN 范围。
                </p>
              )}
              {vlanChoices.map((v) => (
                <label
                  key={v.id}
                  className="flex items-center gap-2 rounded-lg border p-3"
                >
                  <input
                    type="checkbox"
                    name="receiver-vlan"
                    value={v.id}
                    checked={spec.vlanIds.includes(v.id)}
                    onChange={() => toggle("vlanIds", v.id)}
                  />
                  <span className="min-w-0 break-all">{v.label || v.name}</span>
                </label>
              ))}
            </fieldset>
            <label className="flex items-start gap-3 rounded-lg border bg-muted/30 p-3">
              <input
                id="receiver-enabled"
                type="checkbox"
                className="mt-1"
                checked={spec.enabled}
                onChange={(e) =>
                  setSpec((s) => ({ ...s, enabled: e.target.checked }))
                }
              />
              <span>
                申请启用转发
                <span className="mt-1 block text-xs leading-5 text-muted-foreground">
                  未勾选时保持停用；勾选并保存仅提交期望，不自动开始发送。
                </span>
              </span>
            </label>
          </fieldset>
          {conflict && (
            <section className="notice space-y-3" aria-label="接收器版本冲突">
              <p>
                打开时：
                <span className="break-all">
                  {baseline && show(baseline.spec)}
                </span>
              </p>
              {latest && (
                <p>
                  服务器当前：
                  <span className="break-all">{show(latest.spec)}</span>
                </p>
              )}
              <p>
                保留草稿：<span className="break-all">{show(spec)}</span>
              </p>
              {latest && !unavailable && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setBaseline(latest);
                    setLatest(null);
                    setConflict(false);
                    setError("已采用当前修订，请核对草稿后再次明确保存。");
                  }}
                >
                  按当前版本重新核对草稿
                </Button>
              )}
              {unavailable && (
                <p>此记录已删除或当前不再允许编辑，请关闭后刷新权限。</p>
              )}
            </section>
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={onClose}
            >
              取消
            </Button>
            <Button disabled={busy || unavailable || conflict}>
              {busy ? "正在保存…" : "保存申请"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
