import { useHashParameters } from "@/lib/hash-route";
import AS112Guidance from "@/components/AS112Guidance";
import {
  lazy,
  Suspense,
  Fragment,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  Download,
  Pencil,
  Plus,
  Search,
  Trash2,
  RefreshCw,
  KeyRound,
} from "lucide-react";
import { api, save, APIError, captureSessionContext, resourceMutation } from "@/lib/api";
import { resourceForm, withoutPasswords } from "@/lib/resource-conflicts";
import { singleRouterFamily } from "@/lib/router-platforms";
import {
  fanoutDependencies,
  fanoutDraftProblem,
  fanoutPortLabel,
  fanoutPortOwner,
} from "@/lib/reseller-fanouts";
import type { ResourceConflictState } from "@/components/ResourceConflict";
import {
  resources,
  fieldLabel,
  statusLabels,
  type Row,
  type Field,
} from "@/lib/resources";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Empty,
  ErrorNotice,
  Heading,
  Loading,
  message,
} from "@/components/shared";

const MemberLogo = lazy(() => import("@/components/MemberLogo"));
const MemberWelcomeEmail = lazy(() => import("@/components/MemberWelcomeEmail"));
const PeerCredentials = lazy(() => import("@/components/PeerCredentials"));
const PeerReadiness = lazy(() => import("@/components/PeerReadiness"));
const ResourceConflict = lazy(() => import("@/components/ResourceConflict"));
const FabricAssignment = lazy(() => import("@/components/FabricAssignment"));
const ContactGroupExport = lazy(() => import("@/components/ContactGroupExport"));
const ResellerFanoutFields = lazy(() => import("@/components/ResellerFanoutFields"));
const referenceLabel = (
  row: Row,
  kind: string,
  refs: Record<string, Row[]>,
) => {
  const name = row.name ?? row.id;
  if (["vlans", "switches"].includes(kind) && row.infrastructureId) {
    const fabric = refs.infrastructures?.find(
      (i) => i.id === row.infrastructureId,
    )?.name;
    return `${name} · ${fabric ?? String(row.infrastructureId).slice(0, 8)}`;
  }
  return name;
};
const portLabel = (port: Row, refs: Record<string, Row[]>) =>
  `${port.switchName ?? refs.switches?.find((s) => s.id === port.switchId)?.name ?? port.switchId ?? "交换机"} / ${port.name ?? port.id}`;
const connectionPorts = (row?: Row) =>
  Array.isArray(row?.portIds)
    ? (row.portIds as string[])
    : row?.portId
      ? [String(row.portId)]
      : [];
