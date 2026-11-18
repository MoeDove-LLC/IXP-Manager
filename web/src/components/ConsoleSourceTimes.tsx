import { useId } from "react";

function sourceValue(value: unknown, provided: boolean) {
  if (!provided) return "未提供源字段";
  if (value === null) return "源记录为空";
  if (typeof value === "string") {
    if (value.startsWith("0000-00-00")) return `${value}（原系统零值，时间不可用）`;
    return value || "源记录为空字符串";
  }
  return "源字段格式无法识别";
}

export default function ConsoleSourceTimes({ value }: { value: unknown }) {
  const titleId = useId();
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const timestamps = value as Record<string, unknown>;
  if (typeof timestamps.sourceId !== "string") return null;

  return (
    <section aria-labelledby={titleId} className="space-y-3 rounded-lg border p-3">
      <h3 id={titleId} className="text-sm font-medium">原系统时间</h3>
      <p className="text-xs leading-5 text-muted-foreground">
        保留来源原文，未作时区换算；不代表本系统创建或更新时间。
      </p>
      <dl className="space-y-2 text-sm">
        {[
          ["sourceId", "原记录 ID"],
          ["createdAt", "原系统创建时间"],
          ["updatedAt", "原系统更新时间"],
        ].map(([key, label]) => (
          <div key={key} className="grid gap-1 sm:grid-cols-[150px_1fr]">
            <dt className="text-xs text-muted-foreground">{label}</dt>
            <dd className="whitespace-pre-wrap break-all">
              {sourceValue(timestamps[key], Object.hasOwn(timestamps, key))}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
