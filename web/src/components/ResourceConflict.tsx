import { useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { type Row, type Field } from "@/lib/resources";
import {
  isProtectedField,
  resourceForm,
  sameField,
  withoutPasswords,
} from "@/lib/resource-conflicts";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorNotice, Loading, time } from "@/components/shared";

export type ResourceConflictState = {
  mode: "edit" | "delete";
  baseline: Row;
  latest?: Row;
  loading: boolean;
  error?: string;
  deleted?: boolean;
  attempt: number;
};
export default function ResourceConflict({
  kind,
  fields,
  refs,
  draft,
  state,
  onRefresh,
  onResolve,
  onCloseDeleted,
}: {
  kind: string;
  fields: Field[];
  refs: Record<string, Row[]>;
  draft: Record<string, unknown>;
  state: ResourceConflictState;
  onRefresh: () => void;
  onResolve: (latest: Row, merged: Record<string, unknown>) => void;
  onCloseDeleted: () => void;
}) {
  const [choices, setChoices] = useState<Record<string, "mine" | "server">>({}),
    [ack, setAck] = useState(false);
  const before = resourceForm(kind, fields, state.baseline),
    current = resourceForm(kind, fields, state.latest),
    local = withoutPasswords(fields, draft);
  const rows = fields
    .filter((field) => field.type !== "password")
    .map((field) => {
      const mine =
          state.mode === "edit" &&
          !sameField(field, before[field.key], local[field.key]),
        server =
          !!state.latest &&
          !sameField(field, before[field.key], current[field.key]),
        protectedField =
          !!state.latest && isProtectedField(kind, field, state.latest);
      const overlap =
        mine &&
        server &&
        !sameField(field, local[field.key], current[field.key]) &&
        !protectedField;
      return {
        field,
        mine,
        server,
        protectedField,
        overlap,
        choice: protectedField
          ? "server"
          : overlap
            ? choices[field.key]
            : mine
              ? "mine"
              : "server",
      };
    })
    .filter((row) => row.mine || row.server);
  const unresolved = rows.filter((row) => row.overlap && !row.choice).length;
  const value = (field: Field, v: unknown) => {
    if (v == null || v === "" || (Array.isArray(v) && !v.length))
      return "未设置";
    if (field.ref) {
      const label = (id: unknown) => {
        const found = refs[field.ref!]?.find((row) => row.id === id);
        return found?.name ? `${found.name} · ${String(id)}` : String(id);
      };
      return Array.isArray(v) ? v.map(label).join("\n") : label(v);
    }
    if (typeof v === "boolean") return v ? "启用" : "停用";
    if (Array.isArray(v)) return v.map(String).join("\n");
    return field.options?.find(([key]) => key === String(v))?.[1] ?? String(v);
  };
  const resolve = () => {
    if (!state.latest || state.loading || unresolved || !ack) return;
    const merged = { ...current };
    for (const row of rows)
      if (row.choice === "mine") merged[row.field.key] = local[row.field.key];
    onResolve(state.latest, withoutPasswords(fields, merged));
  };
  const downloadDraft = () => {
    const safe = Object.fromEntries(
      fields
        .filter((field) => field.type !== "password")
        .map((field) => [field.key, local[field.key]]),
    );
    const url = URL.createObjectURL(
      new Blob([JSON.stringify({ kind, values: safe }, null, 2)], {
        type: "application/json",
      }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = `${kind}-unsaved-draft.json`;
    link.click();
    URL.revokeObjectURL(url);
  };
  return (
    <section
      className="space-y-4 rounded-lg border border-amber-200 bg-amber-50/40 p-4"
      aria-label={state.mode === "edit" ? "编辑版本冲突" : "删除版本冲突"}
    >
      <div>
        <h3 className="font-semibold">
          {state.deleted
            ? "这条记录已被删除"
            : state.mode === "edit"
              ? "记录已变化，请核对合并方案"
              : "记录已变化，请重新核对删除对象"}
        </h3>
        <p className="mt-2 text-xs leading-6 text-muted-foreground">
          {state.mode === "edit"
            ? "本次保存未写入。你的非密码草稿保留在当前窗口；下方显示打开时、服务器当前值和你的修改，不会自动覆盖。"
            : "本次删除未执行。核对服务器的最新记录后，仍需再次明确确认删除。"}
        </p>
      </div>
      {fields.some((field) => field.type === "password") &&
        state.mode === "edit" && (
          <p className="notice">
            密码字段已清空；不会显示密码或内部凭据，也不会复用上次填写的密码。如需修改，请在核对完成后重新输入。
          </p>
        )}
      <ErrorNotice message={state.error ?? ""} />
      {state.loading ? (
        <Loading />
      ) : state.latest ? (
        <>
          <p className="text-xs text-muted-foreground">
            服务器更新时间：{time(state.latest.updatedAt)}
          </p>
          {!rows.length ? (
            <p className="text-sm leading-6">
              可见字段没有差异，但记录修订已变化。请确认使用当前服务器版本继续操作。
            </p>
          ) : (
            <div className="space-y-4">
              {rows.map((row) => (
                <div
                  key={row.field.key}
                  className="rounded-lg border bg-background p-3"
                  data-conflict-field={row.field.key}
                >
                  <div className="mb-3 flex flex-wrap items-center gap-2">
                    <span className="text-sm font-semibold">
                      {row.field.label}
                    </span>
                    <Badge variant="outline">
                      {row.protectedField
                        ? "只读字段采用服务器值"
                        : row.overlap
                          ? "双方修改了不同的值"
                          : row.mine && row.server
                            ? "双方结果一致"
                            : row.mine
                              ? "仅我的修改"
                              : "仅服务器更新"}
                    </Badge>
                  </div>
                  <div
                    className={`grid gap-3 ${state.mode === "edit" ? "sm:grid-cols-3" : "sm:grid-cols-2"}`}
                  >
                    <div>
                      <p className="mb-1 text-[11px] text-muted-foreground">
                        打开时
                      </p>
                      <p className="whitespace-pre-wrap break-all text-xs leading-5">
                        {value(row.field, before[row.field.key])}
                      </p>
                    </div>
                    <div>
                      <p className="mb-1 text-[11px] text-muted-foreground">
                        服务器当前值
                      </p>
                      <p className="whitespace-pre-wrap break-all text-xs leading-5">
                        {value(row.field, current[row.field.key])}
                      </p>
                    </div>
                    {state.mode === "edit" && (
                      <div>
                        <p className="mb-1 text-[11px] text-muted-foreground">
                          我的草稿
                        </p>
                        <p className="whitespace-pre-wrap break-all text-xs leading-5">
                          {value(row.field, local[row.field.key])}
                        </p>
                      </div>
                    )}
                  </div>
                  {row.overlap ? (
                    <fieldset className="mt-3 flex flex-wrap gap-4 border-t pt-3">
                      <legend className="sr-only">
                        {row.field.label}冲突处理
                      </legend>
                      <label className="flex items-center gap-2 text-xs">
                        <input
                          type="radio"
                          name={`conflict-${row.field.key}`}
                          checked={row.choice === "mine"}
                          onChange={() => {
                            setChoices({ ...choices, [row.field.key]: "mine" });
                            setAck(false);
                          }}
                        />
                        保留我的修改
                      </label>
                      <label className="flex items-center gap-2 text-xs">
                        <input
                          type="radio"
                          name={`conflict-${row.field.key}`}
                          checked={row.choice === "server"}
                          onChange={() => {
                            setChoices({
                              ...choices,
                              [row.field.key]: "server",
                            });
                            setAck(false);
                          }}
                        />
                        采用服务器值
                      </label>
                    </fieldset>
                  ) : (
                    state.mode === "edit" && (
                      <p className="mt-3 text-xs text-primary">
                        合并建议：
                        {row.choice === "mine"
                          ? "保留我的修改"
                          : "采用服务器当前值"}
                      </p>
                    )
                  )}
                </div>
              ))}
            </div>
          )}
          <label className="flex items-start gap-2 text-sm leading-6">
            <input
              className="mt-1"
              type="checkbox"
              checked={ack}
              onChange={(event) => setAck(event.target.checked)}
            />
            {state.mode === "edit"
              ? "我已核对上述变化和合并建议，使用此服务器版本继续编辑。"
              : "我已核对最新记录，仍准备删除该记录。"}
          </label>
          <Button
            type="button"
            disabled={unresolved > 0 || !ack || !state.latest._etag}
            onClick={resolve}
          >
            {state.mode === "edit"
              ? "采用方案并返回编辑"
              : "使用核对后的删除版本"}
          </Button>
          {unresolved > 0 && (
            <p className="text-xs text-muted-foreground">
              还有 {unresolved} 个字段需要明确选择。
            </p>
          )}
        </>
      ) : state.deleted ? (
        <p className="text-sm leading-6">
          {state.mode === "edit"
            ? "无法将草稿合并到已经不存在的记录。可保存非密码草稿副本，再按需要重新建档。"
            : "服务器已不存在此记录，无需再次删除。"}
        </p>
      ) : null}
      <div className="flex flex-wrap gap-2 border-t pt-3">
        {!state.deleted && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={state.loading}
            onClick={onRefresh}
          >
            <RefreshCw />
            重新获取供核对的版本
          </Button>
        )}
        {state.mode === "edit" && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={downloadDraft}
          >
            <Download />
            保存非密码草稿副本
          </Button>
        )}
        {state.mode === "delete" && state.deleted && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onCloseDeleted}
          >
            关闭并刷新列表
          </Button>
        )}
      </div>
    </section>
  );
}
