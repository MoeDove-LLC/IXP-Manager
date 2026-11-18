import type { ProvisioningDiff } from "@/lib/provisioning";

export default function ArtifactDiff({ diff }: { diff: ProvisioningDiff }) {
  return (
    <div>
      <div className="mb-3 flex flex-wrap gap-3 text-xs text-muted-foreground">
        <span>
          {diff.baseId
            ? `对比已批准版本 ${diff.baseId.slice(0, 12)}`
            : "尚无已批准基线，此版本内容均为新增"}
        </span>
        {!diff.baseUnavailable && (
          <>
            <span className="text-green-700">+{diff.added}</span>
            <span className="text-red-700">−{diff.removed}</span>
          </>
        )}
      </div>
      {diff.baseUnavailable && (
        <p className="notice mb-3">
          旧基线文件已清理，下方内容不能视为完整差异。请同时检查当前完整配置和保留的版本元数据。
        </p>
      )}
      {diff.truncated && (
        <p className="notice mb-3">
          差异行数超过显示限制，请同时检查完整配置。
        </p>
      )}
      <pre className="max-h-[540px] overflow-auto rounded-lg border bg-muted/30 py-3 text-xs leading-6">
        <code>
          {diff.changes.map((line, index) => (
            <div
              key={index}
              className={`px-4 ${line.type === "add" ? "bg-green-50 text-green-800" : line.type === "remove" ? "bg-red-50 text-red-800" : "text-muted-foreground"}`}
            >
              {line.type === "add"
                ? "+ "
                : line.type === "remove"
                  ? "− "
                  : "  "}
              {line.text}
            </div>
          ))}
        </code>
      </pre>
    </div>
  );
}