const relatedKinds = (kind: string, fields: Field[]) => [
  ...new Set([
    ...fields.flatMap((field) => (field.ref ? [field.ref] : [])),
    ...(kind === "connections" ? ["switches"] : []),
    ...(kind === "reseller-fanouts" ? fanoutDependencies : []),
    ...(kind === "connections" ||
    fields.some((field) => field.ref === "vlans" || field.ref === "switches")
      ? ["infrastructures"]
      : []),
  ]),
];
function FieldInput({
  field: f,
  value,
  onChange,
  refs,
  editing,
}: {
  field: Field;
  value: unknown;
  onChange: (v: unknown) => void;
  refs: Record<string, Row[]>;
  editing: boolean;
}) {
  const id = `field-${f.key}`;
  const [portSearch, setPortSearch] = useState("");
  return (
    <div
      className={
        f.type === "lines" || f.type === "textarea" || f.type === "multiselect"
          ? "field md:col-span-2"
          : "field"
      }
    >
      <label id={`${id}-label`} htmlFor={id}>
        {f.label}
        {f.required && <span className="ml-1 text-destructive">*</span>}
      </label>
      {f.type === "multiselect" && f.ref ? (
        <div
          id={id}
          role="group"
          aria-labelledby={`${id}-label`}
          className="rounded-md border"
        >
          <div className="border-b p-2">
            <Input
              aria-label={f.ref === "ports" ? "搜索物理端口" : `搜索${f.label}`}
              placeholder={
                f.ref === "ports" ? "按交换机或端口名称查找…" : "按名称查找…"
              }
              value={portSearch}
              onChange={(e) => setPortSearch(e.target.value)}
            />
          </div>
          <div className="max-h-56 overflow-y-auto p-2">
            {(refs[f.ref] ?? [])
              .filter((port) =>
                (f.ref === "ports"
                  ? portLabel(port, refs)
                  : String(port.name ?? port.id)
                )
                  .toLowerCase()
                  .includes(portSearch.toLowerCase()),
              )
              .map((port) => (
                <label
                  key={port.id}
                  className="flex items-center gap-3 rounded p-2 hover:bg-muted/50"
                >
                  <input
                    type="checkbox"
                    name={f.key}
                    value={port.id}
                    checked={Array.isArray(value) && value.includes(port.id)}
                    onChange={(e) => {
                      const selected = Array.isArray(value) ? value : [];
                      onChange(
                        e.target.checked
                          ? [...selected, port.id]
                          : selected.filter((v) => v !== port.id),
                      );
                    }}
                  />
                  <span className="min-w-0 flex-1 truncate text-sm">
                    {f.ref === "ports"
                      ? portLabel(port, refs)
                      : String(port.name ?? port.id)}
                  </span>
                  {f.ref === "ports" && (
                    <span className="text-xs text-muted-foreground">
                      {Number(port.speed) > 0
                        ? `${port.speed} Mbps`
                        : "速率未登记"}
                      {port.status === "inactive" ? " · 停用" : ""}
                    </span>
                  )}
                </label>
              ))}
          </div>
          <p className="border-t px-3 py-2 text-xs text-muted-foreground">
            已选择 {Array.isArray(value) ? value.length : 0}{" "}
            {f.ref === "ports" ? "个物理端口" : "个项目"}
          </p>
        </div>
      ) : f.ref || f.options ? (
        <select
          id={id}
          required={f.required}
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">
            {["baudRate", "parity", "stopBits", "flowControl"].includes(f.key)
              ? "未记录"
              : `请选择${f.label}`}
          </option>
          {f.ref
            ? (refs[f.ref] ?? []).map((r) => (
                <option key={r.id} value={r.id}>
                  {referenceLabel(r, f.ref!, refs)}
                </option>
              ))
            : f.options?.map(([v, l]) => (
                <option key={v} value={v}>
                  {l}
                </option>
              ))}
        </select>
      ) : f.type === "checkbox" ? (
        <label className="flex h-10 items-center gap-2 rounded-md border px-3">
          <input
            id={id}
            type="checkbox"
            checked={Boolean(value)}
            onChange={(e) => onChange(e.target.checked)}
          />
          <span>{value ? "已启用" : "已停用"}</span>
        </label>
      ) : f.type === "lines" || f.type === "textarea" ? (
        <textarea
          id={id}
          required={f.required}
          readOnly={f.readOnly}
          value={String(value ?? "")}
          placeholder={f.placeholder}
          onChange={(e) => onChange(e.target.value)}
          rows={4}
        />
      ) : (
        <Input
          id={id}
          type={f.type ?? "text"}
          readOnly={f.readOnly}
          min={f.min}
          max={f.max}
          step={f.step}
          minLength={f.type === "password" ? 12 : undefined}
          autoComplete={f.type === "password" ? "new-password" : undefined}
          required={f.required || (f.type === "password" && !editing)}
          value={String(value ?? "")}
          placeholder={f.placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {f.help && (
        <span className="text-xs leading-5 text-muted-foreground">
          {f.help}
        </span>
      )}
      {f.ref && !refs[f.ref]?.length && (
        <span className="text-xs text-muted-foreground">
          请先在「{resources[f.ref]?.label}」中创建记录。
        </span>
      )}
    </div>
  );
}
export default function Resources({
  kind,
  role,
  onChange,
}: {
  kind: string;
  role: string;
  onChange: () => void;
}) {
  const schema = resources[kind],
    canWrite = role === "admin" || (role === "operator" && kind !== "users");
  const routeParameters = useHashParameters(kind),
    targetId = routeParameters.get("id") ?? "";
  const consoleFilter =
    kind === "console-connections"
      ? (routeParameters.get("consoleServerId") ?? "")
      : "";
  const isConsole = ["console-servers", "console-connections"].includes(kind);
  const isFanout = kind === "reseller-fanouts";
  const [fanoutRefreshing, setFanoutRefreshing] = useState(false);
  const [logoMember, setLogoMember] = useState<Row | null>(null);
  const [welcomeMember, setWelcomeMember] = useState<{
    member: Row;
    contextCurrent: () => boolean;
  } | null>(null);
  const [rows, setRows] = useState<Row[]>([]),
    [refs, setRefs] = useState<Record<string, Row[]>>({}),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [search, setSearch] = useState("");
  const [routerCapabilities, setRouterCapabilities] = useState<
    {
      platform: string;
      roles: string[];
      collectorPolicies: string[];
      rfc1997Control?: boolean;
      rfc1997DefaultPassthrough?: boolean;
    }[]
  >([]);
  const [editing, setEditing] = useState<Row | null>(null),
    [open, setOpen] = useState(false),
    [form, setForm] = useState<Record<string, unknown>>({}),
    [formError, setFormError] = useState(""),
    [formNotice, setFormNotice] = useState(""),
    [conflict, setConflict] = useState<ResourceConflictState | null>(null),
    [busy, setBusy] = useState(false),
    [deleting, setDeleting] = useState<Row | null>(null),
    [consoleDetail, setConsoleDetail] = useState<Row | null>(null),
    [credentialPeer, setCredentialPeer] = useState<Row | null>(null),
    [readinessPeer, setReadinessPeer] = useState<Row | null>(null),
    [lifecycle, setLifecycle] = useState<{
      member: Row;
      action: string;
    } | null>(null),
    [reason, setReason] = useState(""),
    [lifecycleResult, setLifecycleResult] = useState<{
      disabledConnections: number;
      disabledPeers: number;
      deploymentRequired: boolean;
    } | null>(null);
  const conflictRequest = useRef(0);
  const fanoutRequest = useRef(0);
  useEffect(() => () => { fanoutRequest.current++; }, [kind, role]);
  useEffect(() => { setWelcomeMember(null); }, [kind, role, targetId]);
  useEffect(() => {
    if (targetId) setSearch("");
  }, [targetId]);
  useEffect(
    () => () => {
      conflictRequest.current++;
    },
    [],
  );
  const clearConflict = () => {
    conflictRequest.current++;
    setConflict(null);
  };
  const loadConflict = async (mode: "edit" | "delete", baseline: Row) => {
    const attempt = ++conflictRequest.current;
    setConflict({ mode, baseline, loading: true, attempt });
    setFormNotice("");
    if (mode === "edit")
      setForm((values) => withoutPasswords(schema.fields, values));
    try {
      const related = relatedKinds(kind, schema.fields);
      const [latest, relations] = await Promise.all([
        api<Row>(`/resources/${kind}/${baseline.id}`),
        Promise.all(
          related.map((resource) =>
            api<{ items: Row[] }>(`/resources/${resource}`),
          ),
        ),
      ]);
      if (attempt === conflictRequest.current) {
        setRefs(
          Object.fromEntries(
            related.map((resource, index) => [
              resource,
              relations[index].items,
            ]),
          ),
        );
        setConflict({ mode, baseline, latest, loading: false, attempt });
      }
    } catch (error) {
      if (attempt !== conflictRequest.current) return;
      setConflict({
        mode,
        baseline,
        loading: false,
        attempt,
        ...(error instanceof APIError && error.status === 404
          ? { deleted: true }
          : { error: message(error) }),
      });
    }
  };
  const peerRouter =
    kind === "peers"
      ? refs.routers?.find((r) => r.id === form.routerId)
      : undefined;
  const observesFilteredRoutes =
    peerRouter?.role === "route-collector" &&
    (peerRouter.collectorPolicy || "observe") === "observe";
  const as112Peer = peerRouter?.role === "as112";
  const formRouterCapability = routerCapabilities.find(
    (capability) => capability.platform === (form.platform || "bird2"),
  );
  const bird1 = kind === "routers" && form.platform === "bird1";
  const bird1Family = bird1 ? singleRouterFamily(form) : null;
  const bird1FamilyError = bird1 && !bird1Family
    ? "BIRD 1 每个实例只使用一个地址族。请填写一个本地 IPv4 或 IPv6 地址，另一项留空。"
    : "";
  const bird1RPKIError = bird1 && form.rpkiPolicy === "require-valid"
    ? "当前 BIRD 1 配置不支持实时 RPKI 校验。请选用支持实时校验的平台，或按本实例的策略调整此设置。"
    : "";
  const peerFamily = peerRouter?.platform === "bird1" ? singleRouterFamily(peerRouter) : null;
  const peerAddress = String(form.address ?? "").trim();
  const peerFamilyError = peerFamily && form.enabled === true && peerAddress && (peerAddress.includes(":") ? 6 : 4) !== peerFamily
    ? `此 BIRD 1 实例使用 IPv${peerFamily}，请填写同一地址族的邻居地址。`
    : "";
  const routerFamilyError = bird1FamilyError || bird1RPKIError || peerFamilyError;
  const unsupportedRouterRole =
    kind === "routers" &&
    !!formRouterCapability &&
    !(formRouterCapability.roles ?? ["route-server"]).includes(
      String(form.role || "route-server"),
    );
  useEffect(() => {
    if (kind !== "routers") return;
    const controller = new AbortController();
    api<{
      items: {
        platform: string;
        roles: string[];
        collectorPolicies: string[];
        rfc1997Control?: boolean;
        rfc1997DefaultPassthrough?: boolean;
      }[];
    }>("/router-platforms", { signal: controller.signal })
      .then((data) => {
        if (!controller.signal.aborted) setRouterCapabilities(data.items);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError(message(e));
      });
    return () => controller.abort();
  }, [kind]);
  const load = async () => {
    setLoading(true);
    setError("");
    try {
      const related = relatedKinds(kind, schema.fields);
      const results = await Promise.all([
        api<{ items: Row[] }>(`/resources/${kind}`),
        ...related.map((k) => api<{ items: Row[] }>(`/resources/${k}`)),
      ]);
      setRows(results[0].items);
      setRefs(
        Object.fromEntries(related.map((k, i) => [k, results[i + 1].items])),
      );
    } catch (e) {
      setError(message(e));
    } finally {
      setLoading(false);
    }
  };
  const refreshFanoutOptions = async () => {
    const request = ++fanoutRequest.current;
    const contextCurrent = captureSessionContext();
    setFanoutRefreshing(true);
    setFormError("");
    try {
      const results = await Promise.all(fanoutDependencies.map(resource =>
        api<{ items: Row[] }>(`/resources/${resource}`, { cache: "no-store" }),
      ));
      if (request !== fanoutRequest.current || !contextCurrent()) return;
      setRefs(Object.fromEntries(fanoutDependencies.map((resource, index) => [resource, results[index].items])));
      setFormNotice("端口选项已刷新。当前草稿和打开时的记录版本已保留，保存时仍会复核归属与占用。");
    } catch (e) {
      if (request === fanoutRequest.current && contextCurrent()) setFormError(message(e));
    } finally {
      if (request === fanoutRequest.current && contextCurrent()) setFanoutRefreshing(false);
    }
  };
  useEffect(() => {
    void load();
    setSearch("");
    setOpen(false);
    setDeleting(null);
  }, [kind]);
  const display = (r: Row, key: string) => {
    const field = schema.fields.find((f) => f.key === key),
      value = r[key];
    if (isFanout && ["peeringPortId", "fanoutPortId"].includes(key))
      return `${fanoutPortLabel(String(value ?? ""), refs)} · 库存归属：${fanoutPortOwner(String(value ?? ""), refs)}`;
    if (kind === "routers" && key === "role")
      return statusLabels[String(value || "route-server")] ?? String(value);
    if (key === "speed" && kind === "connections") {
      if (r.capacityKnown === true && typeof r.capacityMbps === "number")
        return String(r.capacityMbps);
      if (r.capacityKnown === false) return "—";
      const ports = connectionPorts(r).map((id) =>
        refs.ports?.find((p) => p.id === id),
      );
      if (ports.length && ports.every((p) => p && Number(p.speed) > 0))
        return String(ports.reduce((sum, p) => sum + Number(p!.speed), 0));
      return typeof r.capacityMbps === "number" && r.capacityMbps > 0
        ? String(r.capacityMbps)
        : typeof value === "number" && value > 0
          ? String(value)
          : "—";
    }
    if (key === "portIds")
      return (
        connectionPorts(r)
          .map((id) => {
            const details = Array.isArray(r.portDetails)
              ? (r.portDetails as Row[])
              : [];
            const port =
              details.find((p) => p.id === id) ??
              refs.ports?.find((p) => p.id === id);
            return port ? portLabel(port, refs) : id;
          })
          .join(", ") || "—"
      );
    if (key === "vlanId" && typeof r.vlanName === "string")
      return r.infrastructureName
        ? `${r.vlanName} · ${r.infrastructureName}`
        : r.vlanName;
    if (key === "linkMode" && kind === "connections")
      return statusLabels[
        String(value ?? (connectionPorts(r).length > 1 ? "lag" : "single"))
      ];
    if (field?.ref && Array.isArray(value))
      return (
        value
          .map(
            (id) =>
              refs[field.ref!]?.find((item) => item.id === id)?.name ?? id,
          )
          .join(", ") || "—"
      );
    if (field?.ref)
      return (
        refs[field.ref]?.find((v) => v.id === value)?.name ??
        (value ? String(value) : "—")
      );
    if (kind === "console-connections" && key === "autobaud")
      return typeof value === "boolean"
        ? value
          ? "自动波特率"
          : "手动参数"
        : "未记录";
    if (
      kind === "console-connections" &&
      ["parity", "flowControl"].includes(key)
    )
      return (
        field?.options?.find(([key]) => key === value)?.[1] ??
        (value ? String(value) : "未记录")
      );
    if (typeof value === "boolean") return value ? "启用" : "停用";
    if (key === "asn") return value ? `AS${value}` : "—";
    return (
      statusLabels[String(value)] ??
      (value == null || value === "" ? "—" : String(value))
    );
  };
  const filtered = rows
    .filter(
      (r) =>
        (!targetId || r.id === targetId) &&
        (!consoleFilter || r.consoleServerId === consoleFilter),
    )
    .filter((r) =>
      schema.columns.some((k) =>
        display(r, k).toLowerCase().includes(search.toLowerCase()),
      ),
    );
  const edit = (row?: Row) => {
    clearConflict();
    setEditing(row ? structuredClone(row) : null);
    const next = resourceForm(kind, schema.fields, row);
    if (!row && consoleFilter) next.consoleServerId = consoleFilter;
    setForm(next);
    setFormError("");
    setFormNotice("");
    setOpen(true);
    if (isFanout) void refreshFanoutOptions();
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (conflict) return;
    if (isFanout && fanoutRefreshing) return;
    if (kind === "routers" && !formRouterCapability) {
      setFormError("平台能力尚未加载，请刷新页面后核对设置。");
      return;
    }
    if (unsupportedRouterRole) {
      setFormError("当前路由平台不支持所选角色，请根据平台能力更换平台或角色。");
      return;
    }
    if (routerFamilyError) {
      setFormError(routerFamilyError);
      return;
    }
    setBusy(true);
    setFormError("");
    setFormNotice("");
    try {
      const payload: Record<string, unknown> = {};
      if (isFanout) {
        const problem = fanoutDraftProblem(form, refs, editing?.id);
        if (problem) throw new Error(problem);
      }
      if (kind === "routers" && form.role === "as112" && !form.as112IPv4 && !form.as112IPv6)
        throw new Error("AS112 至少需要选择一个服务地址族。");
      if (
        kind === "console-servers" &&
        !String(form.hostname ?? "") &&
        !String(form.managementIp ?? "")
      )
        throw new Error("请至少记录主机名或管理 IP。");
      if (
        kind === "console-connections" &&
        (!String(form.portLabel ?? "") ||
          String(form.portLabel) !== String(form.portLabel).trim())
      )
        throw new Error("串口标识必填，且不能含首尾空白；前导零会保留。");
      if (
        kind === "connections" &&
        (!Array.isArray(form.portIds) || !form.portIds.length)
      )
        throw new Error("请至少选择一个物理端口。");
      if (kind === "users") {
        const memberships = Array.isArray(form.memberIds) ? form.memberIds : [];
        const adminMemberships = Array.isArray(form.memberAdminIds)
          ? form.memberAdminIds
          : [];
        if (form.role === "member" && !memberships.length)
          throw new Error("成员账号至少需要关联一个成员网络。");
        if (adminMemberships.some((id) => !memberships.includes(id)))
          throw new Error("可管理的网络必须包含在可访问网络中。");
      }
      for (const f of schema.fields) {
        if (isConsole && ["legacySerial", "legacyConsole"].includes(f.key))
          continue;
        if (kind === "console-connections" && f.key === "autobaud") {
          payload.autobaud =
            form.autobaud === "true"
              ? true
              : form.autobaud === "false"
                ? false
                : null;
          continue;
        }
        if (
          kind === "console-connections" &&
          ["baudRate", "stopBits"].includes(f.key) &&
          (form[f.key] === "" || form[f.key] == null)
        ) {
          payload[f.key] = null;
          continue;
        }
        if (kind === "console-connections" && f.key === "portLabel") {
          payload.portLabel = String(form.portLabel);
          continue;
        }
        if (kind === "routers" && f.key === "rfc1997Passthrough") {
          if (
            form.role !== "as112" &&
            formRouterCapability?.rfc1997Control &&
            ["true", "false"].includes(String(form[f.key]))
          )
            payload[f.key] = String(form[f.key]) === "true";
          continue;
        }
        if (kind === "routers" && f.key.startsWith("as112") && form.role !== "as112") continue;
        if (kind === "routers" && ["as112NextHop4", "as112NextHop6"].includes(f.key) && (form.platform !== "junos" || !form[f.key === "as112NextHop4" ? "as112IPv4" : "as112IPv6"])) continue;
        if (kind === "routers" && form.role === "as112" && ["rpkiPolicy", "rpkiHost", "rpkiPort", "rpkiExpireSeconds"].includes(f.key)) continue;
        if (kind === "peers" && as112Peer && f.key === "allowedPrefixes") continue;
        if (
          f.readOnly &&
          [
            "peeringdbId",
            "peeringdbNetixlanId",
            "snmpIfIndex",
            "snmpIfName",
            "snmpIfType",
            "snmpPhysicalAddress",
            "snmpEngineIdentity",
          ].includes(f.key)
        )
          continue;
        if (kind === "connections" && f.key === "speed") continue;
        if (
          kind === "patch-ports" &&
          ["status", "memberId", "circuitId"].includes(f.key)
        )
          continue;
        if (
          kind === "routers" &&
          f.key === "collectorPolicy" &&
          form.role !== "route-collector"
        )
          continue;
        if (
          kind === "routers" &&
          ["rpkiHost", "rpkiPort", "rpkiExpireSeconds"].includes(f.key) &&
          form.rpkiPolicy !== "require-valid"
        )
          continue;
        const value = form[f.key];
        if (f.type === "password" && !value && editing) continue;
        if (f.type === "number" && value === "" && !f.required) continue;
        payload[f.key] =
          f.type === "number"
            ? Number(value)
            : f.type === "lines"
              ? String(value ?? "")
                  .split(/[\n,]/)
                  .map((v) => v.trim())
                  .filter(Boolean)
              : typeof value === "string" && f.type !== "password"
                ? value.trim()
                : value;
      }
      if (kind === "routers" && form.role === "as112") payload.asn = 112;
      if (kind === "peers" && as112Peer) { payload.allowedPrefixes = []; payload.allowedRoutes = []; }
      if (kind === "connections") {
        if (!payload.linkMode)
          payload.linkMode =
            Array.isArray(form.portIds) && form.portIds.length > 1
              ? "lag"
              : "single";
        if (
          payload.linkMode === "single" &&
          Array.isArray(form.portIds) &&
          form.portIds.length !== 1
        )
          throw new Error("单端口模式必须恰好选择一个端口，请改用 LAG。");
      }
      if (editing)
        await resourceMutation(
          `/resources/${kind}/${editing.id}`,
          "PUT",
          editing._etag,
          payload,
        );
      else await save(`/resources/${kind}`, payload);
      setOpen(false);
      await load();
      onChange();
    } catch (e) {
      if (
        editing &&
        e instanceof APIError &&
        [412, 428, 404].includes(e.status)
      ) {
        setFormError("");
        await loadConflict("edit", editing);
      } else {
        setFormError(message(e));
        if (isFanout && e instanceof APIError && e.status === 409)
          setFormNotice("草稿已保留。请刷新端口选项并核对当前归属，再手动保存；不会自动覆盖或重试。");
      }
    } finally {
      setBusy(false);
    }
  };
  const changeLifecycle = async (e: FormEvent) => {
    e.preventDefault();
    if (!lifecycle) return;
    setBusy(true);
    setFormError("");
    try {
      const result = await save<{
        disabledConnections: number;
        disabledPeers: number;
        deploymentRequired: boolean;
      }>(`/members/${lifecycle.member.id}/${lifecycle.action}`, { reason });
      setLifecycle(null);
      setLifecycleResult(result);
      await load();
      onChange();
    } catch (e) {
      setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!deleting || conflict) return;
    setBusy(true);
    setFormError("");
    try {
      await resourceMutation(
        `/resources/${kind}/${deleting.id}`,
        "DELETE",
        deleting._etag,
      );
      setDeleting(null);
      await load();
      onChange();
    } catch (e) {
      if (e instanceof APIError && [412, 428, 404].includes(e.status)) {
        setFormError("");
        await loadConflict("delete", deleting);
      } else setFormError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const selectedPorts = (Array.isArray(form.portIds) ? form.portIds : []).map(
    (id) => refs.ports?.find((p) => p.id === id),
  );
  const aggregateSpeed =
    selectedPorts.length && selectedPorts.every((p) => p && Number(p.speed) > 0)
      ? selectedPorts.reduce((sum, p) => sum + Number(p!.speed), 0)
      : null;
  const exportCSV = () => {
    const csv = [
      schema.columns.map((k) => fieldLabel(kind, k)),
      ...filtered.map((r) => schema.columns.map((k) => display(r, k))),
    ]
      .map((row) =>
        row
          .map(
            (v) =>
              `"${String(v)
                .replace(/^\s*[=+@-]/, "'$&")
                .replace(/"/g, '""')}"`,
          )
          .join(","),
      )
      .join("\r\n");
    const a = document.createElement("a");
    a.href = URL.createObjectURL(
      new Blob(["\ufeff", csv], { type: "text/csv;charset=utf-8;" }),
    );
    a.download = `${kind}.csv`;
    a.click();
    URL.revokeObjectURL(a.href);
  };
  return (
    <>
      <Heading
        title={schema.label}
        description={schema.description}
        className={kind === "infrastructures" ? "document-heading" : ""}
      >
        {canWrite && (
          <div className="flex flex-wrap gap-2">
            {kind === "infrastructures" && (
              <Suspense
                fallback={
                  <Button variant="outline" disabled>
                    加载迁移工具…
                  </Button>
                }
              >
                <FabricAssignment
                  onApplied={async () => {
                    await load();
                    onChange();
                  }}
                />
              </Suspense>
            )}
            <Button onClick={() => edit()}>
              <Plus />
              新增{schema.singular}
            </Button>
          </div>
        )}
      </Heading>
      {isFanout && (
        <p className="notice mb-4">此处只登记本地配线库存，不推断 VLAN、地址或 BGP 关联，也不操作设备。编辑可替换所选端口；释放只解除本条库存关系。</p>
      )}
      {consoleFilter && (
        <div className="notice mb-4 flex flex-wrap items-center justify-between gap-2">
          <span>
            关联接入：
            {refs["console-servers"]?.find((item) => item.id === consoleFilter)
              ?.name ?? consoleFilter}
            。删除服务器前需先处理这些记录。
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              location.hash = "console-connections";
            }}
          >
            全部控制台接入
          </Button>
        </div>
      )}
      <ErrorNotice message={error} />
      {targetId && (
        <div
          className="notice mt-4 flex flex-wrap items-center justify-between gap-2"
          role="status"
        >
          <span>
            {loading
              ? "正在定位搜索结果…"
              : rows.some((row) => row.id === targetId)
                ? "已定位搜索结果"
                : "此记录已删除，或你已失去访问权限。"}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              location.hash = kind;
              setSearch("");
            }}
          >
            查看全部
          </Button>
        </div>
      )}
      {lifecycleResult && (
        <div className="notice mt-4" role="status">
          成员状态已更新。已停用 {lifecycleResult.disabledConnections ?? 0}{" "}
          条连接、{lifecycleResult.disabledPeers ?? 0} 个会话。
          {lifecycleResult.deploymentRequired ? (
            <>
              <p>实际路由行为仍需发布新的配置。</p>
              <a className="font-medium underline" href="#deployments">
                前往配置中心
              </a>
            </>
          ) : (
            <p>激活成员不会自动恢复连接和会话，请逐项检查后启用。</p>
          )}
        </div>
      )}
      <Card className="mt-4 overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b p-4">
          <div className="relative w-full max-w-xs">
            <Search className="absolute left-3 top-3 size-4 text-muted-foreground" />
            <Input
              className="pl-9"
              aria-label={`搜索${schema.label}`}
              placeholder={`搜索${schema.label}…`}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <div className={kind === "contacts" ? "flex flex-wrap items-center justify-end gap-2" : "flex items-center gap-2"}>
            <span className="mr-2 text-xs text-muted-foreground">
              {filtered.length} 条记录
            </span>
            <Button
              size="icon"
              variant="ghost"
              aria-label="刷新"
              onClick={() => void load()}
              disabled={loading}
            >
              <RefreshCw className={loading ? "animate-spin" : ""} />
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={exportCSV}
              disabled={!filtered.length}
            >
              <Download />
              导出
            </Button>
            {kind === "contacts" && ["admin", "operator", "viewer"].includes(role) && <Suspense fallback={<Button size="sm" variant="outline" disabled>按联系组导出</Button>}><ContactGroupExport /></Suspense>}
          </div>
        </div>
        {loading ? (
          <Loading />
        ) : !rows.length ? (
          <Empty
            title={isFanout ? "暂无经销商分流记录" : `添加第一个${schema.singular}`}
            description={isFanout ? "先维护成员的直属经销商与实际接入，再登记左侧接入端口和右侧经销商端口。" : `在这里维护${schema.label}信息。保存的记录会用于关联配置与后续运维。`}
            action={canWrite ? `新增${schema.singular}` : undefined}
            onAction={() => edit()}
          />
        ) : !filtered.length ? (
          <Empty title="没有匹配的记录" description="请尝试不同的搜索词。" />
        ) : (
          <div className="overflow-x-auto">
            <table>
              <thead>
                <tr>
                  {schema.columns.map((k) => (
                    <th key={k}>{fieldLabel(kind, k)}</th>
                  ))}
                  {canWrite && <th className="text-right">操作</th>}
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr
                    key={r.id}
                    className={r.id === targetId ? "bg-primary/5" : undefined}
                  >
                    {schema.columns.map((k, i) => (
                      <td
                        key={k}
                        className={
                          i === 0 ? "font-medium" : "text-muted-foreground"
                        }
                      >
                        {isConsole && k === "name" ? (
                          <button
                            type="button"
                            className="text-left text-primary hover:underline"
                            aria-label={`查看控制台记录 ${r.name}`}
                            onClick={() => setConsoleDetail(structuredClone(r))}
                          >
                            {display(r, k)}
                          </button>
                        ) : kind === "peers" && k === "name" ? (
                          <div className="space-y-1">
                            <div>{display(r, k)}</div>
                            <button className="text-xs text-primary hover:underline" aria-label={`检查 BGP 配置 ${r.name}`} onClick={() => setReadinessPeer(r)}>
                              检查配置条件
                            </button>
                          </div>
                        ) : kind === "members" && k === "name" ? (
                          <div className="space-y-1">
                            <div>{display(r, k)}</div>
                            <button
                              className="text-xs text-primary hover:underline"
                              aria-label={`成员 Logo ${r.name}`}
                              onClick={() => setLogoMember(r)}
                            >
                              查看成员 Logo
                            </button>
                            {role === "admin" && (
                              <button
                                className="ml-3 text-xs text-primary hover:underline"
                                aria-label={`欢迎邮件 ${r.name}`}
                                onClick={() => setWelcomeMember({ member: r, contextCurrent: captureSessionContext() })}
                              >
                                欢迎邮件
                              </button>
                            )}
                          </div>
                        ) : kind === "console-servers" &&
                          k === "connectionCount" ? (
                          <a
                            className="text-primary hover:underline"
                            href={`#console-connections?consoleServerId=${encodeURIComponent(r.id)}`}
                          >
                            查看 {String(r.connectionCount ?? 0)} 条接入
                          </a>
                        ) : ["status", "enabled", "role", "active"].includes(
                            k,
                          ) ? (
                          <Badge
                            variant={
                              r[k] === "inactive" || r[k] === false
                                ? "outline"
                                : "default"
                            }
                          >
                            {display(r, k)}
                          </Badge>
                        ) : (
                          display(r, k)
                        )}
                      </td>
                    ))}
                    {canWrite && (
                      <td>
                        <div className="flex justify-end gap-1">
                          {kind === "peers" && (
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`管理 BGP 凭据 ${r.name}`}
                              title="BGP 认证凭据"
                              onClick={() => setCredentialPeer(r)}
                            >
                              <KeyRound />
                            </Button>
                          )}
                          {kind === "members" && (
                            <select
                              className="!w-auto !text-xs"
                              aria-label={`成员状态操作 ${r.name}`}
                              value=""
                              onChange={(e) => {
                                if (e.target.value) {
                                  setLifecycle({
                                    member: r,
                                    action: e.target.value,
                                  });
                                  setReason("");
                                  setFormError("");
                                }
                              }}
                            >
                              <option value="">状态操作…</option>
                              {r.status !== "active" && (
                                <option value="activate">激活成员</option>
                              )}
                              {r.status !== "suspended" &&
                                r.status !== "closed" && (
                                  <option value="suspend">暂停成员</option>
                                )}
                              {r.status !== "closed" && (
                                <option value="close">关闭成员</option>
                              )}
                            </select>
                          )}
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={`编辑 ${r.name}`}
                            onClick={() => edit(r)}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="text-muted-foreground hover:text-destructive"
                            aria-label={isFanout ? `释放分流关系 ${r.name}` : `删除 ${r.name}`}
                            onClick={() => {
                              clearConflict();
                              setDeleting(structuredClone(r));
                              setFormError("");
                              setFormNotice("");
                            }}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      </td>
                    )}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      {kind === "members" && role === "admin" && welcomeMember?.contextCurrent() && (
        <Suspense fallback={<Loading />}>
          <MemberWelcomeEmail
            key={welcomeMember.member.id}
            memberId={welcomeMember.member.id}
            name={String(welcomeMember.member.name ?? "当前成员")}
            intentCurrent={welcomeMember.contextCurrent}
            onClose={() => setWelcomeMember(null)}
          />
        </Suspense>
      )}
      <Dialog
        open={!!logoMember}
        onOpenChange={(v) => {
          if (!v) setLogoMember(null);
        }}
      >
        <DialogContent className="max-w-2xl">
          <DialogTitle>成员标识</DialogTitle>
          <DialogDescription>
            查看图片，或在权限允许时管理当前成员的 Logo 与公开发布。
          </DialogDescription>
          {logoMember && (
            <Suspense fallback={<Loading />}>
              <MemberLogo key={logoMember.id} memberId={logoMember.id} />
            </Suspense>
          )}
        </DialogContent>
      </Dialog>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!busy && (value || !conflict)) {
            if (!value) clearConflict();
            setOpen(value);
          }
        }}
      >
        <DialogContent className={conflict ? "max-w-3xl" : undefined}>
          <DialogTitle className="text-lg font-semibold">
            {editing ? "编辑" : "新增"}
            {schema.singular}
          </DialogTitle>
          <DialogDescription className="text-sm text-muted-foreground">
            填写信息后保存。带 * 的项目为必填项。
          </DialogDescription>
          <form onSubmit={submit} className="space-y-5">
            <ErrorNotice message={formError} />
            {formNotice && (
              <p role="status" className="notice">
                {formNotice}
              </p>
            )}
            {conflict?.mode === "edit" && (
              <Suspense fallback={<Loading />}>
                <ResourceConflict
                  key={conflict.attempt}
                  kind={kind}
                  fields={schema.fields}
                  refs={refs}
                  draft={form}
                  state={conflict}
                  onRefresh={() => void loadConflict("edit", conflict.baseline)}
                  onResolve={(latest, merged) => {
                    setEditing(structuredClone(latest));
                    setForm(merged);
                    clearConflict();
                    setFormNotice(
                      "已载入核对后的合并方案，请检查表单后再次保存。",
                    );
                    setFormError("");
                  }}
                  onCloseDeleted={() => {
                    clearConflict();
                    setOpen(false);
                    void load();
                  }}
                />
              </Suspense>
            )}
            {isFanout && (
              <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
                <p className="text-sm text-muted-foreground">左右端口均按当前库存筛选。更换或释放前请核对实际配线。</p>
                <Button type="button" variant="outline" size="sm" disabled={busy || !!conflict || fanoutRefreshing} onClick={() => void refreshFanoutOptions()}>
                  <RefreshCw className={fanoutRefreshing ? "animate-spin" : ""} />
                  刷新端口选项
                </Button>
              </div>
            )}
            {kind === "routers" && form.role === "route-collector" && (
              <p className="notice">
                路由收集器仅接收符合导入策略的路由，不向任何邻居导出路由。
                更换角色只修改配置意图，需重新生成、校验和审批配置后在目标设备应用。
              </p>
            )}
            {kind === "routers" && form.role === "as112" && <>
              <AS112Guidance service={String(form.as112Service)} ipv4={!!form.as112IPv4} ipv6={!!form.as112IPv6} platform={String(form.platform)} nextHop4={String(form.as112NextHop4 ?? "")} nextHop6={String(form.as112NextHop6 ?? "")} link={false} />
              <p className="text-xs leading-6 text-muted-foreground">保存后，在配置工具中建立绑定此节点的 AS112 DNS 服务包规则。{editing && editing.role !== "as112" && "切换已有节点前须先停用并清理其会话白名单；已有名单不会静默沿用。"}</p>
            </>}
            {kind === "peers" && as112Peer && <AS112Guidance />}
            {bird1 && bird1Family && (
              <p className="notice">此实例使用 IPv{bird1Family}。校验需使用实际的 BIRD 1 <code>{bird1Family === 4 ? "bird" : "bird6"}</code>，另一个地址族请建立独立的路由器记录。</p>
            )}
            <ErrorNotice message={routerFamilyError} />
            {kind === "routers" && !formRouterCapability && (
              <p className="notice">
                正在读取平台能力；能力不可用时不能安全保存路由选项，请刷新页面重试。
              </p>
            )}
            {kind === "routers" && form.platform === "openbgpd" && form.role !== "as112" && (
              <p className="notice">
                OpenBGPD 当前不支持本项目的 require-valid 运行时 RPKI
                策略。平台原生社区处理与 BIRD 不同，请核对下方 RFC 1997 选项。
              </p>
            )}
            {unsupportedRouterRole && (
              <ErrorNotice message="当前平台未实现所选路由角色，不能保存此组合。请根据平台能力更换平台或角色。" />
            )}
            {kind === "peers" && observesFilteredRoutes && (
              <p className="notice">
                此会话连接到观察模式的路由收集器，始终不向邻居导出路由。空白名单表示没有授权前缀，接收的路由仅供过滤观察；协议安全检查拒绝的路由不保证保留。
              </p>
            )}
            {kind === "patch-ports" && (
              <p className="notice">
                库存表只维护端口名称、位置与备注；预留、连接和成员归属通过配线工作台操作。
                <a href="#patching" className="ml-2 underline">
                  前往配线工作台
                </a>
              </p>
            )}
            {kind === "peers" && editing && (
              <p className="notice">
                如果此会话已配置 BGP 认证凭据，更换路由器、成员、邻居地址或 ASN
                前，需先通过列表中的凭据管理显式移除凭据。
              </p>
            )}
            {kind === "peers" &&
              Array.isArray(editing?.allowedRoutes) &&
              editing.allowedRoutes.length > 0 && (
                <p className="notice">
                  此会话使用前缀与源 ASN 绑定，白名单由策略工作台管理。
                  <a className="ml-2 underline" href="#policy">
                    前往策略工作台
                  </a>
                </p>
              )}
            <fieldset
              disabled={busy || !!conflict || (isFanout && fanoutRefreshing)}
              className="form-grid min-w-0"
            >
              {schema.fields
                .filter(
                  (f) =>
                    !(isFanout && ["resellerId", "peeringPortId", "fanoutPortId"].includes(f.key)) &&
                    !(kind === "routers" && f.key.startsWith("as112") && form.role !== "as112") &&
                    !(kind === "routers" && ["as112NextHop4", "as112NextHop6"].includes(f.key) && (form.platform !== "junos" || !form[f.key === "as112NextHop4" ? "as112IPv4" : "as112IPv6"])) &&
                    !(kind === "routers" && form.role === "as112" && ["rpkiPolicy", "rpkiHost", "rpkiPort", "rpkiExpireSeconds", "rfc1997Passthrough"].includes(f.key)) &&
                    !(kind === "peers" && as112Peer && ["allowedPrefixes", "allowedRoutes"].includes(f.key)) &&
                    !(
                      isConsole &&
                      ["legacySerial", "legacyConsole"].includes(f.key) &&
                      !form[f.key]
                    ) &&
                    !(
                      kind === "console-connections" &&
                      form.autobaud === "true" &&
                      [
                        "baudRate",
                        "parity",
                        "stopBits",
                        "flowControl",
                      ].includes(f.key)
                    ) &&
                    !(
                      kind === "routers" &&
                      f.key === "rfc1997Passthrough" &&
                      !formRouterCapability?.rfc1997Control
                    ) &&
                    !(
                      kind === "routers" &&
                      f.key === "collectorPolicy" &&
                      form.role !== "route-collector"
                    ) &&
                    !(
                      kind === "routers" &&
                      ["rpkiHost", "rpkiPort", "rpkiExpireSeconds"].includes(
                        f.key,
                      ) &&
                      form.rpkiPolicy !== "require-valid"
                    ),
                )
                .map((f) => (
                  <Fragment key={f.key}>
                    {f.section && (
                      <h3 className="border-b pb-2 pt-3 text-sm font-semibold md:col-span-2">
                        {f.section}
                      </h3>
                    )}
                    {isFanout && f.key === "memberId" ? (
                      <Suspense fallback={<Loading />}>
                        <ResellerFanoutFields form={form} refs={refs} editingId={editing?.id} onChange={setForm} />
                      </Suspense>
                    ) : kind === "patch-ports" &&
                    (["status", "memberId", "circuitId"].includes(f.key) ||
                      (editing &&
                        !["available", "prewired", "broken"].includes(
                          String(editing.status),
                        ) &&
                        ["panelId", "number"].includes(f.key))) ? (
                      <div className="field">
                        <label htmlFor={`field-${f.key}`}>{f.label}</label>
                        <Input
                          id={`field-${f.key}`}
                          readOnly
                          value={
                            f.ref
                              ? String(
                                  refs[f.ref]?.find((r) => r.id === form[f.key])
                                    ?.name ??
                                    form[f.key] ??
                                    "",
                                )
                              : (statusLabels[String(form[f.key])] ??
                                String(form[f.key] ?? ""))
                          }
                        />
                      </div>
                    ) : kind === "routers" && f.key === "rfc1997Passthrough" ? (
                      <div className="field md:col-span-2">
                        <label htmlFor="field-rfc1997Passthrough">
                          RFC 1997 社区处理
                        </label>
                        <select
                          id="field-rfc1997Passthrough"
                          value={String(form.rfc1997Passthrough ?? "native")}
                          onChange={(event) =>
                            setForm({
                              ...form,
                              rfc1997Passthrough: event.target.value,
                            })
                          }
                        >
                          <option value="native">
                            跟随平台默认：
                            {formRouterCapability?.rfc1997DefaultPassthrough
                              ? "保留给客户端处理"
                              : "由服务器执行限制"}
                          </option>
                          <option value="true">明确保留给客户端处理</option>
                          <option value="false">明确由服务器执行限制</option>
                        </select>
                        <p className="text-xs leading-6 text-muted-foreground">
                          NO_EXPORT、NO_ADVERTISE
                          等众所周知社区的处理方式；不是通用 BGP
                          社区过滤器。跟随默认会保留未显式设置的状态。
                          {form.role === "route-collector" &&
                            " 收集器仍然禁止向邻居导出路由，此选项不改变该隔离保证。"}
                        </p>
                      </div>
                    ) : kind === "members" && editing && f.key === "status" ? (
                      <div className="field">
                        <label htmlFor="field-status">状态</label>
                        <Input
                          id="field-status"
                          readOnly
                          value={
                            statusLabels[String(form.status)] ??
                            String(form.status ?? "")
                          }
                        />
                        <span className="text-xs text-muted-foreground">
                          使用成员列表中的状态操作完成激活、暂停或关闭。
                        </span>
                      </div>
                    ) : kind === "connections" && f.key === "speed" ? (
                      <div key={f.key} className="field">
                        <label htmlFor={`field-${f.key}`}>
                          汇总带宽（Mbps）
                        </label>
                        <Input
                          id={`field-${f.key}`}
                          readOnly
                          value={aggregateSpeed ?? ""}
                          placeholder="端口速率未完整登记"
                        />
                        <span className="text-xs text-muted-foreground">
                          依据所选物理端口计算；未知速率不会估算。
                        </span>
                      </div>
                    ) : (
                      <FieldInput
                        key={f.key}
                        field={
                          kind === "routers" && form.role === "as112" && f.key === "asn" ? { ...f, readOnly: true } : kind === "routers" && form.role === "as112" && ["as112NextHop4", "as112NextHop6"].includes(f.key) ? { ...f, required: true } : kind === "routers" &&
                          f.key === "rpkiExpireSeconds" &&
                          ["bird3", "frr"].includes(String(form.platform))
                            ? {
                                ...f,
                                min: 600,
                                help: "BIRD 3 / FRRouting 的 RPKI 缓存过期时间至少为 600 秒。",
                              }
                            : kind === "members" && f.key === "asn"
                              ? {
                                  ...f,
                                  required: ["full", "probono"].includes(
                                    String(form.type),
                                  ),
                                }
                              : kind === "peers" &&
                                  f.key === "allowedPrefixes" &&
                                  Array.isArray(editing?.allowedRoutes) &&
                                  editing.allowedRoutes.length > 0
                                ? { ...f, readOnly: true }
                                : kind === "peers" &&
                                    f.key === "allowedPrefixes"
                                  ? {
                                      ...f,
                                      required:
                                        Boolean(form.enabled) &&
                                        !observesFilteredRoutes,
                                    }
                                  : kind === "connections" &&
                                      f.key === "linkMode" &&
                                      selectedPorts.length > 1
                                    ? {
                                        ...f,
                                        options: f.options?.filter(
                                          ([key]) => key === "lag",
                                        ),
                                      }
                                    : f
                        }
                        refs={refs}
                        editing={!!editing}
                        value={kind === "routers" && form.role === "as112" && f.key === "asn" ? 112 : form[f.key]}
                        onChange={(v) =>
                          setForm({
                            ...form,
                            [f.key]: v,
                            ...(kind === "console-servers" &&
                            f.key === "rackId" &&
                            v
                              ? {
                                  locationId:
                                    refs.racks?.find((rack) => rack.id === v)
                                      ?.locationId ?? form.locationId,
                                }
                              : {}),
                            ...(kind === "routers" && f.key === "role"
                              ? {
                                  collectorPolicy:
                                    v === "route-collector"
                                      ? form.collectorPolicy || "observe"
                                      : "",
                                  ...(v === "as112" ? { asn: 112, as112Service: form.as112Service || "both", as112IPv4: form.as112IPv4 ?? true, as112IPv6: form.as112IPv6 ?? true, rpkiPolicy: "off", rpkiHost: "", rpkiPort: "", rpkiExpireSeconds: "", rfc1997Passthrough: "native" } : {}),
                                }
                              : {}),
                            ...(kind === "routers" && ((f.key === "platform" && v !== "junos") || (f.key === "role" && v !== "as112")) ? { as112NextHop4: "", as112NextHop6: "" } : {}),
                            ...(kind === "connections" &&
                            f.key === "portIds" &&
                            Array.isArray(v) &&
                            v.length > 1
                              ? { linkMode: "lag" }
                              : {}),
                          })
                        }
                      />
                    )}
                  </Fragment>
                ))}
            </fieldset>
            <div className="flex justify-end gap-2 border-t pt-4">
              <Button
                type="button"
                variant="outline"
                disabled={busy}
                onClick={() => {
                  clearConflict();
                  setForm((values) => withoutPasswords(schema.fields, values));
                  setOpen(false);
                }}
              >
                {conflict ? "放弃草稿并关闭" : "取消"}
              </Button>
              <Button
                disabled={
                  busy ||
                  (isFanout && fanoutRefreshing) ||
                  !!conflict ||
                  unsupportedRouterRole ||
                  !!routerFamilyError ||
                  (kind === "routers" && !formRouterCapability)
                }
              >
                {busy ? "正在保存…" : "保存"}
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!deleting}
        onOpenChange={(value) => {
          if (!busy && !value) {
            clearConflict();
            setDeleting(null);
          }
        }}
      >
        <DialogContent className={conflict ? "max-w-3xl" : undefined}>
          <DialogTitle className="text-lg font-semibold">
            {isFanout ? "释放分流关系" : `删除${schema.singular}`}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {isFanout
              ? `确定释放「${deleting?.name}」？仅删除这条本地配线记录，保留成员接入和物理端口，不改变设备配置。`
              : <>确定删除「{deleting?.name}」？已被其他记录使用的数据需要先解除关联。此操作无法撤销。</>}
          </DialogDescription>
          <ErrorNotice message={formError} />
          {kind === "console-servers" &&
            Number(deleting?.connectionCount) > 0 && (
              <div className="notice">
                仍有 {Number(deleting?.connectionCount)}{" "}
                条关联接入，须先解除关联，系统不会级联删除。
                <a
                  className="ml-2 text-primary underline"
                  href={`#console-connections?consoleServerId=${encodeURIComponent(deleting?.id ?? "")}`}
                >
                  查看关联接入
                </a>
              </div>
            )}
          {formNotice && (
            <p role="status" className="notice">
              {formNotice}
            </p>
          )}
          {conflict?.mode === "delete" && (
            <Suspense fallback={<Loading />}>
              <ResourceConflict
                key={conflict.attempt}
                kind={kind}
                fields={schema.fields}
                refs={refs}
                draft={resourceForm(kind, schema.fields, conflict.baseline)}
                state={conflict}
                onRefresh={() => void loadConflict("delete", conflict.baseline)}
                onResolve={(latest) => {
                  setDeleting(structuredClone(latest));
                  clearConflict();
                  setFormNotice(isFanout ? "已采用核对后的服务器版本。请再次确认释放。" : "已采用核对后的服务器版本。请再次确认删除。");
                  setFormError("");
                }}
                onCloseDeleted={() => {
                  clearConflict();
                  setDeleting(null);
                  void load();
                }}
              />
            </Suspense>
          )}
          <div className="flex justify-end gap-2">
            <Button
              variant="outline"
              disabled={busy}
              onClick={() => {
                clearConflict();
                setDeleting(null);
              }}
            >
              取消
            </Button>
            <Button
              variant="destructive"
              disabled={
                busy ||
                !!conflict ||
                (kind === "console-servers" &&
                  Number(deleting?.connectionCount) > 0)
              }
              onClick={() => void remove()}
            >
              {isFanout ? busy ? "正在释放…" : "确认释放" : busy ? "正在删除…" : "确认删除"}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <Dialog
        open={!!consoleDetail}
        onOpenChange={(open) => !open && setConsoleDetail(null)}
      >
        <DialogContent className="max-w-2xl">
          <DialogTitle>控制台库存详情 · {consoleDetail?.name}</DialogTitle>
          <DialogDescription>
            以下为已登记资料与串口参数，不代表设备配置已应用，也不建立远程终端会话。
          </DialogDescription>
          <div className="max-h-[65vh] space-y-3 overflow-y-auto">
            {consoleDetail &&
              schema.fields
                .filter(
                  (field) =>
                    !["legacySerial", "legacyConsole"].includes(field.key) ||
                    consoleDetail[field.key],
                )
                .map((field) => (
                  <div
                    key={field.key}
                    className="grid gap-1 border-b pb-2 sm:grid-cols-[150px_1fr]"
                  >
                    <span className="text-xs text-muted-foreground">
                      {field.label}
                    </span>
                    <span className="whitespace-pre-wrap break-all text-sm">
                      {display(consoleDetail, field.key)}
                    </span>
                  </div>
                ))}
          </div>
          <div className="flex justify-end">
            <Button variant="outline" onClick={() => setConsoleDetail(null)}>
              关闭详情
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      {readinessPeer && kind === "peers" && (
        <Suspense fallback={<Loading />}>
          <PeerReadiness
            peerId={readinessPeer.id}
            name={String(readinessPeer.name ?? readinessPeer.id)}
            onClose={() => setReadinessPeer(null)}
            onEdit={canWrite ? () => {
              setReadinessPeer(null);
              edit(readinessPeer);
            } : undefined}
          />
        </Suspense>
      )}
      {credentialPeer && canWrite && (
        <Suspense fallback={<Loading />}>
          <PeerCredentials
            peer={credentialPeer}
            routerName={
              refs.routers?.find((r) => r.id === credentialPeer.routerId)?.name
            }
            memberName={
              refs.members?.find((r) => r.id === credentialPeer.memberId)?.name
            }
            onClose={() => setCredentialPeer(null)}
            onChanged={async () => {
              await load();
              onChange();
            }}
          />
        </Suspense>
      )}
      <Dialog
        open={!!lifecycle}
        onOpenChange={(v) => !busy && !v && setLifecycle(null)}
      >
        <DialogContent>
          <DialogTitle className="text-lg font-semibold">
            {lifecycle?.action === "activate"
              ? "激活成员"
              : lifecycle?.action === "suspend"
                ? "暂停成员"
                : "关闭成员"}
            ：{lifecycle?.member.name}
          </DialogTitle>
          <DialogDescription className="text-sm leading-6 text-muted-foreground">
            {lifecycle?.action === "activate"
              ? "激活只改变成员状态，不自动恢复已有连接和 BGP 会话。"
              : "此操作会同时停用该成员的连接与 BGP 会话配置。生产路由器仍需单独生成并应用配置。"}
          </DialogDescription>
          <form onSubmit={changeLifecycle} className="space-y-4">
            <ErrorNotice message={formError} />
            <div className="field">
              <label htmlFor="member-status-reason">操作原因</label>
              <textarea
                id="member-status-reason"
                required
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </div>
            <div className="flex justify-end gap-2">
              <Button
                variant="outline"
                type="button"
                disabled={busy}
                onClick={() => setLifecycle(null)}
              >
                取消
              </Button>
              <Button
                variant={
                  lifecycle?.action === "activate" ? "default" : "destructive"
                }
                disabled={busy}
              >
                确认更新状态
              </Button>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
