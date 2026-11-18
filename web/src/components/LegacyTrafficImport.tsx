import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, APIError } from "@/lib/api";
import { ErrorNotice, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import LegacyTrafficReport from "@/components/LegacyTrafficReport";
import type { LegacyInput, LegacyPreview } from "@/lib/legacy-traffic";

export default function LegacyTrafficImport({
  canImport,
  onClose,
  onImported,
}: {
  canImport: boolean;
  onClose: () => void;
  onImported: () => Promise<void>;
}) {
  const [format, setFormat] = useState("ixp-table-json"),
    [name, setName] = useState(""),
    [timezone, setTimezone] = useState(""),
    [backend, setBackend] = useState("mrtg"),
    [table, setTable] = useState("traffic_daily"),
    [unit, setUnit] = useState("bits"),
    [entityKind, setEntityKind] = useState("physical-interface"),
    [entityId, setEntityId] = useState(""),
    [content, setContent] = useState(""),
    [expert, setExpert] = useState(""),
    [fileName, setFileName] = useState(""),
    [busy, setBusy] = useState(false),
    [reading, setReading] = useState(false),
    [error, setError] = useState(""),
    [preview, setPreview] = useState<LegacyPreview | null>(null),
    [reviewedInput, setReviewedInput] = useState<LegacyInput | null>(null),
    [ack, setAck] = useState(false),
    [ownerAck, setOwnerAck] = useState(false),
    [stale, setStale] = useState(false);
  const request = useRef<AbortController | null>(null),
    alive = useRef(true),
    fileRead = useRef(0);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      request.current?.abort();
      fileRead.current++;
    };
  }, []);
  const chooseFile = async (file?: File) => {
    const sequence = ++fileRead.current;
    if (!file) return;
    setError("");
    setContent("");
    setFileName("");
    if (file.size > 16 * 1048576) {
      setError("原导出文件不得超过 16 MiB。");
      return;
    }
    setReading(true);
    try {
      const text = await file.text();
      if (!alive.current || sequence !== fileRead.current) return;
      setContent(text);
      setFileName(file.name);
      if (!name.trim()) setName(file.name);
    } catch (e) {
      if (alive.current) setError(`无法读取本地文件：${message(e)}`);
    } finally {
      if (alive.current && sequence === fileRead.current) setReading(false);
    }
  };
  const inspect = async (event: FormEvent) => {
    event.preventDefault();
    if (busy || reading) return;
    setBusy(true);
    setError("");
    setStale(false);
    const controller = new AbortController();
    request.current = controller;
    try {
      if (!content.trim())
        throw new Error("请选择本地导出文件或填写原始内容。");
      if (new TextEncoder().encode(content).length > 16 * 1048576)
        throw new Error("原始内容不得超过 16 MiB。");
      let extra: Pick<LegacyInput, "columns" | "windows" | "owners"> = {};
      if (expert.trim()) {
        const parsed = JSON.parse(expert);
        if (
          !parsed ||
          typeof parsed !== "object" ||
          Array.isArray(parsed) ||
          Object.keys(parsed).some(
            (k) =>
              !["columns", "windows", "owners"].includes(k) ||
              !Array.isArray(parsed[k]),
          )
        )
          throw new Error("专家配置仅支持 columns、windows、owners 三个数组。");
        extra = parsed;
      }
      const input: LegacyInput = {
        format,
        source: {
          name,
          timezone,
          backend,
          ...(format === "ixp-table-json"
            ? { table }
            : { unit, entityKind, entityId }),
        },
        content,
        ...extra,
      };
      const result = await api<LegacyPreview>("/traffic/legacy/preview", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({ input }),
      });
      if (controller.signal.aborted) return;
      if (!result.batchId || !/^[a-f0-9]{64}$/.test(result.checksum))
        throw new Error("预览未返回有效批次及校验和，无法导入。");
      setReviewedInput(input);
      setPreview(result);
      setAck(false);
      setOwnerAck(false);
    } catch (e) {
      if (!controller.signal.aborted) setError(message(e));
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const commit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!reviewedInput || !preview || !ack || busy || stale) return;
    const form = event.currentTarget,
      fields = new FormData(form),
      controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      await api("/traffic/legacy/import", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({
          input: reviewedInput,
          expectedChecksum: preview.checksum,
          acknowledgeWindows: true,
          acknowledgeOwnership: reviewedInput.owners?.length
            ? ownerAck
            : undefined,
          currentPassword: fields.get("currentPassword"),
          code: fields.get("code") || undefined,
        }),
      });
      if (!controller.signal.aborted) {
        onClose();
        await onImported();
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      setError(message(e));
      if (e instanceof APIError && [409, 404].includes(e.status)) {
        setStale(true);
        setAck(false);
        setOwnerAck(false);
        setError(
          "内容校验和或历史归属证据已变化，本次未导入。请返回输入并重新预览。",
        );
      }
    } finally {
      form.reset();
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  const hasOwners = !!reviewedInput?.owners?.length;
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent
        className="max-w-5xl"
        onEscapeKeyDown={(event) => busy && event.preventDefault()}
        onPointerDownOutside={(event) => busy && event.preventDefault()}
      >
        <DialogTitle className="text-lg font-semibold">
          {preview ? "审阅独立旧统计导入" : "预览旧统计导入"}
        </DialogTitle>
        <DialogDescription className="text-sm leading-6 text-muted-foreground">
          只读取你选择的本地 UTF-8 导出内容，不获取
          URL、不执行命令。默认仅工作人员可见；历史归属必须有具体证据，不根据当前库存自动关联。
        </DialogDescription>
        <ErrorNotice message={error} />
        {!preview ? (
          <form onSubmit={inspect} className="space-y-4">
            <fieldset disabled={busy || reading} className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <div className="field">
                  <label htmlFor="legacy-input-name">历史来源名称</label>
                  <Input
                    id="legacy-input-name"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    required
                    maxLength={200}
                  />
                </div>
                <div className="field">
                  <label htmlFor="legacy-input-timezone">
                    原来源时区（IANA）
                  </label>
                  <Input
                    id="legacy-input-timezone"
                    value={timezone}
                    onChange={(e) => setTimezone(e.target.value)}
                    required
                    placeholder="例如 Europe/Dublin 或 UTC"
                  />
                </div>
                <div className="field">
                  <label htmlFor="legacy-input-format">导出格式</label>
                  <select
                    id="legacy-input-format"
                    value={format}
                    onChange={(e) => setFormat(e.target.value)}
                  >
                    <option value="ixp-table-json">旧 IXP 数据表 JSON</option>
                    <option value="rrd-xport-json">RRD xport JSON</option>
                    <option value="rrd-xport-xml">RRD xport XML</option>
                  </select>
                </div>
                <div className="field">
                  <label htmlFor="legacy-input-backend">原采集后端</label>
                  <select
                    id="legacy-input-backend"
                    value={backend}
                    onChange={(e) => setBackend(e.target.value)}
                  >
                    <option value="mrtg">MRTG</option>
                    <option value="rrd">RRD</option>
                    <option value="sflow-rrd">sFlow / RRD</option>
                  </select>
                </div>
                {format === "ixp-table-json" ? (
                  <div className="field">
                    <label htmlFor="legacy-input-table">原数据库表</label>
                    <select
                      id="legacy-input-table"
                      value={table}
                      onChange={(e) => setTable(e.target.value)}
                    >
                      <option value="traffic_daily">traffic_daily</option>
                      <option value="traffic_daily_phys_ints">
                        traffic_daily_phys_ints
                      </option>
                      <option value="p2p_daily_stats">p2p_daily_stats</option>
                    </select>
                  </div>
                ) : (
                  <>
                    <div className="field">
                      <label htmlFor="legacy-input-unit">原导出基本单位</label>
                      <select
                        id="legacy-input-unit"
                        value={unit}
                        onChange={(e) => setUnit(e.target.value)}
                      >
                        {["bits", "bytes", "packets", "events"].map((u) => (
                          <option key={u}>{u}</option>
                        ))}
                      </select>
                    </div>
                    <div className="field">
                      <label htmlFor="legacy-input-kind">原对象类型</label>
                      <select
                        id="legacy-input-kind"
                        value={entityKind}
                        onChange={(e) => setEntityKind(e.target.value)}
                      >
                        <option value="customer">原成员</option>
                        <option value="physical-interface">原物理接口</option>
                        <option value="dataset">原数据集</option>
                      </select>
                    </div>
                    <div className="field">
                      <label htmlFor="legacy-input-entity">原对象 ID</label>
                      <Input
                        id="legacy-input-entity"
                        required
                        value={entityId}
                        onChange={(e) => setEntityId(e.target.value)}
                      />
                    </div>
                  </>
                )}
              </div>
              <div className="field">
                <label htmlFor="legacy-input-file">
                  本地 UTF-8 导出文件（最多 16 MiB）
                </label>
                <Input
                  id="legacy-input-file"
                  type="file"
                  accept=".json,.xml,.txt,application/json,application/xml,text/plain,text/xml"
                  onChange={(e) => void chooseFile(e.target.files?.[0])}
                />
                {fileName && (
                  <p className="text-xs text-muted-foreground">
                    已读取 {fileName}，可在下方核对内容。
                  </p>
                )}
              </div>
              <div className="field">
                <label htmlFor="legacy-input-content">原始导出内容</label>
                <textarea
                  id="legacy-input-content"
                  className="min-h-40 font-mono text-xs"
                  value={content}
                  onChange={(e) => {
                    fileRead.current++;
                    setContent(e.target.value);
                    setFileName("");
                  }}
                  required
                  spellCheck={false}
                />
              </div>
              <details className="rounded-lg border p-3">
                <summary className="cursor-pointer text-sm font-medium">
                  专家配置：列、明确窗口和历史归属证据
                </summary>
                <p className="my-3 text-xs leading-6 text-muted-foreground">
                  RRD 列须明确 index、原 legend、direction 和 statistic。windows
                  必须提供 recordId、period、start/end 和 evidence；owners
                  必须提供原
                  entityKind/entityId、memberId/memberRevision、validFrom/validTo、referenceSha256
                  和 description。未提供时不会推断历史所有者。
                </p>
                <textarea
                  id="legacy-input-expert"
                  aria-label="历史导入专家 JSON"
                  className="min-h-32 font-mono text-xs"
                  value={expert}
                  onChange={(e) => setExpert(e.target.value)}
                  placeholder={'{"columns": [], "windows": [], "owners": []}'}
                  spellCheck={false}
                />
              </details>
            </fieldset>
            <div className="flex justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                onClick={onClose}
                disabled={busy}
              >
                取消
              </Button>
              <Button disabled={busy || reading}>
                {reading
                  ? "正在读取本地文件…"
                  : busy
                    ? "正在解析…"
                    : "解析并预览"}
              </Button>
            </div>
          </form>
        ) : (
          <>
            <div className="notice text-sm leading-6">
              <p>
                {preview.source.name} · {preview.source.backend} · 原时区{" "}
                {preview.source.timezone}
              </p>
              {preview.source.unit && (
                <p>
                  声明单位 {preview.source.unit} · 原对象{" "}
                  {preview.source.entityKind} / {preview.source.entityId}
                </p>
              )}
              <p>
                {preview.rowCount} 条记录，其中 {preview.staffOnlyRows}{" "}
                条仅工作人员可见。预览最多显示前 100 条，尚未保存。
              </p>
              <p className="break-all font-mono text-xs">
                SHA-256 {preview.checksum}
              </p>
            </div>
            {reviewedInput &&
            (reviewedInput.columns?.length ||
              reviewedInput.windows?.length ||
              reviewedInput.owners?.length) ? (
              <details className="rounded-lg border p-3">
                <summary className="cursor-pointer text-sm font-semibold">
                  审阅已冻结的列、窗口及历史归属声明
                </summary>
                <pre className="mt-3 max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">
                  {JSON.stringify(
                    {
                      columns: reviewedInput.columns ?? [],
                      windows: reviewedInput.windows ?? [],
                      owners: reviewedInput.owners ?? [],
                    },
                    null,
                    2,
                  )}
                </pre>
              </details>
            ) : null}
            <LegacyTrafficReport
              result={{
                sourceKind: "legacy-report",
                batchId: preview.batchId,
                rows: preview.rows,
                totalRows: preview.rowCount,
                offset: 0,
                limit: 100,
                truncated: false,
                noAggregation: true,
                notices: preview.notices,
              }}
              onPage={() => {}}
              busy
            />
            {canImport ? (
              <form onSubmit={commit} className="space-y-4">
                <label className="flex items-start gap-2 rounded-lg border p-3">
                  <input
                    id="legacy-import-ack"
                    type="checkbox"
                    className="mt-1"
                    checked={ack}
                    onChange={(e) => setAck(e.target.checked)}
                    disabled={busy || stale}
                  />
                  <span>
                    我已核对原来源、时区、窗口边界与未知值；此导入独立保留，不跨窗口或与实时来源合计。
                  </span>
                </label>
                {hasOwners && (
                  <label className="flex items-start gap-2 rounded-lg border p-3">
                    <input
                      id="legacy-owner-ack"
                      type="checkbox"
                      className="mt-1"
                      checked={ownerAck}
                      onChange={(e) => setOwnerAck(e.target.checked)}
                      disabled={busy || stale}
                    />
                    <span>
                      我已逐项审阅历史归属证据、有效期与当前成员实体代次，确认这些记录可向所指定成员披露。
                    </span>
                  </label>
                )}
                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="field">
                    <label htmlFor="legacy-import-password">
                      当前管理员密码
                    </label>
                    <Input
                      id="legacy-import-password"
                      name="currentPassword"
                      type="password"
                      autoComplete="current-password"
                      required
                      disabled={busy || stale}
                    />
                  </div>
                  <div className="field">
                    <label htmlFor="legacy-import-code">
                      验证器代码或恢复码（已启用时填写）
                    </label>
                    <Input
                      id="legacy-import-code"
                      name="code"
                      autoComplete="one-time-code"
                      disabled={busy || stale}
                    />
                  </div>
                </div>
                <div className="flex flex-wrap justify-end gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      setPreview(null);
                      setReviewedInput(null);
                      setAck(false);
                      setOwnerAck(false);
                      setError("");
                    }}
                  >
                    返回输入重新预览
                  </Button>
                  <Button
                    disabled={busy || stale || !ack || (hasOwners && !ownerAck)}
                  >
                    {busy ? "正在导入…" : "确认导入此批次"}
                  </Button>
                </div>
              </form>
            ) : (
              <div className="flex flex-wrap items-center justify-between gap-3">
                <p className="text-sm text-muted-foreground">
                  当前账号可预览，实际导入需要管理员重新认证。
                </p>
                <Button
                  variant="outline"
                  onClick={() => {
                    setPreview(null);
                    setReviewedInput(null);
                  }}
                >
                  返回输入
                </Button>
              </div>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
