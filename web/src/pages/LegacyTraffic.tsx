import { useEffect, useRef, useState, type FormEvent } from "react";
import { RefreshCw, Search, Upload, Trash2 } from "lucide-react";
import { api } from "@/lib/api";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
  time,
} from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import LegacyTrafficReport from "@/components/LegacyTrafficReport";
import LegacyTrafficImport from "@/components/LegacyTrafficImport";
import LegacyTrafficDelete from "@/components/LegacyTrafficDelete";
import { fromUTCInput } from "@/lib/traffic";
import {
  legacyEntityLabels,
  legacyPeriodLabels,
  type LegacyCatalog,
  type LegacyMetadata,
  type LegacyResult,
} from "@/lib/legacy-traffic";

type Filter = {
  batchId: string;
  entityKind: string;
  entityId: string;
  period: string;
  category: string;
  family: string;
  originalDayFrom: string;
  originalDayTo: string;
  windowFrom: string;
  windowTo: string;
};
const initial: Filter = {
  batchId: "",
  entityKind: "",
  entityId: "",
  period: "",
  category: "",
  family: "",
  originalDayFrom: "",
  originalDayTo: "",
  windowFrom: "",
  windowTo: "",
};
export default function LegacyTraffic({ role }: { role: string }) {
  const [catalog, setCatalog] = useState<LegacyCatalog | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [filter, setFilter] = useState<Filter>(initial),
    [result, setResult] = useState<LegacyResult | null>(null),
    [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false),
    [deleting, setDeleting] = useState<LegacyMetadata | null>(null);
  const request = useRef<AbortController | null>(null),
    catalogRequest = useRef<AbortController | null>(null);
  const run = async (next: Filter, offset = 0) => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setResult(null);
    try {
      if (!next.batchId) throw new Error("请选择一个独立导入批次。");
      if (
        next.originalDayFrom &&
        next.originalDayTo &&
        next.originalDayFrom > next.originalDayTo
      )
        throw new Error("原报告起始日期不能晚于结束日期。");
      const params = new URLSearchParams({
        batchId: next.batchId,
        offset: String(offset),
        limit: "100",
      });
      if (
        next.windowFrom &&
        next.windowTo &&
        Date.parse(fromUTCInput(next.windowFrom)) >=
          Date.parse(fromUTCInput(next.windowTo))
      )
        throw new Error("实际窗口结束时间必须晚于开始时间。");
      for (const [key, value] of Object.entries(next))
        if (value)
          params.set(
            key,
            key === "windowFrom" || key === "windowTo"
              ? fromUTCInput(value)
              : value,
          );
      const data = await api<LegacyResult>(`/traffic/legacy/query?${params}`, {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (
        data.sourceKind !== "legacy-report" ||
        data.noAggregation !== true ||
        data.batchId !== next.batchId
      )
        throw new Error("返回的历史来源、批次或不聚合声明不匹配，未显示数据。");
      setResult(data);
    } catch (e) {
      if (!controller.signal.aborted) setError(message(e));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const load = async () => {
    catalogRequest.current?.abort();
    request.current?.abort();
    const controller = new AbortController();
    catalogRequest.current = controller;
    setLoading(true);
    setError("");
    setResult(null);
    setBusy(false);
    try {
      const data = await api<LegacyCatalog>("/traffic/legacy/catalog", {
        signal: controller.signal,
      });
      if (controller.signal.aborted) return;
      if (data.sourceKind !== "legacy-report")
        throw new Error("返回的数据不是独立旧统计目录。");
      setCatalog(data);
      const next = {
        ...filter,
        batchId: data.batches.some((b) => b.id === filter.batchId)
          ? filter.batchId
          : (data.batches[0]?.id ?? ""),
      };
      setFilter(next);
      if (next.batchId) void run(next);
    } catch (e) {
      if (!controller.signal.aborted) {
        setCatalog(null);
        setError(message(e));
      }
    } finally {
      if (!controller.signal.aborted) setLoading(false);
    }
  };
  useEffect(() => {
    void load();
    return () => {
      request.current?.abort();
      catalogRequest.current?.abort();
    };
  }, []);
  const change = (key: keyof Filter, value: string) => {
    request.current?.abort();
    setBusy(false);
    setResult(null);
    setFilter((f) => ({ ...f, [key]: value }));
  };
  const selected = catalog?.batches.find((b) => b.id === filter.batchId);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    void run(filter);
  };
  return (
    <>
      <Heading
        className="flex-col sm:flex-row"
        title="旧统计归档"
        description="独立查看原 MRTG、RRD 与 P2P 报表；保持原窗口、单位和未知值。"
      >
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={loading || busy}
            onClick={() => void load()}
          >
            <RefreshCw />
            刷新历史批次
          </Button>
          {catalog?.capabilities.canPreview && role !== "member" && (
            <Button onClick={() => setImporting(true)}>
              <Upload />
              预览旧统计导入
            </Button>
          )}
        </div>
      </Heading>
      <p className="notice mb-5">
        旧统计按单个导入批次展示，不与实时计数器或 sFlow
        相加。滚动窗口可能重叠；原报告日期不等于窗口结束时间，缺失或 NaN
        保持未知。
      </p>
      <ErrorNotice message={error} />
      {loading ? (
        <Loading />
      ) : (
        catalog && (
          <div className="space-y-5">
            {!catalog.batches.length ? (
              <Card>
                <Empty
                  title="暂无可访问的旧统计批次"
                  description="工作人员可从本地旧表或 RRD 导出文件预览导入；历史归属不按今天的成员、端口或 VLAN 自动推断。"
                />
              </Card>
            ) : (
              <>
                <Card>
                  <CardHeader>
                    <CardTitle>选择独立历史来源</CardTitle>
                  </CardHeader>
                  <CardContent>
                    <form onSubmit={submit} className="space-y-4">
                      <div className="field">
                        <label htmlFor="legacy-batch">导入批次</label>
                        <select
                          id="legacy-batch"
                          value={filter.batchId}
                          onChange={(e) => change("batchId", e.target.value)}
                        >
                          {catalog.batches.map((b) => (
                            <option key={b.id} value={b.id}>
                              {b.source.name} · {time(b.importedAt)} ·{" "}
                              {b.rowCount} 条
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                        <div className="field">
                          <label htmlFor="legacy-entity-kind">原对象类型</label>
                          <select
                            id="legacy-entity-kind"
                            value={filter.entityKind}
                            onChange={(e) =>
                              change("entityKind", e.target.value)
                            }
                          >
                            <option value="">全部可见类型</option>
                            {Object.entries(legacyEntityLabels).map(
                              ([value, label]) => (
                                <option key={value} value={value}>
                                  {label}
                                </option>
                              ),
                            )}
                          </select>
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-entity-id">原对象 ID</label>
                          <Input
                            id="legacy-entity-id"
                            value={filter.entityId}
                            onChange={(e) => change("entityId", e.target.value)}
                            placeholder="原系统 ID，不映射当前库存"
                          />
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-period">原报告周期</label>
                          <select
                            id="legacy-period"
                            value={filter.period}
                            onChange={(e) => change("period", e.target.value)}
                          >
                            <option value="">全部周期（分别展示）</option>
                            {Object.entries(legacyPeriodLabels).map(
                              ([value, label]) => (
                                <option key={value} value={value}>
                                  {label}
                                </option>
                              ),
                            )}
                          </select>
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-category">原指标类别</label>
                          <Input
                            id="legacy-category"
                            value={filter.category}
                            onChange={(e) => change("category", e.target.value)}
                            placeholder="例如 bits / packets / errors"
                          />
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-family">地址族</label>
                          <select
                            id="legacy-family"
                            value={filter.family}
                            onChange={(e) => change("family", e.target.value)}
                          >
                            <option value="">全部（包括未区分）</option>
                            <option value="4">IPv4</option>
                            <option value="6">IPv6</option>
                          </select>
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-from">原报告日期起始</label>
                          <Input
                            id="legacy-from"
                            type="date"
                            value={filter.originalDayFrom}
                            onChange={(e) =>
                              change("originalDayFrom", e.target.value)
                            }
                          />
                        </div>
                        <div className="field">
                          <label htmlFor="legacy-to">原报告日期结束</label>
                          <Input
                            id="legacy-to"
                            type="date"
                            value={filter.originalDayTo}
                            onChange={(e) =>
                              change("originalDayTo", e.target.value)
                            }
                          />
                        </div>
                      </div>
                      <details className="rounded-lg border p-3">
                        <summary className="cursor-pointer text-sm font-medium">
                          按已知实际窗口筛选（UTC，可选）
                        </summary>
                        <p className="my-3 text-xs leading-6 text-muted-foreground">
                          只匹配边界已知的窗口；仍返回整条原窗口，不截断或按比例调整总量。边界未知的滚动报表会被排除。
                        </p>
                        <div className="grid gap-4 sm:grid-cols-2">
                          <div className="field">
                            <label htmlFor="legacy-window-from">
                              实际窗口起始（UTC）
                            </label>
                            <Input
                              id="legacy-window-from"
                              type="datetime-local"
                              step="1"
                              value={filter.windowFrom}
                              onChange={(e) =>
                                change("windowFrom", e.target.value)
                              }
                            />
                          </div>
                          <div className="field">
                            <label htmlFor="legacy-window-to">
                              实际窗口结束（UTC）
                            </label>
                            <Input
                              id="legacy-window-to"
                              type="datetime-local"
                              step="1"
                              value={filter.windowTo}
                              onChange={(e) =>
                                change("windowTo", e.target.value)
                              }
                            />
                          </div>
                        </div>
                      </details>
                      <Button disabled={busy}>
                        <Search />
                        {busy ? "正在读取…" : "查询旧统计"}
                      </Button>
                    </form>
                  </CardContent>
                </Card>
                {selected && (
                  <Card>
                    <CardContent className="pt-5 text-xs leading-6">
                      <div className="flex flex-wrap items-start justify-between gap-3">
                        <div className="min-w-0">
                          <p className="break-all font-semibold">
                            {selected.source.name}
                          </p>
                          <p>
                            原后端 {selected.source.backend} · 原时区{" "}
                            {selected.source.timezone} · 格式 {selected.format}
                          </p>
                          {selected.source.table && (
                            <p>原表 {selected.source.table}</p>
                          )}
                          <p className="break-all">批次 {selected.id}</p>
                          {selected.reviewed === false && (
                            <p>尚无管理员审阅记录；此批次未向成员发布。</p>
                          )}
                          {selected.checksum && (
                            <p className="break-all font-mono">
                              SHA-256 {selected.checksum}
                            </p>
                          )}
                        </div>
                        {catalog.capabilities.canDelete && role === "admin" && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => setDeleting(selected)}
                          >
                            <Trash2 />
                            删除此导入
                          </Button>
                        )}
                      </div>
                    </CardContent>
                  </Card>
                )}
                {busy ? (
                  <Loading />
                ) : (
                  result && (
                    <LegacyTrafficReport
                      result={result}
                      onPage={(offset) => void run(filter, offset)}
                      busy={busy}
                    />
                  )
                )}
              </>
            )}
            {catalog.bytes != null && catalog.maxBytes != null && (
              <p className="text-xs text-muted-foreground">
                独立历史归档 {(catalog.bytes / 1048576).toFixed(2)} /{" "}
                {(catalog.maxBytes / 1048576).toFixed(0)} MiB ·{" "}
                {catalog.batches.length}
                {catalog.maxBatches != null
                  ? ` / ${catalog.maxBatches}`
                  : ""}{" "}
                个批次
              </p>
            )}
            {!!catalog.notices?.length && (
              <div className="notice space-y-1">
                {catalog.notices.map((notice, i) => (
                  <p key={i}>{notice}</p>
                ))}
              </div>
            )}
          </div>
        )
      )}
      {importing && catalog && (
        <LegacyTrafficImport
          canImport={role === "admin" && catalog.capabilities.canImport}
          onClose={() => setImporting(false)}
          onImported={load}
        />
      )}
      {deleting && (
        <LegacyTrafficDelete
          batch={deleting}
          onClose={() => setDeleting(null)}
          onDeleted={load}
        />
      )}
    </>
  );
}
