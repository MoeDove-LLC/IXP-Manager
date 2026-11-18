import type { Field, Row } from "@/lib/resources";

export const resourceForm = (
  kind: string,
  fields: Field[],
  row?: Row,
): Record<string, unknown> =>
  Object.fromEntries(
    fields.map((field) => {
      const value = row?.[field.key];
      if (field.type === "password") return [field.key, ""];
      if (
        ["switches", "console-servers"].includes(kind) &&
        field.key === "active" &&
        value == null
      )
        return [field.key, true];
      if (kind === "console-connections" && field.key === "autobaud")
        return [
          field.key,
          typeof value === "boolean" ? String(value) : "unknown",
        ];
      if (kind === "console-connections" && field.key === "portLabel")
        return [
          field.key,
          value ?? (row?.port == null ? "" : String(row.port)),
        ];
      if (kind === "routers" && field.key === "rfc1997Passthrough")
        return [
          field.key,
          typeof value === "boolean" ? String(value) : "native",
        ];
      if (kind === "routers" && field.key === "role")
        return [field.key, value || "route-server"];
      if (kind === "routers" && field.key === "collectorPolicy")
        return [field.key, value || "observe"];
      if (
        ["monitor", "canPing4", "canPing6"].includes(field.key) &&
        (value == null || value === "")
      )
        return [field.key, true];
      const ports = Array.isArray(row?.portIds)
        ? row.portIds
        : row?.portId
          ? [String(row.portId)]
          : [];
      if (kind === "connections" && field.key === "linkMode")
        return [field.key, value ?? (ports.length > 1 ? "lag" : "single")];
      if (field.type === "multiselect")
        return [
          field.key,
          field.key === "portIds"
            ? ports
            : Array.isArray(value)
              ? [...value]
              : field.key === "memberIds" && row?.memberId
                ? [String(row.memberId)]
                : [],
        ];
      if (field.type === "lines")
        return [field.key, Array.isArray(value) ? value.join("\n") : ""];
      return [field.key, value ?? field.default ?? ""];
    }),
  );

export const withoutPasswords = (
  fields: Field[],
  form: Record<string, unknown>,
) => ({
  ...form,
  ...Object.fromEntries(
    fields
      .filter((field) => field.type === "password")
      .map((field) => [field.key, ""]),
  ),
});

export function sameField(
  field: Field,
  left: unknown,
  right: unknown,
): boolean {
  const normalized = (value: unknown) => {
    if (field.type === "number")
      return value === "" || value == null ? null : Number(value);
    if (field.type === "lines")
      return String(value ?? "")
        .split(/[\n,]/)
        .map((part) => part.trim())
        .filter(Boolean);
    if (typeof value === "string") return value.trim();
    return value ?? "";
  };
  return JSON.stringify(normalized(left)) === JSON.stringify(normalized(right));
}

export function isProtectedField(
  kind: string,
  field: Field,
  row: Row,
): boolean {
  return (
    !!field.readOnly ||
    (kind === "members" && field.key === "status") ||
    (kind === "connections" && field.key === "speed") ||
    (kind === "patch-ports" &&
      (["status", "memberId", "circuitId"].includes(field.key) ||
        (!["available", "prewired", "broken"].includes(String(row.status)) &&
          ["panelId", "number"].includes(field.key)))) ||
    (kind === "peers" &&
      field.key === "allowedPrefixes" &&
      Array.isArray(row.allowedRoutes) &&
      row.allowedRoutes.length > 0)
  );
}
