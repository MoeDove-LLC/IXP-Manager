import { useEffect, useState, type FormEvent } from "react";
import { Network, RefreshCw } from "lucide-react";
import { api, save } from "@/lib/api";
import type { Row } from "@/lib/resources";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { ErrorNotice, Loading, message } from "@/components/shared";
export default function FabricAssignment({
  onApplied,
}: {
  onApplied: () => void | Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [loading, setLoading] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [success, setSuccess] = useState(""),
    [infrastructures, setInfrastructures] = useState<Row[]>([]),
    [vlans, setVlans] = useState<Row[]>([]),
    [switches, setSwitches] = useState<Row[]>([]),
    [ports, setPorts] = useState<Row[]>([]),
    [connections, setConnections] = useState<Row[]>([]),
    [target, setTarget] = useState(""),
    [vlanIds, setVlanIds] = useState<string[]>([]),
    [switchIds, setSwitchIds] = useState<string[]>([]);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const kinds = [
        "infrastructures",
        "vlans",
        "switches",
        "ports",
        "connections",
      ];
      const values = await Promise.all(
        kinds.map((k) => api<{ items: Row[] }>(`/resources/${k}`)),
      );
      setInfrastructures(values[0].items);
      setVlans(values[1].items);
      setSwitches(values[2].items);
      setPorts(values[3].items);
      setConnections(values[4].items);
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    if (open) {
      setSuccess("");
      setTarget("");
      setVlanIds([]);
      setSwitchIds([]);
      void load();
    }
  }, [open]);
  const source = (id: unknown) =>
    infrastructures.find((i) => i.id === id)?.name ?? "兼容未绑定网络";
  const expand = () => {
    const chosenVlans = new Set(vlanIds),
      chosenSwitches = new Set(switchIds);
    let changed = true;
    while (changed) {
      changed = false;
      for (const connection of connections) {
        const ids = Array.isArray(connection.portIds)
          ? connection.portIds
          : connection.portId
            ? [connection.portId]
            : [];
        const involved = ids
          .map((id) => ports.find((p) => p.id === id)?.switchId)
          .filter((id): id is string => typeof id === "string");
        if (
          chosenVlans.has(String(connection.vlanId)) ||
          involved.some((id) => chosenSwitches.has(id))
        ) {
          if (!chosenVlans.has(String(connection.vlanId))) {
            chosenVlans.add(String(connection.vlanId));
            changed = true;
          }
          for (const id of involved)
            if (!chosenSwitches.has(id)) {
              chosenSwitches.add(id);
              changed = true;
            }
        }
      }
    }
    setVlanIds([...chosenVlans]);
    setSwitchIds([...chosenSwitches]);
    setError("");
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      if (!vlanIds.length && !switchIds.length)
        throw new Error("请至少选择一个 VLAN 或交换机。");
      await save("/fabrics/assign", {
        infrastructureId: target === "__legacy__" ? "" : target,
        vlanIds,
        switchIds,
      });
      setSuccess("库存关联已原子更新。交换机和路由器的实际运行配置没有改变。");
      await onApplied();
      await load();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <Network />
        批量调整网络归属
      </Button>
      <Dialog open={open} onOpenChange={(v) => !busy && setOpen(v)}>
        <DialogContent className="max-w-3xl">
          <DialogTitle className="text-lg font-semibold">
            关联完整的交换网络
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将选中的 VLAN 与交换机一同移至目标网络。已有接入或跨交换机 LAG
            的关联必须保持完整；出现冲突时整批取消。
          </DialogDescription>
          <ErrorNotice message={error} />
          {success && (
            <p className="notice" role="status">
              {success}
            </p>
          )}
          {loading ? (
            <Loading />
          ) : (
            <form onSubmit={submit} className="space-y-5">
              <div className="field">
                <label htmlFor="fabric-target">目标交换网络</label>
                <select
                  id="fabric-target"
                  value={target}
                  required
                  onChange={(e) => setTarget(e.target.value)}
                >
                  <option value="">选择目标网络</option>
                  <option value="__legacy__">兼容未绑定网络</option>
                  {infrastructures.map((i) => (
                    <option key={i.id} value={i.id}>
                      {i.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className="text-xs text-muted-foreground">
                  已选 {vlanIds.length} 个 VLAN、{switchIds.length} 台交换机
                </span>
                <div className="flex gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="secondary"
                    disabled={!vlanIds.length && !switchIds.length}
                    onClick={expand}
                  >
                    补全相连资源
                  </Button>
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    aria-label="刷新网络归属"
                    onClick={() => void load()}
                  >
                    <RefreshCw />
                  </Button>
                </div>
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <fieldset className="rounded-lg border p-3">
                  <legend className="px-1 text-sm font-medium">VLAN</legend>
                  <div className="max-h-64 space-y-2 overflow-y-auto">
                    {vlans.map((v) => (
                      <label
                        key={v.id}
                        className="flex items-start gap-2 rounded p-2 hover:bg-muted/50"
                      >
                        <input
                          className="mt-1"
                          type="checkbox"
                          value={v.id}
                          checked={vlanIds.includes(v.id)}
                          onChange={(e) =>
                            setVlanIds(
                              e.target.checked
                                ? [...vlanIds, v.id]
                                : vlanIds.filter((id) => id !== v.id),
                            )
                          }
                        />
                        <span className="text-sm">
                          {v.name} · VLAN {String(v.tag)}
                          <span className="mt-1 block text-xs text-muted-foreground">
                            {source(v.infrastructureId)}
                          </span>
                        </span>
                      </label>
                    ))}
                    {!vlans.length && (
                      <p className="text-xs text-muted-foreground">
                        尚无 VLAN。
                      </p>
                    )}
                  </div>
                </fieldset>
                <fieldset className="rounded-lg border p-3">
                  <legend className="px-1 text-sm font-medium">交换机</legend>
                  <div className="max-h-64 space-y-2 overflow-y-auto">
                    {switches.map((s) => (
                      <label
                        key={s.id}
                        className="flex items-start gap-2 rounded p-2 hover:bg-muted/50"
                      >
                        <input
                          className="mt-1"
                          type="checkbox"
                          value={s.id}
                          checked={switchIds.includes(s.id)}
                          onChange={(e) =>
                            setSwitchIds(
                              e.target.checked
                                ? [...switchIds, s.id]
                                : switchIds.filter((id) => id !== s.id),
                            )
                          }
                        />
                        <span className="text-sm">
                          {s.name}
                          <span className="mt-1 block text-xs text-muted-foreground">
                            {source(s.infrastructureId)}
                          </span>
                        </span>
                      </label>
                    ))}
                    {!switches.length && (
                      <p className="text-xs text-muted-foreground">
                        尚无交换机。
                      </p>
                    )}
                  </div>
                </fieldset>
              </div>
              <p className="notice">
                “补全相连资源”按当前接入记录扩展选择，不自动改动设备。提交时服务器会再次检查编号冲突和连接完整性。
              </p>
              <div className="flex justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => setOpen(false)}
                >
                  关闭
                </Button>
                <Button disabled={busy || !target}>
                  {busy ? "正在验证…" : "确认更新库存归属"}
                </Button>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
