import { useEffect, useRef, useState, type FormEvent } from "react";
import { useHashParameters } from "@/lib/hash-route";
import {
  ArrowRight,
  Link2,
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
} from "lucide-react";
import { api, save, APIError } from "@/lib/api";
import {
  coreCapacity,
  corePortLabel,
  coreTypes,
  type CoreBundleRecord,
  type CoreInventory,
  type CoreLegacyLink,
  type CoreList,
} from "@/lib/core";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
import CorePlan from "@/components/CorePlan";
import CoreObservations from "@/components/CoreObservations";
import CoreBundleEditor from "@/components/CoreBundleEditor";

const emptyInventory: CoreInventory = { switches: [], ports: [], members: [] };
export default function Core({ role }: { role: string }) {
  const targetId = useHashParameters("core").get("id") ?? "";
  const [data, setData] = useState<CoreList | null>(null),
    [inventory, setInventory] = useState<CoreInventory>(emptyInventory),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [selected, setSelected] = useState(""),
    [detail, setDetail] = useState<CoreBundleRecord | null>(null),
    [detailError, setDetailError] = useState(""),
    [refresh, setRefresh] = useState(0),
    [tab, setTab] = useState("bundles"),
    [search, setSearch] = useState(""),
    [editor, setEditor] = useState<{
      initial?: CoreBundleRecord;
      adopt: CoreLegacyLink[];
    } | null>(null),
    [adoptIds, setAdoptIds] = useState<string[]>([]),
    [adoptDestination, setAdoptDestination] = useState(""),
    [deleting, setDeleting] = useState<CoreBundleRecord | null>(null),
    [deleteError, setDeleteError] = useState(""),
    [deleteConflict, setDeleteConflict] = useState<CoreBundleRecord | null>(
      null,
    ),
    [deleteAck, setDeleteAck] = useState(false),
    [resultNotice, setResultNotice] = useState("");
  const alive = useRef(true),
    sequence = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sequence.current++;
    };
  }, []);
  const canWrite =
    (role === "admin" || role === "operator") && data?.capabilities.canWrite;
  useEffect(() => {
    if (targetId && data) {
      const exists = data.items.some((item) => item.id === targetId);
      setSelected(exists ? targetId : "");
      setError(exists ? "" : "此核心互联已删除，或你已失去访问权限。");
      setTab("bundles");
      setSearch("");
    }
  }, [targetId, data?.items]);
  const load = async () => {
    const version = ++sequence.current;
    setLoading(true);
    setError("");
    try {
      const [list, stock] = await Promise.all([
        api<CoreList>("/core"),
        api<CoreInventory>("/core/inventory"),
      ]);
      if (!alive.current || sequence.current !== version) return;
      setData(list);
      setInventory(stock);
      setSelected((id) =>
        list.items.some((item) => item.id === id)
          ? id
          : (list.items[0]?.id ?? ""),
      );
      setAdoptIds((ids) =>
        ids.filter((id) => list.unbundledLinks.some((link) => link.id === id)),
      );
    } catch (error) {
      if (alive.current) setError(message(error));
    } finally {
      if (alive.current && sequence.current === version) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);
  useEffect(() => {
    setDetail(null);
    setDetailError("");
    if (!selected) return;
    const controller = new AbortController();
    api<CoreBundleRecord>(`/core/bundles/${selected}`, {
      signal: controller.signal,
    })
      .then((record) => {
        if (!controller.signal.aborted) setDetail(record);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setDetailError(message(error));
      });
    return () => controller.abort();
  }, [selected, refresh]);
  const startAdoption = async () => {
    if (!data || !adoptIds.length) return;
    setBusy(true);
    setError("");
    try {
      const links = data.unbundledLinks.filter((link) =>
        adoptIds.includes(link.id),
      );
      if (links.some((link) => !link._etag))
        throw new Error("未归组记录缺少审阅版本，请刷新后核对。");
      const initial = adoptDestination
        ? await api<CoreBundleRecord>(`/core/bundles/${adoptDestination}`)
        : undefined;
      setEditor({ initial, adopt: structuredClone(links) });
    } catch (error) {
      setError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!deleting || deleteConflict) return;
    setBusy(true);
    setDeleteError("");
    const form = new FormData(event.currentTarget);
    try {
      await save(
        `/core/bundles/${deleting.id}`,
        {
          expectedRevision: deleting.revision,
          reason: String(form.get("reason") ?? "").trim(),
        },
        "DELETE",
      );
      setDeleting(null);
      setDetail(null);
      setSelected("");
      setResultNotice(
        "整个 Bundle 已从库存移除，双端端口记录仍保留核心预留用途。设备配置与实际运行状态没有自动改变。",
      );
      await load();
    } catch (error) {
      if (error instanceof APIError && [409, 428].includes(error.status)) {
        try {
          const current = await api<CoreBundleRecord>(
            `/core/bundles/${deleting.id}`,
          );
          setDeleteConflict(current);
          setDeleteAck(false);
          setDeleteError(
            "Bundle 或关联库存已变化，本次删除未执行。请核对当前完整方案，再单独确认删除。",
          );
        } catch (readError) {
          setDeleteError(message(readError));
        }
      } else setDeleteError(message(error));
    } finally {
      setBusy(false);
    }
  };
  const rows = (data?.items ?? []).filter((item) =>
    `${item.name} ${coreTypes[item.type]} ${inventory.switches.find((sw) => sw.id === item.sideA.switchId)?.name ?? ""} ${inventory.switches.find((sw) => sw.id === item.sideB.switchId)?.name ?? ""}`
      .toLowerCase()
      .includes(search.toLowerCase()),
  );
  return (
    <>
      <Heading
        title="核心互联"
        description="以完整双端拓扑管理 ECMP 与 LAG，把库存意图、配置草案和实际观测分开。"
        className="flex-wrap"
      >
        {canWrite && (
          <Button
            disabled={busy || loading}
            onClick={() => setEditor({ adopt: [] })}
          >
            <Plus />
            新增核心 Bundle
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
          刷新
        </Button>
      </Heading>
      <div className="notice mb-5">
        保存整体库存不会下发交换机配置。核心端口释放后仍保留
        coreReserved，转为成员用途前需在端口库存明确解除预留并核对实际设备。
      </div>
      <ErrorNotice message={error} />
      {resultNotice && (
        <p role="status" className="notice mb-4">
          {resultNotice}
        </p>
      )}
      <div className="mb-5 flex flex-wrap gap-2">
        <Button
          variant={tab === "bundles" ? "secondary" : "ghost"}
          aria-pressed={tab === "bundles"}
          onClick={() => setTab("bundles")}
        >
          Bundle 工作台
        </Button>
        <Button
          variant={tab === "unbundled" ? "secondary" : "ghost"}
          aria-pressed={tab === "unbundled"}
          onClick={() => setTab("unbundled")}
        >
          未归组兼容库存{data ? `（${data.unbundledLinks.length}）` : ""}
        </Button>
      </div>
      {!data ? (
        loading ? (
          <Loading />
        ) : null
      ) : tab === "unbundled" ? (
        <Card className="overflow-hidden">
          <CardHeader>
            <CardTitle>保留既有端口对，显式选择归组</CardTitle>
            <p className="text-xs leading-6 text-muted-foreground">
              这些 flat links 继续占用端口，layer2/layer3 不代表已知
              LAG/ECMP。归组必须保留旧端口对并选择正确类型，原名称与备注会保留在审计。
            </p>
            <a className="text-xs text-primary underline" href="#core-links">
              打开兼容库存表编辑名称、层次或备注
            </a>
          </CardHeader>
          {!data.unbundledLinks.length ? (
            <Empty description="没有未归组核心链路。" />
          ) : (
            <>
              <div className="overflow-x-auto">
                <table>
                  <thead>
                    <tr>
                      {canWrite && <th>选择</th>}
                      <th>旧记录</th>
                      <th>已登记层次</th>
                      <th>A 端口</th>
                      <th>B 端口</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.unbundledLinks.map((link) => (
                      <tr key={link.id}>
                        {canWrite && (
                          <td>
                            <input
                              type="checkbox"
                              aria-label={`归组选择 ${link.name ?? link.id}`}
                              checked={adoptIds.includes(link.id)}
                              onChange={(event) =>
                                setAdoptIds(
                                  event.target.checked
                                    ? [...adoptIds, link.id]
                                    : adoptIds.filter((id) => id !== link.id),
                                )
                              }
                            />
                          </td>
                        )}
                        <td className="text-sm">
                          {link.name}
                          <p className="mt-1 max-w-xs whitespace-pre-wrap break-words text-xs text-muted-foreground">
                            {link.notes}
                          </p>
                        </td>
                        <td className="text-xs">
                          {link.type === "layer2"
                            ? "二层"
                            : link.type === "layer3"
                              ? "三层"
                              : "未指定，需先核对"}
                        </td>
                        <td className="text-xs">
                          {corePortLabel(link.portAId, inventory)}
                        </td>
                        <td className="text-xs">
                          {corePortLabel(link.portBId, inventory)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              {canWrite && (
                <CardContent className="space-y-3 border-t pt-5">
                  <div className="field">
                    <label htmlFor="core-adopt-destination">归组目标</label>
                    <select
                      id="core-adopt-destination"
                      value={adoptDestination}
                      onChange={(event) =>
                        setAdoptDestination(event.target.value)
                      }
                    >
                      <option value="">
                        创建新的 Bundle（类型需明确选择）
                      </option>
                      {data.items.map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.name} · {coreTypes[item.type]}
                        </option>
                      ))}
                    </select>
                  </div>
                  <Button
                    disabled={busy || !adoptIds.length}
                    onClick={() => void startAdoption()}
                  >
                    <Link2 />
                    核对并归组选中的 {adoptIds.length} 条链路
                  </Button>
                </CardContent>
              )}
            </>
          )}
        </Card>
      ) : (
        <>
          <div className="mb-4">
            <Input
              aria-label="筛选核心 Bundle"
              className="max-w-sm"
              placeholder="按名称、类型或交换机筛选…"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
          </div>
          {!data.items.length ? (
            <Card>
              <Empty
                title="还没有核心 Bundle"
                description="创建完整双端与成对端口方案，或先检查待归组兼容库存。"
                action={canWrite ? "新增核心 Bundle" : undefined}
                onAction={() => setEditor({ adopt: [] })}
              />
            </Card>
          ) : (
            <div className="deployment-grid">
              <Card className="self-start overflow-hidden">
                <CardHeader>
                  <CardTitle>核心 Bundle</CardTitle>
                </CardHeader>
                <div className="max-h-[700px] overflow-y-auto">
                  {rows.map((item) => (
                    <button
                      key={item.id}
                      data-core-bundle={item.id}
                      className={`w-full border-t px-5 py-4 text-left ${selected === item.id ? "bg-primary/5" : "hover:bg-muted/30"}`}
                      onClick={() => setSelected(item.id)}
                    >
                      <p className="text-sm font-semibold">{item.name}</p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {coreTypes[item.type]} · {item.links.length} 条链路
                      </p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {item.derived.capacityKnown
                          ? coreCapacity(item.derived.capacityMbps)
                          : "库存容量待核对"}
                      </p>
                      <Badge className="mt-2" variant="outline">
                        {item.enabled ? "期望启用" : "期望停用"}
                      </Badge>
                    </button>
                  ))}
                  {!rows.length && <Empty description="当前筛选没有匹配项。" />}
                </div>
              </Card>
              <div className="min-w-0 space-y-5">
                <ErrorNotice message={detailError} />
                {!detail ? (
                  selected && !detailError ? (
                    <Loading />
                  ) : null
                ) : (
                  <>
                    <Card className="min-w-0">
                      <CardHeader>
                        <div className="flex flex-wrap justify-between gap-3">
                          <CardTitle>{detail.name}</CardTitle>
                          {canWrite && (
                            <div className="flex gap-2">
                              <Button
                                variant="outline"
                                size="sm"
                                disabled={busy}
                                onClick={() =>
                                  setEditor({
                                    initial: structuredClone(detail),
                                    adopt: [],
                                  })
                                }
                              >
                                <Pencil />
                                编辑完整方案
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                disabled={busy}
                                onClick={() => {
                                  setDeleting(structuredClone(detail));
                                  setDeleteConflict(null);
                                  setDeleteError("");
                                  setDeleteAck(false);
                                }}
                              >
                                <Trash2 />
                                删除 Bundle
                              </Button>
                            </div>
                          )}
                        </div>
                        <p className="text-xs text-muted-foreground">
                          更新时间 {time(detail.updatedAt)} ·{" "}
                          {detail.graphTitle}
                        </p>
                      </CardHeader>
                      <CardContent>
                        <CorePlan
                          bundle={detail}
                          inventory={inventory}
                          derived={detail.derived}
                          warnings={detail.warnings}
                        />
                        <div className="mt-5 flex flex-wrap gap-3 border-t pt-4">
                          <a
                            className="inline-flex items-center gap-2 text-sm text-primary underline"
                            href="#provisioning"
                          >
                            <ArrowRight className="size-4" />
                            前往配置工具审核交换机核心计划
                          </a>
                          <a
                            className="text-sm text-primary underline"
                            href="#switches"
                          >
                            维护交换机 ASN 与 loopback
                          </a>
                        </div>
                        <p className="mt-2 text-xs leading-6 text-muted-foreground">
                          switch-core 生成固定 JSON
                          计划，只做结构检查，仍需目标平台适配；不是可直接应用的交换机
                          CLI。
                        </p>
                      </CardContent>
                    </Card>
                    <CoreObservations
                      key={`${detail.id}:${detail.bindingRevision}`}
                      bundle={detail}
                      inventory={inventory}
                    />
                  </>
                )}
              </div>
            </div>
          )}
        </>
      )}
      {editor && data && (
        <CoreBundleEditor
          initial={editor.initial}
          adopt={editor.adopt}
          capabilities={data.capabilities}
          onClose={() => setEditor(null)}
          onSaved={(record) => {
            setEditor(null);
            setTab("bundles");
            setSelected(record.id);
            setDetail(record);
            setResultNotice(
              "完整双端库存事务已保存。请继续核对真实观测，配置需通过独立规则生成和审核。",
            );
            setRefresh((value) => value + 1);
            void load();
          }}
        />
      )}
      <Dialog
        open={!!deleting}
        onOpenChange={(value) => !value && !busy && setDeleting(null)}
      >
        <DialogContent className={deleteConflict ? "max-w-3xl" : undefined}>
          <DialogTitle className="text-lg font-semibold">
            删除整个核心 Bundle？
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            将移除“{deleting?.name}
            ”及其成对链路、端点库存关系。交换机和物理端口记录保留，核心预留标记不会自动清除；实际设备配置不变。
          </DialogDescription>
          <ErrorNotice message={deleteError} />
          {deleteConflict ? (
            <div className="space-y-4">
              <CorePlan bundle={deleteConflict} inventory={inventory} compact />
              <label className="flex items-start gap-2 text-sm leading-6">
                <input
                  type="checkbox"
                  className="mt-1"
                  checked={deleteAck}
                  onChange={(event) => setDeleteAck(event.target.checked)}
                />
                我已核对服务器当前完整方案，仍准备删除它。
              </label>
              <Button
                disabled={!deleteAck || busy}
                onClick={() => {
                  setDeleting(structuredClone(deleteConflict));
                  setDeleteConflict(null);
                  setDeleteError(
                    "已采用核对后的修订，请重新填写原因并再次确认删除。",
                  );
                }}
              >
                使用核对后的删除基线
              </Button>
            </div>
          ) : (
            <form onSubmit={remove} className="space-y-4">
              <div className="field">
                <label htmlFor="core-delete-reason">删除原因 *</label>
                <Input
                  id="core-delete-reason"
                  name="reason"
                  required
                  minLength={1}
                  maxLength={1000}
                />
              </div>
              <label className="flex items-start gap-2 text-sm leading-6">
                <input type="checkbox" required className="mt-1" />
                我确认删除整个库存拓扑，已安排设备配置与端口用途的后续处理。
              </label>
              <div className="flex justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  disabled={busy}
                  onClick={() => setDeleting(null)}
                >
                  取消
                </Button>
                <Button variant="destructive" disabled={busy}>
                  {busy ? "正在删除…" : "确认删除完整 Bundle"}
                </Button>
              </div>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
