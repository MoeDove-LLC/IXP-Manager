import { useEffect, useRef, useState } from "react";
import { api, APIError } from "@/lib/api";
import { ErrorNotice, message } from "@/components/shared";
import { Button } from "@/components/ui/button";
import type {
  TrafficCatalog,
  TrafficSourceControl as Source,
} from "@/lib/traffic";

const statuses: Record<string, string> = {
  "automatic-snmp": "自动选择 SNMP",
  "automatic-router": "自动选择唯一的路由采集器",
  explicit: "已指定来源",
  ambiguous: "来源不唯一",
  disabled: "已停用归档",
  "mapping-required": "等待 SNMP 映射",
  unavailable: "暂无可用来源",
};
const label = (control: Source, value: string) =>
  control.choices.find((choice) => choice.value === value)?.label ??
  "此前选择的来源（当前不可用）";

export default function TrafficSourceControl({
  portId,
  control,
  canWrite,
  onSaved,
}: {
  portId: string;
  control: Source;
  canWrite: boolean;
  onSaved: () => Promise<void>;
}) {
  const [baseline, setBaseline] = useState(control),
    [selection, setSelection] = useState(control.configured);
  const [latest, setLatest] = useState<Source | null>(null),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false),
    [unavailable, setUnavailable] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);
  const eligible = canWrite && baseline.editable && !unavailable;
  const validChoice = baseline.choices.some(
    (choice) => choice.value === selection,
  );
  const submit = async () => {
    if (!eligible || latest || !validChoice || !baseline.revision) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api("/traffic/source", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({
          portId,
          selection,
          expectedRevision: baseline.revision,
        }),
      });
      if (!controller.signal.aborted) await onSaved();
    } catch (e) {
      if (controller.signal.aborted) return;
      if (e instanceof APIError && e.status === 409) {
        setError("来源或端口关联已被更新；所选来源已保留，请核对当前设置。");
        try {
          const catalog = await api<TrafficCatalog>("/traffic/catalog", {
            signal: controller.signal,
          });
          if (controller.signal.aborted) return;
          const current = catalog.targets.find(
            (target) => target.scope === "port" && target.id === portId,
          )?.sourceControl;
          if (!current || !current.editable) {
            setUnavailable(true);
            setError("当前无法编辑此端口来源，请刷新对象权限。");
          } else setLatest(current);
        } catch (readError) {
          if (!controller.signal.aborted) {
            setUnavailable(true);
            setError(`无法重新核对来源：${message(readError)}。草稿未提交。`);
          }
        }
      } else {
        setError(message(e));
        if (e instanceof APIError && [403, 404].includes(e.status))
          setUnavailable(true);
      }
    } finally {
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  return (
    <div
      className="rounded-lg border bg-muted/20 p-3"
      data-testid="traffic-source-control"
    >
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2 text-xs">
        <span className="font-medium">计数器归档来源</span>
        <span className="text-muted-foreground">
          {statuses[baseline.status] ?? baseline.status}
        </span>
      </div>
      {eligible ? (
        <div className="flex flex-wrap items-end gap-2">
          <div className="field min-w-0 flex-1 basis-52">
            <label htmlFor="traffic-source" className="sr-only">
              计数器归档来源
            </label>
            <select
              id="traffic-source"
              value={selection}
              disabled={busy || !!latest}
              onChange={(event) => {
                setSelection(event.target.value);
                setNotice("");
              }}
            >
              {!validChoice && (
                <option value={selection} disabled>
                  此前选择的来源（当前不可用）
                </option>
              )}
              {baseline.choices.map((choice) => (
                <option key={choice.value} value={choice.value}>
                  {choice.label}
                </option>
              ))}
            </select>
          </div>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={
              busy ||
              !!latest ||
              !validChoice ||
              !baseline.revision ||
              selection === baseline.configured
            }
            onClick={() => void submit()}
          >
            {busy ? "正在保存…" : "保存归档来源"}
          </Button>
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">
          {label(baseline, baseline.configured)}
        </p>
      )}
      <p className="mt-2 text-xs leading-6 text-muted-foreground">
        选择此物理端口唯一的计数来源；跨来源不计算差值，修改不回填缺失历史，也不改变设备配置。
      </p>
      {error && (
        <div className="mt-3">
          <ErrorNotice message={error} />
        </div>
      )}
      {latest && (
        <div className="notice mt-3 space-y-2">
          <p>打开时：{label(baseline, baseline.configured)}</p>
          <p>
            当前设置：{label(latest, latest.configured)} ·{" "}
            {statuses[latest.status] ?? latest.status}
          </p>
          <p>保留草稿：{label(baseline, selection)}</p>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={busy}
            onClick={() => {
              setBaseline(latest);
              setLatest(null);
              setError("");
              if (selection === latest.configured) void onSaved();
              else
                setNotice("已采用当前审阅版本；请核对所选来源，再点击保存。");
            }}
          >
            按当前设置重新核对
          </Button>
        </div>
      )}
      {notice && <p className="mt-3 text-xs text-muted-foreground">{notice}</p>}
      {unavailable && (
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="mt-3"
          onClick={() => void onSaved()}
        >
          刷新对象权限
        </Button>
      )}
    </div>
  );
}
