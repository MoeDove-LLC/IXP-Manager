import {
  Building2,
  Cable,
  ContactRound,
  EthernetPort,
  Globe2,
  Network,
  Router,
  Server,
  Users,
  Warehouse,
  ShieldCheck,
  Tag,
  NotebookPen,
  Terminal,
  Blocks,
} from "lucide-react";
import { resources as schemas, valueLabels } from "./schema";
export const rfc1997Labels: Record<string, string> = {
  native: "跟随平台默认",
  true: "保留给客户端处理",
  false: "由服务器执行限制",
};
const optionLabel = (key: string, value: string) =>
  key === "autobaud"
    ? ({ unknown: "未记录", true: "自动波特率", false: "手动参数" }[value] ??
      value)
    : key === "parity"
      ? ({ even: "偶校验", odd: "奇校验", none: "无校验" }[value] ?? value)
      : key === "flowControl"
        ? ({
            none: "无流控",
            "rts-cts": "硬件 RTS/CTS",
            "xon-xoff": "软件 XON/XOFF",
          }[value] ?? value)
        : key === "rfc1997Passthrough"
          ? (rfc1997Labels[value] ?? value)
          : (valueLabels[value] ?? value);

export type Row = Record<string, unknown> & {
  id: string;
  name?: string;
  _etag?: string;
};
export type Field = {
  key: string;
  label: string;
  section?: string;
  type?: string;
  required?: boolean;
  readOnly?: boolean;
  placeholder?: string;
  help?: string;
  options?: [string, string][];
  ref?: string;
  default?: string | number | boolean;
  min?: number;
  max?: number;
  step?: number | string;
};
export type Resource = {
  label: string;
  singular: string;
  description: string;
  icon: typeof Users;
  group: string;
  fields: Field[];
  columns: string[];
};
const icons: Record<string, typeof Users> = {
  members: Users,
  contacts: ContactRound,
  "contact-groups": ContactRound,
  "contact-group-memberships": ContactRound,
  connections: Cable,
  peers: Network,
  locations: Building2,
  racks: Warehouse,
  switches: Server,
  ports: EthernetPort,
  vlans: Globe2,
  routers: Router,
  users: ShieldCheck,
  infrastructures: Network,
  vendors: Building2,
  tags: Tag,
  "member-tags": Tag,
  notes: NotebookPen,
  equipment: Server,
  "console-servers": Terminal,
  "console-connections": Terminal,
  "patch-panels": Blocks,
  "patch-ports": EthernetPort,
  "core-links": Cable,
  "mac-addresses": Network,
  "ip-addresses": Globe2,
  "irr-sources": Globe2,
};
const memberKinds = new Set([
  "members",
  "contacts",
  "contact-groups",
  "contact-group-memberships",
  "connections",
  "peers",
  "tags",
  "member-tags",
  "notes",
  "equipment",
]);
const addressKinds = new Set([
  "vlans",
  "mac-addresses",
  "ip-addresses",
  "irr-sources",
]);
export const resources: Record<string, Resource> = Object.fromEntries(
  schemas.map((schema) => [
    schema.key,
    {
      label: schema.label,
      singular: schema.singular,
      description: schema.description,
      icon: icons[schema.key] ?? Network,
      group:
        schema.key === "users"
          ? "系统管理"
          : memberKinds.has(schema.key)
            ? "成员与互联"
            : addressKinds.has(schema.key)
              ? "地址与路由策略"
              : "基础设施",
      columns: schema.columns,
      fields: schema.fields.map((field) => ({
        key: field.key,
        label: field.label,
        section: field.section,
        readOnly: field.readOnly,
        type: field.type === "prefixes" ? "lines" : field.type,
        required: field.required,
        placeholder: field.placeholder,
        help: field.hint,
        options: field.options?.map(
          (value) => [value, optionLabel(field.key, value)] as [string, string],
        ),
        ref: field.reference,
        default: field.default as string | number | boolean | undefined,
        min: field.min,
        max: field.max,
        step: field.step,
      })),
    },
  ]),
);
export const fieldLabel = (kind: string, key: string) =>
  kind === "console-servers" && key === "connectionCount"
    ? "关联接入"
    : (resources[kind]?.fields.find((field) => field.key === key)?.label ??
      key);
export const statusLabels: Record<string, string> = {
  ...valueLabels,
  approved: "已批准",
};

// Backend remains authoritative for references, accepted values and validation limits.
export type ServerSchema = Record<
  string,
  Record<
    string,
    {
      type: string;
      required?: boolean;
      ref?: string;
      options?: string[];
      min?: number;
      max?: number;
    }
  >
>;
export function configureResources(schemas: ServerSchema) {
  for (const [kind, schema] of Object.entries(schemas)) {
    const resource = resources[kind];
    if (!resource) continue;
    resource.fields = resource.fields
      .filter((field) => schema[field.key])
      .map((field) => {
        const server = schema[field.key];
        return {
          ...field,
          required: server.required,
          ref: server.ref,
          min: server.min,
          max: server.max,
          options:
            server.options?.map(
              (value) =>
                [value, optionLabel(field.key, value)] as [string, string],
            ) ?? field.options,
          type:
            server.type === "references"
              ? "multiselect"
              : server.type === "url"
                ? "url"
                : field.type,
        };
      });
  }
}
